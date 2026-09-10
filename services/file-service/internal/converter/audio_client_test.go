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

const validMP3Fixture = "\xff\xfb\x90\x44payload"

func TestClientConvertAudioRejectsOversizedInput(t *testing.T) {
	client, err := NewClient("http://converter", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	oversized := bytes.NewReader(make([]byte, MaxAudioInputBytes+1))
	if _, err := client.ConvertAudio(context.Background(), AudioFormatOgg, oversized); !errors.Is(err, ErrPermanent) {
		t.Fatalf("error = %v, want ErrPermanent", err)
	}
}

func TestClientConvertAudioRejectsANonMP3ContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not mp3"))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConvertAudio(context.Background(), AudioFormatOgg, bytes.NewReader([]byte("source"))); !errors.Is(err, ErrPermanent) {
		t.Fatalf("error = %v, want ErrPermanent", err)
	}
}

// TestClientConvertAudioRejectsAMalformedMP3Body guards the response side the
// same way the sidecar itself guards its own output: a 200 whose body does
// not actually start like MP3 is never handed back to the caller as success,
// even though the Content-Type header claimed audio/mpeg.
func TestClientConvertAudioRejectsAMalformedMP3Body(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not an mp3 frame"))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConvertAudio(context.Background(), AudioFormatOgg, bytes.NewReader([]byte("source"))); !errors.Is(err, ErrPermanent) {
		t.Fatalf("error = %v, want ErrPermanent", err)
	}
}

func TestClientConvertAudioSendsTheFormatHeaderAndTheRightPath(t *testing.T) {
	var gotPath, gotFormat string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFormat = r.Header.Get("X-Audio-Format")
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(validMP3Fixture))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mp3, err := client.ConvertAudio(context.Background(), AudioFormatWav, bytes.NewReader([]byte("source")))
	if err != nil {
		t.Fatalf("ConvertAudio = %v", err)
	}
	if gotPath != "/v1/convert-audio" || gotFormat != "wav" {
		t.Fatalf("path/format = %q/%q", gotPath, gotFormat)
	}
	if string(mp3) != validMP3Fixture {
		t.Fatalf("mp3 = %q", mp3)
	}
}

func TestClientConvertAudioClassifiesResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"success", http.StatusOK, []byte(validMP3Fixture), nil},
		// An ID3v2-tagged MP3 (what a real browser upload of an .mp3 file
		// usually looks like) rather than a bare frame sync word — the other
		// half of validMP3Response's check.
		{"success with ID3 tag", http.StatusOK, append([]byte("ID3"), []byte("\x04\x00\x00\x00\x00\x00\x00payload")...), nil},
		{"blocked", http.StatusUnprocessableEntity, []byte(`{"code":"blocked"}`), ErrBlocked},
		{"invalid", http.StatusUnprocessableEntity, []byte(`{"code":"invalid_audio"}`), ErrPermanent},
		{"timeout", http.StatusGatewayTimeout, []byte(`{"code":"timeout"}`), ErrTransient},
		{"server failure", http.StatusInternalServerError, []byte(`{"code":"conversion_failed"}`), ErrTransient},
		{"oversized", http.StatusOK, append([]byte(validMP3Fixture), make([]byte, MaxAudioOutputBytes+1)...), ErrPermanent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(test.status)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			mp3, err := client.ConvertAudio(context.Background(), AudioFormatOgg, bytes.NewReader([]byte("source")))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want class %v", err, test.want)
			}
			if test.want == nil && !bytes.Equal(mp3, test.body) {
				t.Fatalf("mp3 = %q", mp3)
			}
		})
	}
}

// TestClientConvertAudioClassifiesUnknownErrorCodes mirrors
// TestClientConvertClassifiesUnknownErrorCodes (Convert's own test): a code
// this client does not recognise still gets a class, from the status alone.
func TestClientConvertAudioClassifiesUnknownErrorCodes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"unknown 5xx", http.StatusBadGateway, ErrTransient},
		{"unknown 4xx", http.StatusTeapot, ErrPermanent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"code":"something_unrecognized"}`))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ConvertAudio(context.Background(), AudioFormatOgg, bytes.NewReader([]byte("source"))); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want class %v", err, test.want)
			}
		})
	}
}

// TestClientConvertAudioIsRedirectRefused mirrors
// TestClientConvertsWithoutFollowingRedirects: the sidecar is never expected
// to redirect, and following one anyway would risk sending audio bytes
// somewhere this client never verified.
func TestClientConvertAudioIsRedirectRefused(t *testing.T) {
	redirected := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			redirected = true
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("X-Audio-Format") != "ogg" {
			t.Errorf("format header = %q", r.Header.Get("X-Audio-Format"))
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ConvertAudio(context.Background(), AudioFormatOgg, bytes.NewReader([]byte("source")))
	if !errors.Is(err, ErrTransient) || redirected {
		t.Fatalf("error/redirected = %v/%v", err, redirected)
	}
}

// TestClientConvertAudioClassifiesATransportFailureAsTransient is distinct
// from the canceled-context case below: here the request never even reaches
// a listener (nothing is bound on this port), so ctx.Err() is nil and the
// failure must be classified from the transport error alone.
func TestClientConvertAudioClassifiesATransportFailureAsTransient(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ConvertAudio(context.Background(), AudioFormatOgg, bytes.NewReader([]byte("source")))
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
}

func TestClientConvertAudioReturnsTheContextErrorWhenCanceled(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.ConvertAudio(ctx, AudioFormatOgg, bytes.NewReader([]byte("source")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
