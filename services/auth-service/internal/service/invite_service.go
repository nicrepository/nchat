package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	defaultInviteTokenTTLHours = 72
	inviteAcceptActionPath     = "/auth/invites/accept"
)

// InviteStore persists invite state without receiving plaintext invite tokens or passwords.
type InviteStore interface {
	UserExistsByEmail(ctx context.Context, email string) (bool, error)
	ActiveInviteExistsByEmail(ctx context.Context, email string) (bool, error)
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
	CreateInvite(ctx context.Context, email, displayName, fullName, tokenHash string, expiresAt time.Time, encryptedPayload string) (domain.InviteResult, error)
	AcceptInviteTx(ctx context.Context, tokenHash, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error)
}

// InviteManager is the HTTP-facing invite contract.
type InviteManager interface {
	CreateInvite(ctx context.Context, input domain.AdminInviteInput) (domain.InviteResult, error)
	AcceptInvite(ctx context.Context, input domain.AcceptInviteInput) (domain.AcceptInviteResult, error)
}

type InviteOption func(*InviteService)

func WithInviteOutboxEncryptor(encryptor *EmailOutboxEncryptor) InviteOption {
	return func(s *InviteService) {
		s.emailOutbox = encryptor
	}
}

// InviteService implements admin invite creation and public invite acceptance.
type InviteService struct {
	tokens      *TokenManager
	store       InviteStore
	emailOutbox *EmailOutboxEncryptor
}

func NewInviteService(tokens *TokenManager, store InviteStore, opts ...InviteOption) *InviteService {
	svc := &InviteService{tokens: tokens, store: store}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func (s *InviteService) EmailHandoffAvailable() bool {
	return s != nil && s.emailOutbox != nil
}

func (s *InviteService) CreateInvite(ctx context.Context, input domain.AdminInviteInput) (domain.InviteResult, error) {
	if s.emailOutbox == nil {
		return domain.InviteResult{}, domain.ErrEmailOutboxUnavailable
	}

	email := domain.NormalizeEmail(input.Email)
	displayName := strings.TrimSpace(input.DisplayName)
	fullName := strings.TrimSpace(input.FullName)

	if err := domain.ValidateEmail(email); err != nil {
		return domain.InviteResult{}, err
	}
	if displayName == "" {
		return domain.InviteResult{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}

	exists, err := s.store.UserExistsByEmail(ctx, email)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("check user exists: %w", err)
	}
	if exists {
		return domain.InviteResult{}, domain.ErrDuplicateEmail
	}

	pending, err := s.store.ActiveInviteExistsByEmail(ctx, email)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("check active invite: %w", err)
	}
	if pending {
		return domain.InviteResult{}, domain.ErrInviteAlreadyPending
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("get policy: %w", err)
	}
	inviteTTLHours := policy.InviteTokenTTLHours
	if inviteTTLHours <= 0 {
		inviteTTLHours = defaultInviteTokenTTLHours
	}

	rawToken, err := s.tokens.GenerateOpaqueToken()
	if err != nil {
		return domain.InviteResult{}, err
	}
	tokenHash := s.tokens.HashInviteToken(rawToken)
	expiresAt := time.Now().UTC().Add(time.Duration(inviteTTLHours) * time.Hour)
	encryptedPayload, err := s.emailOutbox.Encrypt(EmailOutboxPlaintext{
		Kind:       "invite",
		Token:      rawToken,
		ActionPath: inviteAcceptActionPath,
		ToEmail:    email,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("encrypt invite outbox payload: %w", err)
	}

	return s.store.CreateInvite(ctx, email, displayName, fullName, tokenHash, expiresAt, encryptedPayload)
}

func (s *InviteService) AcceptInvite(ctx context.Context, input domain.AcceptInviteInput) (domain.AcceptInviteResult, error) {
	token := strings.TrimSpace(input.Token)
	displayName := strings.TrimSpace(input.DisplayName)
	fullName := strings.TrimSpace(input.FullName)
	if token == "" {
		return domain.AcceptInviteResult{}, fmt.Errorf("%w: token is required", domain.ErrInvalidInput)
	}
	if displayName == "" {
		return domain.AcceptInviteResult{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}
	if input.Password == "" {
		return domain.AcceptInviteResult{}, fmt.Errorf("%w: password is required", domain.ErrInvalidInput)
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("get policy: %w", err)
	}
	if err := domain.ValidatePassword(input.Password, policy); err != nil {
		return domain.AcceptInviteResult{}, err
	}
	passwordHash, err := HashPassword(input.Password)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("hash password: %w", err)
	}
	tokenHash := s.tokens.HashInviteToken(token)
	return s.store.AcceptInviteTx(ctx, tokenHash, displayName, fullName, passwordHash)
}
