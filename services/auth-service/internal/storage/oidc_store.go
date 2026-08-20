package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

type PGXOIDCStore struct {
	pool Pool
}

func NewPGXOIDCStore(pool Pool) *PGXOIDCStore {
	return &PGXOIDCStore{pool: pool}
}

func (s *PGXOIDCStore) CreateAuthRequest(ctx context.Context, req domain.OIDCLoginRequest) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.oidc_auth_requests
		  (id, provider, state_hash, nonce_hash, pkce_verifier_encrypted, redirect_after, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		req.ID, req.Provider, req.StateHash, req.NonceHash, req.PKCEVerifierEncrypted, nullableString(req.RedirectAfter), req.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert oidc auth request: %w", err)
	}
	return nil
}

func (s *PGXOIDCStore) ConsumeAuthRequest(ctx context.Context, provider, stateHash string) (domain.OIDCConsumedAuthRequest, error) {
	var req domain.OIDCConsumedAuthRequest
	err := s.pool.QueryRow(ctx, `
		UPDATE auth.oidc_auth_requests
		SET used_at = now()
		WHERE provider = $1
		  AND state_hash = $2
		  AND used_at IS NULL
		  AND expires_at > now()
		RETURNING id, provider, nonce_hash, pkce_verifier_encrypted, COALESCE(redirect_after, '')`,
		provider, stateHash,
	).Scan(&req.ID, &req.Provider, &req.NonceHash, &req.PKCEVerifierEncrypted, &req.RedirectAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OIDCConsumedAuthRequest{}, domain.ErrInvalidToken
	}
	if err != nil {
		return domain.OIDCConsumedAuthRequest{}, fmt.Errorf("consume oidc auth request: %w", err)
	}
	return req, nil
}

func (s *PGXOIDCStore) CreateOIDCSessionAndExchange(ctx context.Context, input domain.OIDCSessionInput, buildExchange func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error)) (domain.OIDCCreatedSession, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.OIDCCreatedSession{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	policy, err := selectLoginPolicy(ctx, tx)
	if err != nil {
		return domain.OIDCCreatedSession{}, err
	}

	user, err := resolveOIDCUser(ctx, tx, input)
	if err != nil {
		return domain.OIDCCreatedSession{}, err
	}

	deviceID, err := resolveLoginDevice(ctx, tx, user.ID, domain.CreateSessionInput{
		Email:                 input.Email,
		RefreshTokenHash:      input.RefreshTokenHash,
		RefreshExpiresAt:      input.RefreshExpiresAt,
		DeviceFingerprintHash: input.DeviceFingerprintHash,
		DeviceName:            input.DeviceName,
		Platform:              input.Platform,
		IPAddress:             input.IPAddress,
		UserAgent:             input.UserAgent,
	}, policy)
	if err != nil {
		if errors.Is(err, errDeviceRevoked) || errors.Is(err, domain.ErrInvalidCredentials) {
			return domain.OIDCCreatedSession{}, domain.ErrInvalidCredentials
		}
		return domain.OIDCCreatedSession{}, err
	}

	if err := recordLoginAttempt(ctx, tx, user.ID, input.Email, true, "", input.IPAddress, input.UserAgent); err != nil {
		return domain.OIDCCreatedSession{}, fmt.Errorf("record oidc login attempt: %w", err)
	}

	session, err := insertLoginSession(ctx, tx, user.ID, deviceID, domain.CreateSessionInput{
		RefreshTokenHash: input.RefreshTokenHash,
		RefreshExpiresAt: input.RefreshExpiresAt,
		IPAddress:        input.IPAddress,
		UserAgent:        input.UserAgent,
	}, policy)
	if err != nil {
		return domain.OIDCCreatedSession{}, err
	}

	if err := insertInitialRefreshTokenHistory(ctx, tx, session.ID, input.RefreshTokenHash); err != nil {
		return domain.OIDCCreatedSession{}, err
	}

	if _, err := tx.Exec(ctx, `UPDATE auth.users SET last_login_at = now() WHERE id = $1`, user.ID); err != nil {
		return domain.OIDCCreatedSession{}, fmt.Errorf("update oidc last_login_at: %w", err)
	}

	exchange, err := buildExchange(session, user)
	if err != nil {
		return domain.OIDCCreatedSession{}, err
	}
	if err := insertOIDCExchange(ctx, tx, exchange); err != nil {
		return domain.OIDCCreatedSession{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.OIDCCreatedSession{}, fmt.Errorf("commit tx: %w", err)
	}
	return domain.OIDCCreatedSession{Session: session, User: user}, nil
}

func resolveOIDCUser(ctx context.Context, tx pgx.Tx, input domain.OIDCSessionInput) (domain.LoginUser, error) {
	user, found, err := selectOIDCUserBySubject(ctx, tx, input.Provider, input.Subject)
	if err != nil {
		return domain.LoginUser{}, err
	}
	if found {
		// The row is already locked FOR UPDATE by the select above, so the
		// profile refresh runs inside the same transaction as the login.
		displayName, syncErr := syncOIDCUserProfile(ctx, tx, user.ID, input)
		if syncErr != nil {
			return domain.LoginUser{}, syncErr
		}
		user.DisplayName = displayName
		return user, nil
	}

	emailExists, err := oidcEmailExists(ctx, tx, input.Email)
	if err != nil {
		return domain.LoginUser{}, err
	}
	if emailExists {
		return domain.LoginUser{}, domain.ErrOIDCAccountConflict
	}
	if !input.AutoProvision {
		return domain.LoginUser{}, domain.ErrOIDCProvisioningDisabled
	}

	user, inserted, err := insertOIDCUser(ctx, tx, input)
	if err != nil {
		return domain.LoginUser{}, err
	}
	if inserted {
		return user, nil
	}

	// The insert found a unique constraint already taken. Nothing above locked
	// the absent row, so the winner of a concurrent first login is the ordinary
	// outcome here: the ON CONFLICT clause waited for that transaction to
	// commit, and its account is now visible. Re-reading by subject is what
	// tells the two cases apart — the same identity means log in, anything else
	// means the e-mail belongs to an account this subject does not own.
	user, found, err = selectOIDCUserBySubject(ctx, tx, input.Provider, input.Subject)
	if err != nil {
		return domain.LoginUser{}, err
	}
	if !found {
		return domain.LoginUser{}, domain.ErrOIDCAccountConflict
	}
	return user, nil
}

func selectOIDCUserBySubject(ctx context.Context, tx pgx.Tx, provider, subject string) (domain.LoginUser, bool, error) {
	var user domain.LoginUser
	var status string
	var deletedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, email::text, display_name, status, deleted_at
		FROM auth.users
		WHERE auth_source = 'oidc'
		  AND external_provider = $1
		  AND external_subject = $2
		FOR UPDATE`,
		provider, subject,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &status, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.LoginUser{}, false, nil
	}
	if err != nil {
		return domain.LoginUser{}, false, fmt.Errorf("select oidc user by subject: %w", err)
	}
	if status != "active" || deletedAt != nil {
		return domain.LoginUser{}, true, domain.ErrInvalidCredentials
	}
	return user, true, nil
}

func oidcEmailExists(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id
		FROM auth.users
		WHERE email = $1
		LIMIT 1
		FOR UPDATE`,
		email,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select oidc email conflict: %w", err)
	}
	return true, nil
}

func insertOIDCUser(ctx context.Context, tx pgx.Tx, input domain.OIDCSessionInput) (domain.LoginUser, bool, error) {
	// display_name is NOT NULL, so provisioning is the one place the generic
	// placeholder applies. full_name and avatar_url stay NULL when unknown.
	displayName := input.DisplayName
	if displayName == "" {
		displayName = domain.DefaultDisplayName
	}
	var user domain.LoginUser
	// ON CONFLICT DO NOTHING rather than a bare insert: a raw 23505 would abort
	// the whole login transaction, leaving no way to tell a lost provisioning
	// race from a genuine e-mail collision. The clause covers every unique
	// constraint on the table — users_email_unique and the partial index on
	// (external_provider, external_subject) alike — and returns no row instead,
	// which the caller resolves.
	err := tx.QueryRow(ctx, `
		INSERT INTO auth.users
		  (email, display_name, full_name, avatar_url, status, auth_source,
		   external_provider, external_subject, email_verified_at)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), 'active', 'oidc', $5, $6, now())
		ON CONFLICT DO NOTHING
		RETURNING id, email::text, display_name`,
		input.Email, displayName, input.FullName, input.AvatarURL,
		input.Provider, input.Subject,
	).Scan(&user.ID, &user.Email, &user.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.LoginUser{}, false, nil
	}
	if err != nil {
		return domain.LoginUser{}, false, fmt.Errorf("insert oidc user: %w", err)
	}
	return user, true, nil
}

// syncOIDCUserProfile refreshes the profile of a returning OIDC user from the
// claims of the login that just succeeded, so a rename at the IdP reaches the
// sidebar on the next sign-in instead of staying frozen at provisioning time.
//
// COALESCE(NULLIF($n, ”), column) is the whole policy: a claim the provider
// stopped sending arrives here as an empty string and leaves the stored value
// untouched. A temporarily missing optional claim therefore never degrades an
// identity, and clearing a name stays an explicit operation.
//
// avatar_url uses the reverse precedence — COALESCE(avatar_url, NULLIF($4, ”))
// — so the OIDC picture only ever fills an EMPTY avatar and never overwrites one
// already set. That makes the user-uploaded avatar authoritative: once a user
// uploads (the sole operational producer), no later login can clobber it with a
// provider picture.
func syncOIDCUserProfile(ctx context.Context, tx pgx.Tx, userID string, input domain.OIDCSessionInput) (string, error) {
	var displayName string
	err := tx.QueryRow(ctx, `
		UPDATE auth.users
		SET display_name = COALESCE(NULLIF($2, ''), display_name),
		    full_name    = COALESCE(NULLIF($3, ''), full_name),
		    avatar_url   = COALESCE(avatar_url, NULLIF($4, '')),
		    updated_at   = now()
		WHERE id = $1
		RETURNING display_name`,
		userID, input.DisplayName, input.FullName, input.AvatarURL,
	).Scan(&displayName)
	if err != nil {
		return "", fmt.Errorf("sync oidc user profile: %w", err)
	}
	return displayName, nil
}

func insertOIDCExchange(ctx context.Context, tx pgx.Tx, exchange domain.OIDCExchangeInput) error {
	userJSON, err := json.Marshal(oidcExchangeUserJSON{
		ID:                 exchange.User.ID,
		Email:              exchange.User.Email,
		DisplayName:        exchange.User.DisplayName,
		MustChangePassword: exchange.User.MustChangePassword,
	})
	if err != nil {
		return fmt.Errorf("marshal oidc exchange user: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth.oidc_exchange_codes
		  (id, provider, code_hash, access_value_encrypted, refresh_value_encrypted, bearer_scheme, expires_in, user_json, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)`,
		exchange.ID, exchange.Provider, exchange.CodeHash, exchange.AccessValueEncrypted, exchange.RefreshValueEncrypted,
		exchange.BearerScheme, exchange.ExpiresIn, string(userJSON), exchange.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert oidc exchange code: %w", err)
	}
	return nil
}

func (s *PGXOIDCStore) ConsumeExchange(ctx context.Context, provider, codeHash string) (domain.OIDCConsumedExchange, error) {
	var exchange domain.OIDCConsumedExchange
	var userJSON []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE auth.oidc_exchange_codes ec
		SET used_at = now()
		FROM auth.users u
		WHERE ec.provider = $1
		  AND ec.code_hash = $2
		  AND ec.used_at IS NULL
		  AND ec.expires_at > now()
		  AND u.id::text = ec.user_json->>'id'
		  AND u.status = 'active'
		  AND u.deleted_at IS NULL
		RETURNING ec.id, ec.provider, ec.access_value_encrypted, ec.refresh_value_encrypted, ec.bearer_scheme, ec.expires_in, ec.user_json`,
		provider, codeHash,
	).Scan(&exchange.ID, &exchange.Provider, &exchange.AccessValueEncrypted, &exchange.RefreshValueEncrypted, &exchange.BearerScheme, &exchange.ExpiresIn, &userJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OIDCConsumedExchange{}, domain.ErrInvalidToken
	}
	if err != nil {
		return domain.OIDCConsumedExchange{}, fmt.Errorf("consume oidc exchange code: %w", err)
	}
	var safeUser oidcExchangeUserJSON
	if err := json.Unmarshal(userJSON, &safeUser); err != nil {
		return domain.OIDCConsumedExchange{}, fmt.Errorf("decode oidc exchange user: %w", err)
	}
	exchange.User = domain.LoginUser{
		ID:                 safeUser.ID,
		Email:              safeUser.Email,
		DisplayName:        safeUser.DisplayName,
		MustChangePassword: safeUser.MustChangePassword,
	}
	return exchange, nil
}

type oidcExchangeUserJSON struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	MustChangePassword bool   `json:"must_change_password"`
}
