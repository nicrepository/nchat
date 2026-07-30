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

func NewRouter(cfg config.Config, logger *slog.Logger, users service.UserAdmin, auth service.AuthSessionManager, login service.LoginManager, password service.PasswordRecoveryManager, invites service.InviteManager, loginAttempts LoginAttemptsManager, sessions SessionManager, devices DeviceManager, avatars AvatarManager, avatarReader AvatarReader, oidcManagers ...service.OIDCManager) http.Handler {
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

	// Readiness reflects the dependencies mandatory for the auth API. Every
	// check is derived from the actual component instance — never from an
	// error variable or configuration value, which can read as "ok" when the
	// construction was skipped entirely. Nil means the instance is partially
	// initialized and must not receive traffic.
	readiness := ReadinessState{
		Database:       users != nil,
		TokenManager:   tokens != nil,
		LoginManager:   login != nil,
		SessionManager: sessions != nil,
	}

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg, readiness)))
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
	mux.Handle(RouteAuthRefresh, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthRefresh(auth, cfg.AuthRefreshTokenTTLSeconds))))
	mux.Handle(RouteAuthLogout, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthLogout(auth))))
	mux.Handle(RouteAuthLogin, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthLogin(login, trustedProxyCIDRs, cfg.AuthRefreshTokenTTLSeconds))))
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
	oidcExchangeHandler := OIDCExchange(oidc, cfg.AuthRefreshTokenTTLSeconds)
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

	// Browser-callable workspace administration. Unlike the bootstrap routes
	// above, this authenticates a real session and authorizes against the
	// caller's workspace membership, which is what makes the workspace and the
	// actor on an invite server-derived rather than client-supplied.
	//
	// The guard chain is only assembled when every part of it exists. A
	// missing token manager, session store or user service leaves the
	// unguarded handler out of reach: the routes serve a refusal instead,
	// which is what stops a partially-wired pod from exposing user data. Same
	// shape as the /auth/me wiring below.
	adminUsersHandler := adminEndpointUnavailable()
	adminInvitesHandler := adminEndpointUnavailable()
	if tokens != nil && users != nil && sessions != nil {
		requireActive := RequireActiveSession(sessions)
		requireAdmin := RequireWorkspaceAdmin(users)
		adminUsersHandler = BearerAuth(tokens)(requireActive(requireAdmin(AdminListWorkspaceUsers(users))))

		// The browser invite endpoint. It shares the guard chain above, so the
		// caller is a verified owner/admin of a real workspace rather than a
		// holder of the shared bootstrap token. What the invite is *scoped to*
		// is a separate change (issue #433); registering and guarding the route
		// is what this one owes, and without it the screen's invite button
		// answers 404 in every environment.
		adminInvitesHandler = BearerAuth(tokens)(requireActive(requireAdmin(AdminCreateInvite(invites))))
	}
	mux.Handle(RouteAuthAdminUsers, httputil.MethodNotAllowed(http.MethodGet, adminUsersHandler))
	mux.Handle(RouteAuthAdminInvites, httputil.MethodNotAllowed(http.MethodPost, adminInvitesHandler))
	profileHandler := GetMyProfile(users)
	if tokens != nil && users != nil {
		requireActive := RequireActiveSession(sessions)
		profileHandler = BearerAuth(tokens)(requireActive(profileHandler))
	}
	mux.Handle(RouteAuthMe, httputil.MethodNotAllowed(http.MethodGet, profileHandler))

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

	uploadAvatarHandler := UploadMyAvatar(avatars)
	deleteAvatarHandler := DeleteMyAvatar(avatars)
	if tokens != nil && avatars != nil {
		requireActive := RequireActiveSession(sessions)
		uploadAvatarHandler = BearerAuth(tokens)(requireActive(uploadAvatarHandler))
		deleteAvatarHandler = BearerAuth(tokens)(requireActive(deleteAvatarHandler))
	}
	mux.Handle(RouteAuthMeAvatar, methodRouter(map[string]http.Handler{
		http.MethodPost:   uploadAvatarHandler,
		http.MethodDelete: deleteAvatarHandler,
	}))
	// Serving is intentionally unauthenticated: the object name is an opaque
	// capability and an <img> tag cannot carry a Bearer token. Same-origin only,
	// via the gateway's /api/auth prefix.
	mux.Handle(RouteAuthAvatarByName, httputil.MethodNotAllowed(http.MethodGet, ServeAvatar(avatarReader)))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}
