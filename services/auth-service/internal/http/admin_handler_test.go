package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

type fakeUserCreator struct {
	user domain.User
	err  error
}

func (f *fakeUserCreator) CreateUser(_ context.Context, _ domain.CreateUserInput) (domain.User, error) {
	return f.user, f.err
}

var testUser = domain.User{
	ID: "uuid-1", Email: "user@example.com", DisplayName: "User Name",
	Status: "active", AuthSource: "manual",
	EmailVerifiedAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
}

func postAdminUsers(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminCreateUser_Success_Returns201(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{user: testUser})
	body := `{"email":"user@example.com","display_name":"User Name","initial_password":"Abcdef1!","must_change_password":true}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminCreateUser_ResponseHasNoPasswordHash(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{user: testUser})
	body := `{"email":"user@example.com","display_name":"User","initial_password":"Abcdef1!"}`
	rec := postAdminUsers(t, handler, body)

	raw := rec.Body.String()
	if strings.Contains(raw, "argon2") || strings.Contains(raw, "password_hash") || strings.Contains(raw, "hash") {
		t.Fatalf("response must not leak password hash: %s", raw)
	}
}

func TestAdminCreateUser_ResponseShape(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{user: testUser})
	body := `{"email":"user@example.com","display_name":"User","initial_password":"Abcdef1!"}`
	rec := postAdminUsers(t, handler, body)

	var env struct {
		Data *struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Status      string `json:"status"`
			AuthSource  string `json:"auth_source"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Data == nil {
		t.Fatal("expected data envelope")
	}
	if env.Data.ID != "uuid-1" {
		t.Fatalf("expected id uuid-1, got %q", env.Data.ID)
	}
	if env.Data.Email != "user@example.com" {
		t.Fatalf("expected user@example.com, got %q", env.Data.Email)
	}
	if env.Data.Status != "active" {
		t.Fatalf("expected active, got %q", env.Data.Status)
	}
}

func TestAdminCreateUser_ServiceNil_Returns503(t *testing.T) {
	handler := httpapi.AdminCreateUser(nil)
	rec := postAdminUsers(t, handler, `{}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAdminCreateUser_InvalidJSON_Returns400(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{})
	rec := postAdminUsers(t, handler, `not-json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminCreateUser_DuplicateEmail_Returns409(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{err: domain.ErrDuplicateEmail})
	body := `{"email":"dup@example.com","display_name":"U","initial_password":"P@ss123!"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "conflict")
}

func TestAdminCreateUser_InvalidInput_Returns400(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{
		err: fmt.Errorf("%w: email is required", domain.ErrInvalidInput),
	})
	body := `{"display_name":"U","initial_password":"P@ss123!"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminCreateUser_PasswordPolicy_Returns400(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{
		err: fmt.Errorf("%w: minimum length 12", domain.ErrPasswordPolicy),
	})
	body := `{"email":"u@e.com","display_name":"U","initial_password":"short"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminCreateUser_InternalError_Returns500(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{err: errors.New("db crashed")})
	body := `{"email":"u@e.com","display_name":"U","initial_password":"P@ss123!"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// ── AdminUpdateUserStatus tests ────────────────────────────────────────────

type fakeUserStatusManager struct {
	user domain.User
	err  error
}

func (f *fakeUserStatusManager) UpdateUserStatus(_ context.Context, _, _, _ string) (domain.User, error) {
	return f.user, f.err
}

func patchAdminUserStatus(t *testing.T, handler http.Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/admin/users/"+id+"/status", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", id)
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminUpdateUserStatus_Success_Returns200(t *testing.T) {
	svc := &fakeUserStatusManager{user: domain.User{
		ID: "user-1", Email: "u@example.com", DisplayName: "U",
		Status: "suspended", AuthSource: "manual",
	}}
	handler := httpapi.AdminUpdateUserStatus(svc)
	rec := patchAdminUserStatus(t, handler, "user-1", `{"status":"suspended"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUpdateUserStatus_ServiceNil_Returns503(t *testing.T) {
	handler := httpapi.AdminUpdateUserStatus(nil)
	rec := patchAdminUserStatus(t, handler, "user-1", `{"status":"suspended"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAdminUpdateUserStatus_InvalidJSON_Returns400(t *testing.T) {
	handler := httpapi.AdminUpdateUserStatus(&fakeUserStatusManager{})
	rec := patchAdminUserStatus(t, handler, "user-1", `not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminUpdateUserStatus_UnknownStatus_Returns422(t *testing.T) {
	handler := httpapi.AdminUpdateUserStatus(&fakeUserStatusManager{})
	rec := patchAdminUserStatus(t, handler, "user-1", `{"status":"deleted"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "invalid_status")
}

func TestAdminUpdateUserStatus_NotFound_Returns404(t *testing.T) {
	handler := httpapi.AdminUpdateUserStatus(&fakeUserStatusManager{err: domain.ErrNotFound})
	rec := patchAdminUserStatus(t, handler, "no-id", `{"status":"suspended"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAdminUpdateUserStatus_InvalidTransition_Returns422(t *testing.T) {
	handler := httpapi.AdminUpdateUserStatus(&fakeUserStatusManager{err: domain.ErrStatusTransitionNotAllowed})
	rec := patchAdminUserStatus(t, handler, "user-1", `{"status":"active"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "invalid_transition")
}

func TestAdminUpdateUserStatus_InternalError_Returns500(t *testing.T) {
	handler := httpapi.AdminUpdateUserStatus(&fakeUserStatusManager{err: errors.New("db error")})
	rec := patchAdminUserStatus(t, handler, "user-1", `{"status":"suspended"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestAdminUpdateUserStatus_ResponseHasNoSensitiveFields(t *testing.T) {
	svc := &fakeUserStatusManager{user: domain.User{
		ID: "user-1", Email: "u@example.com", DisplayName: "U", Status: "suspended", AuthSource: "manual",
	}}
	handler := httpapi.AdminUpdateUserStatus(svc)
	rec := patchAdminUserStatus(t, handler, "user-1", `{"status":"suspended"}`)

	raw := rec.Body.String()
	for _, bad := range []string{"password", "hash", "token", "secret"} {
		if strings.Contains(strings.ToLower(raw), bad) {
			t.Fatalf("response must not contain %q: %s", bad, raw)
		}
	}
}

func TestAdminUpdateUserStatus_Forbidden_Returns403(t *testing.T) {
handler := httpapi.AdminUpdateUserStatus(&fakeUserStatusManager{err: domain.ErrForbidden})
rec := patchAdminUserStatus(t, handler, "user-1", `{"status":"suspended"}`)
if rec.Code != http.StatusForbidden {
t.Fatalf("expected 403, got %d", rec.Code)
}
assertErrorCode(t, rec.Body.Bytes(), "forbidden")
}
