package httpapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func TestExpensiveAttachmentRoutesAreRateLimited(t *testing.T) {
	for _, route := range []struct {
		method, suffix string
		status         int
	}{{http.MethodGet, "/audio.mp3", http.StatusPartialContent}, {http.MethodPost, "/document-preview/regenerate", http.StatusNoContent}} {
		t.Run(route.suffix, func(t *testing.T) {
			limiter := httpapi.NewUserRateLimiter(2, time.Hour)
			t.Cleanup(limiter.Stop)
			cases := &derivativeUseCases{fakeUseCases: readyUseCases(), derived: service.Download{Filename: "voice.mp3", ContentType: "audio/mpeg", Size: 3, Content: seekableContent([]byte("MP3"))}}
			router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"), httpapi.RouterDependencies{TokenValidator: costRouteValidator{}, Attachments: cases, RateLimiter: limiter})
			path := "/attachments/" + uuid.NewString() + route.suffix
			request := func(auth bool) *http.Request {
				r := httptest.NewRequest(route.method, path, nil)
				if auth {
					r.Header.Set("Authorization", "Bearer "+testToken)
				}
				r.Header.Set("Range", "bytes=0-0")
				return r
			}
			unauthorized := httptest.NewRecorder()
			router.ServeHTTP(unauthorized, request(false))
			if unauthorized.Code != 401 {
				t.Fatalf("anonymous: %d", unauthorized.Code)
			}
			for i := 0; i < 2; i++ {
				res := httptest.NewRecorder()
				router.ServeHTTP(res, request(true))
				if res.Code != route.status {
					t.Fatalf("burst %d: %d %s", i, res.Code, res.Body.String())
				}
			}
			res := httptest.NewRecorder()
			router.ServeHTTP(res, request(true))
			if res.Code != 429 || res.Header().Get("Retry-After") == "" {
				t.Fatalf("rate limit: %d", res.Code)
			}
			// The existing limiter keys authenticated users, not attachment identifiers.
			other := request(true)
			other.Header.Set("Authorization", "Bearer second-token")
			otherResponse := httptest.NewRecorder()
			router.ServeHTTP(otherResponse, other)
			if otherResponse.Code != route.status {
				t.Fatalf("independent user budget: %d", otherResponse.Code)
			}
		})
	}
}

func TestMP3AdmissionUnavailableIs503(t *testing.T) {
	cases := &derivativeUseCases{fakeUseCases: readyUseCases(), err: domain.ErrUnavailable}
	router := newTestRouter(t, cases, enabledConfig())
	req := httptest.NewRequest(http.MethodGet, "/attachments/"+uuid.NewString()+"/audio.mp3", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != 503 {
		t.Fatalf("busy: %d %s", res.Code, res.Body.String())
	}
}

type costRouteValidator struct{}

func (costRouteValidator) ValidateAccessToken(raw string) (httpapi.Principal, error) {
	if raw == testToken {
		return staticValidator{token: testToken}.ValidateAccessToken(raw)
	}
	if raw == "second-token" {
		return httpapi.Principal{UserID: "other-user", SessionID: testSessionID, AccessExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	return httpapi.Principal{}, errors.New("invalid token")
}
