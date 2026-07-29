package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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

// dbBootstrapTimeout bounds the total retry window for the initial database
// connection. Keep it below the Kubernetes startupProbe budget (60s) so a
// failed bootstrap exits and the container is restarted before the kubelet
// intervenes.
const dbBootstrapTimeout = 30 * time.Second

// openDBWithRetry is swappable in tests so app bootstrap failure paths run
// without real network access or real sleeps.
var openDBWithRetry = storage.OpenDBWithRetry

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc

	closeDB      func()
	shutdownOnce sync.Once
}

// Shutdown closes the database pool and flushes the tracing exporter.
// Safe to call multiple times — subsequent calls are no-ops.
func (a *App) Shutdown(ctx context.Context) error {
	var err error
	a.shutdownOnce.Do(func() {
		if a.closeDB != nil {
			a.closeDB()
		}
		if a.TracingShutdown != nil {
			err = a.TracingShutdown(ctx)
		}
	})
	return err
}

// New assembles the application. Bootstrap outcomes by state:
//
//   - DATABASE_URL configured but unreachable: retry with backoff, then
//     fail fast — New returns an error, the process exits non-zero and
//     Kubernetes restarts the container.
//   - DATABASE_URL absent: configuration choice, not a transient failure —
//     the process stays alive and /readyz reports 503.
//   - Invalid JWT config: the process stays alive and /readyz reports 503.
//
// In every degraded state the pod never becomes Ready, so the Service sends
// it no traffic.
func New(cfg config.Config) (*App, error) {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	var users service.UserAdmin
	var auth service.AuthSessionManager
	var login service.LoginManager
	var password service.PasswordRecoveryManager
	var invites service.InviteManager
	var oidc service.OIDCManager
	var pool storage.Pool
	var closeDB func()
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), dbBootstrapTimeout)
		openedPool, dbErr := openDBWithRetry(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds, logger)
		cancel()
		if dbErr != nil {
			// Fail fast: a half-wired server must never start serving.
			// Kubernetes restarts the container and the retry window resets.
			logger.Error("database bootstrap failed; refusing degraded start", "reason", "open_db_failed")
			_ = shutdown(context.Background())
			return nil, dbErr
		}
		pool = openedPool
		if closer, ok := openedPool.(interface{ Close() }); ok {
			closeDB = closer.Close
		}
		users = service.NewUserService(storage.NewPGXUserStore(pool))
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
		invites = service.NewInviteService(tokens, storage.NewPGXInviteStore(pool),
			service.WithInviteOutboxEncryptor(emailOutboxEncryptor),
			service.WithInviteRateLimit(domain.InviteRateLimit{
				MaxPerWindow:  cfg.AuthInviteRateLimitPerActor,
				WindowMinutes: cfg.AuthInviteRateLimitWindowMinutes,
			}),
			service.WithBootstrapWorkspace(cfg.AuthBootstrapWorkspaceID))
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

	// Pass untyped nils when device sessions were not wired: a nil
	// *DeviceSessionService inside a non-nil interface would defeat the
	// router's `sessions != nil` readiness and gating checks (typed-nil trap).
	var sessionManager httpapi.SessionManager
	var deviceManager httpapi.DeviceManager
	if deviceSessions != nil {
		sessionManager = deviceSessions
		deviceManager = deviceSessions
	}

	// Avatar endpoints require both a database (for the association) and a
	// writable persistent directory (for the files). Either missing disables
	// them without degrading the rest of the service. Same typed-nil care.
	var avatarManager httpapi.AvatarManager
	var avatarReader httpapi.AvatarReader
	if pool != nil && cfg.AuthAvatarDir != "" {
		fsStore, avatarErr := storage.NewFilesystemAvatarStore(cfg.AuthAvatarDir)
		if avatarErr != nil {
			logger.Warn("avatar endpoints disabled", "reason", "avatar_dir_unavailable")
		} else {
			avatarManager = service.NewAvatarService(fsStore, storage.NewPGXUserStore(pool), cfg.AuthAvatarBaseURL)
			avatarReader = fsStore
		}
	} else if cfg.AuthAvatarDir == "" {
		logger.Warn("avatar endpoints disabled", "reason", "avatar_dir_not_configured")
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, users, auth, login, password, invites, loginAttempts, sessionManager, deviceManager, avatarManager, avatarReader, oidc),
		TracingShutdown: shutdown,
		closeDB:         closeDB,
	}, nil
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
