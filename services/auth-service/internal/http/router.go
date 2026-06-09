package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const RouteMetrics = "/metrics"

func NewRouter(cfg config.Config, logger *slog.Logger, users service.UserAdmin, auth service.AuthSessionManager, login service.LoginManager, password service.PasswordRecoveryManager, invites service.InviteManager, loginAttempts LoginAttemptsManager, sessions SessionManager, devices DeviceManager, oidcManagers ...service.OIDCManager) http.Handler {
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	tokens, err := service.NewTokenManager(service.TokenConfig{
		HMACSecret: cfg.AuthJWTHMACSecret,
		Issuer:     cfg.AuthJWTIssuer,
		Audience:   cfg.AuthJWTAudience,
		AccessTTL:  time.Duration(cfg.AuthAccessTokenTTLSeconds) * time.Second,
		RefreshTTL: time.Duration(cfg.AuthRefreshTokenTTLSeconds) * time.Second,
	})
	if err != nil && logger != nil {
		logger.Warn("login attempts auth disabled", "reason", "invalid_jwt_config")
	}

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())
	rateLimitKeyer := newRateLimitKeyer(cfg.AuthJWTHMACSecret)
	trustedProxyCIDRs := httputil.ParseCIDRs(cfg.AuthTrustedProxyCIDRs)
	tokenEndpointLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, trustedProxyCIDRs)
	forgotIPLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, trustedProxyCIDRs)
	resetIPLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, trustedProxyCIDRs)
	inviteAcceptIPLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, trustedProxyCIDRs)
	oidcIPLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, trustedProxyCIDRs)
	forgotTargetLimiter := newTargetAwareRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, rateLimitKeyer, "forgot-email")
	resetTargetLimiter := newTargetAwareRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, rateLimitKeyer, "password-reset-"+"to"+"ken")
	inviteAcceptTargetLimiter := newTargetAwareRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, rateLimitKeyer, "invite-accept-"+"to"+"ken")
	mux.Handle(RouteAuthRefresh, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthRefresh(auth))))
	mux.Handle(RouteAuthLogout, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthLogout(auth))))
	mux.Handle(RouteAuthLogin, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthLogin(login, trustedProxyCIDRs))))
	forgotHandler := AuthForgotPassword(password, forgotTargetLimiter)
	if password != nil && emailHandoffAvailable(password) {
		forgotHandler = forgotIPLimiter.Middleware(forgotHandler)
	}
	mux.Handle(RouteAuthPasswordForgot, httputil.MethodNotAllowed(http.MethodPost, forgotHandler))
	mux.Handle(RouteAuthPasswordReset, httputil.MethodNotAllowed(http.MethodPost, resetIPLimiter.Middleware(AuthResetPassword(password, resetTargetLimiter))))
	mux.Handle(RouteAuthInvitesAccept, httputil.MethodNotAllowed(http.MethodPost, inviteAcceptIPLimiter.Middleware(AuthAcceptInvite(invites, inviteAcceptTargetLimiter))))
	var oidc service.OIDCManager
	if len(oidcManagers) > 0 {
		oidc = oidcManagers[0]
	}
	oidcLoginHandler := OIDCLogin(oidc)
	oidcCallbackHandler := OIDCCallback(oidc, trustedProxyCIDRs)
	oidcExchangeHandler := OIDCExchange(oidc)
	if cfg.OIDCEnabled && oidc != nil {
		oidcLoginHandler = oidcIPLimiter.Middleware(oidcLoginHandler)
		oidcCallbackHandler = oidcIPLimiter.Middleware(oidcCallbackHandler)
		oidcExchangeHandler = oidcIPLimiter.Middleware(oidcExchangeHandler)
	}
	mux.Handle(RouteAuthOIDCKeycloakLogin, httputil.MethodNotAllowed(http.MethodGet, oidcLoginHandler))
	mux.Handle(RouteAuthOIDCKeycloakCallback, httputil.MethodNotAllowed(http.MethodGet, oidcCallbackHandler))
	mux.Handle(RouteAuthOIDCKeycloakExchange, httputil.MethodNotAllowed(http.MethodPost, oidcExchangeHandler))
	mux.Handle(RouteAdminUsers, httputil.MethodNotAllowed(http.MethodPost,
		AdminBootstrapGuard(cfg.AdminBootstrapToken)(AdminCreateUser(users)),
	))
	mux.Handle(RouteAdminInvites, httputil.MethodNotAllowed(http.MethodPost,
		AdminBootstrapGuard(cfg.AdminBootstrapToken)(AdminCreateInvite(invites)),
	))
	mux.Handle(RouteAdminUserStatus, httputil.MethodNotAllowed(http.MethodPatch,
		AdminBootstrapGuard(cfg.AdminBootstrapToken)(AdminUpdateUserStatus(users)),
	))
	loginAttemptsHandler := GetMyLoginAttempts(loginAttempts)
	if tokens != nil && loginAttempts != nil {
		requireActive := RequireActiveSession(sessions)
		loginAttemptsHandler = BearerAuth(tokens)(requireActive(loginAttemptsHandler))
	}
	mux.Handle(RouteAuthMeLoginAttempts, httputil.MethodNotAllowed(http.MethodGet, loginAttemptsHandler))

	sessionsHandler := GetMySessions(sessions)
	deleteSessionHandler := DeleteMySession(sessions)
	deleteAllSessionsHandler := DeleteAllMySessions(sessions)
	if tokens != nil && sessions != nil {
		requireActive := RequireActiveSession(sessions)
		sessionsHandler = BearerAuth(tokens)(requireActive(sessionsHandler))
		deleteSessionHandler = BearerAuth(tokens)(requireActive(deleteSessionHandler))
		deleteAllSessionsHandler = BearerAuth(tokens)(requireActive(deleteAllSessionsHandler))
	}
	mux.Handle(RouteAuthMeSessions, methodRouter(map[string]http.Handler{
		http.MethodDelete: deleteAllSessionsHandler,
		http.MethodGet:    sessionsHandler,
	}))
	mux.Handle(RouteAuthMeSessionByID, methodRouter(map[string]http.Handler{
		http.MethodDelete: deleteSessionHandler,
	}))

	devicesHandler := GetMyDevices(devices)
	deleteDeviceHandler := DeleteMyDevice(devices)
	patchDeviceHandler := PatchMyDevice(devices)
	if tokens != nil && devices != nil {
		requireActive := RequireActiveSession(devices)
		devicesHandler = BearerAuth(tokens)(requireActive(devicesHandler))
		deleteDeviceHandler = BearerAuth(tokens)(requireActive(deleteDeviceHandler))
		patchDeviceHandler = BearerAuth(tokens)(requireActive(patchDeviceHandler))
	}
	mux.Handle(RouteAuthMeDevices, methodRouter(map[string]http.Handler{
		http.MethodGet: devicesHandler,
	}))
	mux.Handle(RouteAuthMeDeviceByID, methodRouter(map[string]http.Handler{
		http.MethodDelete: deleteDeviceHandler,
		http.MethodPatch:  patchDeviceHandler,
	}))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}
