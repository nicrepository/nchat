package httputil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const requestIDHeader = "X-Request-ID"

type requestIDContextKey struct{}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// RequestID adopts an inbound X-Request-ID when the caller sent one, so a trace
// started upstream keeps its identity across services, and generates one
// otherwise.
//
// That makes the value a *correlation hint*, not evidence: any client can
// choose it. Never use it as the identity of a security-relevant record — see
// GeneratedRequestID.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(requestIDHeader)
		if requestID == "" {
			requestID = newRequestID()
		}

		w.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GeneratedRequestID always mints the identifier itself and ignores whatever
// the caller sent.
//
// It exists for surfaces whose request ID ends up in a record someone will
// later rely on — the administrative audit trail is the one this was written
// for. If the caller could choose that value, the subject of an investigation
// would be choosing the identifier the investigation is indexed by: they could
// collide their own entries with an innocent request's, or flood the trail with
// one repeated ID. Neither grants privilege, and both waste the reviewer's
// time, which is the whole point of keeping a trail.
//
// The generated value is also what the response carries, so an operator holding
// a report can find exactly the row it belongs to.
func GeneratedRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				WriteError(w, http.StatusInternalServerError, ErrCodeInternal, "internal error")
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

func MethodNotAllowed(allowed string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != allowed {
			w.Header().Set("Allow", allowed)
			WriteError(w, http.StatusMethodNotAllowed, ErrCodeBadRequest, "method not allowed")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(bytes[:])
}
