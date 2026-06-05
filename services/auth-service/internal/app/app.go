package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	var users service.UserCreator
	var auth service.AuthSessionManager
	var login service.LoginManager
	var password service.PasswordRecoveryManager
	var invites service.InviteManager
	var oidc service.OIDCManager
	var pool storage.Pool
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
		defer cancel()
		openedPool, err := storage.OpenDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if err != nil {
			logger.Warn("database unavailable; auth database endpoints disabled", "reason", "open_db_failed")
		} else {
			pool = openedPool
			users = service.NewUserService(storage.NewPGXUserStore(pool))
		}
	}

	emailOutboxEncryptor, emailOutboxErr := emailcrypto.New(cfg.AuthEmailOutboxEncryptionKey)
	if emailOutboxErr != nil {
		logger.Warn("email outbox handoff disabled", "reason", "invalid_email_outbox_encryption_key")
	}

	tokens, err := service.NewTokenManager(service.TokenConfig{
		HMACSecret: cfg.AuthJWTHMACSecret,
		Issuer:     cfg.AuthJWTIssuer,
		Audience:   cfg.AuthJWTAudience,
		AccessTTL:  time.Duration(cfg.AuthAccessTokenTTLSeconds) * time.Second,
		RefreshTTL: time.Duration(cfg.AuthRefreshTokenTTLSeconds) * time.Second,
	})

	var loginAttempts httpapi.LoginAttemptsManager
	var deviceSessions *service.DeviceSessionService
	switch {
	case err != nil:
		logger.Warn("auth token endpoints disabled", "reason", "invalid_jwt_config")
	case pool == nil:
		logger.Warn("auth token endpoints disabled", "reason", "database_not_configured")
	default:
		auth = service.NewAuthService(tokens, storage.NewPGXSessionStore(pool))
		login = service.NewLoginService(tokens, storage.NewPGXLoginStore(pool, service.VerifyPassword, service.RunDummyPasswordVerification))
		password = service.NewPasswordResetService(tokens, storage.NewPGXPasswordResetStore(pool), service.WithPasswordResetOutboxEncryptor(emailOutboxEncryptor))
		invites = service.NewInviteService(tokens, storage.NewPGXInviteStore(pool), service.WithInviteOutboxEncryptor(emailOutboxEncryptor))
		loginAttempts = service.NewLoginAttemptsService(storage.NewPGXLoginAttemptsStore(pool))
		deviceSessions = service.NewDeviceSessionService(storage.NewPGXDeviceSessionStore(pool))
	}

	if cfg.OIDCEnabled {
		allowedDomains := splitOIDCDomains(cfg.OIDCAllowedEmailDomains)
		warnOIDCAllowedEmailDomainsUnset(logger, cfg, allowedDomains)
		oidcConfigured := true
		oidcProviderName, resolvedProvider, providerErr := resolveOIDCProvider(cfg)
		cfg.OIDCProviderName = oidcProviderName
		if providerErr != nil {
			logger.Warn("oidc endpoints unavailable", "reason", oidcProviderBootstrapReason(providerErr))
			oidcConfigured = false
		}
		if err != nil || pool == nil {
			logger.Warn("oidc endpoints unavailable", "reason", "auth_dependencies_unavailable")
			oidcConfigured = false
		}

		var oidcStore service.OIDCStore
		var provider service.OIDCProvider
		if oidcConfigured {
			oidcStore = storage.NewPGXOIDCStore(pool)
			provider = resolvedProvider
		}
		oidcService, oidcErr := service.NewOIDCService(service.OIDCServiceConfig{
			Enabled:             cfg.OIDCEnabled,
			Configured:          oidcConfigured,
			ProviderName:        oidcProviderName,
			FrontendCallbackURL: cfg.OIDCFrontendCallbackURL,
			StateTTL:            time.Duration(cfg.OIDCStateTTLMinutes) * time.Minute,
			AutoProvision:       cfg.OIDCAutoProvisionEnabled,
			AllowedDomains:      allowedDomains,
		}, tokens, oidcStore, provider)
		if oidcErr != nil {
			logger.Warn("oidc endpoints unavailable", "reason", "oidc_service_init_failed")
		} else {
			oidc = oidcService
		}
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, users, auth, login, password, invites, loginAttempts, deviceSessions, deviceSessions, oidc),
		TracingShutdown: shutdown,
	}
}

func splitOIDCDomains(raw string) []string {
	parts := strings.Split(raw, ",")
	domains := make([]string, 0, len(parts))
	for _, part := range parts {
		domain := strings.ToLower(strings.TrimSpace(part))
		if domain != "" {
			domains = append(domains, domain)
		}
	}
	return domains
}

func resolveOIDCProvider(cfg config.Config) (string, service.OIDCProvider, error) {
	providerName := cfg.NormalizedOIDCProviderName()
	if err := cfg.ValidateOIDC(); err != nil {
		return providerName, nil, err
	}

	registry := service.NewProviderRegistry()
	keycloakProvider := service.NewKeycloakProvider(service.KeycloakProviderConfig{
		IssuerURL:    cfg.OIDCIssuerURL,
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		RedirectURL:  cfg.OIDCRedirectURL,
		Scopes:       cfg.OIDCScopes,
		HTTPTimeout:  time.Duration(cfg.OIDCHTTPTimeoutSeconds) * time.Second,
	})
	if err := registry.Register(domain.IdentityProviderSlugKeycloak, keycloakProvider); err != nil {
		return providerName, nil, err
	}

	provider, err := registry.Resolve(domain.IdentityProviderSlug(providerName))
	if err != nil {
		return providerName, nil, err
	}
	return providerName, provider, nil
}

func oidcProviderBootstrapReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrOIDCMisconfigured):
		return "invalid_oidc_config"
	case errors.Is(err, domain.ErrOIDCDisabled):
		return "provider_not_resolved"
	default:
		return "provider_registration_failed"
	}
}

func warnOIDCAllowedEmailDomainsUnset(logger *slog.Logger, cfg config.Config, domains []string) {
	if logger == nil || !cfg.OIDCEnabled || len(domains) > 0 {
		return
	}
	logger.Warn("OIDC_ALLOWED_EMAIL_DOMAINS is not set; all email domains are permitted")
}
