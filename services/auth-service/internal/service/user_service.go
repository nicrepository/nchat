package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	// The runtime image is distroless (see Dockerfile.service), which ships
	// no /usr/share/zoneinfo. Without this, every timezone other than "UTC"
	// and "Local" would fail validateTimezone's time.LoadLocation call in
	// production even though the exact same code works on a developer's
	// machine, where the OS usually does have a zoneinfo database. This
	// blank import embeds the full IANA database in the binary instead of
	// depending on the runtime environment providing one.
	_ "time/tzdata"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// UserCreator is the interface for creating new users.
type UserCreator interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput) (domain.User, error)
}

// UserStatusManager is the interface for admin status change operations.
type UserStatusManager interface {
	// UpdateUserStatus transitions targetID to newStatus.
	// callerID is the requesting admin's user ID (from JWT); pass "" when identity
	// is unavailable (e.g. AdminBootstrapGuard). Self-deactivation is rejected only
	// when callerID is non-empty and matches targetID.
	// Status update and session revocation (on suspension) are atomic.
	UpdateUserStatus(ctx context.Context, callerID, targetID, newStatus string) (domain.User, error)
}

// SelfProfileReader reads the authenticated user's own minimal profile.
type SelfProfileReader interface {
	GetProfile(ctx context.Context, userID string) (domain.SelfProfile, error)
}

// SelfProfileWriter edits the authenticated user's own minimal profile.
type SelfProfileWriter interface {
	// UpdateDisplayName validates and persists a new display_name for userID,
	// then returns the resulting profile — the same shape GetProfile returns,
	// so a caller can render the confirmed value without a second round trip.
	UpdateDisplayName(ctx context.Context, userID, displayName string) (domain.SelfProfile, error)
	// UpdateProfileFields validates and persists job_title, bio, timezone and
	// custom_status for userID, then returns the resulting
	// profile. Each argument is independently optional: nil leaves that field
	// untouched, a pointer to "" clears it, and a pointer to a non-empty
	// string sets it (after validation) — see UpdateProfileFields's own doc
	// comment below. custom_status is intentionally a single text field.
	UpdateProfileFields(ctx context.Context, userID string, jobTitle, bio, timezone, customStatus *string) (domain.SelfProfile, error)
}

// WorkspaceAdminResolver answers "which workspace does this caller
// administer?" — the question that turns a session into authority.
//
// It is what lets the administrative endpoints derive the workspace and the
// actor entirely server-side, instead of trusting either from the request.
type WorkspaceAdminResolver interface {
	// GetAdminWorkspaceID returns the workspace the caller administers, or
	// ErrForbidden when they administer none.
	GetAdminWorkspaceID(ctx context.Context, userID string) (string, error)
}

// WorkspaceUserLister reads one page of a workspace's members.
type WorkspaceUserLister interface {
	// ListWorkspaceUsers returns up to limit users of workspaceID starting
	// after cursorStr, plus an opaque cursor for the next page ("" when the
	// page just returned is the last one).
	ListWorkspaceUsers(ctx context.Context, workspaceID string, limit int, cursorStr string) ([]domain.WorkspaceUser, string, error)
}

// UserAdmin is the combined interface used by the auth HTTP handlers. It also
// carries the self-profile read so the router can serve GET /auth/me from the
// same user service without a new constructor parameter.
type UserAdmin interface {
	UserCreator
	UserStatusManager
	SelfProfileReader
	SelfProfileWriter
	WorkspaceAdminResolver
	WorkspaceUserLister
}

// UserService implements UserCreator and UserStatusManager.
type UserService struct {
	store storage.UserStore
}

// NewUserService creates a UserService backed by the given store.
func NewUserService(store storage.UserStore) *UserService {
	return &UserService{store: store}
}

// GetProfile returns the authenticated user's own minimal profile. The userID
// comes from the session; there is no lookup by any client-supplied identifier.
func (s *UserService) GetProfile(ctx context.Context, userID string) (domain.SelfProfile, error) {
	return s.store.GetSelfProfile(ctx, userID)
}

// selfDisplayNameMinLen and selfDisplayNameMaxLen bound a user's own
// display_name. There is no requirement document specifying this field's
// length, so the bound is not invented: it reuses the value already chosen
// for the same kind of field in this package — a short, human-chosen label —
// by UpdateDeviceDisplayName below (deviceDisplayNameMaxLen).
const (
	selfDisplayNameMinLen = 1
	selfDisplayNameMaxLen = 80
)

// UpdateDisplayName validates and persists a new display_name for userID, then
// returns the resulting profile so the caller can render the confirmed value.
// The userID comes from the session, exactly like GetProfile; there is no
// lookup or update by any client-supplied identifier. Only an active,
// non-deleted user can be updated — the same scope GetSelfProfile reads and
// SetAvatarURL writes under, enforced by the store.
func (s *UserService) UpdateDisplayName(ctx context.Context, userID, displayName string) (domain.SelfProfile, error) {
	displayName = sanitizeDisplayName(displayName)
	if len([]rune(displayName)) < selfDisplayNameMinLen {
		return domain.SelfProfile{}, fmt.Errorf("%w: display_name must be at least 1 character", domain.ErrInvalidInput)
	}
	if len([]rune(displayName)) > selfDisplayNameMaxLen {
		return domain.SelfProfile{}, fmt.Errorf("%w: display_name must be at most 80 characters", domain.ErrInvalidInput)
	}
	return s.store.UpdateDisplayName(ctx, userID, displayName)
}

// selfShortFieldMaxLen bounds job_title and custom_status: same category as
// display_name (a short, human-chosen label), so it reuses
// selfDisplayNameMaxLen's value and justification rather than inventing a new
// number.
const selfShortFieldMaxLen = selfDisplayNameMaxLen

// selfBioMaxLen bounds bio. Unlike the fields above, there is no sibling field
// in this codebase for "free-form paragraph text" to reuse a bound from — this
// value is a judgment call, not a requirement, and can be revisited.
const selfBioMaxLen = 500

// UpdateProfileFields validates and persists job_title, bio, timezone and
// custom_status for userID, then returns the resulting profile.
//
// Each argument is independently optional at the transport level (nil means
// "the caller did not touch this field" and is passed straight through to the
// store, which leaves the column alone), but once a field IS provided, "" is
// treated as "clear it" rather than rejected — unlike display_name, none of
// these four fields is ever required to be non-empty. job_title and
// custom_status reuse sanitizeDisplayName's trim-and-strip-control-characters
// treatment. Bio has its own sanitizer because it is multiline: it preserves
// line feeds while still dropping other control characters. Timezone gets its
// own validation, since a syntactically fine string is not the same thing as a
// real time zone.
func (s *UserService) UpdateProfileFields(ctx context.Context, userID string, jobTitle, bio, timezone, customStatus *string) (domain.SelfProfile, error) {
	if jobTitle != nil {
		sanitized := sanitizeDisplayName(*jobTitle)
		if len([]rune(sanitized)) > selfShortFieldMaxLen {
			return domain.SelfProfile{}, fmt.Errorf("%w: job_title must be at most %d characters", domain.ErrInvalidInput, selfShortFieldMaxLen)
		}
		jobTitle = &sanitized
	}
	if bio != nil {
		sanitized := sanitizeBio(*bio)
		if len([]rune(sanitized)) > selfBioMaxLen {
			return domain.SelfProfile{}, fmt.Errorf("%w: bio must be at most %d characters", domain.ErrInvalidInput, selfBioMaxLen)
		}
		bio = &sanitized
	}
	if customStatus != nil {
		sanitized := sanitizeDisplayName(*customStatus)
		if len([]rune(sanitized)) > selfShortFieldMaxLen {
			return domain.SelfProfile{}, fmt.Errorf("%w: custom_status must be at most %d characters", domain.ErrInvalidInput, selfShortFieldMaxLen)
		}
		customStatus = &sanitized
	}
	if timezone != nil {
		validated, err := validateTimezone(*timezone)
		if err != nil {
			return domain.SelfProfile{}, err
		}
		timezone = &validated
	}

	return s.store.UpdateProfileFields(ctx, userID, jobTitle, bio, timezone, customStatus)
}

// sanitizeBio normalizes all line endings to LF, trims only external
// whitespace, and removes control characters except LF. A bio is free-form
// multiline text, unlike a display name; treating LF as invalid would silently
// flatten the value the textarea lets a user enter.
func sanitizeBio(bio string) string {
	bio = strings.ReplaceAll(bio, "\r\n", "\n")
	bio = strings.ReplaceAll(bio, "\r", "\n")
	bio = strings.TrimSpace(bio)

	var sb strings.Builder
	for _, r := range bio {
		if unicode.IsControl(r) && r != '\n' {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// validateTimezone accepts "" (clearing the field) or a real IANA time zone
// name, and rejects everything else. A free-text timezone field would let a
// client store a string that looks plausible but resolves to nothing useful;
// validating against the actual database is what makes the stored value safe
// for a future consumer to compute a local time from. "Local" is rejected even
// though time.LoadLocation accepts it, because it names the server's own
// system zone, not a zone the user chose — storing it would silently change
// meaning if the server's deployment environment ever changes.
func validateTimezone(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == "Local" {
		return "", fmt.Errorf("%w: timezone must be a valid IANA time zone name", domain.ErrInvalidInput)
	}
	if _, err := time.LoadLocation(trimmed); err != nil {
		return "", fmt.Errorf("%w: timezone must be a valid IANA time zone name", domain.ErrInvalidInput)
	}
	return trimmed, nil
}

func (s *UserService) CreateUser(ctx context.Context, input domain.CreateUserInput) (domain.User, error) {
	input.Email = domain.NormalizeEmail(input.Email)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.FullName = strings.TrimSpace(input.FullName)

	if err := domain.ValidateEmail(input.Email); err != nil {
		return domain.User{}, err
	}
	if input.DisplayName == "" {
		return domain.User{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}
	if input.InitialPassword == "" {
		return domain.User{}, fmt.Errorf("%w: initial_password is required", domain.ErrInvalidInput)
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("get policy: %w", err)
	}

	if err := domain.ValidatePassword(input.InitialPassword, policy); err != nil {
		return domain.User{}, err
	}

	hash, err := HashPassword(input.InitialPassword)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}

	return s.store.CreateUser(ctx, input, hash)
}

// UpdateUserStatus transitions targetID to newStatus. Status change and session
// revocation (on suspension) are performed atomically by the storage layer.
// Returns ErrForbidden if callerID is non-empty and equals targetID.
// Returns ErrNotFound if the user does not exist.
// Returns ErrStatusTransitionNotAllowed for unsupported transitions.
func (s *UserService) UpdateUserStatus(ctx context.Context, callerID, targetID, newStatus string) (domain.User, error) {
	if callerID != "" && callerID == targetID {
		return domain.User{}, domain.ErrForbidden
	}
	return s.store.UpdateUserStatus(ctx, targetID, newStatus)
}

// GetAdminWorkspaceID returns the workspace userID administers.
//
// The lookup is delegated unchanged: the authorization lives in the SQL, where
// it holds atomically with the read, rather than in a check here that a later
// caller could skip.
func (s *UserService) GetAdminWorkspaceID(ctx context.Context, userID string) (string, error) {
	return s.store.GetAdminWorkspaceID(ctx, userID)
}

// encodeWorkspaceUserCursor renders a keyset position as an opaque token.
//
// base64url without padding, because the value travels in a query string and
// the standard alphabet's "+" and "/" would need escaping. It is encoding, not
// protection, and it does not need to be: the token carries no secret and no
// personal data — only the caller's own workspace and the id of a user inside
// it. Tampering can at most move the caller's position within the workspace
// they already administer, because the workspace filter is applied server-side
// on every request regardless of the cursor.
func encodeWorkspaceUserCursor(c domain.WorkspaceUserCursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeWorkspaceUserCursor parses a cursor and checks it belongs here.
//
// Every rejection is the same ErrInvalidInput: malformed, wrong version and
// another tenant's cursor are indistinguishable to the caller, so a cursor
// cannot be used to probe whether a workspace exists. The workspace check is
// defence in depth — the listing query filters by the session's workspace
// regardless, so a foreign cursor could at worst skip rows within the caller's
// own workspace, never reach another's.
func decodeWorkspaceUserCursor(cursorStr, workspaceID string) (*domain.WorkspaceUserCursor, error) {
	if cursorStr == "" {
		return nil, nil
	}
	// Length first, before any decode or allocation: the value is entirely
	// client-controlled, and a megabyte of base64 must cost a comparison rather
	// than a parse.
	if len(cursorStr) > domain.WorkspaceUserCursorMaxEncodedBytes {
		return nil, fmt.Errorf("%w: invalid cursor", domain.ErrInvalidInput)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursorStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid cursor", domain.ErrInvalidInput)
	}
	var c domain.WorkspaceUserCursor
	// DisallowUnknownFields: a cursor carrying fields this build does not know
	// is not a cursor this build minted.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%w: invalid cursor", domain.ErrInvalidInput)
	}
	// Every field that the cursor's meaning depends on is checked. A cursor
	// missing any of them is not a partially usable position — it is not a
	// position at all, and must fail predictably rather than reach the query and
	// produce an incoherent page.
	if c.Version != domain.WorkspaceUserCursorVersion {
		return nil, fmt.Errorf("%w: invalid cursor", domain.ErrInvalidInput)
	}
	if !isUUID(c.UserID) || !isUUID(c.WorkspaceID) || c.WorkspaceID != workspaceID {
		return nil, fmt.Errorf("%w: invalid cursor", domain.ErrInvalidInput)
	}
	return &c, nil
}

// workspaceUserCursorUUID matches the canonical textual UUID form. Validating
// shape here keeps a malformed identifier out of the ::uuid casts downstream,
// where it would surface as a database error rather than a 400.
var workspaceUserCursorUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return workspaceUserCursorUUID.MatchString(s) }

// ListWorkspaceUsers returns one page of workspaceID's members.
//
// workspaceID must come from GetAdminWorkspaceID for the same caller — this
// method performs no authorization of its own and must never be handed a
// client-supplied value.
//
// limit is clamped to the domain bounds here as a backstop; the handler
// rejects out-of-range input before this point, so a value arriving out of
// range means an internal caller, not a request.
//
// One extra row is requested to decide whether another page exists, which
// avoids a second COUNT query and cannot disagree with the rows just read.
func (s *UserService) ListWorkspaceUsers(ctx context.Context, workspaceID string, limit int, cursorStr string) ([]domain.WorkspaceUser, string, error) {
	if limit < domain.WorkspaceUserPageMinLimit {
		limit = domain.WorkspaceUserPageDefaultLimit
	}
	if limit > domain.WorkspaceUserPageMaxLimit {
		limit = domain.WorkspaceUserPageMaxLimit
	}

	cursor, err := decodeWorkspaceUserCursor(cursorStr, workspaceID)
	if err != nil {
		return nil, "", err
	}

	// The cursor's user id is the position outright, because the listing is
	// ordered by that id. No second query is needed to place it, and a member
	// who left between two pages no longer invalidates the cursor: their id is
	// still a valid point to resume after, they simply are not in the results.
	after := ""
	if cursor != nil {
		after = cursor.UserID
	}

	users, err := s.store.ListWorkspaceUsers(ctx, workspaceID, limit+1, after)
	if err != nil {
		return nil, "", err
	}

	if len(users) <= limit {
		return users, "", nil
	}

	users = users[:limit]
	last := users[len(users)-1]
	next, err := encodeWorkspaceUserCursor(domain.WorkspaceUserCursor{
		Version:     domain.WorkspaceUserCursorVersion,
		WorkspaceID: workspaceID,
		UserID:      last.ID,
	})
	if err != nil {
		return nil, "", err
	}
	return users, next, nil
}
