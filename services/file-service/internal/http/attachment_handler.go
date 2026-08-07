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
	// errCodeUploadBusy tells a caller they already hold as many concurrent
	// uploads as they may. It is the existing rate-limit code, so clients that
	// already handle 429 need no change.
	errCodeUploadBusy = httputil.ErrCodeRateLimited
	// errCodeNotScanned tells the client the attachment exists and is visible
	// but has not been cleared by the antimalware scan yet. It carries no
	// information the caller did not already have.
	errCodeNotScanned = "file_not_scanned"
	errCodeRangeUnsup = "range_not_supported"
	// errCodePreviewUnavailable tells the client there is no preview to read
	// right now. It says nothing about why: the attachment's own metadata
	// carries previewStatus, so a client already knows whether to wait or to
	// draw its fallback, and this route never has to describe internal state.
	errCodePreviewUnavailable = "preview_not_available"
)

// UploadAdmission bounds how many uploads are in flight across the cluster.
//
// It is asked *after* the caller has been authenticated and authorised and
// *before* the body is read, so a refusal costs nothing and an unauthorised
// caller can never consume a slot. Acquire must not block waiting for capacity:
// waiting would mean holding an unread body open, which is the resource this
// control exists to protect.
//
// The returned release is deferred immediately and is safe to call twice.
type UploadAdmission interface {
	Acquire(ctx context.Context, userID string, reservationBytes int64) (func(), error)
}

// AttachmentUseCases is the service surface the attachment routes depend on.
type AttachmentUseCases interface {
	AuthorizeUpload(ctx context.Context, input service.AuthorizeUploadInput) (service.UploadTarget, error)
	Upload(ctx context.Context, input service.UploadInput) (service.AttachmentView, error)
	Metadata(ctx context.Context, input service.AttachmentAuthInput) (service.AttachmentView, error)
	Download(ctx context.Context, input service.AttachmentAuthInput) (service.Download, error)
	Preview(ctx context.Context, input service.AttachmentAuthInput) (service.Download, error)
	ListDestinationAttachments(ctx context.Context, input service.ListDestinationAttachmentsInput) ([]service.AttachmentView, error)
	Ready() bool
}

// listAttachmentsResponse is the listing payload. The array is named rather
// than returned bare so a cursor can be added later without breaking clients.
type listAttachmentsResponse struct {
	Attachments []service.AttachmentView `json:"attachments"`
}

// AttachmentHandler serves the RF-30 upload, metadata and download routes.
type AttachmentHandler struct {
	useCases AttachmentUseCases
	// maxUploadBytes is the deployment ceiling used to bound the raw request
	// body. See upload for why it is not the RF-32 limit.
	maxUploadBytes int64
	// admission bounds in-flight uploads across the cluster. Nil disables the
	// control, which is only ever the case in tests that construct the handler
	// directly; the wiring in app.go always supplies one.
	admission      UploadAdmission
	retryAfterSecs int
	metrics        *AttachmentMetrics
	logger         *slog.Logger
}

func NewAttachmentHandler(
	useCases AttachmentUseCases,
	maxUploadBytes int64,
	admission UploadAdmission,
	retryAfterSeconds int,
	metrics *AttachmentMetrics,
	logger *slog.Logger,
) *AttachmentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if retryAfterSeconds <= 0 {
		retryAfterSeconds = 30
	}
	return &AttachmentHandler{
		useCases: useCases, maxUploadBytes: maxUploadBytes,
		admission: admission, retryAfterSecs: retryAfterSeconds,
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
//
// The order below is the security-relevant part and is not free to change:
//
//  1. authenticate the bearer token and the session (middleware, upstream);
//  2. resolve the destination from the *path*;
//  3. authorise the caller on that destination, and read its size policy —
//     one database round trip, no body;
//  4. reserve cluster-wide and per-user upload capacity;
//  5. only now cap and read the body.
//
// Nothing before step 5 touches r.Body. That is what makes a refusal cheap: a
// caller with no access, or one arriving at a full cluster, is answered while
// the request is still just headers, so an unauthorised or unlucky client can
// never make the service read — or buffer, or store — anything at all.
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

	// Authorization and the workspace's size policy, both from the destination
	// row, both before the body exists as far as this handler is concerned.
	target, err := h.useCases.AuthorizeUpload(r.Context(), service.AuthorizeUploadInput{
		Destination: destination,
		UserID:      principal.UserID,
		SessionID:   principal.SessionID,
	})
	if err != nil {
		h.failUpload(w, r, startedAt, kind, err)
		return
	}

	release, err := h.acquireAdmission(r.Context(), principal.UserID, target.MaxUploadBytes)
	if err != nil {
		h.failAdmission(w, r, startedAt, kind, err)
		return
	}
	// Deferred immediately: success, any error below, a panic unwinding through
	// here, or a client that hangs up all give the slot back.
	defer release()

	// Cap the whole request body regardless of what Content-Length claims, and
	// before any part is parsed. A body with no Content-Length, a lying one, or
	// a never-ending stream all hit the same wall.
	//
	// This cap is the deployment ceiling, not the RF-32 limit: it bounds the
	// request without ever being the thing that refuses a legitimately sized
	// file — the service's own per-request budget, taken from the policy
	// resolved above, does that on the bytes it actually reads.
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
		Target:       target,
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

// acquireAdmission reserves capacity, or explains why it could not.
//
// A nil admission means the handler was built without the control, which only
// happens in tests that wire it directly; production wiring always supplies
// one, and Ready() refuses to serve an unwired feature.
func (h *AttachmentHandler) acquireAdmission(
	ctx context.Context, userID string, reservationBytes int64,
) (func(), error) {
	if h.admission == nil {
		return func() {}, nil
	}
	return h.admission.Acquire(ctx, userID, reservationBytes)
}

// failAdmission answers a refused upload.
//
// The two exhausted resources get different statuses because they mean
// different things to the caller: their own concurrency is theirs to fix and
// frees up as their transfers finish (429), while a full cluster or an
// admission backend that cannot answer is not (503). Neither response says how
// many slots exist, how many are in use, who holds them, or how many replicas
// there are.
func (h *AttachmentHandler) failAdmission(
	w http.ResponseWriter, r *http.Request, startedAt time.Time, kind domain.DestinationKind, err error,
) {
	status, code, message := http.StatusServiceUnavailable, errCodeServiceUnavailable,
		"upload capacity is temporarily unavailable"
	result := "cluster_at_capacity"
	switch {
	case errors.Is(err, domain.ErrUserAtCapacity):
		status, code = http.StatusTooManyRequests, errCodeUploadBusy
		message, result = "too many concurrent uploads for this user", "user_at_capacity"
	case errors.Is(err, domain.ErrAdmissionUnavailable):
		// Fail closed: not being able to decide is never "admit anyway".
		result = "admission_unavailable"
	case errors.Is(err, domain.ErrUnauthorized):
		h.failUpload(w, r, startedAt, kind, err)
		return
	case ctxCancelled(err):
		result = "admission_cancelled"
	}
	w.Header().Set("Retry-After", strconv.Itoa(h.retryAfterSecs))
	h.metrics.observeUpload(result)
	h.logUpload(r, startedAt, kind, result, status, "")
	httputil.WriteError(w, status, code, message)
}

func ctxCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
func (h *AttachmentHandler) ListChannelAttachments(w http.ResponseWriter, r *http.Request) {
	h.listAttachments(w, r, domain.DestinationKindChannel, r.PathValue("channelID"))
}

// ListConversationAttachments handles GET /dm/{conversationID}/attachments.
func (h *AttachmentHandler) ListConversationAttachments(w http.ResponseWriter, r *http.Request) {
	h.listAttachments(w, r, domain.DestinationKindDM, r.PathValue("conversationID"))
}

// listAttachments serves both listing routes.
//
// It returns metadata only — never content and never a token — so a member of a
// channel or a participant of a conversation learns what has been shared there
// and in what scan state, and nothing about how any of it is stored. A
// destination the caller cannot reach answers 404, the same as one that does
// not exist. The kind comes from the route, never from the request, so the two
// destination spaces can never be crossed.
func (h *AttachmentHandler) listAttachments(
	w http.ResponseWriter, r *http.Request, kind domain.DestinationKind, destinationID string,
) {
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
	views, err := h.useCases.ListDestinationAttachments(r.Context(), service.ListDestinationAttachmentsInput{
		Destination: domain.Destination{Kind: kind, ID: destinationID},
		UserID:      principal.UserID,
		SessionID:   principal.SessionID,
		Limit:       limit,
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
	// Publishing a length is only safe because Download has already returned.
	// The recorded size is part of the wrapped data key's associated data, so
	// reaching this line means the unwrap succeeded against exactly this number:
	// a size_bytes edited in the database fails above, before any status line or
	// header is written, instead of becoming a smaller Content-Length that would
	// let a prefix pass for the whole file.
	//
	// The reader carries the same length as an independent invariant, so a
	// stream that stops producing bytes early still aborts the response below.
	w.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
	w.WriteHeader(http.StatusOK)

	if !streamExactly(w, download) {
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

// GetPreview handles GET /attachments/{attachmentID}/preview.
//
// This is the one response in the service that is served inline, and it is safe
// to be exactly because of what it contains: a JPEG this service encoded from
// pixels it decoded itself. The uploaded bytes are never echoed here, so an
// HTML, SVG or script upload cannot arrive as something a browser will run —
// there is no path by which it becomes a preview at all. nosniff is set anyway,
// so even the declared type cannot be second-guessed.
//
// Authorization is the download's, not a lighter version of it: the same
// visibility query, and the same refusal to serve anything the malware scan has
// not cleared. A preview is a rendering of the file, so it can never be the way
// around a gate the file itself is behind.
func (h *AttachmentHandler) GetPreview(w http.ResponseWriter, r *http.Request) {
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

	preview, err := h.useCases.Preview(r.Context(), service.AttachmentAuthInput{
		AttachmentID: r.PathValue("attachmentID"),
		UserID:       principal.UserID,
		SessionID:    principal.SessionID,
	})
	if err != nil {
		status, code := attachmentErrorStatus(err)
		h.metrics.ObservePreview(code)
		h.logAttachment(r, startedAt, "preview", code, status, "")
		writeAttachmentError(w, err)
		return
	}
	defer func() { _ = preview.Content.Close() }()

	w.Header().Set("Content-Type", preview.ContentType)
	// No filename is offered. The bytes are not the uploaded file, so naming
	// them after it would be wrong, and a preview is meant to be rendered
	// rather than saved.
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "none")
	// Not cached anywhere. A preview is derived from content whose visibility is
	// re-evaluated on every request — losing access to a channel must stop the
	// thumbnails too — and a shared cache has no way to make that decision.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.FormatInt(preview.Size, 10))
	w.WriteHeader(http.StatusOK)

	result := "served"
	if !streamExactly(w, preview) {
		// Headers are already committed, so the status cannot be corrected — the
		// short body is what stops a truncated object being accepted as whole.
		result = "stream_failed"
	}
	h.metrics.ObservePreview(result)
	h.logAttachment(r, startedAt, "preview", result, http.StatusOK, r.PathValue("attachmentID"))
}

// streamExactly copies a decrypted body and reports whether exactly the
// promised number of bytes arrived.
//
// Both callers need the same guarantee: the length was authenticated before any
// header was written, so a stream that produces fewer bytes is an integrity
// failure and the response has to end short rather than complete.
func streamExactly(w io.Writer, download service.Download) bool {
	written, err := io.Copy(w, download.Content)
	return err == nil && written == download.Size
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
	case errors.Is(err, domain.ErrPreviewUnavailable):
		return http.StatusConflict, errCodePreviewUnavailable
	case errors.Is(err, domain.ErrUnavailable):
		return http.StatusServiceUnavailable, errCodeServiceUnavailable
	default:
		return http.StatusInternalServerError, httputil.ErrCodeInternal
	}
}

func writeAttachmentError(w http.ResponseWriter, err error) {
	status, code := attachmentErrorStatus(err)
	httputil.WriteError(w, status, code, attachmentErrorMessage(status, code))
}

// attachmentErrorMessage keeps response text constant per outcome. The
// underlying error never reaches the client.
//
// The code refines the status for the one status that carries two meanings:
// 409 is both "not scanned yet" and "no preview", and a client that asked for a
// preview must not be told the file is awaiting a scan when it is not.
func attachmentErrorMessage(status int, code string) string {
	if code == errCodePreviewUnavailable {
		return "attachment preview is not available"
	}
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
