package converter

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type barrierAudio struct {
	entered, release chan struct{}
	err              error
}

func (b *barrierAudio) ConvertAudio(ctx context.Context, _ AudioFormat, _ []byte) ([]byte, error) {
	b.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
	}
	return []byte("ID3audio"), b.err
}
func audioAdmissionRequest(ctx context.Context) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/convert-audio", bytes.NewBufferString("OggS-input")).WithContext(ctx)
	req.Header.Set("X-Audio-Format", "ogg")
	return req
}
func TestAudioHandlerAdmission(t *testing.T) {
	for _, failure := range []error{nil, ErrConversionFailed, context.Canceled} {
		b := &barrierAudio{entered: make(chan struct{}, 2), release: make(chan struct{}), err: failure}
		handler := NewHandler(nil, WithAudioConverter(b))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { res := httptest.NewRecorder(); handler.ServeHTTP(res, audioAdmissionRequest(ctx)); done <- res }()
		<-b.entered
		busy := httptest.NewRecorder()
		handler.ServeHTTP(busy, audioAdmissionRequest(context.Background()))
		if busy.Code != 503 {
			t.Fatalf("busy status: %d", busy.Code)
		}
		assertErrorCode(t, busy, "busy")
		if errors.Is(failure, context.Canceled) {
			cancel()
		} else {
			close(b.release)
		}
		res := <-done
		cancel()
		if errors.Is(failure, context.Canceled) {
			close(b.release)
		}
		if failure == nil && res.Code != 200 {
			t.Fatalf("conversion: %d", res.Code)
		}
		// A new request reaches the converter, proving admission was released.
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
		next := httptest.NewRecorder()
		handler.ServeHTTP(next, audioAdmissionRequest(ctx))
		if next.Code != 408 {
			t.Fatalf("canceled: %d", next.Code)
		}
		ready := httptest.NewRecorder()
		handler.ServeHTTP(ready, audioAdmissionRequest(context.Background()))
		if ready.Code == 503 {
			t.Fatal("slot leaked")
		}
	}
}
