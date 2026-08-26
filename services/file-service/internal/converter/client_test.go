package converter

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClientRejectsUnsafeURLs(t *testing.T) {
	for _, raw := range []string{"", "document-converter:8089", "ftp://converter", "http://user:pass@converter", "http://converter/path"} {
		if _, err := NewClient(raw, time.Second); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
}

func TestClientConvertsWithoutFollowingRedirects(t *testing.T) {
	redirected := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			redirected = true
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("X-Document-Format") != "docx" {
			t.Errorf("format header = %q", r.Header.Get("X-Document-Format"))
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Convert(context.Background(), FormatDOCX, bytes.NewReader([]byte("document")))
	if !errors.Is(err, ErrTransient) || redirected {
		t.Fatalf("error/redirected = %v/%v", err, redirected)
	}
}

func TestClientClassifiesResponsesAndLimitsPDF(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"success", http.StatusOK, []byte("%PDF-1.7\n%%EOF"), nil},
		{"blocked", http.StatusUnprocessableEntity, []byte(`{"code":"blocked"}`), ErrBlocked},
		{"invalid", http.StatusUnprocessableEntity, []byte(`{"code":"invalid_document"}`), ErrPermanent},
		{"timeout", http.StatusGatewayTimeout, []byte(`{"code":"timeout"}`), ErrTransient},
		{"server failure", http.StatusInternalServerError, []byte(`{"code":"conversion_failed"}`), ErrTransient},
		{"oversized", http.StatusOK, make([]byte, MaxPDFBytes+1), ErrPermanent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/pdf")
				w.WriteHeader(test.status)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			pdf, err := client.Convert(context.Background(), FormatDOCX, bytes.NewReader([]byte("document")))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want class %v", err, test.want)
			}
			if test.want == nil && !bytes.Equal(pdf, test.body) {
				t.Fatalf("pdf = %q", pdf)
			}
		})
	}
}
