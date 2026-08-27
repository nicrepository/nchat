package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
)

type LiveKitTokenSigner struct {
	environment domain.Environment
	apiKey      string
	apiSecret   string
	now         func() time.Time
	encode      liveKitTokenEncoder
}

type liveKitTokenEncoder func(apiKey, apiSecret, identity, displayName, room string, ttl time.Duration) (SignedToken, error)

// NewLiveKitTokenSigner binds a signer to one validated environment.
func NewLiveKitTokenSigner(environment domain.Environment, apiKey, apiSecret string, now func() time.Time) (*LiveKitTokenSigner, error) {
	return newLiveKitTokenSigner(environment, apiKey, apiSecret, now, encodeLiveKitToken)
}

func newLiveKitTokenSigner(environment domain.Environment, apiKey, apiSecret string, now func() time.Time, encoder liveKitTokenEncoder) (*LiveKitTokenSigner, error) {
	environment, err := domain.ParseEnvironment(environment.String())
	if err != nil {
		return nil, fmt.Errorf("LiveKit environment is invalid: %w", err)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("LiveKit API key is required")
	}
	if apiSecret == "" {
		return nil, fmt.Errorf("LiveKit API secret is required")
	}
	if now == nil {
		now = time.Now
	}
	if encoder == nil {
		return nil, fmt.Errorf("LiveKit token encoder is required")
	}
	return &LiveKitTokenSigner{
		environment: environment,
		apiKey:      strings.TrimSpace(apiKey), apiSecret: apiSecret,
		now: now, encode: encoder,
	}, nil
}

func (s *LiveKitTokenSigner) Sign(ctx context.Context, input SignInput) (SignedToken, error) {
	if err := ctx.Err(); err != nil {
		return SignedToken{}, err
	}
	if _, err := domain.ParseParticipantIdentity(s.environment, input.Identity); err != nil {
		return SignedToken{}, fmt.Errorf("invalid LiveKit identity")
	}
	if _, _, err := domain.ParseRoomName(s.environment, input.Room); err != nil {
		return SignedToken{}, fmt.Errorf("invalid LiveKit room")
	}
	now := s.now().UTC()
	deadline := input.ExpiresAt.UTC().Truncate(time.Second)
	if !deadline.After(now) {
		return SignedToken{}, fmt.Errorf("invalid LiveKit token expiry")
	}

	signed, err := s.encode(s.apiKey, s.apiSecret, input.Identity, input.DisplayName, input.Room, deadline.Sub(now))
	if err != nil {
		return SignedToken{}, fmt.Errorf("issue LiveKit token")
	}
	if err := ctx.Err(); err != nil {
		return SignedToken{}, err
	}
	if signed.Token == "" || !signed.ExpiresAt.After(now) || signed.ExpiresAt.After(deadline) {
		return SignedToken{}, fmt.Errorf("issued LiveKit token exceeds authorized expiry")
	}
	return SignedToken{Token: signed.Token, ExpiresAt: signed.ExpiresAt.UTC()}, nil
}

func encodeLiveKitToken(apiKey, apiSecret, identity, displayName, room string, ttl time.Duration) (SignedToken, error) {
	grant := &auth.VideoGrant{
		RoomJoin:          true,
		Room:              room,
		CanPublishSources: []string{"camera", "microphone", "screen_share"},
	}
	grant.SetCanPublish(true)
	grant.SetCanSubscribe(true)
	grant.SetCanPublishData(false)
	grant.SetCanUpdateOwnMetadata(false)

	// SetName is the SDK's own participant-name field (JWT "name" claim,
	// distinct from identity): the LiveKit client SDK exposes it to every
	// other participant as Participant.name. Empty is valid — the frontend
	// falls back to a generic label rather than the identity UUID.
	raw, err := auth.NewAccessToken(apiKey, apiSecret).
		SetIdentity(identity).
		SetName(displayName).
		SetValidFor(ttl).
		SetVideoGrant(grant).
		ToJWT()
	if err != nil {
		return SignedToken{}, fmt.Errorf("issue LiveKit token")
	}

	verifier, err := auth.ParseAPIToken(raw)
	if err != nil {
		return SignedToken{}, fmt.Errorf("verify issued LiveKit token")
	}
	claims, _, err := verifier.Verify(apiSecret)
	if err != nil || claims.Expiry == nil {
		return SignedToken{}, fmt.Errorf("verify issued LiveKit token")
	}
	return SignedToken{
		Token:     raw,
		ExpiresAt: time.Unix(int64(*claims.Expiry), 0).UTC(),
	}, nil
}
