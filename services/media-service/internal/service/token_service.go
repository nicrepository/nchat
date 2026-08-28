package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
)

var (
	ErrAuthorizationDependency = errors.New("authorization dependency failure")
	ErrTokenSigning            = errors.New("token signing failure")
)

type AuthorizationInput struct {
	Kind            domain.ResourceKind
	ResourceID      string
	UserID          string
	SessionID       string
	ParticipationID string
}

type AuthorizedResource struct {
	ID               string
	SessionExpiresAt time.Time
	// DisplayName is the caller's own server-resolved presentation name
	// (full_name, else display_name, else empty). Never part of the
	// authorization decision — only carried through to the LiveKit token so
	// other participants see a real name instead of a generic label.
	DisplayName string
}

type ResourceAuthorizer interface {
	Authorize(ctx context.Context, input AuthorizationInput) (AuthorizedResource, error)
}

type SignInput struct {
	Identity string
	// DisplayName is presentation only, carried into the LiveKit token's
	// participant name; it is never identity, never room, never a grant.
	DisplayName string
	Room        string
	ExpiresAt   time.Time
}

type SignedToken struct {
	Token     string
	ExpiresAt time.Time
}

type TokenSigner interface {
	Sign(ctx context.Context, input SignInput) (SignedToken, error)
}

type IssueTokenInput struct {
	Kind            domain.ResourceKind
	ResourceID      string
	UserID          string
	SessionID       string
	ParticipationID string
	AccessExpiresAt time.Time
}

type IssuedToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type TokenService struct {
	authorizer  ResourceAuthorizer
	signer      TokenSigner
	environment domain.Environment
	ttl         time.Duration
	now         func() time.Time
}

func NewTokenService(
	authorizer ResourceAuthorizer,
	signer TokenSigner,
	environment domain.Environment,
	ttl time.Duration,
	now func() time.Time,
) *TokenService {
	if now == nil {
		now = time.Now
	}
	return &TokenService{
		authorizer: authorizer, signer: signer,
		environment: environment, ttl: ttl, now: now,
	}
}

func (s *TokenService) Issue(ctx context.Context, input IssueTokenInput) (IssuedToken, error) {
	if s == nil || s.authorizer == nil || s.signer == nil || s.ttl <= 0 {
		return IssuedToken{}, domain.ErrUnavailable
	}
	environment, err := domain.ParseEnvironment(s.environment.String())
	if err != nil {
		return IssuedToken{}, domain.ErrUnavailable
	}
	userID, err := uuid.Parse(input.UserID)
	if err != nil {
		return IssuedToken{}, domain.ErrUnauthorized
	}
	sessionID, err := uuid.Parse(input.SessionID)
	if err != nil {
		return IssuedToken{}, domain.ErrUnauthorized
	}
	if !input.Kind.Valid() {
		return IssuedToken{}, domain.ErrInvalidInput
	}
	participationID := ""
	if input.ParticipationID != "" {
		parsed, err := uuid.Parse(input.ParticipationID)
		if err != nil {
			return IssuedToken{}, domain.ErrInvalidInput
		}
		participationID = parsed.String()
	}

	authorized, err := s.authorizer.Authorize(ctx, AuthorizationInput{
		Kind: input.Kind, ResourceID: input.ResourceID,
		UserID: userID.String(), SessionID: sessionID.String(),
		ParticipationID: participationID,
	})
	if err != nil {
		if !errors.Is(err, domain.ErrInvalidInput) &&
			!errors.Is(err, domain.ErrUnauthorized) &&
			!errors.Is(err, domain.ErrNotFound) &&
			!errors.Is(err, domain.ErrUnavailable) {
			return IssuedToken{}, fmt.Errorf("%w: %w", ErrAuthorizationDependency, err)
		}
		return IssuedToken{}, fmt.Errorf("authorize media resource: %w", err)
	}
	room, err := domain.RoomName(environment, input.Kind, authorized.ID)
	if err != nil {
		return IssuedToken{}, fmt.Errorf("derive media room: %w", err)
	}
	identity, err := domain.ParticipantIdentity(environment, userID.String())
	if err != nil {
		return IssuedToken{}, fmt.Errorf("derive media identity: %w", err)
	}

	now := s.now().UTC()
	deadline := now.Add(s.ttl)
	if input.AccessExpiresAt.Before(deadline) {
		deadline = input.AccessExpiresAt.UTC()
	}
	if authorized.SessionExpiresAt.Before(deadline) {
		deadline = authorized.SessionExpiresAt.UTC()
	}
	deadline = deadline.Truncate(time.Second)
	if !deadline.After(now) {
		return IssuedToken{}, domain.ErrUnauthorized
	}

	signed, err := s.signer.Sign(ctx, SignInput{
		Identity:    identity,
		DisplayName: authorized.DisplayName,
		Room:        room,
		ExpiresAt:   deadline,
	})
	if err != nil {
		return IssuedToken{}, fmt.Errorf("%w: %w", ErrTokenSigning, err)
	}
	if signed.Token == "" || !signed.ExpiresAt.After(now) || signed.ExpiresAt.After(deadline) {
		return IssuedToken{}, fmt.Errorf("%w: invalid signed token bounds", ErrTokenSigning)
	}
	return IssuedToken{Token: signed.Token, ExpiresAt: signed.ExpiresAt.UTC()}, nil
}
