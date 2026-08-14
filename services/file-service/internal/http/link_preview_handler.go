package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/file-service/internal/linkpreview"
)

const (
	errCodeURLNotAllowed       = "url_not_allowed"
	errCodeUpstreamUnavailable = "upstream_unavailable"
	errCodeUpstreamTimeout     = "upstream_timeout"
	// errCodeMaliciousURL is the RF-21 refusal. It is its own code, separate
	// from url_not_allowed, because the two mean different things to a user: one
	// says this service will not fetch that kind of address, the other says the
	// link is dangerous. A client must be able to tell them apart without
	// reading the message text.
	errCodeMaliciousURL = "malicious_url"
	// errCodeLinkCheckUnavailable says the verdict could not be obtained. It is
	// separate from malicious_url for the same reason: one is permanent, the
	// other is worth retrying.
	errCodeLinkCheckUnavailable = "link_check_unavailable"
	// errCodeLinkCheckPending says the scan was just queued and the preview
	// should be requested again shortly. Distinct from the unavailable code
	// because nothing is broken: URL Scanner is submit-then-poll and the
	// submission has only now happened.
	errCodeLinkCheckPending = "link_check_pending"
)

// maxLinkPreviewRequestBytes bounds the request body. The payload is one URL
// capped at linkpreview.MaxURLLength, so this is that plus room for the JSON
// envelope and nothing more.
const maxLinkPreviewRequestBytes = linkpreview.MaxURLLength + 512

// LinkPreviewUseCase is the service surface the route depends on.
type LinkPreviewUseCase interface {
	Preview(ctx context.Context, rawURL string) (linkpreview.Preview, error)
}

// LinkPreviewHandler serves the RF-10 Open Graph route.
type LinkPreviewHandler struct {
	previews LinkPreviewUseCase
}

func NewLinkPreviewHandler(previews LinkPreviewUseCase) *LinkPreviewHandler {
	return &LinkPreviewHandler{previews: previews}
}

type linkPreviewRequest struct {
	URL string `json:"url"`
}

// Create handles POST /link-preview.
//
// It is a POST rather than a GET with the URL in the query string, and that is
// a deliberate contract choice: the value is user-supplied and points at a
// third party, so keeping it in a body keeps it out of access logs, out of
// referrers and out of intermediary caches. The method also says what the route
// is — an action that causes an outbound request — rather than pretending the
// remote page is a resource of this service.
//
// The response only ever carries normalised metadata. No remote body, header or
// status reaches the client, so the endpoint cannot be used as a proxy.
func (h *LinkPreviewHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.previews == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeServiceUnavailable,
			"link previews are unavailable")
		return
	}
	request, err := decodeLinkPreviewRequest(w, r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest,
			"request body must be a JSON object with a url field")
		return
	}

	preview, err := h.previews.Preview(r.Context(), request.URL)
	if err != nil {
		status, code, message := linkPreviewError(err)
		httputil.WriteError(w, status, code, message)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, preview)
}

// decodeLinkPreviewRequest reads the body as exactly one JSON object.
//
// "Exactly one" is the part worth stating. A decoder stops as soon as it has
// read a complete value, so on its own it accepts `{"url":"..."} anything` and
// a body carrying two objects — the first is used and the rest is silently
// discarded. A request whose trailing half is ignored is not a request this
// service should answer: the caller and the service would disagree about what
// was asked, which is the shape request smuggling takes. So the stream is
// required to be at EOF afterwards, with only whitespace allowed in between
// (the decoder skips that itself).
//
// The byte cap and DisallowUnknownFields still apply, and the second read costs
// nothing extra: it consumes what the capped body already holds rather than
// re-reading it.
func decodeLinkPreviewRequest(w http.ResponseWriter, r *http.Request) (linkPreviewRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLinkPreviewRequestBytes)
	var request linkPreviewRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return linkPreviewRequest{}, err
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return linkPreviewRequest{}, errTrailingRequestData
	}
	return request, nil
}

// errTrailingRequestData marks a body that carried more than the one object the
// contract defines. It never reaches the client: the handler answers with the
// same fixed message every malformed body gets.
var errTrailingRequestData = errors.New("request body carried trailing data")

// linkPreviewError maps a service error onto the response.
//
// Every message is fixed text chosen here. Nothing from the remote side and
// nothing about the destination is repeated: a caller learns that a URL was
// refused, never that it resolved to an address in some range, because the
// difference between those two answers is a map of the internal network.
func linkPreviewError(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, linkpreview.ErrInvalidURL):
		return http.StatusBadRequest, httputil.ErrCodeBadRequest, "url is not valid"
	case errors.Is(err, linkpreview.ErrURLNotAllowed):
		return http.StatusBadRequest, errCodeURLNotAllowed, "url is not allowed"
	case errors.Is(err, linkpreview.ErrMaliciousURL):
		// The verdict itself is the whole answer. Which category the provider
		// reported is not repeated: it would tell whoever is probing exactly
		// which of their domains are already known.
		return http.StatusForbidden, errCodeMaliciousURL, "this link was blocked for security reasons"
	case errors.Is(err, linkpreview.ErrSafetyUnavailable):
		return http.StatusServiceUnavailable, errCodeLinkCheckUnavailable,
			"the link could not be checked for safety, try again"
	case errors.Is(err, linkpreview.ErrSafetyPending):
		// 202: accepted, being checked, come back. No Open Graph fetch has
		// happened and none will until the verdict is in.
		return http.StatusAccepted, errCodeLinkCheckPending,
			"this link is being checked for safety, try again shortly"
	case errors.Is(err, linkpreview.ErrUnsupportedContentType):
		return http.StatusUnsupportedMediaType, errCodeUnsupportedMedia,
			"the linked resource is not an HTML page"
	case errors.Is(err, linkpreview.ErrNoMetadata):
		return http.StatusNotFound, errCodePreviewUnavailable,
			"the linked page has no preview metadata"
	case errors.Is(err, linkpreview.ErrTimeout):
		return http.StatusGatewayTimeout, errCodeUpstreamTimeout,
			"the linked site did not respond in time"
	case errors.Is(err, context.Canceled):
		return http.StatusBadRequest, httputil.ErrCodeBadRequest, "request cancelled"
	default:
		return http.StatusBadGateway, errCodeUpstreamUnavailable,
			"the linked site could not be reached"
	}
}
