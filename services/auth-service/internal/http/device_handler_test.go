package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

// ---- mock -------------------------------------------------------------------

type mockDeviceManager struct {
	devices       []domain.DeviceInfo
	policy        domain.DeviceSessionPolicy
	listErr       error
	revokeErr     error
	updateNameErr error

	lastUserID         string
	lastDeviceID       string
	lastDisplayName    string
	lastIncludeRevoked bool
	lastLimit          int
}

func (m *mockDeviceManager) ListDevices(_ context.Context, userID, _ string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	m.lastUserID = userID
	m.lastIncludeRevoked = includeRevoked
	m.lastLimit = limit
	return m.devices, m.policy, m.listErr
}
func (m *mockDeviceManager) RevokeDevice(_ context.Context, deviceID, userID string) error {
	m.lastDeviceID = deviceID
	m.lastUserID = userID
	return m.revokeErr
}
func (m *mockDeviceManager) UpdateDeviceDisplayName(_ context.Context, deviceID, userID, name string) error {
	m.lastDeviceID = deviceID
	m.lastUserID = userID
	m.lastDisplayName = name
	return m.updateNameErr
}
func (m *mockDeviceManager) ValidateActiveSession(_ context.Context, _, _ string) error {
	return nil
}

// ---- GET /auth/me/devices ---------------------------------------------------

func TestGetMyDevices_NoBearer_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(&mockDeviceManager{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestGetMyDevices_ReturnsOnlyOwnDevices(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-dev-1"
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-d1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	now := time.Now().UTC()
	svc := &mockDeviceManager{
		devices: []domain.DeviceInfo{
			{ID: "device-1", LastSeenAt: now, FirstSeenAt: now, SessionCount: 1, Current: true},
		},
		policy: domain.DeviceSessionPolicy{MaxDevicesPerUser: 5},
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastUserID != userID {
		t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				ID           string `json:"id"`
				SessionCount int    `json:"session_count"`
				Current      bool   `json:"current"`
			} `json:"data"`
			Meta struct {
				MaxDevicesPerUser int `json:"max_devices_per_user"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data.Data) != 1 {
		t.Fatalf("expected 1 device, got %d", len(envelope.Data.Data))
	}
	if envelope.Data.Data[0].ID != "device-1" {
		t.Fatalf("unexpected device id: %q", envelope.Data.Data[0].ID)
	}
	if envelope.Data.Data[0].SessionCount != 1 {
		t.Fatalf("expected session_count 1, got %d", envelope.Data.Data[0].SessionCount)
	}
	if envelope.Data.Meta.MaxDevicesPerUser != 5 {
		t.Fatalf("expected max_devices_per_user 5, got %d", envelope.Data.Meta.MaxDevicesPerUser)
	}
}

func TestGetMyDevices_HidesSensitiveFields(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	now := time.Now().UTC()
	svc := &mockDeviceManager{
		devices: []domain.DeviceInfo{
			{ID: "d1", LastIP: "10.0.0.1", FirstSeenAt: now, LastSeenAt: now},
		},
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if containsAny(body, "device_fingerprint_hash", "refresh_token_hash", "password") {
		t.Fatalf("response must not contain sensitive fields: %s", body)
	}
	if containsAny(body, "10.0.0.1") {
		t.Fatalf("raw IP must not appear in response: %s", body)
	}
	if !containsAny(body, "10.0.*.*") {
		t.Fatalf("expected masked IP '10.0.*.*' in response: %s", body)
	}
}

func TestGetMyDevices_IncludeRevokedQueryParam(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	svc := &mockDeviceManager{}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/devices?include_revoked=true", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !svc.lastIncludeRevoked {
		t.Fatal("expected include_revoked=true passed to service")
	}
}

// ---- DELETE /auth/me/devices/{device_id} ------------------------------------

func TestDeleteMyDevice_NoBearer_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(&mockDeviceManager{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/some-id", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestDeleteMyDevice_InvalidUUID_Returns400(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("device_id", "not-a-uuid")

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(&mockDeviceManager{}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "bad_request")
}

func TestDeleteMyDevice_OwnDevice_Returns204(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-revoke-dev"
	accessToken, _, err := tokens.GenerateAccessToken(userID, "sid-curr")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validDeviceID := "123e4567-e89b-12d3-a456-426614174002"
	svc := &mockDeviceManager{}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/"+validDeviceID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("device_id", validDeviceID)

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastUserID != userID {
		t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
	}
}

func TestDeleteMyDevice_CrossUserDevice_Returns404(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validDeviceID := "123e4567-e89b-12d3-a456-426614174003"
	svc := &mockDeviceManager{revokeErr: domain.ErrNotFound}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/"+validDeviceID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("device_id", validDeviceID)

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "not_found")
}

// ---- PATCH /auth/me/devices/{device_id} -------------------------------------

func TestPatchMyDevice_NoBearer_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(&mockDeviceManager{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/some-id", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestPatchMyDevice_InvalidUUID_Returns400(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/bad-id",
		strings.NewReader(`{"display_name":"My Phone"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("device_id", "bad-id")

	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(&mockDeviceManager{}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPatchMyDevice_ValidName_Returns204(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-patch-dev"
	accessToken, _, err := tokens.GenerateAccessToken(userID, "sid-curr")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validDeviceID := "123e4567-e89b-12d3-a456-426614174004"
	svc := &mockDeviceManager{}

	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
		strings.NewReader(`{"display_name":"My Laptop"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("device_id", validDeviceID)

	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastDisplayName != "My Laptop" {
		t.Fatalf("expected display_name 'My Laptop', got %q", svc.lastDisplayName)
	}
	if svc.lastUserID != userID {
		t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
	}
}

func TestPatchMyDevice_RevokedOrCrossUser_Returns404(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validDeviceID := "123e4567-e89b-12d3-a456-426614174005"
	svc := &mockDeviceManager{updateNameErr: domain.ErrNotFound}

	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
		strings.NewReader(`{"display_name":"Test"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("device_id", validDeviceID)

	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestPatchMyDevice_InvalidDisplayName_Returns400(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validDeviceID := "123e4567-e89b-12d3-a456-426614174006"
	svc := &mockDeviceManager{updateNameErr: domain.ErrInvalidInput}

	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
		strings.NewReader(`{"display_name":""}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("device_id", validDeviceID)

	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec.Body.Bytes(), "bad_request")
}

func TestPatchMyDevice_TrailingJSON_Returns400(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validDeviceID := "123e4567-e89b-12d3-a456-426614174007"
	svc := &mockDeviceManager{}
	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
		strings.NewReader(`{"display_name":"Phone"}{}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("device_id", validDeviceID)

	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastDisplayName != "" {
		t.Fatalf("service should not be called for trailing JSON, got display_name %q", svc.lastDisplayName)
	}
}

func TestPatchMyDevice_TooLargeBody_Returns413(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validDeviceID := "123e4567-e89b-12d3-a456-426614174008"
	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
		strings.NewReader(`{"display_name":"`+strings.Repeat("a", 5000)+`"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("device_id", validDeviceID)

	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(&mockDeviceManager{}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec.Body.Bytes(), "request_too_large")
}

func TestGetMyDevices_ServiceUnavailable_Returns503(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestGetMyDevices_InternalError_Returns500(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	svc := &mockDeviceManager{listErr: errors.New("db down")}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal_error")
}

func TestGetMyDevices_ClampsLimitAndSerializesRevokedAt(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	now := time.Now().UTC().Truncate(time.Second)
	revokedAt := now.Add(-time.Minute)
	svc := &mockDeviceManager{
		devices: []domain.DeviceInfo{{ID: "device-revoked", FirstSeenAt: now, LastSeenAt: now, RevokedAt: &revokedAt}},
		policy:  domain.DeviceSessionPolicy{MaxDevicesPerUser: 7},
	}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/devices?limit=500", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastLimit != 100 {
		t.Fatalf("expected limit clamped to 100, got %d", svc.lastLimit)
	}
	body := rec.Body.String()
	if !strings.Contains(body, revokedAt.Format(time.RFC3339)) {
		t.Fatalf("expected revoked_at in response: %s", body)
	}
}

func TestDeleteMyDevice_ServiceUnavailable_Returns503(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	deviceID := "123e4567-e89b-12d3-a456-426614174109"
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/"+deviceID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("device_id", deviceID)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestDeleteMyDevice_InternalError_Returns500(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	deviceID := "123e4567-e89b-12d3-a456-426614174110"
	svc := &mockDeviceManager{revokeErr: errors.New("db down")}
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/"+deviceID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("device_id", deviceID)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal_error")
}

func TestPatchMyDevice_ServiceUnavailable_Returns503(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	deviceID := "123e4567-e89b-12d3-a456-426614174111"
	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+deviceID, strings.NewReader(`{"display_name":"Phone"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("device_id", deviceID)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestPatchMyDevice_InternalError_Returns500(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	deviceID := "123e4567-e89b-12d3-a456-426614174112"
	svc := &mockDeviceManager{updateNameErr: errors.New("db down")}
	handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+deviceID, strings.NewReader(`{"display_name":"Phone"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("device_id", deviceID)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal_error")
}
