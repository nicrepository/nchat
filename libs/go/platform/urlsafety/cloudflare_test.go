package urlsafety

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Nothing in this file reaches the network. Every exchange is against a local
// server whose response the test decides, so the client's contract is asserted
// rather than the provider's availability.
//
// The contract asserted here is Cloudflare URL Scanner and nothing else. A
// previous round of this feature had the client talking to Domain Intelligence
// (/intel/domain) while the documentation, the Secret template and the runbook
// all described URL Scanner; these tests are what makes that drift fail the
// build instead of surviving a review.

func scannerAgainst(t *testing.T, handler http.HandlerFunc) (*CloudflareScanner, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, "acct-123", "secret-token", server.Client())
	if err != nil {
		t.Fatalf("newCloudflareScanner: %v", err)
	}
	return scanner, server
}

// --- submit ---------------------------------------------------------------

// The whole submit contract in one assertion: method, path, account,
// credentials, content type, the full URL in the body, and Unlisted visibility.
func TestSubmitUsesTheDocumentedURLScannerContract(t *testing.T) {
	var (
		gotMethod, gotPath, gotAuth, gotContentType string
		gotBody                                     submitRequest
		gotHeader                                   http.Header
	)
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotHeader = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid":"182bd5e5-6e1a-4fe4-a799-aa6d9a6ab26e","visibility":"unlisted"}`))
	})

	const target = "https://example.com/redirect?to=https%3A%2F%2Felsewhere.example"
	scanID, err := scanner.SubmitScan(context.Background(), target)

	if err != nil {
		t.Fatalf("SubmitScan: %v", err)
	}
	if scanID != "182bd5e5-6e1a-4fe4-a799-aa6d9a6ab26e" {
		t.Fatalf("scan id: %q", scanID)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method: %q", gotMethod)
	}
	if gotPath != "/accounts/acct-123/urlscanner/v2/scan" {
		t.Fatalf("path: %q", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("authorization: %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type: %q", gotContentType)
	}
	// The full URL, path and query included. A submission that dropped either
	// would be scanning a different resource than the one that was written.
	if gotBody.URL != target {
		t.Fatalf("submitted url: %q", gotBody.URL)
	}
	if gotBody.Visibility != "Unlisted" {
		t.Fatalf("visibility must be Unlisted, got %q", gotBody.Visibility)
	}
	// Nothing of the user's or of NChat's travels with the submission. There is
	// no field for it in the request body, and no header carries one either.
	for _, forbidden := range []string{"Cookie", "X-Forwarded-For", "X-Request-Id", "Referer"} {
		if gotHeader.Get(forbidden) != "" {
			t.Fatalf("%s must not be sent to the provider: %q", forbidden, gotHeader.Get(forbidden))
		}
	}
}

// The provider documents 200 and 201 for an accepted submission.
func TestSubmitAcceptsCreatedAsWellAsOK(t *testing.T) {
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"uuid":"abc"}`))
	})

	if _, err := scanner.SubmitScan(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("201 must be accepted: %v", err)
	}
}

func TestSubmitRefusesUnusableAnswers(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"unauthorized":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"forbidden":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"rate limited":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) },
		"server error":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"empty body":     func(w http.ResponseWriter, _ *http.Request) {},
		"no uuid":        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"message":"ok"}`)) },
		"blank uuid":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"uuid":"  "}`)) },
		"truncated":      func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"uuid":"ab`)) },
		"not json":       func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
		"trailing junk":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"uuid":"ab"}garbage`)) },
		"two documents":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"uuid":"ab"}{"uuid":"cd"}`)) },
		"json array":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[{"uuid":"ab"}]`)) },
		"trailing array": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"uuid":"ab"}[]`)) },
	} {
		t.Run(name, func(t *testing.T) {
			scanner, _ := scannerAgainst(t, handler)

			scanID, err := scanner.SubmitScan(context.Background(), "https://example.com/")

			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("want ErrUnavailable, got %v", err)
			}
			if scanID != "" {
				t.Fatalf("a refused submission produced a scan id: %q", scanID)
			}
		})
	}
}

// Trailing whitespace after the document is not trailing data: a server is free
// to end a body with a newline.
func TestSubmitAcceptsTrailingWhitespace(t *testing.T) {
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{\"uuid\":\"ab\"}\n  \n"))
	})

	if _, err := scanner.SubmitScan(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("a trailing newline is not trailing data: %v", err)
	}
}

// --- result ---------------------------------------------------------------

func TestResultReadsTheDocumentedVerdictField(t *testing.T) {
	for name, testCase := range map[string]struct {
		body string
		want Verdict
	}{
		"malicious": {
			body: `{"task":{"uuid":"182bd5e5-6e1a","success":true},"verdicts":{"overall":{"malicious":true,"categories":["phishing"]}}}`,
			want: VerdictMalicious,
		},
		"benign": {
			body: `{"task":{"uuid":"182bd5e5-6e1a","success":true},"verdicts":{"overall":{"malicious":false,"categories":[]}}}`,
			want: VerdictSafe,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var gotMethod, gotPath, gotAuth string
			scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.body))
			})

			verdict, err := scanner.GetScanResult(context.Background(), "182bd5e5-6e1a")

			if err != nil || verdict != testCase.want {
				t.Fatalf("verdict=%v err=%v", verdict, err)
			}
			if gotMethod != http.MethodGet {
				t.Fatalf("method: %q", gotMethod)
			}
			if gotPath != "/accounts/acct-123/urlscanner/v2/result/182bd5e5-6e1a" {
				t.Fatalf("path: %q", gotPath)
			}
			if gotAuth != "Bearer secret-token" {
				t.Fatalf("authorization: %q", gotAuth)
			}
		})
	}
}

// The documented progress signal: 404 means the scan is still running. It is its
// own error precisely so it cannot be mistaken for "no such scan, give up".
func TestResultReportsPendingOn404(t *testing.T) {
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"message":"not found"}]}`))
	})

	verdict, err := scanner.GetScanResult(context.Background(), "abc")

	if !errors.Is(err, ErrScanPending) {
		t.Fatalf("want ErrScanPending, got %v", err)
	}
	if verdict == VerdictSafe {
		t.Fatal("a scan still running produced a safe verdict")
	}
}

func TestResultRefusesUnusableAnswers(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"unauthorized": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"empty body":   func(w http.ResponseWriter, _ *http.Request) {},
		"not json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`nope`)) },
		"truncated": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"verdicts":{"overall":{"malicious":fals`))
		},
		"trailing junk": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"verdicts":{"overall":{"malicious":false}}}garbage`))
		},
		"two documents": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"verdicts":{"overall":{"malicious":false}}}{"verdicts":{"overall":{"malicious":true}}}`))
		},
		// The fields that decide the block are absent. Absent is not false.
		"no verdicts": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"task":{"uuid":"abc","success":true}}`))
		},
		"no overall": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"verdicts":{}}`))
		},
		"no malicious field": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"verdicts":{"overall":{"categories":[]}}}`))
		},
		// A report the provider marks unsuccessful is not a clean page.
		"task unsuccessful": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"task":{"uuid":"abc","success":false},"verdicts":{"overall":{"malicious":false}}}`))
		},
		// success absent is not success. A truncated write, a proxy envelope or a
		// future response shape all land here, and none of them describe a
		// completed scan.
		"task success absent": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"task":{"uuid":"abc"},"verdicts":{"overall":{"malicious":false}}}`))
		},
		"task absent entirely": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"verdicts":{"overall":{"malicious":false}}}`))
		},
		// Nothing ties this body to a scan.
		"task uuid absent": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"task":{"success":true},"verdicts":{"overall":{"malicious":false}}}`))
		},
		"task uuid blank": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"task":{"uuid":"   ","success":true},"verdicts":{"overall":{"malicious":false}}}`))
		},
		// The one that matters most: a cached, misrouted or substituted response
		// describing a different URL's scan must not clear this one.
		"task uuid mismatched": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"task":{"uuid":"some-other-scan","success":true},"verdicts":{"overall":{"malicious":false}}}`))
		},
		// And a mismatched id must not condemn it either — the verdict simply
		// does not apply.
		"task uuid mismatched and malicious": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"task":{"uuid":"some-other-scan","success":true},"verdicts":{"overall":{"malicious":true}}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			scanner, _ := scannerAgainst(t, handler)

			verdict, err := scanner.GetScanResult(context.Background(), "abc")

			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("want ErrUnavailable, got %v", err)
			}
			// Neither direction: an unusable report yields no verdict at all, so
			// it can be neither cached as safe nor acted on as malicious.
			if verdict.IsFinal() {
				t.Fatalf("%s produced a final verdict: %v", name, verdict)
			}
		})
	}
}

func TestResultRefusesAnEmptyScanID(t *testing.T) {
	var called bool
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"verdicts":{"overall":{"malicious":false}}}`))
	})

	if _, err := scanner.GetScanResult(context.Background(), "  "); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if called {
		t.Fatal("an empty scan id must not reach the provider")
	}
}

// --- transport ------------------------------------------------------------

func TestTimeoutIsAFailure(t *testing.T) {
	// The handler blocks until the test is done with it. Releasing it explicitly
	// — rather than relying on the client's timeout to propagate to the server's
	// request context — is what keeps server.Close from waiting on a handler
	// that never returns. Cleanups run last-registered-first, so this one is
	// registered after server.Close and therefore runs before it.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	scanner, err := newCloudflareScanner(server.URL, "acct", "token", client)
	if err != nil {
		t.Fatalf("newCloudflareScanner: %v", err)
	}

	verdict, err := scanner.GetScanResult(context.Background(), "abc")
	if verdict == VerdictSafe || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("verdict=%v err=%v", verdict, err)
	}
	if _, err := scanner.SubmitScan(context.Background(), "https://example.com/"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("submit timeout: %v", err)
	}
}

func TestContextCancellationIsReported(t *testing.T) {
	// The handler says when the request actually arrived, so the cancel happens
	// after it rather than after a guessed interval. sync.Once because the
	// client is free to retry and call this more than once.
	var arrived sync.Once
	started := make(chan struct{})
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		arrived.Do(func() { close(started) })
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	verdict, err := scanner.GetScanResult(ctx, "abc")
	if verdict == VerdictSafe {
		t.Fatal("a cancelled request produced a safe verdict")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// An oversized body must not be read past the bound, and must not be trusted for
// a verdict either.
func TestOversizedBodyIsRefused(t *testing.T) {
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"verdicts":{"overall":{"malicious":false}},"pad":"`))
		_, _ = w.Write([]byte(strings.Repeat("a", maxResponseBytes+1024)))
	})

	verdict, err := scanner.GetScanResult(context.Background(), "abc")
	if verdict == VerdictSafe || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("verdict=%v err=%v", verdict, err)
	}
}

func TestCredentialsAreRequired(t *testing.T) {
	for name, credentials := range map[string][2]string{
		"no account": {"", "token"},
		"no token":   {"acct", ""},
		"neither":    {" ", " "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newCloudflareScanner("https://example.invalid", credentials[0], credentials[1], nil); err == nil {
				t.Fatal("a scanner that could only ever fail was built")
			}
		})
	}
}

// decodeExactlyOne is the guard behind every "trailing junk" case above; this
// asserts it directly so its contract is readable without a server.
func TestDecodeExactlyOne(t *testing.T) {
	for name, testCase := range map[string]struct {
		body    string
		wantErr bool
	}{
		"one document":        {body: `{"uuid":"a"}`},
		"trailing newline":    {body: "{\"uuid\":\"a\"}\n"},
		"trailing garbage":    {body: `{"uuid":"a"}x`, wantErr: true},
		"two documents":       {body: `{"uuid":"a"}{"uuid":"b"}`, wantErr: true},
		"trailing empty json": {body: `{"uuid":"a"}{}`, wantErr: true},
		"empty":               {body: ``, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			var got submitResponse
			err := decodeExactlyOne(io.NopCloser(strings.NewReader(testCase.body)), &got)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, testCase.wantErr)
			}
		})
	}
}
