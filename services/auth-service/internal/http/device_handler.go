package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// DeviceManager is the service interface for device management endpoints.
type DeviceManager interface {
	ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
	RevokeDevice(ctx context.Context, deviceID, userID string) error
	UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

type deviceResponse struct {
	ID           string  `json:"id"`
	DisplayName  *string `json:"display_name"`
	Platform     *string `json:"platform"`
	LastIP       string  `json:"last_ip,omitempty"`
	FirstSeenAt  string  `json:"first_seen_at"`
	LastSeenAt   string  `json:"last_seen_at"`
	RevokedAt    *string `json:"revoked_at"`
	SessionCount int     `json:"session_count"`
	Current      bool    `json:"current"`
}

type devicesMeta struct {
	MaxDevicesPerUser int `json:"max_devices_per_user"`
}

type devicesListResponse struct {
	Data       []deviceResponse        `json:"data"`
	Meta       devicesMeta             `json:"meta"`
	Pagination loginAttemptsPagination `json:"pagination"`
}

type patchDeviceRequest struct {
	DisplayName string `json:"display_name"`
}

// GetMyDevices returns the authenticated user's linked devices.
func GetMyDevices(svc DeviceManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "devices endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}
		currentSessionID := GetContextSessionID(r)

		limit := 50
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > 100 {
			limit = 100
		}
		includeRevoked := r.URL.Query().Get("include_revoked") == "true"

		devices, policy, err := svc.ListDevices(r.Context(), userID, currentSessionID, includeRevoked, limit)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}

		data := make([]deviceResponse, 0, len(devices))
		for _, d := range devices {
			resp := deviceResponse{
				ID:           d.ID,
				DisplayName:  d.DisplayName,
				Platform:     d.Platform,
				LastIP:       maskIPAddress(d.LastIP),
				FirstSeenAt:  d.FirstSeenAt.Format(time.RFC3339),
				LastSeenAt:   d.LastSeenAt.Format(time.RFC3339),
				SessionCount: d.SessionCount,
				Current:      d.Current,
			}
			if d.RevokedAt != nil {
				t := d.RevokedAt.Format(time.RFC3339)
				resp.RevokedAt = &t
			}
			data = append(data, resp)
		}

		httputil.WriteJSON(w, http.StatusOK, devicesListResponse{
			Data:       data,
			Meta:       devicesMeta{MaxDevicesPerUser: policy.MaxDevicesPerUser},
			Pagination: loginAttemptsPagination{Limit: limit},
		})
	})
}

// DeleteMyDevice revokes a device owned by the authenticated user and all its sessions.
// Returns 204 on success or already-revoked own device.
// Returns 404 for unknown or cross-user device.
// Returns 400 for malformed device_id.
func DeleteMyDevice(svc DeviceManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "devices endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		deviceID := r.PathValue("device_id")
		if !isValidUUID(deviceID) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid device_id")
			return
		}

		if err := svc.RevokeDevice(r.Context(), deviceID, userID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "device not found")
				return
			}
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// PatchMyDevice updates the display_name of an active device owned by the authenticated user.
// Returns 204 on success.
// Returns 404 for unknown, revoked, or cross-user device.
// Returns 400 for malformed device_id or invalid display_name.
func PatchMyDevice(svc DeviceManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "devices endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		deviceID := r.PathValue("device_id")
		if !isValidUUID(deviceID) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid device_id")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestBodyBytes)
		var body patchDeviceRequest
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&body); err != nil {
			writeDecodeError(w, err)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeDecodeError(w, err)
			return
		}

		if err := svc.UpdateDeviceDisplayName(r.Context(), deviceID, userID, body.DisplayName); err != nil {
			switch {
			case errors.Is(err, domain.ErrNotFound):
				httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "device not found")
			case errors.Is(err, domain.ErrInvalidInput):
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
			default:
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
