package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
)

const liveKitSignerTestSecret = "livekit-test-secret-with-sufficient-length"

const (
	signerTestEnv      domain.Environment = "production"
	signerTestOtherEnv domain.Environment = "development"
	signerTestIdentity                    = "production:" + serviceTestUserID
)

func TestLiveKitTokenEncoderUsesOfficialSDKWithRoomBoundMinimalGrant(t *testing.T) {
	result, err := encodeLiveKitToken(
		"livekit-test-key",
		liveKitSignerTestSecret,
		signerTestIdentity,
		"",
		"production:channel:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		5*time.Minute,
	)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	verifier, err := auth.ParseAPIToken(result.Token)
	if err != nil {
		t.Fatalf("parse official LiveKit token: %v", err)
	}
	claims, grants, err := verifier.Verify(liveKitSignerTestSecret)
	if err != nil {
		t.Fatalf("verify official LiveKit token: %v", err)
	}
	if grants.Identity != signerTestIdentity || grants.Name != "" || grants.Metadata != "" {
		t.Fatalf("unexpected participant claims: %+v", grants)
	}
	video := grants.Video
	if video == nil || !video.RoomJoin || video.Room != "production:channel:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("token is not room-bound: %+v", video)
	}
	if !video.GetCanPublish() || !video.GetCanSubscribe() {
		t.Fatalf("camera/microphone calls require publish and subscribe: %+v", video)
	}
	if video.GetCanPublishData() || video.GetCanUpdateOwnMetadata() ||
		video.RoomAdmin || video.RoomCreate || video.RoomList || video.RoomRecord {
		t.Fatalf("token has administrative or excessive grants: %+v", video)
	}
	got := video.CanPublishSources
	wantSources := []string{"camera", "microphone", "screen_share"}
	if len(got) != len(wantSources) {
		t.Fatalf("unexpected publish sources: %v", got)
	}
	for _, source := range wantSources {
		if !slices.Contains(got, source) {
			t.Fatalf("expected publish source %q, got %v", source, got)
		}
	}
	if slices.Contains(got, "screen_share_audio") {
		t.Fatalf("screen_share_audio must never be granted: %v", got)
	}
	if grants.SIP != nil || grants.Agent != nil || grants.Inference != nil || grants.Observability != nil {
		t.Fatalf("unexpected non-video grants: %+v", grants)
	}
	wantExpiry := time.Unix(int64(*claims.Expiry), 0).UTC()
	if !result.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("result expiry %v does not match token %v", result.ExpiresAt, wantExpiry)
	}
}

func TestLiveKitTokenEncoderSetsParticipantNameFromDisplayNameNeverIdentity(t *testing.T) {
	result, err := encodeLiveKitToken(
		"livekit-test-key",
		liveKitSignerTestSecret,
		signerTestIdentity,
		"Ana Lima",
		"production:channel:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		5*time.Minute,
	)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	verifier, err := auth.ParseAPIToken(result.Token)
	if err != nil {
		t.Fatalf("parse official LiveKit token: %v", err)
	}
	_, grants, err := verifier.Verify(liveKitSignerTestSecret)
	if err != nil {
		t.Fatalf("verify official LiveKit token: %v", err)
	}
	if grants.Identity != signerTestIdentity {
		t.Fatalf("identity must stay the canonical UUID, got %q", grants.Identity)
	}
	if grants.Name != "Ana Lima" {
		t.Fatalf("expected participant name %q, got %q", "Ana Lima", grants.Name)
	}
}

func TestLiveKitTokenSignerPassesDisplayNameThroughToTheEncoder(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	var gotDisplayName string
	encoder := func(_ string, _ string, _ string, displayName string, _ string, _ time.Duration) (SignedToken, error) {
		gotDisplayName = displayName
		return SignedToken{Token: "token", ExpiresAt: now.Add(time.Minute)}, nil
	}
	signer := mustTestLiveKitSigner(t, func() time.Time { return now }, encoder)

	if _, err := signer.Sign(context.Background(), SignInput{
		Identity:    signerTestIdentity,
		DisplayName: "Pedro Almeida",
		Room:        "production:dm:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		ExpiresAt:   now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if gotDisplayName != "Pedro Almeida" {
		t.Fatalf("expected display name to reach the encoder, got %q", gotDisplayName)
	}
}

func TestLiveKitTokenSignerUsesDeterministicAbsoluteDeadlineAcrossSecondBoundary(t *testing.T) {
	deadline := time.Date(2026, 7, 21, 12, 1, 0, 0, time.UTC)
	tests := []struct {
		name string
		now  time.Time
	}{
		{name: "immediately before second change", now: time.Date(2026, 7, 21, 12, 0, 0, 999_999_999, time.UTC)},
		{name: "immediately after second change", now: time.Date(2026, 7, 21, 12, 0, 1, 1, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTTL time.Duration
			encoder := func(_ string, _ string, _ string, _ string, _ string, ttl time.Duration) (SignedToken, error) {
				gotTTL = ttl
				return SignedToken{
					Token:     "deterministic-token",
					ExpiresAt: tt.now.Add(ttl).Truncate(time.Second),
				}, nil
			}
			signer := mustTestLiveKitSigner(t, func() time.Time { return tt.now }, encoder)

			result, err := signer.Sign(context.Background(), SignInput{
				Identity:  signerTestIdentity,
				Room:      "production:dm:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				ExpiresAt: deadline,
			})
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			if gotTTL != deadline.Sub(tt.now) {
				t.Fatalf("expected duration %v, got %v", deadline.Sub(tt.now), gotTTL)
			}
			if result.ExpiresAt.After(deadline) {
				t.Fatalf("token expiry %v exceeded deadline %v", result.ExpiresAt, deadline)
			}
		})
	}
}

func TestLiveKitTokenSignerRejectsExpiryBeyondAbsoluteDeadline(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 500_000_000, time.UTC)
	deadline := now.Add(time.Minute).Truncate(time.Second)
	encoder := func(_ string, _ string, _ string, _ string, _ string, _ time.Duration) (SignedToken, error) {
		return SignedToken{Token: "overlong", ExpiresAt: deadline.Add(time.Second)}, nil
	}
	signer := mustTestLiveKitSigner(t, func() time.Time { return now }, encoder)

	result, err := signer.Sign(context.Background(), SignInput{
		Identity:  signerTestIdentity,
		Room:      "production:channel:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		ExpiresAt: deadline,
	})
	if err == nil || result.Token != "" {
		t.Fatalf("expected overlong token to fail closed: result=%+v err=%v", result, err)
	}
}

func TestLiveKitTokenSignerRejectsInvalidConfigurationAndInput(t *testing.T) {
	fixedNow := func() time.Time {
		return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	}
	if _, err := NewLiveKitTokenSigner(signerTestEnv, "", liveKitSignerTestSecret, fixedNow); err == nil {
		t.Fatal("expected missing API key to fail")
	}
	if _, err := NewLiveKitTokenSigner(signerTestEnv, "key", "", fixedNow); err == nil {
		t.Fatal("expected missing API secret to fail")
	}
	signer := mustTestLiveKitSigner(t, fixedNow, func(_ string, _ string, _ string, _ string, _ string, _ time.Duration) (SignedToken, error) {
		return SignedToken{Token: "unused"}, nil
	})
	for _, input := range []SignInput{
		{Identity: "production:not-a-uuid", Room: "production:channel:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ExpiresAt: fixedNow().Add(time.Minute)},
		{Identity: serviceTestUserID, Room: "client-room", ExpiresAt: fixedNow().Add(time.Minute)},
		{Identity: signerTestIdentity, Room: "production:dm:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	} {
		if result, err := signer.Sign(context.Background(), input); err == nil || result.Token != "" {
			t.Fatalf("expected invalid sign input to fail without token: input=%+v result=%+v err=%v", input, result, err)
		}
	}
}

func TestLiveKitTokenSignerPropagatesCanceledContextWithoutToken(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	signer := mustTestLiveKitSigner(t, func() time.Time { return now }, func(_ string, _ string, _ string, _ string, _ string, _ time.Duration) (SignedToken, error) {
		t.Fatal("encoder must not be called for a canceled context")
		return SignedToken{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := signer.Sign(ctx, SignInput{
		Identity:  signerTestIdentity,
		Room:      "production:dm:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		ExpiresAt: now.Add(time.Minute),
	})
	if !errors.Is(err, context.Canceled) || result.Token != "" {
		t.Fatalf("expected cancellation without token: result=%+v err=%v", result, err)
	}
}

func mustTestLiveKitSigner(t *testing.T, now func() time.Time, encoder liveKitTokenEncoder) *LiveKitTokenSigner {
	t.Helper()
	signer, err := newLiveKitTokenSigner(signerTestEnv, "key", liveKitSignerTestSecret, now, encoder)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer
}

// The signer is the last structural gate before a JWT exists. It holds its own
// environment, so a TokenService bug cannot talk it into minting a token into
// another deployment's namespace.
func TestLiveKitTokenSignerRefusesValuesFromAnotherEnvironment(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	encoder := func(_, _, _, _, _ string, _ time.Duration) (SignedToken, error) {
		return SignedToken{Token: "token", ExpiresAt: now.Add(time.Minute)}, nil
	}
	signer := mustTestLiveKitSigner(t, func() time.Time { return now }, encoder)

	const validRoom = "production:call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tt := range []struct {
		name     string
		identity string
		room     string
	}{
		{"room from another environment", signerTestIdentity, "development:call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{"identity from another environment", "development:" + serviceTestUserID, validRoom},
		{"pre-namespace room", signerTestIdentity, "call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{"pre-namespace identity", serviceTestUserID, validRoom},
		{"unknown kind", signerTestIdentity, "production:group:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{"invalid room uuid", signerTestIdentity, "production:call:not-a-uuid"},
		{"invalid identity uuid", "production:not-a-uuid", validRoom},
		{"administrative-looking room", signerTestIdentity, "production:admin"},
		{"extra room segment", signerTestIdentity, validRoom + ":extra"},
		{"non-canonical room uuid", signerTestIdentity, "production:call:AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := signer.Sign(context.Background(), SignInput{
				Identity: tt.identity, Room: tt.room, ExpiresAt: now.Add(time.Minute),
			}); err == nil {
				t.Fatalf("signer accepted identity %q room %q", tt.identity, tt.room)
			}
		})
	}
}

func TestLiveKitTokenSignerAcceptsItsOwnEnvironment(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	var gotIdentity, gotRoom string
	encoder := func(_, _, identity, _, room string, _ time.Duration) (SignedToken, error) {
		gotIdentity, gotRoom = identity, room
		return SignedToken{Token: "token", ExpiresAt: now.Add(time.Minute)}, nil
	}
	signer := mustTestLiveKitSigner(t, func() time.Time { return now }, encoder)

	const room = "production:call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := signer.Sign(context.Background(), SignInput{
		Identity: signerTestIdentity, Room: room, ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if gotIdentity != signerTestIdentity || gotRoom != room {
		t.Fatalf("encoder received identity %q room %q", gotIdentity, gotRoom)
	}
}

func TestNewLiveKitTokenSignerRefusesAnInvalidEnvironment(t *testing.T) {
	fixedNow := func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	for _, environment := range []domain.Environment{"", ":", "prod:evil", "PROD", " "} {
		if _, err := NewLiveKitTokenSigner(environment, "key", liveKitSignerTestSecret, fixedNow); err == nil {
			t.Fatalf("signer constructed with environment %q", environment)
		}
	}
	if _, err := NewLiveKitTokenSigner(signerTestOtherEnv, "key", liveKitSignerTestSecret, fixedNow); err != nil {
		t.Fatalf("a valid environment must construct: %v", err)
	}
}
