package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
	"go.opentelemetry.io/otel/trace"
)

const liveKitTokenBodyLimit = 4096

const (
	unavailableCauseIntegrationDisabled     = "integration_disabled"
	unavailableCauseDependenciesUnavailable = "dependencies_unavailable"
	unavailableCauseServiceUnavailable      = "service_unavailable"
)

type LiveKitTokenIssuer interface {
	Issue(context.Context, service.IssueTokenInput) (service.IssuedToken, error)
}

type liveKitTokenRequest struct {
	CallID string `json:"call_id"`
}

func LiveKitToken(issuer LiveKitTokenIssuer, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		principal, ok := AuthenticatedPrincipal(r)
		if !ok {
			writeLiveKitTokenError(w, r, logger, startedAt, "unknown", domain.ErrUnauthorized)
			return
		}
		if issuer == nil {
			writeLiveKitTokenError(w, r, logger, startedAt, "unknown", domain.ErrDependenciesUnavailable)
			return
		}
		if !isJSONRequestContentType(r.Header.Get("Content-Type")) {
			logLiveKitTokenResult(r, logger, startedAt, "unknown", "invalid_content_type", http.StatusUnsupportedMediaType, "")
			httputil.WriteError(w, http.StatusUnsupportedMediaType, httputil.ErrCodeBadRequest, "content type must be application/json")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, liveKitTokenBodyLimit)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request liveKitTokenRequest
		if err := decoder.Decode(&request); err != nil {
			writeLiveKitTokenDecodeError(w, r, logger, startedAt, err)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeLiveKitTokenDecodeError(w, r, logger, startedAt, err)
			return
		}
		callID, err := uuid.Parse(request.CallID)
		if err != nil {
			writeLiveKitTokenError(w, r, logger, startedAt, "call", domain.ErrInvalidInput)
			return
		}

		issued, err := issuer.Issue(r.Context(), service.IssueTokenInput{
			Kind:            domain.ResourceKindCall,
			ResourceID:      callID.String(),
			UserID:          principal.UserID,
			SessionID:       principal.SessionID,
			AccessExpiresAt: principal.AccessExpiresAt,
		})
		if err != nil {
			writeLiveKitTokenError(w, r, logger, startedAt, "call", err)
			return
		}

		logLiveKitTokenResult(r, logger, startedAt, "call", "issued", http.StatusOK, "")
		httputil.WriteJSON(w, http.StatusOK, issued)
	})
}

func LiveKitUnavailable(logger *slog.Logger) http.Handler {
	return liveKitUnavailable(logger, domain.ErrIntegrationDisabled)
}

func LiveKitDependenciesUnavailable(logger *slog.Logger) http.Handler {
	return liveKitUnavailable(logger, domain.ErrDependenciesUnavailable)
}

func liveKitUnavailable(logger *slog.Logger, err error) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLiveKitTokenError(w, r, logger, time.Now(), "unknown", err)
	})
}

func writeLiveKitTokenDecodeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, startedAt time.Time, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		logLiveKitTokenResult(r, logger, startedAt, "unknown", "body_too_large", http.StatusRequestEntityTooLarge, "")
		httputil.WriteError(w, http.StatusRequestEntityTooLarge, httputil.ErrCodeBadRequest, "request body too large")
		return
	}
	logLiveKitTokenResult(r, logger, startedAt, "unknown", "invalid_request", http.StatusBadRequest, "")
	httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
}

func writeLiveKitTokenError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, startedAt time.Time, kind string, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		logLiveKitTokenResult(r, logger, startedAt, kind, "invalid_request", http.StatusBadRequest, "")
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, domain.ErrUnauthorized):
		logLiveKitTokenResult(r, logger, startedAt, kind, "unauthorized", http.StatusUnauthorized, "")
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
	case errors.Is(err, domain.ErrNotFound):
		logLiveKitTokenResult(r, logger, startedAt, kind, "not_found", http.StatusNotFound, "")
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "resource not found")
	case errors.Is(err, domain.ErrUnavailable):
		logLiveKitTokenResult(r, logger, startedAt, kind, "unavailable", http.StatusServiceUnavailable, liveKitUnavailableCause(err))
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "media integration unavailable")
	default:
		logLiveKitTokenResult(r, logger, startedAt, kind, "internal_error", http.StatusInternalServerError, liveKitFailureCause(err))
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func liveKitUnavailableCause(err error) string {
	switch {
	case errors.Is(err, domain.ErrIntegrationDisabled):
		return unavailableCauseIntegrationDisabled
	case errors.Is(err, domain.ErrDependenciesUnavailable):
		return unavailableCauseDependenciesUnavailable
	default:
		return unavailableCauseServiceUnavailable
	}
}

func logLiveKitTokenResult(r *http.Request, logger *slog.Logger, startedAt time.Time, kind, result string, status int, cause string) {
	attrs := []slog.Attr{
		slog.String("request_id", httputil.RequestIDFromContext(r.Context())),
		slog.String("resource_kind", kind),
		slog.String("result", result),
		slog.Int("status", status),
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	}
	if spanContext := trace.SpanContextFromContext(r.Context()); spanContext.IsValid() {
		attrs = append(attrs, slog.String("trace_id", spanContext.TraceID().String()))
	}
	if cause != "" {
		attrs = append(attrs, slog.String("cause", cause))
	}
	level := slog.LevelInfo
	switch {
	case status >= http.StatusInternalServerError && status != http.StatusServiceUnavailable:
		level = slog.LevelError
	case status == http.StatusServiceUnavailable:
		level = slog.LevelWarn
	case status >= http.StatusBadRequest:
		level = slog.LevelDebug
	}
	logger.LogAttrs(r.Context(), level, "LiveKit token request completed", attrs...)
}

func liveKitFailureCause(err error) string {
	switch {
	case errors.Is(err, service.ErrAuthorizationDependency):
		return "storage"
	case errors.Is(err, service.ErrTokenSigning):
		return "signer"
	default:
		return "unexpected"
	}
}

func isJSONRequestContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}
