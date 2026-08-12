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

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/linkpreview"
)

// fakeLinkPreviews answers with whatever the test wired, and records what it
// was asked, so the handler can be tested without any network at all.
type fakeLinkPreviews struct {
	preview  linkpreview.Preview
	err      error
	requests []string
}

func (f *fakeLinkPreviews) Preview(_ context.Context, rawURL string) (linkpreview.Preview, error) {
	f.requests = append(f.requests, rawURL)
	return f.preview, f.err
}

// baseConfig is the health-only configuration: uploads off, link previews off.
// The link preview routes must work with uploads disabled, so the harness never
// starts from enabledConfig.
func baseConfig() config.Config {
	return config.Config{
		ServiceName: "file-service", Env: "test", Port: 8083,
		ReadHeaderTimeoutSeconds: 5,
	}
}

func linkPreviewConfig() config.Config {
	cfg := baseConfig()
	cfg.LinkPreviewEnabled = true
	return cfg
}

// assertLinkPreviewResponse mirrors the envelope checks the other route tests
// make: status, JSON content type, and the middleware headers.
func assertLinkPreviewResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("expected status %d, got %d (body %s)", wantStatus, response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json, got %q", got)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID")
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
}

func linkPreviewRouter(t *testing.T, cfg config.Config, previews httpapi.LinkPreviewUseCase) http.Handler {
	t.Helper()
	return httpapi.NewRouter(cfg, platformlog.New("file-service", "test"), httpapi.RouterDependencies{
		TokenValidator: staticValidator{token: testToken},
		LinkPreviews:   previews,
	})
}

func linkPreviewRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, httpapi.RouteLinkPreview, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	return request
}

type linkPreviewEnvelope struct {
	Data  linkpreview.Preview `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeLinkPreview(t *testing.T, response *httptest.ResponseRecorder) linkPreviewEnvelope {
	t.Helper()
	var envelope linkPreviewEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (body %q)", err, response.Body.String())
	}
	return envelope
}

func TestLinkPreviewReturnsMetadata(t *testing.T) {
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{
		URL:         "https://example.com/page",
		Title:       "Example page",
		Description: "What it is about",
		ImageURL:    "https://cdn.example.com/card.png",
		SiteName:    "Example",
	}}
	router := linkPreviewRouter(t, linkPreviewConfig(), previews)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/page"}`))

	assertLinkPreviewResponse(t, response, http.StatusOK)
	envelope := decodeLinkPreview(t, response)
	if envelope.Error != nil {
		t.Fatalf("unexpected error %+v", envelope.Error)
	}
	if envelope.Data.Title != "Example page" || envelope.Data.SiteName != "Example" {
		t.Fatalf("unexpected payload %+v", envelope.Data)
	}
	if len(previews.requests) != 1 || previews.requests[0] != "https://example.com/page" {
		t.Fatalf("unexpected service calls %v", previews.requests)
	}
}

// TestLinkPreviewOmitsAbsentFields keeps the contract honest: a field the page
// did not declare is absent, not an empty string a client has to special-case.
func TestLinkPreviewOmitsAbsentFields(t *testing.T) {
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{
		URL: "https://example.com/page", Title: "Only a title",
	}}
	router := linkPreviewRouter(t, linkPreviewConfig(), previews)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/page"}`))

	assertLinkPreviewResponse(t, response, http.StatusOK)
	for _, field := range []string{"description", "imageUrl", "siteName"} {
		if strings.Contains(response.Body.String(), field) {
			t.Fatalf("expected %q to be omitted, body was %s", field, response.Body.String())
		}
	}
}

// TestLinkPreviewDeliversMetadataAsData is the XSS assertion at the boundary:
// a hostile title is carried as a JSON string value and is never emitted as
// markup, so nothing the remote page wrote can escape the envelope.
func TestLinkPreviewDeliversMetadataAsData(t *testing.T) {
	payload := `<script>alert(1)</script>`
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{
		URL: "https://example.com/page", Title: payload, Description: `<img src=x onerror=alert(1)>`,
	}}
	router := linkPreviewRouter(t, linkPreviewConfig(), previews)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/page"}`))

	assertLinkPreviewResponse(t, response, http.StatusOK)
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json, got %q", got)
	}
	// The value round-trips exactly: it was neither executed, stripped nor
	// double-escaped on the way out.
	envelope := decodeLinkPreview(t, response)
	if envelope.Data.Title != payload {
		t.Fatalf("title: %q", envelope.Data.Title)
	}
	// Go's encoder escapes the angle brackets on the wire, so the raw body
	// carries no <script> a sniffing client could act on.
	if strings.Contains(response.Body.String(), "<script>") {
		t.Fatalf("raw markup reached the wire: %s", response.Body.String())
	}
}

func TestLinkPreviewRequiresAuthentication(t *testing.T) {
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{URL: "https://example.com/"}}
	router := linkPreviewRouter(t, linkPreviewConfig(), previews)

	for name, header := range map[string]string{
		"absent":        "",
		"empty bearer":  "Bearer ",
		"invalid token": "Bearer nope",
		"wrong scheme":  "Basic " + testToken,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, httpapi.RouteLinkPreview,
				strings.NewReader(`{"url":"https://example.com/"}`))
			if header != "" {
				request.Header.Set("Authorization", header)
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			assertLinkPreviewResponse(t, response, http.StatusUnauthorized)
		})
	}
	if len(previews.requests) != 0 {
		t.Fatalf("an unauthenticated request reached the service: %v", previews.requests)
	}
}

func TestLinkPreviewRejectsInvalidPayloads(t *testing.T) {
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{URL: "https://example.com/"}}
	router := linkPreviewRouter(t, linkPreviewConfig(), previews)

	for name, body := range map[string]string{
		"empty body":     "",
		"not json":       "url=https://example.com",
		"array":          `["https://example.com/"]`,
		"unknown field":  `{"url":"https://example.com/","follow":true}`,
		"truncated json": `{"url":"https://example.com/"`,
		"oversized":      `{"url":"https://example.com/` + strings.Repeat("a", 4096) + `"}`,

		// Trailing data. A decoder stops at the end of the first value, so
		// without an explicit EOF check each of these would be answered from
		// its first object and the rest silently discarded — the caller and the
		// service would disagree about what was asked.
		"trailing text":      `{"url":"https://example.com/"} garbage`,
		"trailing brace":     `{"url":"https://example.com/"}}`,
		"second object":      `{"url":"https://example.com/"}{"url":"https://evil.example/"}`,
		"second object nl":   "{\"url\":\"https://example.com/\"}\n{\"url\":\"https://evil.example/\"}",
		"trailing array":     `{"url":"https://example.com/"}["x"]`,
		"trailing scalar":    `{"url":"https://example.com/"} 7`,
		"trailing null":      `{"url":"https://example.com/"} null`,
		"trailing string":    `{"url":"https://example.com/"} "x"`,
		"trailing malformed": `{"url":"https://example.com/"} {`,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()

			router.ServeHTTP(response, linkPreviewRequest(body))

			assertLinkPreviewResponse(t, response, http.StatusBadRequest)
		})
	}
	if len(previews.requests) != 0 {
		t.Fatalf("a malformed request reached the service: %v", previews.requests)
	}
}

// TestLinkPreviewAcceptsSurroundingWhitespace: whitespace is not trailing data.
// The contract is "exactly one JSON object", not "no bytes after the brace".
func TestLinkPreviewAcceptsSurroundingWhitespace(t *testing.T) {
	for name, body := range map[string]string{
		"trailing newline": "{\"url\":\"https://example.com/\"}\n",
		"trailing spaces":  `{"url":"https://example.com/"}   `,
		"trailing crlf":    "{\"url\":\"https://example.com/\"}\r\n",
		"trailing tab":     "{\"url\":\"https://example.com/\"}\t",
		"leading space":    ` {"url":"https://example.com/"}`,
		"surrounded":       "\n\t {\"url\":\"https://example.com/\"} \n",
		"exact eof":        `{"url":"https://example.com/"}`,
	} {
		t.Run(name, func(t *testing.T) {
			previews := &fakeLinkPreviews{preview: linkpreview.Preview{URL: "https://example.com/"}}
			router := linkPreviewRouter(t, linkPreviewConfig(), previews)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, linkPreviewRequest(body))

			assertLinkPreviewResponse(t, response, http.StatusOK)
			if len(previews.requests) != 1 {
				t.Fatalf("expected one service call, got %v", previews.requests)
			}
		})
	}
}

// TestLinkPreviewDoesNotActOnTheFirstOfTwoObjects is the smuggling case stated
// on its own: the first object names a permitted URL and the second names the
// real target. Neither may be fetched.
func TestLinkPreviewDoesNotActOnTheFirstOfTwoObjects(t *testing.T) {
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{URL: "https://example.com/"}}
	router := linkPreviewRouter(t, linkPreviewConfig(), previews)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, linkPreviewRequest(
		`{"url":"https://example.com/"}{"url":"http://169.254.169.254/"}`))

	assertLinkPreviewResponse(t, response, http.StatusBadRequest)
	if len(previews.requests) != 0 {
		t.Fatalf("the service was called for a smuggled body: %v", previews.requests)
	}
}

// TestLinkPreviewMapsServiceErrors pins the whole error contract, and with it
// the rule that no refusal describes the destination.
func TestLinkPreviewMapsServiceErrors(t *testing.T) {
	cases := map[string]struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		"invalid url": {
			linkpreview.ErrInvalidURL, http.StatusBadRequest, "bad_request",
		},
		"blocked destination": {
			fmt.Errorf("%w: destination is not permitted", linkpreview.ErrURLNotAllowed),
			http.StatusBadRequest, "url_not_allowed",
		},
		"not html": {
			linkpreview.ErrUnsupportedContentType, http.StatusUnsupportedMediaType,
			"unsupported_media_type",
		},
		"no metadata": {
			linkpreview.ErrNoMetadata, http.StatusNotFound, "preview_not_available",
		},
		"timeout": {
			linkpreview.ErrTimeout, http.StatusGatewayTimeout, "upstream_timeout",
		},
		"upstream failure": {
			linkpreview.ErrUpstream, http.StatusBadGateway, "upstream_unavailable",
		},
		"unclassified": {
			errors.New("boom"), http.StatusBadGateway, "upstream_unavailable",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			router := linkPreviewRouter(t, linkPreviewConfig(), &fakeLinkPreviews{err: testCase.err})
			response := httptest.NewRecorder()

			router.ServeHTTP(response, linkPreviewRequest(`{"url":"http://10.0.0.1/admin"}`))

			assertLinkPreviewResponse(t, response, testCase.wantStatus)
			envelope := decodeLinkPreview(t, response)
			if envelope.Error == nil || envelope.Error.Code != testCase.wantCode {
				t.Fatalf("expected code %q, got %+v", testCase.wantCode, envelope.Error)
			}
		})
	}
}

// TestLinkPreviewErrorsRevealNoTopology is the "not an oracle" assertion: the
// response must not repeat an address, a hostname or an internal detail, even
// when the service's own error carried one.
func TestLinkPreviewErrorsRevealNoTopology(t *testing.T) {
	previews := &fakeLinkPreviews{
		err: fmt.Errorf("%w: blocked 10.1.2.3:5432 (postgres.internal)", linkpreview.ErrURLNotAllowed),
	}
	router := linkPreviewRouter(t, linkPreviewConfig(), previews)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, linkPreviewRequest(`{"url":"http://postgres.internal/"}`))

	assertLinkPreviewResponse(t, response, http.StatusBadRequest)
	body := response.Body.String()
	for _, forbidden := range []string{"10.1.2.3", "5432", "postgres.internal", "blocked"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestLinkPreviewIsUnavailableWhenDisabled(t *testing.T) {
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{URL: "https://example.com/"}}
	router := linkPreviewRouter(t, baseConfig(), previews)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/"}`))

	// 503 and never 404: a disabled feature must not look like a missing route.
	assertLinkPreviewResponse(t, response, http.StatusServiceUnavailable)
	if len(previews.requests) != 0 {
		t.Fatalf("a request reached a disabled service: %v", previews.requests)
	}
}

func TestLinkPreviewIsUnavailableWithoutDependencies(t *testing.T) {
	router := httpapi.NewRouter(linkPreviewConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{TokenValidator: staticValidator{token: testToken}})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/"}`))

	assertLinkPreviewResponse(t, response, http.StatusServiceUnavailable)
}

func TestLinkPreviewRejectsOtherMethods(t *testing.T) {
	router := linkPreviewRouter(t, linkPreviewConfig(), &fakeLinkPreviews{})

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		request := httptest.NewRequest(method, httpapi.RouteLinkPreview, nil)
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		if response.Code == http.StatusOK {
			t.Fatalf("expected %s to be refused, got 200", method)
		}
	}
}

// TestLinkPreviewIsRateLimited: the route is the only one where a caller
// decides how much outbound work this service does, so the budget must apply.
func TestLinkPreviewIsRateLimited(t *testing.T) {
	const limit = 3
	previews := &fakeLinkPreviews{preview: linkpreview.Preview{URL: "https://example.com/"}}
	limiter := httpapi.NewUserRateLimiter(limit, time.Minute)
	t.Cleanup(limiter.Stop)
	router := httpapi.NewRouter(linkPreviewConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			TokenValidator:         staticValidator{token: testToken},
			LinkPreviews:           previews,
			LinkPreviewRateLimiter: limiter,
		})

	for attempt := range limit {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/"}`))
		if response.Code != http.StatusOK {
			t.Fatalf("attempt %d: expected 200, got %d", attempt, response.Code)
		}
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/"}`))

	assertLinkPreviewResponse(t, response, http.StatusTooManyRequests)
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header")
	}
	if len(previews.requests) != limit {
		t.Fatalf("expected %d requests to reach the service, got %d", limit, len(previews.requests))
	}
}

// TestLinkPreviewDoesNotShareTheUploadBudget: the two limiters are separate, so
// previewing links cannot spend the allowance that protects uploads.
func TestLinkPreviewDoesNotShareTheUploadBudget(t *testing.T) {
	uploadLimiter := httpapi.NewUserRateLimiter(1, time.Minute)
	t.Cleanup(uploadLimiter.Stop)
	previewLimiter := httpapi.NewUserRateLimiter(5, time.Minute)
	t.Cleanup(previewLimiter.Stop)
	router := httpapi.NewRouter(linkPreviewConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			TokenValidator:         staticValidator{token: testToken},
			LinkPreviews:           &fakeLinkPreviews{preview: linkpreview.Preview{URL: "https://example.com/"}},
			RateLimiter:            uploadLimiter,
			LinkPreviewRateLimiter: previewLimiter,
		})

	for attempt := range 3 {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, linkPreviewRequest(`{"url":"https://example.com/"}`))
		if response.Code != http.StatusOK {
			t.Fatalf("attempt %d: expected 200, got %d", attempt, response.Code)
		}
	}
	if !uploadLimiter.Allow(testUserID) {
		t.Fatal("link previews consumed the upload budget")
	}
}
