package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	defaultInviteTokenTTLHours = 72
	inviteAcceptActionPath     = "/auth/invites/accept"
)

// InviteStore persists invite state without receiving plaintext invite tokens or passwords.
//
// The duplicate/pending checks and the rate-limit budget deliberately live
// inside CreateInvite rather than as separate calls: checking in one statement
// and inserting in another leaves a window where two requests both pass the
// check. The store performs all of them in one transaction under a lock.
type InviteStore interface {
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
	// BootstrapWorkspaceState reports whether the bootstrap window is still
	// open for workspaceID. It closes permanently once the workspace has an
	// active owner or admin, and is also shut while the workspace is not
	// operational or does not exist.
	BootstrapWorkspaceState(ctx context.Context, workspaceID string) (domain.BootstrapWorkspaceState, error)
	// now is the caller's canonical instant: the store uses it both to retire
	// invites whose TTL has elapsed and to judge whether one is still active,
	// and expiresAt is derived from that same instant.
	CreateInvite(ctx context.Context, input domain.AdminInviteInput, tokenHash string, now, expiresAt time.Time, encryptedPayload string, limit domain.InviteRateLimit) (domain.InviteResult, error)
	AcceptInviteTx(ctx context.Context, tokenHash, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error)
}

// InviteManager is the HTTP-facing invite contract.
type InviteManager interface {
	CreateInvite(ctx context.Context, input domain.AdminInviteInput) (domain.InviteResult, error)
	// CreateBootstrapInvite is the initialization-only sibling of CreateInvite.
	// It is a separate method rather than a flag so the two authority models
	// cannot be confused at a call site: this one never reads a session.
	CreateBootstrapInvite(ctx context.Context, input domain.BootstrapInviteInput) (domain.InviteResult, error)
	AcceptInvite(ctx context.Context, input domain.AcceptInviteInput) (domain.AcceptInviteResult, error)
}

type InviteOption func(*InviteService)

func WithInviteOutboxEncryptor(encryptor *emailcrypto.Encryptor) InviteOption {
	return func(s *InviteService) {
		s.emailOutbox = encryptor
	}
}

// WithInviteRateLimit sets the per-actor, per-workspace invite budget. Omitting
// it leaves the service unlimited, which is only appropriate for tests — the
// app always configures one.
func WithInviteRateLimit(limit domain.InviteRateLimit) InviteOption {
	return func(s *InviteService) {
		s.rateLimit = limit
	}
}

// InviteService implements admin invite creation and public invite acceptance.
type InviteService struct {
	tokens      *TokenManager
	store       InviteStore
	emailOutbox *emailcrypto.Encryptor
	rateLimit   domain.InviteRateLimit
	// bootstrapWorkspaceID is empty unless an operator configured one, which
	// is what keeps the bootstrap endpoint disabled by default.
	bootstrapWorkspaceID string
}

func NewInviteService(tokens *TokenManager, store InviteStore, opts ...InviteOption) *InviteService {
	svc := &InviteService{tokens: tokens, store: store}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func (s *InviteService) EmailHandoffAvailable() bool {
	return s != nil && s.emailOutbox != nil
}

func (s *InviteService) CreateInvite(ctx context.Context, input domain.AdminInviteInput) (domain.InviteResult, error) {
	if s.emailOutbox == nil {
		return domain.InviteResult{}, domain.ErrEmailOutboxUnavailable
	}

	// Authority first. Both values are set by the HTTP guard from the session;
	// an empty one means this was reached by a path that never established who
	// is inviting into what, and there is no safe default for either. The
	// bootstrap command below is the only caller allowed to omit an actor, and
	// it does not come through here.
	if strings.TrimSpace(input.WorkspaceID) == "" {
		return domain.InviteResult{}, fmt.Errorf("%w: workspace is required", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(input.ActorID) == "" {
		return domain.InviteResult{}, fmt.Errorf("%w: actor is required", domain.ErrInvalidInput)
	}

	// Overwritten, not defaulted: whatever kind reached this input is
	// discarded, so no caller and no payload can raise an ordinary invite to
	// the bootstrap kind.
	input.Kind = domain.InviteKindMember
	return s.issueInvite(ctx, input)
}

// CreateBootstrapInvite issues an invite using the bootstrap credential, for an
// environment that has no administrator yet.
//
// It exists because nothing else can onboard the first person: no HTTP route
// creates a chat.workspace_members row except invite acceptance, so a fresh
// deployment has no owner/admin and every session-scoped admin route answers
// 403. This is the seam that breaks that circle.
//
// Two things are decided entirely server-side and are not expressible in the
// request: the workspace comes from configuration (see WithBootstrapWorkspace),
// and the issuer is domain.BootstrapInviteIssuer. The caller supplies only who
// to invite.
//
// It is refused once the target workspace has an active owner or admin: from
// that moment the authenticated, workspace-scoped endpoint can do the job, and
// leaving a pre-shared credential able to inject invitations into a live
// workspace would be a standing risk with no remaining purpose. That verdict is
// permanent — archiving a workspace does not un-initialize it — and the
// endpoint is equally refused while the workspace is not operational or does
// not exist. Every refusal reports the same thing to the caller.
func (s *InviteService) CreateBootstrapInvite(ctx context.Context, input domain.BootstrapInviteInput) (domain.InviteResult, error) {
	if s.emailOutbox == nil {
		return domain.InviteResult{}, domain.ErrEmailOutboxUnavailable
	}
	if s.bootstrapWorkspaceID == "" {
		return domain.InviteResult{}, domain.ErrBootstrapUnavailable
	}

	state, err := s.store.BootstrapWorkspaceState(ctx, s.bootstrapWorkspaceID)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("check bootstrap workspace state: %w", err)
	}
	if !state.BootstrapOpen() {
		return domain.InviteResult{}, domain.ErrBootstrapUnavailable
	}

	return s.issueInvite(ctx, domain.AdminInviteInput{
		WorkspaceID: s.bootstrapWorkspaceID,
		ActorID:     domain.BootstrapInviteIssuer,
		Email:       input.Email,
		DisplayName: input.DisplayName,
		FullName:    input.FullName,
		Kind:        domain.InviteKindBootstrapOwner,
	})
}

// issueInvite is the shared issuance path: validate the invitee, mint the
// token, encrypt the handoff, persist. It performs no authority checks of its
// own — each caller above establishes the workspace and issuer first, and this
// function trusts what it is given precisely because neither caller takes those
// values from the client.
func (s *InviteService) issueInvite(ctx context.Context, input domain.AdminInviteInput) (domain.InviteResult, error) {
	email := domain.NormalizeEmail(input.Email)
	displayName := strings.TrimSpace(input.DisplayName)
	fullName := strings.TrimSpace(input.FullName)

	if err := domain.ValidateEmail(email); err != nil {
		return domain.InviteResult{}, err
	}
	if displayName == "" {
		return domain.InviteResult{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("get policy: %w", err)
	}
	inviteTTLHours := policy.InviteTokenTTLHours
	if inviteTTLHours <= 0 {
		inviteTTLHours = defaultInviteTokenTTLHours
	}

	rawToken, err := s.tokens.GenerateOpaqueToken()
	if err != nil {
		return domain.InviteResult{}, err
	}
	tokenHash := s.tokens.HashInviteToken(rawToken)
	// One reading of the clock for the whole operation. The store uses it to
	// decide which invites have lapsed, and the new invite's expiry is derived
	// from the same instant, so "already expired" and "expires at" can never be
	// judged against two slightly different nows.
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(inviteTTLHours) * time.Hour)
	encryptedPayload, err := s.emailOutbox.Encrypt(emailcrypto.Plaintext{
		Kind:       "invite",
		Token:      rawToken,
		ActionPath: inviteAcceptActionPath,
		ToEmail:    email,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("encrypt invite outbox payload: %w", err)
	}

	return s.store.CreateInvite(ctx, domain.AdminInviteInput{
		WorkspaceID: input.WorkspaceID,
		ActorID:     input.ActorID,
		Email:       email,
		DisplayName: displayName,
		FullName:    fullName,
		Kind:        input.Kind,
	}, tokenHash, now, expiresAt, encryptedPayload, s.rateLimit)
}

// WithBootstrapWorkspace sets the workspace bootstrap invites are issued into.
// Without it CreateBootstrapInvite refuses: there is no default and no lookup,
// so the bootstrap credential cannot be pointed at a workspace nobody chose.
func WithBootstrapWorkspace(workspaceID string) InviteOption {
	return func(s *InviteService) {
		s.bootstrapWorkspaceID = strings.TrimSpace(workspaceID)
	}
}

func (s *InviteService) AcceptInvite(ctx context.Context, input domain.AcceptInviteInput) (domain.AcceptInviteResult, error) {
	token := strings.TrimSpace(input.Token)
	displayName := strings.TrimSpace(input.DisplayName)
	fullName := strings.TrimSpace(input.FullName)
	if token == "" {
		return domain.AcceptInviteResult{}, fmt.Errorf("%w: token is required", domain.ErrInvalidInput)
	}
	if displayName == "" {
		return domain.AcceptInviteResult{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}
	if input.Password == "" {
		return domain.AcceptInviteResult{}, fmt.Errorf("%w: password is required", domain.ErrInvalidInput)
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("get policy: %w", err)
	}
	if err := domain.ValidatePassword(input.Password, policy); err != nil {
		return domain.AcceptInviteResult{}, err
	}
	passwordHash, err := HashPassword(input.Password)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("hash password: %w", err)
	}
	tokenHash := s.tokens.HashInviteToken(token)
	return s.store.AcceptInviteTx(ctx, tokenHash, displayName, fullName, passwordHash)
}
