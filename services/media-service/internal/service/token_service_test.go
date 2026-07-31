package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/media-service/internal/domain"
)

const (
	serviceTestUserID    = "11111111-1111-4111-8111-111111111111"
	serviceTestSessionID = "22222222-2222-4222-8222-222222222222"
	serviceTestResource  = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
)

type tokenAuthorizerStub struct {
	result AuthorizedResource
	err    error
	input  AuthorizationInput
}

func (s *tokenAuthorizerStub) Authorize(_ context.Context, input AuthorizationInput) (AuthorizedResource, error) {
	s.input = input
	return s.result, s.err
}

type tokenSignerStub struct {
	result SignedToken
	err    error
	input  SignInput
	calls  int
}

func (s *tokenSignerStub) Sign(_ context.Context, input SignInput) (SignedToken, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func TestTokenServiceIssuesServerDerivedIdentityRoomAndDeadline(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 750_000_000, time.UTC)
	wantDeadline := now.Add(5 * time.Minute).Truncate(time.Second)
	authorizer := &tokenAuthorizerStub{result: AuthorizedResource{
		ID: serviceTestResource, SessionExpiresAt: now.Add(20 * time.Minute),
	}}
	signer := &tokenSignerStub{result: SignedToken{
		Token: "livekit-token", ExpiresAt: wantDeadline,
	}}
	svc := NewTokenService(authorizer, signer, 5*time.Minute, func() time.Time { return now })

	result, err := svc.Issue(context.Background(), IssueTokenInput{
		Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
		UserID: serviceTestUserID, SessionID: serviceTestSessionID,
		AccessExpiresAt: now.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if result.Token != "livekit-token" || !result.ExpiresAt.Equal(wantDeadline) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if signer.input.Identity != serviceTestUserID {
		t.Fatalf("identity must come from authenticated user, got %q", signer.input.Identity)
	}
	if signer.input.Room != "call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("room must be server-derived, got %q", signer.input.Room)
	}
	if !signer.input.ExpiresAt.Equal(wantDeadline) {
		t.Fatalf("expected deadline %v, got %v", wantDeadline, signer.input.ExpiresAt)
	}
	if authorizer.input.UserID != serviceTestUserID || authorizer.input.SessionID != serviceTestSessionID {
		t.Fatalf("authorization did not receive authenticated principal: %+v", authorizer.input)
	}
}

func TestTokenServiceCapsDeadlineByAccessTokenAndSession(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		accessExpiry  time.Time
		sessionExpiry time.Time
		wantDeadline  time.Time
	}{
		{name: "access token", accessExpiry: now.Add(90 * time.Second), sessionExpiry: now.Add(10 * time.Minute), wantDeadline: now.Add(90 * time.Second)},
		{name: "session", accessExpiry: now.Add(10 * time.Minute), sessionExpiry: now.Add(45 * time.Second), wantDeadline: now.Add(45 * time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorizer := &tokenAuthorizerStub{result: AuthorizedResource{ID: serviceTestResource, SessionExpiresAt: tt.sessionExpiry}}
			signer := &tokenSignerStub{result: SignedToken{Token: "token", ExpiresAt: tt.wantDeadline}}
			svc := NewTokenService(authorizer, signer, 5*time.Minute, func() time.Time { return now })
			_, err := svc.Issue(context.Background(), IssueTokenInput{
				Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
				UserID: serviceTestUserID, SessionID: serviceTestSessionID,
				AccessExpiresAt: tt.accessExpiry,
			})
			if err != nil {
				t.Fatalf("issue: %v", err)
			}
			if !signer.input.ExpiresAt.Equal(tt.wantDeadline) {
				t.Fatalf("expected deadline %v, got %v", tt.wantDeadline, signer.input.ExpiresAt)
			}
		})
	}
}

func TestTokenServiceAcceptsConfiguredTTLBounds(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 250_000_000, time.UTC)
	for _, ttl := range []time.Duration{60 * time.Second, 600 * time.Second} {
		t.Run(ttl.String(), func(t *testing.T) {
			wantDeadline := now.Add(ttl).Truncate(time.Second)
			authorizer := &tokenAuthorizerStub{result: AuthorizedResource{
				ID: serviceTestResource, SessionExpiresAt: now.Add(20 * time.Minute),
			}}
			signer := &tokenSignerStub{result: SignedToken{Token: "token", ExpiresAt: wantDeadline}}
			svc := NewTokenService(authorizer, signer, ttl, func() time.Time { return now })

			if _, err := svc.Issue(context.Background(), IssueTokenInput{
				Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
				UserID: serviceTestUserID, SessionID: serviceTestSessionID,
				AccessExpiresAt: now.Add(20 * time.Minute),
			}); err != nil {
				t.Fatalf("issue with TTL %v: %v", ttl, err)
			}
			if !signer.input.ExpiresAt.Equal(wantDeadline) {
				t.Fatalf("expected deadline %v, got %v", wantDeadline, signer.input.ExpiresAt)
			}
		})
	}
}

func TestTokenServiceRejectsExpiredBoundsWithoutSigning(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	authorizer := &tokenAuthorizerStub{result: AuthorizedResource{ID: serviceTestResource, SessionExpiresAt: now}}
	signer := &tokenSignerStub{}
	svc := NewTokenService(authorizer, signer, 5*time.Minute, func() time.Time { return now })

	result, err := svc.Issue(context.Background(), IssueTokenInput{
		Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
		UserID: serviceTestUserID, SessionID: serviceTestSessionID,
		AccessExpiresAt: now.Add(time.Minute),
	})
	if !errors.Is(err, domain.ErrUnauthorized) || signer.calls != 0 || result.Token != "" {
		t.Fatalf("expected unauthorized without token, result=%+v err=%v calls=%d", result, err, signer.calls)
	}
}

func TestTokenServicePropagatesAuthorizationAndSignerFailuresWithoutToken(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	dbErr := errors.New("db unavailable")
	authorizer := &tokenAuthorizerStub{err: dbErr}
	signer := &tokenSignerStub{}
	svc := NewTokenService(authorizer, signer, 5*time.Minute, func() time.Time { return now })
	input := IssueTokenInput{
		Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
		UserID: serviceTestUserID, SessionID: serviceTestSessionID,
		AccessExpiresAt: now.Add(time.Minute),
	}
	result, err := svc.Issue(context.Background(), input)
	if !errors.Is(err, dbErr) || !errors.Is(err, ErrAuthorizationDependency) || result.Token != "" || signer.calls != 0 {
		t.Fatalf("authorization failure emitted token: result=%+v err=%v", result, err)
	}

	authorizer.err = nil
	authorizer.result = AuthorizedResource{ID: serviceTestResource, SessionExpiresAt: now.Add(time.Minute)}
	signErr := errors.New("sign failed")
	signer.err = signErr
	result, err = svc.Issue(context.Background(), input)
	if !errors.Is(err, signErr) || !errors.Is(err, ErrTokenSigning) || result.Token != "" {
		t.Fatalf("signer failure emitted token: result=%+v err=%v", result, err)
	}
}

func TestTokenServiceRejectsSignerExpiryBeyondAuthorizedDeadline(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	authorizer := &tokenAuthorizerStub{result: AuthorizedResource{ID: serviceTestResource, SessionExpiresAt: now.Add(time.Minute)}}
	signer := &tokenSignerStub{result: SignedToken{Token: "too-long", ExpiresAt: now.Add(2 * time.Minute)}}
	svc := NewTokenService(authorizer, signer, 5*time.Minute, func() time.Time { return now })

	result, err := svc.Issue(context.Background(), IssueTokenInput{
		Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
		UserID: serviceTestUserID, SessionID: serviceTestSessionID,
		AccessExpiresAt: now.Add(3 * time.Minute),
	})
	if err == nil || result.Token != "" {
		t.Fatalf("expected overlong signed token to be rejected: result=%+v err=%v", result, err)
	}
}

func TestTokenServiceRejectsInvalidConfigurationAndPrincipal(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	validAuthorizer := &tokenAuthorizerStub{result: AuthorizedResource{
		ID: serviceTestResource, SessionExpiresAt: now.Add(time.Minute),
	}}
	validSigner := &tokenSignerStub{result: SignedToken{
		Token: "token", ExpiresAt: now.Add(time.Minute),
	}}
	validInput := IssueTokenInput{
		Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
		UserID: serviceTestUserID, SessionID: serviceTestSessionID,
		AccessExpiresAt: now.Add(time.Minute),
	}
	tests := []struct {
		name string
		svc  *TokenService
		edit func(*IssueTokenInput)
		want error
	}{
		{name: "nil service", svc: nil, want: domain.ErrUnavailable},
		{name: "nil authorizer", svc: NewTokenService(nil, validSigner, time.Minute, nil), want: domain.ErrUnavailable},
		{name: "nil signer", svc: NewTokenService(validAuthorizer, nil, time.Minute, nil), want: domain.ErrUnavailable},
		{name: "invalid ttl", svc: NewTokenService(validAuthorizer, validSigner, 0, nil), want: domain.ErrUnavailable},
		{name: "invalid user", svc: NewTokenService(validAuthorizer, validSigner, time.Minute, func() time.Time { return now }),
			edit: func(input *IssueTokenInput) { input.UserID = "invalid" }, want: domain.ErrUnauthorized},
		{name: "invalid session", svc: NewTokenService(validAuthorizer, validSigner, time.Minute, func() time.Time { return now }),
			edit: func(input *IssueTokenInput) { input.SessionID = "invalid" }, want: domain.ErrUnauthorized},
		{name: "invalid kind", svc: NewTokenService(validAuthorizer, validSigner, time.Minute, func() time.Time { return now }),
			edit: func(input *IssueTokenInput) { input.Kind = "group" }, want: domain.ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validInput
			if tt.edit != nil {
				tt.edit(&input)
			}
			result, err := tt.svc.Issue(context.Background(), input)
			if !errors.Is(err, tt.want) || result.Token != "" {
				t.Fatalf("expected %v without token, result=%+v err=%v", tt.want, result, err)
			}
		})
	}
}

func TestTokenServiceRejectsInvalidCanonicalResourceFromStorage(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	authorizer := &tokenAuthorizerStub{result: AuthorizedResource{
		ID: "invalid", SessionExpiresAt: now.Add(time.Minute),
	}}
	signer := &tokenSignerStub{}
	result, err := NewTokenService(authorizer, signer, time.Minute, func() time.Time { return now }).
		Issue(context.Background(), IssueTokenInput{
			Kind: domain.ResourceKindCall, ResourceID: serviceTestResource,
			UserID: serviceTestUserID, SessionID: serviceTestSessionID,
			AccessExpiresAt: now.Add(time.Minute),
		})
	if err == nil || result.Token != "" || signer.calls != 0 {
		t.Fatalf("invalid persisted resource must fail closed: result=%+v err=%v calls=%d", result, err, signer.calls)
	}
}
