// Package domain contains file-service entities and invariants.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
)

// Error categories. Handlers map these to HTTP statuses; nothing below ever
// carries storage topology, driver text or crypto material.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrUnauthorized = errors.New("unauthorized")
	ErrNotFound     = errors.New("not found")
	ErrUnavailable  = errors.New("service unavailable")

	// ErrTooLarge is returned when the plaintext exceeds the configured cap.
	ErrTooLarge = errors.New("file too large")
	// ErrEmptyFile rejects a zero-byte upload before anything is persisted.
	ErrEmptyFile = fmt.Errorf("%w: empty file", ErrInvalidInput)
	// ErrTooManyFiles rejects a multipart body carrying more than one file.
	ErrTooManyFiles = fmt.Errorf("%w: exactly one file is allowed", ErrInvalidInput)
	// ErrNotDownloadable is returned for an attachment that exists and is
	// visible to the caller but has not been cleared by the antimalware scan.
	ErrNotDownloadable = errors.New("attachment not available for download")

	ErrUploadsDisabled         = fmt.Errorf("%w: uploads disabled", ErrUnavailable)
	ErrDependenciesUnavailable = fmt.Errorf("%w: dependencies unavailable", ErrUnavailable)

	// Cluster-wide upload admission control.
	//
	// The three are distinct on purpose: the caller's own limit is the caller's
	// to fix and is retryable soon (429), a full cluster is nobody's fault and
	// is retryable later (503), and an admission backend that cannot answer
	// fails closed rather than admitting an uncounted upload (503).

	// ErrNoCapacity is the internal "every slot is taken" signal. It never
	// reaches a handler on its own; callers translate it into one of the two
	// below so the response says which resource ran out without saying how much
	// of it exists.
	ErrNoCapacity = errors.New("no upload capacity")
	// ErrUserAtCapacity means this user already holds their concurrent uploads.
	ErrUserAtCapacity = fmt.Errorf("%w: user upload concurrency reached", ErrNoCapacity)
	// ErrClusterAtCapacity means every global upload slot is in use.
	ErrClusterAtCapacity = fmt.Errorf("%w: cluster upload concurrency reached", ErrNoCapacity)
	// ErrAdmissionUnavailable means admission could not be decided at all.
	ErrAdmissionUnavailable = fmt.Errorf("%w: upload admission unavailable", ErrUnavailable)
)

// DestinationKind is the logical target an attachment belongs to. An attachment
// has exactly one, never both: the two upload routes each pin one value and the
// database CHECK enforces the same invariant.
type DestinationKind string

const (
	DestinationKindChannel DestinationKind = "channel"
	DestinationKindDM      DestinationKind = "dm"
)

// Valid reports whether the kind is one this service supports.
func (k DestinationKind) Valid() bool {
	return k == DestinationKindChannel || k == DestinationKindDM
}

// Destination is a validated upload target. The workspace is deliberately
// absent: it is never accepted from the client and is derived server-side from
// the destination row during authorization.
type Destination struct {
	Kind DestinationKind
	ID   string
}

// NewDestination validates the kind and the destination UUID.
func NewDestination(kind DestinationKind, id string) (Destination, error) {
	if !kind.Valid() {
		return Destination{}, fmt.Errorf("%w: invalid destination kind", ErrInvalidInput)
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return Destination{}, fmt.Errorf("%w: invalid destination id", ErrInvalidInput)
	}
	return Destination{Kind: kind, ID: parsed.String()}, nil
}

// Status is the attachment lifecycle state. The client never supplies it.
type Status string

const (
	// StatusPendingUpload is the state a row is created in, before the object
	// exists in storage. A row left here is a recoverable orphan, never a
	// downloadable attachment.
	StatusPendingUpload Status = "pending_upload"
	// StatusPendingScan hands the object over to the asynchronous antimalware
	// worker (out of scope for this issue). It is not downloadable.
	StatusPendingScan Status = "pending_scan"
	// StatusClean is the only downloadable state.
	StatusClean Status = "clean"
	// StatusRejected is set by the scanner for a detected threat.
	StatusRejected Status = "rejected"
	// StatusFailed records an upload that could not be completed.
	StatusFailed Status = "failed"
	// StatusDeleted marks an attachment removed from the destination.
	StatusDeleted Status = "deleted"
)

// Valid reports whether the status is one of the closed set the CHECK allows.
func (s Status) Valid() bool {
	switch s {
	case StatusPendingUpload, StatusPendingScan, StatusClean,
		StatusRejected, StatusFailed, StatusDeleted:
		return true
	default:
		return false
	}
}

// Downloadable reports whether content may be served. Only a scanned, clean
// attachment qualifies; every other state, including the initial one, does not.
func (s Status) Downloadable() bool {
	return s == StatusClean
}

// Upload size bounds (RF-32, issue #458).
//
// They are re-exported from libs/go/platform/uploadpolicy rather than restated.
// The effective limit for an upload is an administrative value that chat-service
// owns and this service enforces, so the two modules must agree on the default
// and on the bounds by construction, not by two copies of the same numbers
// staying in sync.
//
// The floor and the ceiling are defensive: a cap below the floor would make the
// feature unusable through a typo, and one above the ceiling would let a single
// request tie up a service instance for an unbounded time and consume unbounded
// storage.
const (
	DefaultMaxUploadBytes = uploadpolicy.DefaultMaxUploadBytes
	MinMaxUploadBytes     = uploadpolicy.MinMaxUploadBytes
	MaxMaxUploadBytes     = uploadpolicy.MaxMaxUploadBytes
)

// MaxFilenameBytes bounds the retained display name. It matches the column
// CHECK, so a name the domain accepts is always storable.
const MaxFilenameBytes = 255

// Listing bounds for a destination's recent attachments (issue #435).
//
// The list is a recency preview, not a paginated archive: there is no cursor,
// so the ceiling is what stops a caller from asking for a whole channel's
// history in one request. A missing or out-of-range limit becomes the default.
const (
	DefaultAttachmentListLimit = 20
	MaxAttachmentListLimit     = 50
)

// NormalizeAttachmentListLimit clamps a client-supplied limit into the closed
// range above. Zero and negative values mean "unspecified" and take the default.
func NormalizeAttachmentListLimit(limit int) int {
	if limit <= 0 {
		return DefaultAttachmentListLimit
	}
	if limit > MaxAttachmentListLimit {
		return MaxAttachmentListLimit
	}
	return limit
}

// Listable reports whether an attachment in this status belongs in a
// destination's file list.
//
// pending_upload and failed are excluded: they are incomplete or abandoned
// uploads, never files a member put in the channel. pending_scan and rejected
// are included so the UI can show that a file exists and what the scan decided
// — neither is downloadable, which Downloadable, not this predicate, decides.
func (s Status) Listable() bool {
	switch s {
	case StatusPendingScan, StatusClean, StatusRejected:
		return true
	default:
		return false
	}
}

// DefaultContentType is served whenever no type could be detected. It is inert.
const DefaultContentType = "application/octet-stream"

// MaxDeclaredMIMEBytes bounds the client-declared type kept for auditing.
const MaxDeclaredMIMEBytes = 255

// NormalizeFilename reduces a client-supplied filename to a display-only label.
//
// The original name is metadata and nothing else: it never becomes a path, a
// directory, a storage key or a command argument — the storage key is derived
// from the server-generated attachment UUID. Normalisation therefore only has
// to make the value safe to store, to log-adjacent code paths, and to echo back
// in a Content-Disposition header:
//
//   - only the final path segment survives, so "../../etc/passwd" becomes
//     "passwd" and no separator remains in the result;
//   - control characters (including NUL) and DEL are dropped, so the value
//     cannot break a header, a terminal or a log line;
//   - surrounding whitespace and dots are trimmed, so "." and ".." cannot
//     survive as a name;
//   - the result is capped at MaxFilenameBytes on a rune boundary.
//
// Invalid UTF-8 is rejected rather than repaired: a name that is not text is
// not a display label.
func NormalizeFilename(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: filename is not valid UTF-8", ErrInvalidInput)
	}

	name := raw
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}

	var builder strings.Builder
	for _, r := range name {
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		builder.WriteRune(r)
	}
	name = strings.Trim(builder.String(), " \t.")

	if name == "" {
		return "", fmt.Errorf("%w: filename is empty", ErrInvalidInput)
	}
	return truncateUTF8(name, MaxFilenameBytes), nil
}

// NormalizeDeclaredMIME keeps the client's Content-Type for auditing without
// trusting it. It is never used to decide how content is served: the detected
// type is. An unusable value degrades to DefaultContentType rather than
// failing the upload, because the declared type carries no authority.
func NormalizeDeclaredMIME(raw string) string {
	var builder strings.Builder
	for _, r := range raw {
		if r == utf8.RuneError || unicode.IsControl(r) || r > unicode.MaxASCII {
			continue
		}
		builder.WriteRune(r)
	}
	value := strings.TrimSpace(builder.String())
	if value == "" {
		return DefaultContentType
	}
	return truncateUTF8(value, MaxDeclaredMIMEBytes)
}

// StorageObjectKey derives the SeaweedFS key for an attachment. It is built
// only from the server-generated UUID, so no client-controlled byte ever
// reaches the storage path, and it is never returned to a client.
func StorageObjectKey(attachmentID uuid.UUID) string {
	return "nchat/attachments/" + attachmentID.String()
}

// StorageProviderSeaweedFS is the only provider this phase persists.
const StorageProviderSeaweedFS = "seaweedfs"

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	truncated := value[:limit]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}
