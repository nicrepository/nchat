package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
)

const spikeTokenBodyLimit = 1024

type spikeTokenRequest struct {
	Room     string `json:"room"`
	Identity string `json:"identity"`
	Name     string `json:"name,omitempty"`
}

func SpikeToken(issuer service.SpikeTokenIssuer, cfg config.Config, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !spikeOriginAllowed(r.Header.Get("Origin"), cfg.MediaSpikeAllowedOrigins) {
			httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "origin not allowed")
			return
		}
		if issuer == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "media spike is unavailable")
			return
		}
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			httputil.WriteError(w, http.StatusUnsupportedMediaType, httputil.ErrCodeBadRequest, "content type must be application/json")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, spikeTokenBodyLimit)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request spikeTokenRequest
		if err := decoder.Decode(&request); err != nil {
			writeSpikeDecodeError(w, err)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeSpikeDecodeError(w, err)
			return
		}

		roomRef := spikeRoomReference(request.Room)
		logger.Info("LiveKit spike token issuance attempted", "room_ref", roomRef)
		token, err := issuer.Issue(request.Room, request.Identity, request.Name)
		if err != nil {
			if errors.Is(err, service.ErrInvalidSpikeRoom) || errors.Is(err, service.ErrInvalidSpikeIdentity) || errors.Is(err, service.ErrInvalidSpikeName) {
				logger.Warn("LiveKit spike token issuance rejected", "room_ref", roomRef, "reason", "invalid_input")
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid room, identity, or name")
				return
			}
			logger.Error("LiveKit spike token issuance failed", "room_ref", roomRef, "reason", "internal_error")
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}

		logger.Info("LiveKit spike token issued", "room_ref", roomRef)
		httputil.WriteJSON(w, http.StatusOK, token)
	})
}

func writeSpikeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		httputil.WriteError(w, http.StatusRequestEntityTooLarge, httputil.ErrCodeBadRequest, "request body too large")
		return
	}
	httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
}

func spikeOriginAllowed(origin, allowed string) bool {
	if origin == "" {
		return false
	}
	for _, candidate := range strings.Split(allowed, ",") {
		if origin == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func spikeRoomReference(room string) string {
	sum := sha256.Sum256([]byte(room))
	return hex.EncodeToString(sum[:6])
}
