package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

func TestGetMyLoginAttempts_NoBearer_Returns401(t *testing.T) {
	svc := &mockLoginAttemptsManager{}
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestGetMyLoginAttempts_InvalidBearer_Returns401(t *testing.T) {
	svc := &mockLoginAttemptsManager{}
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer invalid.token")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestGetMyLoginAttempts_ValidBearer_ReturnsData(t *testing.T) {
	userID := "user-789"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-123")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts: []domain.LoginAttempt{
			{
				ID:            101,
				Email:         "test@example.com",
				IPAddress:     "192.168.1.100",
				UserAgent:     "Mozilla/5.0",
				FailureReason: "invalid password",
				CreatedAt:     time.Now().UTC(),
			},
		},
		nextCursor: "",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if svc.lastUserID != userID {
		t.Fatalf("expected service to receive userID %q, got %q", userID, svc.lastUserID)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				ID            string `json:"id"`
				Email         string `json:"email"`
				IPAddress     string `json:"ip_address"`
				UserAgent     string `json:"user_agent"`
				FailureReason string `json:"failure_reason"`
				CreatedAt     string `json:"created_at"`
			} `json:"data"`
			Pagination struct {
				Limit      int     `json:"limit"`
				NextCursor *string `json:"next_cursor"`
			} `json:"pagination"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(envelope.Data.Data) != 1 {
		t.Fatalf("expected 1 item, got %d", len(envelope.Data.Data))
	}
	if envelope.Data.Data[0].ID != "101" {
		t.Fatalf("expected id '101', got %q", envelope.Data.Data[0].ID)
	}
}

func TestGetMyLoginAttempts_NextCursorPresent(t *testing.T) {
	userID := "user-999"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-456")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts:   []domain.LoginAttempt{},
		nextCursor: "cursor-abc",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data struct {
			Pagination struct {
				NextCursor *string `json:"next_cursor"`
			} `json:"pagination"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if envelope.Data.Pagination.NextCursor == nil {
		t.Fatal("expected next_cursor to be present")
	}
	if *envelope.Data.Pagination.NextCursor != "cursor-abc" {
		t.Fatalf("expected next_cursor 'cursor-abc', got %q", *envelope.Data.Pagination.NextCursor)
	}
}

func TestGetMyLoginAttempts_NoPasswordFields(t *testing.T) {
	userID := "user-111"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-222")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts: []domain.LoginAttempt{
			{
				ID:            201,
				Email:         "test@example.com",
				IPAddress:     "10.0.0.1",
				UserAgent:     "curl/7.0",
				FailureReason: "rate limited",
				CreatedAt:     time.Now().UTC(),
			},
		},
		nextCursor: "",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if containsAny(body, "password", "token", "hash") {
		t.Fatalf("response should not contain password/token/hash fields: %s", body)
	}
}

func TestGetMyLoginAttempts_IPv4Masked(t *testing.T) {
	userID := "user-333"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-444")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts: []domain.LoginAttempt{
			{
				ID:            301,
				Email:         "test@example.com",
				IPAddress:     "1.2.3.4",
				UserAgent:     "test",
				FailureReason: "invalid",
				CreatedAt:     time.Now().UTC(),
			},
		},
		nextCursor: "",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				IPAddress string `json:"ip_address"`
			} `json:"data"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(envelope.Data.Data) != 1 {
		t.Fatalf("expected 1 item, got %d", len(envelope.Data.Data))
	}
	if envelope.Data.Data[0].IPAddress != "1.2.*.*" {
		t.Fatalf("expected masked IP '1.2.*.*', got %q", envelope.Data.Data[0].IPAddress)
	}
}

func TestGetMyLoginAttempts_IPv6LoopbackMasked(t *testing.T) {
	userID := "user-ipv6"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-ipv6")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts: []domain.LoginAttempt{
			{
				ID:            302,
				Email:         "test@example.com",
				IPAddress:     "::1",
				UserAgent:     "test",
				FailureReason: "invalid",
				CreatedAt:     time.Now().UTC(),
			},
		},
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				IPAddress string `json:"ip_address"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Data[0].IPAddress != "::*" {
		t.Fatalf("expected masked IPv6 '::*', got %q", envelope.Data.Data[0].IPAddress)
	}
}

func TestGetMyLoginAttempts_UserAgentTruncated(t *testing.T) {
	userID := "user-555"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-666")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	longUserAgent := ""
	for i := 0; i < 300; i++ {
		longUserAgent += "A"
	}

	svc := &mockLoginAttemptsManager{
		attempts: []domain.LoginAttempt{
			{
				ID:            401,
				Email:         "test@example.com",
				IPAddress:     "5.6.7.8",
				UserAgent:     longUserAgent,
				FailureReason: "invalid",
				CreatedAt:     time.Now().UTC(),
			},
		},
		nextCursor: "",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				UserAgent string `json:"user_agent"`
			} `json:"data"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(envelope.Data.Data) != 1 {
		t.Fatalf("expected 1 item, got %d", len(envelope.Data.Data))
	}
	if len(envelope.Data.Data[0].UserAgent) > 200 {
		t.Fatalf("expected user agent length <= 200, got %d", len(envelope.Data.Data[0].UserAgent))
	}
}

func TestGetMyLoginAttempts_IDAsString(t *testing.T) {
	userID := "user-777"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-888")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts: []domain.LoginAttempt{
			{
				ID:            9223372036854775807, // max int64
				Email:         "test@example.com",
				IPAddress:     "9.9.9.9",
				UserAgent:     "test",
				FailureReason: "invalid",
				CreatedAt:     time.Now().UTC(),
			},
		},
		nextCursor: "",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(envelope.Data.Data) != 1 {
		t.Fatalf("expected 1 item, got %d", len(envelope.Data.Data))
	}
	if envelope.Data.Data[0].ID != "9223372036854775807" {
		t.Fatalf("expected id as decimal string '9223372036854775807', got %q", envelope.Data.Data[0].ID)
	}
}

func TestGetMyLoginAttempts_InvalidCursor_Returns400(t *testing.T) {
	userID := "user-bad"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-bad")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		returnErr: domain.ErrInvalidInput,
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts?cursor=invalid", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "bad_request")
}

func TestGetMyLoginAttempts_NilService_Returns503(t *testing.T) {
	handler := httpapi.GetMyLoginAttempts(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestGetMyLoginAttempts_MissingContextUser_Returns401(t *testing.T) {
	svc := &mockLoginAttemptsManager{}
	handler := httpapi.GetMyLoginAttempts(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestGetMyLoginAttempts_LimitQueryPassed(t *testing.T) {
	userID := "user-limit"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-limit")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts:   []domain.LoginAttempt{},
		nextCursor: "",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts?limit=25", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if svc.lastLimit != 25 {
		t.Fatalf("expected limit 25 passed to service, got %d", svc.lastLimit)
	}
}

func TestGetMyLoginAttempts_LimitClamped(t *testing.T) {
	userID := "user-limit-clamp"
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-clamp")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	svc := &mockLoginAttemptsManager{
		attempts:   []domain.LoginAttempt{},
		nextCursor: "",
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMyLoginAttempts(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/login-attempts?limit=200", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if svc.lastLimit != 100 {
		t.Fatalf("expected clamped limit 100 passed to service, got %d", svc.lastLimit)
	}

	var body loginAttemptsTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Pagination.Limit != 100 {
		t.Fatalf("expected pagination.limit=100 in response, got %d", body.Data.Pagination.Limit)
	}
}

type loginAttemptsTestResponse struct {
	Data struct {
		Data       []interface{} `json:"data"`
		Pagination struct {
			Limit      int     `json:"limit"`
			NextCursor *string `json:"next_cursor"`
		} `json:"pagination"`
	} `json:"data"`
}

type mockLoginAttemptsManager struct {
	attempts   []domain.LoginAttempt
	nextCursor string
	returnErr  error
	lastUserID string
	lastLimit  int
	lastCursor string
}

func (m *mockLoginAttemptsManager) GetMyAttempts(ctx context.Context, userID string, limit int, cursorStr string) ([]domain.LoginAttempt, string, error) {
	m.lastUserID = userID
	m.lastLimit = limit
	m.lastCursor = cursorStr
	if m.returnErr != nil {
		return nil, "", m.returnErr
	}
	return m.attempts, m.nextCursor, nil
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
