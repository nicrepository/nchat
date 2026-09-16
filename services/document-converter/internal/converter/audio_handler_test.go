package converter

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubAudioConverter struct {
	mp3 []byte
	err error
}

func (r stubAudioConverter) ConvertAudio(context.Context, AudioFormat, []byte) ([]byte, error) {
	return r.mp3, r.err
}

const validMP3Fixture = "\xff\xfb\x90\x44payload"

func TestHandlerConvertsAudioForEachSupportedFormat(t *testing.T) {
	sources := map[AudioFormat][]byte{
		AudioFormatOgg:  append([]byte("OggS"), make([]byte, 10)...),
		AudioFormatWav:  append(append([]byte("RIFF\x00\x00\x00\x00"), []byte("WAVE")...), make([]byte, 4)...),
		AudioFormatWebM: append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 10)...),
		AudioFormatMP4:  append([]byte{0, 0, 0, 0x20}, append([]byte("ftyp"), make([]byte, 10)...)...),
	}
	for format, body := range sources {
		t.Run(string(format), func(t *testing.T) {
			handler := NewHandler(stubRunner{}, WithAudioConverter(stubAudioConverter{mp3: []byte(validMP3Fixture)}))
			req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader(body))
			req.Header.Set("X-Audio-Format", string(format))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "audio/mpeg" {
				t.Fatalf("status/content-type = %d/%q; body=%s", res.Code, res.Header().Get("Content-Type"), res.Body.String())
			}
			if res.Body.String() != validMP3Fixture {
				t.Fatalf("body = %q, want the converter's own MP3 bytes", res.Body.String())
			}
		})
	}
}

func TestHandlerRejectsAnUnconfiguredAudioRoute(t *testing.T) {
	handler := NewHandler(stubRunner{})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader([]byte("OggS")))
	req.Header.Set("X-Audio-Format", "ogg")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.Code)
	}
}

func TestHandlerRejectsAnUnknownAudioFormatHeader(t *testing.T) {
	handler := NewHandler(stubRunner{}, WithAudioConverter(stubAudioConverter{mp3: []byte(validMP3Fixture)}))
	req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader([]byte("OggS")))
	req.Header.Set("X-Audio-Format", "flac")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}
	assertErrorCode(t, res, "blocked")
}

// TestHandlerRejectsAMislabeledAudioBody is the guard against exactly the
// regression this feature exists to prevent on the other side of the wire:
// a request that only claims to be OGG/WAV/WebM/MP4 by header, whose bytes do
// not start with that container's own magic number, is refused before ffmpeg
// ever sees it — never "converted" (i.e. passed through) as if renaming had
// worked.
func TestHandlerRejectsAMislabeledAudioBody(t *testing.T) {
	tests := []struct {
		name   string
		format AudioFormat
		body   []byte
	}{
		{"ogg claims webm bytes", AudioFormatOgg, []byte{0x1A, 0x45, 0xDF, 0xA3, 0, 0, 0, 0}},
		{"wav claims plain text", AudioFormatWav, []byte("not-a-wav-file-at-all")},
		{"webm claims ogg bytes", AudioFormatWebM, append([]byte("OggS"), make([]byte, 8)...)},
		{"mp4 missing ftyp box", AudioFormatMP4, make([]byte, 16)},
		{"empty body", AudioFormatOgg, []byte{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(stubRunner{}, WithAudioConverter(stubAudioConverter{mp3: []byte(validMP3Fixture)}))
			req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader(test.body))
			req.Header.Set("X-Audio-Format", string(test.format))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body=%s", res.Code, res.Body.String())
			}
			assertErrorCode(t, res, "blocked")
		})
	}
}

func TestHandlerClassifiesEveryAudioConverterError(t *testing.T) {
	tests := []struct {
		name       string
		runnerErr  error
		wantStatus int
		wantCode   string
	}{
		{"timeout", ErrTimeout, http.StatusGatewayTimeout, "timeout"},
		{"output too large", ErrOutputTooLarge, http.StatusUnprocessableEntity, "output_too_large"},
		{"blocked", ErrBlocked, http.StatusUnprocessableEntity, "blocked"},
		{"unknown error", errors.New("unexpected"), http.StatusInternalServerError, "conversion_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(stubRunner{}, WithAudioConverter(stubAudioConverter{err: test.runnerErr}))
			req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader(append([]byte("OggS"), make([]byte, 8)...)))
			req.Header.Set("X-Audio-Format", "ogg")
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", res.Code, test.wantStatus, res.Body.String())
			}
			assertErrorCode(t, res, test.wantCode)
		})
	}
}

// TestHandlerRejectsNonMP3OutputFromTheAudioConverter is the guard on the
// output side: even a "successful" runner call is never served as 200 unless
// what it produced actually starts like MP3.
func TestHandlerRejectsNonMP3OutputFromTheAudioConverter(t *testing.T) {
	handler := NewHandler(stubRunner{}, WithAudioConverter(stubAudioConverter{mp3: []byte("not an mp3 at all")}))
	req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader(append([]byte("OggS"), make([]byte, 8)...)))
	req.Header.Set("X-Audio-Format", "ogg")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	assertErrorCode(t, res, "conversion_failed")
}

func TestHandlerRejectsAnOversizedAudioInput(t *testing.T) {
	handler := NewHandler(stubRunner{}, WithAudioConverter(stubAudioConverter{mp3: []byte(validMP3Fixture)}))
	tooLarge := make([]byte, MaxAudioInputBytes+1)
	copy(tooLarge, "OggS")
	req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader(tooLarge))
	req.Header.Set("X-Audio-Format", "ogg")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.Code)
	}
}

func TestHandlerRejectsAnOversizedAudioOutput(t *testing.T) {
	oversized := append([]byte(validMP3Fixture), make([]byte, MaxAudioOutputBytes+1)...)
	handler := NewHandler(stubRunner{}, WithAudioConverter(stubAudioConverter{mp3: oversized}))
	req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewReader(append([]byte("OggS"), make([]byte, 8)...)))
	req.Header.Set("X-Audio-Format", "ogg")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}
	assertErrorCode(t, res, "output_too_large")
}

// TestConvertRouteIsUnaffectedByTheAudioRoute is the regression guard: wiring
// /v1/convert-audio alongside /v1/convert must not change anything about the
// document route's own behaviour.
func TestConvertRouteIsUnaffectedByTheAudioRoute(t *testing.T) {
	body := zipDocument(t, map[string]string{"word/document.xml": "x"})
	handler := NewHandler(stubRunner{pdf: []byte("%PDF-1.7\n%%EOF")}, WithAudioConverter(stubAudioConverter{mp3: []byte(validMP3Fixture)}))
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(body))
	req.Header.Set("X-Document-Format", "docx")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("status/content-type = %d/%q; body=%s", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}
}
