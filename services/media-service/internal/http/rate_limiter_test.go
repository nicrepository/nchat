package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUserRateLimiterIsPerUserAndResetsAfterWindow(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	limiter := newUserRateLimiter(1, time.Minute, func() time.Time { return now })
	t.Cleanup(limiter.Stop)

	if !limiter.Allow("user-a") || limiter.Allow("user-a") {
		t.Fatal("expected second request for the same user to be limited")
	}
	if !limiter.Allow("user-b") {
		t.Fatal("expected an independent user budget")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow("user-a") {
		t.Fatal("expected budget to reset after the sliding window")
	}
}

func TestUserRateLimiterMiddlewareReturns429(t *testing.T) {
	limiter := newUserRateLimiter(1, time.Minute, time.Now)
	t.Cleanup(limiter.Stop)
	calls := 0
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))

	for i, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil)
		request = request.WithContext(authenticatedRequestContext())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("request %d: expected %d, got %d", i+1, want, response.Code)
		}
		if want == http.StatusTooManyRequests && response.Header().Get("Retry-After") != "60" {
			t.Fatalf("expected Retry-After 60, got %q", response.Header().Get("Retry-After"))
		}
	}
	if calls != 1 {
		t.Fatalf("expected one downstream call, got %d", calls)
	}
}

func TestUserRateLimiterPassesMissingPrincipalToAuthLayer(t *testing.T) {
	limiter := newUserRateLimiter(1, time.Minute, time.Now)
	t.Cleanup(limiter.Stop)
	called := false
	limiter.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil))
	if !called {
		t.Fatal("expected missing principal to pass through for authentication rejection")
	}
}

func TestUserRateLimiterGarbageCollectsOnlyStaleUsers(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	limiter := newUserRateLimiter(2, time.Minute, func() time.Time { return now })
	t.Cleanup(limiter.Stop)
	limiter.Allow("stale")
	now = now.Add(2 * time.Minute)
	limiter.Allow("active")

	limiter.gc()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if _, ok := limiter.entries["stale"]; ok {
		t.Fatal("expected stale entry to be removed")
	}
	if _, ok := limiter.entries["active"]; !ok {
		t.Fatal("expected active entry to remain")
	}
}

func TestUserRateLimiterRejectsInvalidConstruction(t *testing.T) {
	for _, build := range []func(){
		func() { newUserRateLimiter(0, time.Minute, time.Now) },
		func() { newUserRateLimiter(1, 0, time.Now) },
		func() { newUserRateLimiter(1, time.Minute, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("expected invalid limiter construction to panic")
				}
			}()
			build()
		}()
	}
	var limiter *UserRateLimiter
	limiter.Stop()
}
