package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// multipartOverhead is the slack added to the byte cap so multipart framing
// (boundaries, part headers) cannot make a legitimately sized file fail.
const multipartOverhead = 8 << 10

// uploadFormField is the single accepted multipart field name.
const uploadFormField = "file"

const (
	errCodePayloadTooLarge    = "payload_too_large"
	errCodeUnsupportedMedia   = "unsupported_media_type"
	errCodeServiceUnavailable = "service_unavailable"
	// errCodeNotScanned tells the client the attachment exists and is visible
	// but has not been cleared by the antimalware scan yet. It carries no
	// information the caller did not already have.
	errCodeNotScanned = "file_not_scanned"
	errCodeRangeUnsup = "range_not_supported"
)

// AttachmentUseCases is the service surface the attachment routes depend on.
type AttachmentUseCases interface {
	Upload(ctx context.Context, input service.UploadInput) (service.AttachmentView, error)
	Metadata(ctx context.Context, input service.AttachmentAuthInput) (service.AttachmentView, error)
	Download(ctx context.Context, input service.AttachmentAuthInput) (service.Download, error)
	ListChannelAttachments(ctx context.Context, input service.ListChannelAttachmentsInput) ([]service.AttachmentView, error)
	Ready() bool
}

// listAttachmentsResponse is the listing payload. The array is named rather
// than returned bare so a cursor can be added later without breaking clients.
type listAttachmentsResponse struct {
	Attachments []service.AttachmentView `json:"attachments"`
}

// AttachmentHandler serves the RF-30 upload, metadata and download routes.
type AttachmentHandler struct {
	useCases       AttachmentUseCases
	maxUploadBytes int64
	metrics        *AttachmentMetrics
	logger         *slog.Logger
}

func NewAttachmentHandler(
	useCases AttachmentUseCases,
	maxUploadBytes int64,
	metrics *AttachmentMetrics,
	logger *slog.Logger,
) *AttachmentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AttachmentHandler{
		useCases: useCases, maxUploadBytes: maxUploadBytes,
		metrics: metrics, logger: logger,
	}
}

// Ready reports whether the routes have a usable service behind them.
func (h *AttachmentHandler) Ready() bool {
	return h != nil && h.useCases != nil && h.useCases.Ready() && h.maxUploadBytes > 0
}

// UploadToChannel handles POST /channels/{channelID}/attachments.
func (h *AttachmentHandler) UploadToChannel(w http.ResponseWriter, r *http.Request) {
	h.upload(w, r, domain.DestinationKindChannel, r.PathValue("channelID"))
}

// UploadToConversation handles POST /dm/{conversationID}/attachments.
func (h *AttachmentHandler) UploadToConversation(w http.ResponseWriter, r *http.Request) {
	h.upload(w, r, domain.DestinationKindDM, r.PathValue("conversationID"))
}

// upload streams one multipart file straight into the service. The destination
// kind comes from the route, never from the body, so "exactly one destination"
// holds structurally: there is no request shape that names two.
func (h *AttachmentHandler) upload(
	w http.ResponseWriter, r *http.Request, kind domain.DestinationKind, destinationID string,
) {
	startedAt := time.Now()
	principal, ok := AuthenticatedPrincipal(r)
	if !ok {
		h.failUpload(w, r, startedAt, kind, domain.ErrUnauthorized)
		return
	}
	if !h.Ready() {
		h.failUpload(w, r, startedAt, kind, domain.ErrDependenciesUnavailable)
		return
	}
	destination, err := domain.NewDestination(kind, destinationID)
	if err != nil {
		h.failUpload(w, r, startedAt, kind, err)
		return
	}

	// Cap the whole request body regardless of what Content-Length claims, and
	// before any part is parsed. A body with no Content-Length, a lying one, or
	// a never-ending stream all hit the same wall.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes+multipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		// Covers a non-multipart content type and a multipart one with no
		// boundary; both are the same client mistake.
		h.logUpload(r, startedAt, kind, "unsupported_media_type", http.StatusUnsupportedMediaType, "")
		httputil.WriteError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedMedia,
			"expected multipart/form-data with a single file field")
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		h.failUpload(w, r, startedAt, kind, fmt.Errorf("%w: missing file part", domain.ErrInvalidInput))
		return
	}
	defer func() { _ = part.Close() }()
	if part.FormName() != uploadFormField || part.FileName() == "" {
		h.failUpload(w, r, startedAt, kind,
			fmt.Errorf("%w: expected a single %q file field", domain.ErrInvalidInput, uploadFormField))
		return
	}

	view, err := h.useCases.Upload(r.Context(), service.UploadInput{
		Destination:  destination,
		UserID:       principal.UserID,
		SessionID:    principal.SessionID,
		Filename:     part.FileName(),
		DeclaredMIME: part.Header.Get("Content-Type"),
		Content:      &singleFileReader{part: part, reader: reader},
	})
	if err != nil {
		h.failUpload(w, r, startedAt, kind, err)
		return
	}

	h.metrics.observeUpload("created")
	h.logUploadResult(r, startedAt, kind, "created", http.StatusCreated, view.ID, view.Size)
	httputil.WriteJSON(w, http.StatusCreated, view)
}

// GetMetadata handles GET /attachments/{attachmentID}.
func (h *AttachmentHandler) GetMetadata(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	principal, ok := AuthenticatedPrincipal(r)
	if !ok {
		writeAttachmentError(w, domain.ErrUnauthorized)
		return
	}
	if !h.Ready() {
		writeAttachmentError(w, domain.ErrDependenciesUnavailable)
		return
	}
	view, err := h.useCases.Metadata(r.Context(), service.AttachmentAuthInput{
		AttachmentID: r.PathValue("attachmentID"),
		UserID:       principal.UserID,
		SessionID:    principal.SessionID,
	})
	if err != nil {
		status, code := attachmentErrorStatus(err)
		h.logAttachment(r, startedAt, "metadata", code, status, "")
		writeAttachmentError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, view)
}

// ListChannelAttachments handles GET /channels/{channelID}/attachments.
//
// It returns metadata only — never content and never a token — so a member of a
// channel learns what has been shared there and in what scan state, and nothing
// about how any of it is stored. A channel the caller cannot reach answers 404,
// the same as one that does not exist.
func (h *AttachmentHandler) ListChannelAttachments(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	principal, ok := AuthenticatedPrincipal(r)
	if !ok {
		writeAttachmentError(w, domain.ErrUnauthorized)
		return
	}
	if !h.Ready() {
		writeAttachmentError(w, domain.ErrDependenciesUnavailable)
		return
	}
	limit, ok := parseListLimit(w, r)
	if !ok {
		return
	}
	views, err := h.useCases.ListChannelAttachments(r.Context(), service.ListChannelAttachmentsInput{
		ChannelID: r.PathValue("channelID"),
		UserID:    principal.UserID,
		SessionID: principal.SessionID,
		Limit:     limit,
	})
	if err != nil {
		status, code := attachmentErrorStatus(err)
		h.logAttachment(r, startedAt, "list", code, status, "")
		writeAttachmentError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, listAttachmentsResponse{Attachments: views})
}

// parseListLimit reads the optional ?limit=. An absent value means "the
// default"; a malformed one is rejected rather than silently defaulted, so a
// client never believes it asked for a page size it did not get. The ceiling
// itself is enforced in the domain, not here.
func parseListLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest,
			"limit must be a positive integer")
		return 0, false
	}
	return limit, true
}

// DownloadContent handles GET /attachments/{attachmentID}/content.
//
// Nothing is served inline. Every response carries an attachment disposition
// and nosniff, so an uploaded HTML, SVG or script file is downloaded, never
// rendered in the origin the API is served from.
func (h *AttachmentHandler) DownloadContent(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	principal, ok := AuthenticatedPrincipal(r)
	if !ok {
		writeAttachmentError(w, domain.ErrUnauthorized)
		return
	}
	if !h.Ready() {
		writeAttachmentError(w, domain.ErrDependenciesUnavailable)
		return
	}
	// Range is refused outright rather than partially honoured. Serving a byte
	// range requires seeking into an authenticated chunk stream, which this
	// envelope version does not support; answering a range with the whole body,
	// or with an unverified slice, would be worse than refusing.
	if r.Header.Get("Range") != "" {
		w.Header().Set("Accept-Ranges", "none")
		h.metrics.observeDownload("range_rejected")
		h.logAttachment(r, startedAt, "download", errCodeRangeUnsup, http.StatusRequestedRangeNotSatisfiable, "")
		httputil.WriteError(w, http.StatusRequestedRangeNotSatisfiable, errCodeRangeUnsup,
			"range requests are not supported")
		return
	}

	download, err := h.useCases.Download(r.Context(), service.AttachmentAuthInput{
		AttachmentID: r.PathValue("attachmentID"),
		UserID:       principal.UserID,
		SessionID:    principal.SessionID,
	})
	if err != nil {
		status, code := attachmentErrorStatus(err)
		h.metrics.observeDownload(code)
		h.logAttachment(r, startedAt, "download", code, status, "")
		writeAttachmentError(w, err)
		return
	}
	defer func() { _ = download.Content.Close() }()

	w.Header().Set("Content-Type", download.ContentType)
	w.Header().Set("Content-Disposition", contentDisposition(download.Filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "none")
	// The size is the authenticated plaintext length recorded at upload, so it
	// is only correct while the stream verifies; a mismatch aborts the response
	// below rather than being padded over.
	w.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
	w.WriteHeader(http.StatusOK)

	written, copyErr := io.Copy(w, download.Content)
	if copyErr != nil || written != download.Size {
		// Headers are already committed, so the status cannot be corrected. The
		// short body makes net/http drop the connection without a valid
		// terminator, which is what stops a truncated or tampered object from
		// being accepted as a complete file.
		h.metrics.observeDownload("stream_failed")
		h.logAttachment(r, startedAt, "download", "stream_failed", http.StatusOK, r.PathValue("attachmentID"))
		return
	}
	h.metrics.observeDownload("served")
	h.logAttachment(r, startedAt, "download", "served", http.StatusOK, r.PathValue("attachmentID"))
}

// Unavailable answers every attachment route while the feature is disabled or
// its dependencies are missing, so the routes never silently 404.
func Unavailable(message string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeServiceUnavailable, message)
	})
}

// singleFileReader streams the file part and, on reaching its end, verifies
// that it was the only part. The check happens as part of reading rather than
// after it, so a second file surfaces as a stream failure the upload path
// already compensates for, instead of leaving a stored object behind.
type singleFileReader struct {
	part    io.Reader
	reader  *multipart.Reader
	checked bool
}

func (r *singleFileReader) Read(p []byte) (int, error) {
	n, err := r.part.Read(p)
	if err == nil {
		return n, nil
	}
	return n, r.classify(err)
}

// classify decides what a read failure means. The order matters: the body cap
// wins over everything, a genuine transport failure is never mistaken for a
// clean end, and only a real end of part triggers the extra-part check.
func (r *singleFileReader) classify(err error) error {
	var maxBytesError *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesError):
		return domain.ErrTooLarge
	case !errors.Is(err, io.EOF):
		return err
	case r.hasExtraPart():
		return domain.ErrTooManyFiles
	default:
		return io.EOF
	}
}

// hasExtraPart looks past the file part exactly once, at its end. Reporting the
// extra part as a read failure — rather than after the upload returns — is what
// lets the service compensate instead of leaving a stored object behind.
func (r *singleFileReader) hasExtraPart() bool {
	if r.checked {
		return false
	}
	r.checked = true
	_, err := r.reader.NextPart()
	return !errors.Is(err, io.EOF)
}

// contentDisposition builds a safe attachment header. The filename is already
// normalised (no control characters, no path separators), and it is emitted
// twice: a quoted ASCII fallback with anything unquotable replaced, and an
// RFC 5987 encoded parameter carrying the real name.
func contentDisposition(filename string) string {
	var ascii strings.Builder
	for _, r := range filename {
		// Quotes and backslashes would break out of the quoted-string. ";" and
		// "," are replaced too: they are legal inside quotes, but a client that
		// splits parameters before honouring the quoting would see a second
		// parameter, and the fallback is a lossy display name anyway.
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == ';' || r == ',' {
			ascii.WriteByte('_')
			continue
		}
		ascii.WriteRune(r)
	}
	fallback := ascii.String()
	if fallback == "" {
		fallback = "attachment"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		fallback, encodeExtValue(filename))
}

// encodeExtValue percent-encodes everything outside RFC 5987 attr-char.
//
// net/url's escapers are not usable here: they leave ";" and "'" unescaped,
// and both are delimiters in this header — ";" would end the parameter and let
// a filename inject another one, "'" is the ext-value separator. The allowed
// set below is exactly attr-char, so nothing a filename contains can be read
// as header syntax.
func encodeExtValue(value string) string {
	const upperhex = "0123456789ABCDEF"
	var encoded strings.Builder
	for _, b := range []byte(value) {
		if isAttrChar(b) {
			encoded.WriteByte(b)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(upperhex[b>>4])
		encoded.WriteByte(upperhex[b&0x0f])
	}
	return encoded.String()
}

func isAttrChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}

// attachmentErrorStatus maps a domain error to a status and a sanitised code.
// No branch reveals storage state, SQL detail or whether an id exists.
func attachmentErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, errCodePayloadTooLarge
	case errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, httputil.ErrCodeBadRequest
	case errors.Is(err, domain.ErrUnauthorized):
		return http.StatusUnauthorized, httputil.ErrCodeUnauthorized
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, httputil.ErrCodeNotFound
	case errors.Is(err, domain.ErrNotDownloadable):
		return http.StatusConflict, errCodeNotScanned
	case errors.Is(err, domain.ErrUnavailable):
		return http.StatusServiceUnavailable, errCodeServiceUnavailable
	default:
		return http.StatusInternalServerError, httputil.ErrCodeInternal
	}
}

func writeAttachmentError(w http.ResponseWriter, err error) {
	status, code := attachmentErrorStatus(err)
	httputil.WriteError(w, status, code, attachmentErrorMessage(status))
}

// attachmentErrorMessage keeps response text constant per status. The
// underlying error never reaches the client.
func attachmentErrorMessage(status int) string {
	switch status {
	case http.StatusRequestEntityTooLarge:
		return "file exceeds the configured size limit"
	case http.StatusBadRequest:
		return "invalid upload request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusNotFound:
		return "attachment not found"
	case http.StatusConflict:
		return "attachment is awaiting malware scan"
	case http.StatusServiceUnavailable:
		return "attachment storage unavailable"
	default:
		return "internal error"
	}
}

func (h *AttachmentHandler) failUpload(
	w http.ResponseWriter, r *http.Request, startedAt time.Time, kind domain.DestinationKind, err error,
) {
	status, code := attachmentErrorStatus(err)
	h.metrics.observeUpload(code)
	h.logUpload(r, startedAt, kind, code, status, "")
	writeAttachmentError(w, err)
}

func (h *AttachmentHandler) logUpload(
	r *http.Request, startedAt time.Time, kind domain.DestinationKind,
	result string, status int, attachmentID string,
) {
	h.logUploadResult(r, startedAt, kind, result, status, attachmentID, 0)
}

// logUploadResult records only operational identifiers and outcomes. The
// filename, the multipart body, the Authorization header, the storage key and
// every byte of key material are deliberately absent.
func (h *AttachmentHandler) logUploadResult(
	r *http.Request, startedAt time.Time, kind domain.DestinationKind,
	result string, status int, attachmentID string, size int64,
) {
	attrs := []slog.Attr{
		slog.String("request_id", httputil.RequestIDFromContext(r.Context())),
		slog.String("destination_kind", string(kind)),
		slog.String("result", result),
		slog.Int("status", status),
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	}
	if attachmentID != "" {
		attrs = append(attrs, slog.String("attachment_id", attachmentID))
	}
	if size > 0 {
		attrs = append(attrs, slog.Int64("bytes", size))
	}
	h.logger.LogAttrs(r.Context(), logLevelForStatus(status), "attachment upload completed", attrs...)
}

func (h *AttachmentHandler) logAttachment(
	r *http.Request, startedAt time.Time, operation, result string, status int, attachmentID string,
) {
	attrs := []slog.Attr{
		slog.String("request_id", httputil.RequestIDFromContext(r.Context())),
		slog.String("operation", operation),
		slog.String("result", result),
		slog.Int("status", status),
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	}
	if attachmentID != "" {
		attrs = append(attrs, slog.String("attachment_id", attachmentID))
	}
	h.logger.LogAttrs(r.Context(), logLevelForStatus(status), "attachment request completed", attrs...)
}

func logLevelForStatus(status int) slog.Level {
	switch {
	case status == http.StatusServiceUnavailable:
		return slog.LevelWarn
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}
