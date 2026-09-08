package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// maxPushRequestBodyBytes bounds the registration body.
//
// Derived from the payload rather than picked: an endpoint is capped at
// domain.MaxEndpointBytes, the two keys and the device identifier are each far
// smaller, and the remainder is room for the JSON around them. The body is
// capped before it is read, so no request makes this service buffer more than
// this.
const maxPushRequestBodyBytes int64 = domain.MaxEndpointBytes + 1024

// pushSubscriptionService is the use-case surface this handler needs. Narrow on
// purpose: the delivery-result transition is not on it, because no HTTP route
// may reach that.
type pushSubscriptionService interface {
	Register(ctx context.Context, principal domain.Principal, registration domain.Registration) (domain.PushSubscription, error)
	List(ctx context.Context, principal domain.Principal) ([]domain.PushSubscription, error)
	Disable(ctx context.Context, principal domain.Principal, subscriptionID string) error
}

// PushSubscriptionHandler serves the Web Push subscription routes (issue #745).
type PushSubscriptionHandler struct {
	service pushSubscriptionService
}

// NewPushSubscriptionHandler creates the handler over a service.
func NewPushSubscriptionHandler(service pushSubscriptionService) *PushSubscriptionHandler {
	return &PushSubscriptionHandler{service: service}
}

// registerRequest is the whole accepted body.
//
// The four fields a browser's PushSubscription carries and not one more. There is
// no user_id, workspace_id, status, failure_count or timestamp on this type, and
// the decoder refuses any field that is not on it, so a body naming one is
// rejected outright rather than silently ignored.
type registerRequest struct {
	DeviceID string `json:"device_id"`
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// subscriptionView is what a client is told about its own subscription.
//
// The endpoint and both keys are never in it: the client already holds them, and
// a response that repeats them turns every proxy log and error report into a
// place they can be found. Nor is the failure history, which is operational and
// tells a client nothing it can act on.
type subscriptionView struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type listResponse struct {
	Subscriptions []subscriptionView `json:"subscriptions"`
}

func newSubscriptionView(subscription domain.PushSubscription) subscriptionView {
	return subscriptionView{
		ID:         subscription.ID,
		DeviceID:   subscription.DeviceID,
		Status:     string(subscription.Status),
		CreatedAt:  subscription.CreatedAt,
		LastSeenAt: subscription.LastSeenAt,
	}
}

// Register handles POST /api/notifications/push/subscriptions.
//
// Idempotent: re-presenting the same device returns the same subscription, so
// the status is 200 on both the first registration and every retry. A created/
// updated distinction would tell a caller whether a row already existed, which
// is a fact about state they are not asking about.
func (h *PushSubscriptionHandler) Register(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request registerRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	subscription, err := h.service.Register(r.Context(), principal, domain.Registration{
		DeviceID: request.DeviceID,
		Endpoint: request.Endpoint,
		P256dh:   request.P256dh,
		Auth:     request.Auth,
	})
	if err != nil {
		writePushError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, newSubscriptionView(subscription))
}

// List handles GET /api/notifications/push/subscriptions.
//
// Always the caller's own, scoped by the resolved principal. There is no
// parameter that addresses another user's, so there is nothing to authorise
// beyond having a principal at all.
func (h *PushSubscriptionHandler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	subscriptions, err := h.service.List(r.Context(), principal)
	if err != nil {
		writePushError(w, err)
		return
	}
	views := make([]subscriptionView, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		views = append(views, newSubscriptionView(subscription))
	}
	httputil.WriteJSON(w, http.StatusOK, listResponse{Subscriptions: views})
}

// Disable handles DELETE /api/notifications/push/subscriptions/{subscriptionID}.
//
// The identifier in the path is a routing parameter and never an authority: the
// write is scoped by the principal as well, so somebody else's identifier
// matches no row and is answered 404 — the same answer an identifier that never
// existed gets.
func (h *PushSubscriptionHandler) Disable(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	subscriptionID := r.PathValue("subscriptionID")
	if !validSubscriptionID(subscriptionID) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest,
			"subscription_id must be a valid UUID")
		return
	}
	if err := h.service.Disable(r.Context(), principal, subscriptionID); err != nil {
		writePushError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validSubscriptionID keeps a malformed path parameter away from a query that
// would cast it to uuid, where it would raise a database error instead of the
// bad request it is.
func validSubscriptionID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil
}

// requireJSONContentType refuses a body whose declared type is not JSON.
//
// A request that arrives as a form or as text/plain is not one this API agreed
// to parse, and those are also the content types a cross-origin form post can
// send without a preflight.
func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	media, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if !strings.EqualFold(strings.TrimSpace(media), "application/json") {
		httputil.WriteError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedMedia,
			"content-type must be application/json")
		return false
	}
	return true
}

// decodeJSONBody reads the body into the strict request type.
//
// Three properties, all load-bearing:
//
//   - the body is capped before it is read, so no request can make this service
//     buffer an arbitrary amount;
//   - DisallowUnknownFields refuses every field the type does not name, which is
//     what makes mass assignment of user_id, workspace_id, status or the failure
//     counters impossible rather than merely unimplemented;
//   - exactly one JSON value is accepted, so a body carrying a second object
//     after the first is refused instead of half-applied.
//
// One fixed message for every rejection: a malformed body, an unknown field and
// an oversized one are indistinguishable from outside, so the shape of the type
// cannot be mapped by probing it.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPushRequestBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeInvalidBody(w)
		return false
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		writeInvalidBody(w)
		return false
	}
	return true
}

func writeInvalidBody(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
}
