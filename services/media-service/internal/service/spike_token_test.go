package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
)

const testLiveKitSecret = "test-livekit-secret-with-sufficient-length"

func TestLiveKitSpikeTokenIssuerIssuesMinimalRoomBoundToken(t *testing.T) {
	issuer, err := NewLiveKitSpikeTokenIssuer(SpikeTokenConfig{
		ServerURL: "ws://127.0.0.1:7880",
		APIKey:    "test-key",
		APISecret: testLiveKitSecret,
		Room:      "spike-1to1",
		TTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	issuedAfter := time.Now().Unix()
	result, err := issuer.Issue("spike-1to1", "browser-a", "Browser A")
	issuedBefore := time.Now().Unix()
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if result.ServerURL != "ws://127.0.0.1:7880" || result.Room != "spike-1to1" || result.Identity != "browser-a" || result.ExpiresInSeconds != 300 {
		t.Fatalf("unexpected token result: %+v", result)
	}

	verifier, err := auth.ParseAPIToken(result.Token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	_, grants, err := verifier.Verify(testLiveKitSecret)
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if grants.Identity != "browser-a" || grants.Name != "Browser A" {
		t.Fatalf("unexpected participant grants: %+v", grants)
	}
	video := grants.Video
	if video == nil || !video.RoomJoin || video.Room != "spike-1to1" || !video.GetCanPublish() || !video.GetCanSubscribe() {
		t.Fatalf("unexpected video grant: %+v", video)
	}
	if video.GetCanPublishData() || video.GetCanUpdateOwnMetadata() || video.RoomAdmin || video.RoomCreate || video.RoomList || video.RoomRecord {
		t.Fatalf("token has excessive grants: %+v", video)
	}
	if got := video.CanPublishSources; len(got) != 2 || got[0] != "camera" || got[1] != "microphone" {
		t.Fatalf("unexpected publish sources: %v", got)
	}

	claims := decodeJWTClaims(t, result.Token)
	if claims.ExpiresAt < issuedAfter+299 || claims.ExpiresAt > issuedBefore+301 {
		t.Fatalf("expected expiration about 300 seconds after issuance, got %d", claims.ExpiresAt)
	}
}

func TestLiveKitSpikeTokenIssuerRejectsInvalidInput(t *testing.T) {
	issuer, err := NewLiveKitSpikeTokenIssuer(SpikeTokenConfig{
		ServerURL: "ws://127.0.0.1:7880",
		APIKey:    "test-key",
		APISecret: testLiveKitSecret,
		Room:      "spike-1to1",
		TTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	tests := []struct {
		name     string
		room     string
		identity string
		display  string
		want     error
	}{
		{name: "different room", room: "other-room", identity: "browser-a", display: "Browser A", want: ErrInvalidSpikeRoom},
		{name: "invalid room characters", room: "bad room", identity: "browser-a", display: "Browser A", want: ErrInvalidSpikeRoom},
		{name: "empty identity", room: "spike-1to1", identity: "", display: "Browser A", want: ErrInvalidSpikeIdentity},
		{name: "identity too long", room: "spike-1to1", identity: strings.Repeat("a", 65), display: "Browser A", want: ErrInvalidSpikeIdentity},
		{name: "invalid identity characters", room: "spike-1to1", identity: "browser/a", display: "Browser A", want: ErrInvalidSpikeIdentity},
		{name: "name too long", room: "spike-1to1", identity: "browser-a", display: strings.Repeat("a", 65), want: ErrInvalidSpikeName},
		{name: "name control character", room: "spike-1to1", identity: "browser-a", display: "Browser\nA", want: ErrInvalidSpikeName},
		{name: "name markup", room: "spike-1to1", identity: "browser-a", display: "<script>", want: ErrInvalidSpikeName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := issuer.Issue(tt.room, tt.identity, tt.display)
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestNewLiveKitSpikeTokenIssuerRejectsMissingConfig(t *testing.T) {
	_, err := NewLiveKitSpikeTokenIssuer(SpikeTokenConfig{})
	if !errors.Is(err, ErrInvalidSpikeConfig) {
		t.Fatalf("expected invalid config, got %v", err)
	}
}

type jwtClaims struct {
	ExpiresAt int64 `json:"exp"`
}

func decodeJWTClaims(t *testing.T, token string) jwtClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected JWT, got %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal JWT claims: %v", err)
	}
	return claims
}
