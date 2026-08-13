package httpapi

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/media-service/internal/service"
)

func TestLiveKitTokenHandlerAcceptsOnlyCallID(t *testing.T) {
	issuer := &fakeTokenIssuer{result: service.IssuedToken{
		Token: "signed-livekit-token", ExpiresAt: time.Now().UTC().Add(time.Minute),
	}}
	response := serveLiveKitHandler(LiveKitToken(issuer, handlerTestServerURL, slog.Default()),
		`{"call_id":"`+handlerTestResource+`"}`, authenticatedRequestContext())
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if issuer.input.Kind != "call" || issuer.input.ResourceID != handlerTestResource {
		t.Fatalf("unexpected issuer input: %+v", issuer.input)
	}

	for _, body := range []string{
		`{"kind":"dm","id":"` + handlerTestResource + `"}`,
		`{"call_id":"` + handlerTestResource + `","room":"chosen"}`,
		`{"call_id":"` + handlerTestResource + `","identity":"other"}`,
	} {
		issuer.calls = 0
		response := serveLiveKitHandler(LiveKitToken(issuer, handlerTestServerURL, slog.Default()), body, authenticatedRequestContext())
		if response.Code != http.StatusBadRequest || issuer.calls != 0 {
			t.Fatalf("legacy/client-controlled payload accepted: status=%d body=%s", response.Code, body)
		}
	}
}
