package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const RouteMetrics = "/metrics"

func NewRouter(cfg config.Config, logger *slog.Logger, users service.UserCreator, auth service.AuthSessionManager, login service.LoginManager, password service.PasswordRecoveryManager, invites service.InviteManager) http.Handler {
	_ = logger

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())
	rateLimitKeyer := newRateLimitKeyer(cfg.AuthJWTHMACSecret)
	tokenEndpointLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, cfg.AuthTrustedProxyCIDRs)
	forgotIPLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, cfg.AuthTrustedProxyCIDRs)
	resetIPLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, cfg.AuthTrustedProxyCIDRs)
	inviteAcceptIPLimiter := NewTokenEndpointRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, cfg.AuthTrustedProxyCIDRs)
	forgotTargetLimiter := newTargetAwareRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, rateLimitKeyer, "forgot-email")
	resetTargetLimiter := newTargetAwareRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, rateLimitKeyer, "password-reset-token")
	inviteAcceptTargetLimiter := newTargetAwareRateLimiter(cfg.AuthTokenEndpointRateLimitPerMinute, cfg.AuthTokenEndpointRateLimitBurst, rateLimitKeyer, "invite-accept-token")
	mux.Handle(RouteAuthRefresh, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthRefresh(auth))))
	mux.Handle(RouteAuthLogout, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthLogout(auth))))
	mux.Handle(RouteAuthLogin, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthLogin(login))))
	forgotHandler := AuthForgotPassword(password, forgotTargetLimiter)
	if password != nil && emailHandoffAvailable(password) {
		forgotHandler = forgotIPLimiter.Middleware(forgotHandler)
	}
	mux.Handle(RouteAuthPasswordForgot, httputil.MethodNotAllowed(http.MethodPost, forgotHandler))
	mux.Handle(RouteAuthPasswordReset, httputil.MethodNotAllowed(http.MethodPost, resetIPLimiter.Middleware(AuthResetPassword(password, resetTargetLimiter))))
	mux.Handle(RouteAuthInvitesAccept, httputil.MethodNotAllowed(http.MethodPost, inviteAcceptIPLimiter.Middleware(AuthAcceptInvite(invites, inviteAcceptTargetLimiter))))
	mux.Handle(RouteAdminUsers, httputil.MethodNotAllowed(http.MethodPost,
		AdminBootstrapGuard(cfg.AdminBootstrapToken)(AdminCreateUser(users)),
	))
	mux.Handle(RouteAdminInvites, httputil.MethodNotAllowed(http.MethodPost,
		AdminBootstrapGuard(cfg.AdminBootstrapToken)(AdminCreateInvite(invites)),
	))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}
