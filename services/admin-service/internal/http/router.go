package httpapi

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

const RouteMetrics = "/metrics"

// RouterDependencies are the privileged components the Admin API needs. They
// arrive already constructed so the router never decides whether a dependency
// is safe to build — it only decides whether it has one.
type RouterDependencies struct {
	TokenValidator  accessTokenValidator
	Sessions        AdminSessionManager
	Authenticator   AdminAuthenticator
	CSRF            CSRFValidator
	Audit           *AuditPorts
	RateLimiter     *IPRateLimiter
	ReadinessPinger PostgresPinger
	// Management is the issue #579 surface. It is a separate pointer so a
	// deployment that has the foundation wired but not the management services
	// answers 503 on those paths instead of serving one of them unguarded — the
	// same all-or-nothing rule the rest of this router applies.
	Management *ManagementPorts
	// Configuration is the issue #580 surface. Separate from Management for the
	// same reason Management is separate from the foundation: a deployment that
	// has one and not the other answers 503 on the missing paths rather than
	// serving any of them without their guards.
	Configuration ConfigAdmin
	// Observability is the issue #581 surface: the dashboard summary and the
	// Health Center. Separate for the same all-or-nothing reason as the others.
	Observability *ObservabilityPorts
	// HealthCollectors are the Prometheus collectors the health surface
	// exports. They are handed in already built so the router only decides
	// whether to register them, which is the same decision it makes about
	// every other collector: only when metrics are enabled.
	HealthCollectors []prometheus.Collector
}

// ManagementPorts groups the three management surfaces. They are wired
// together because they are enabled together: there is no supported deployment
// that serves the user directory and not the channel one.
type ManagementPorts struct {
	Users    UserAdmin
	Channels ChannelAdmin
	Policies PolicyAdmin
}

// NewManagementPorts wires the management services into the router.
func NewManagementPorts(users UserAdmin, channels ChannelAdmin, policies PolicyAdmin) *ManagementPorts {
	return &ManagementPorts{Users: users, Channels: channels, Policies: policies}
}

// AuditPorts groups the two directions of the audit trail. They are one
// component; splitting the interfaces keeps each consumer honest about which
// direction it uses — the capability guard only records, the audit endpoint
// only reads.
type AuditPorts struct {
	Recorder AuthorizationRecorder
	Reader   AuditReader
}

// NewAuditPorts wires the audit service into the router.
func NewAuditPorts(recorder AuthorizationRecorder, reader AuditReader) *AuditPorts {
	return &AuditPorts{Recorder: recorder, Reader: reader}
}

func NewRouter(cfg config.Config, logger *slog.Logger, dependencies ...RouterDependencies) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var deps RouterDependencies
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg, deps.ReadinessPinger)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())
	// Register is a no-op when PROMETHEUS_METRICS_ENABLED is unset, so the
	// health surface stays instrumented exactly as much as the rest of the
	// platform and never more.
	metrics.Register(deps.HealthCollectors...)

	registerAdminAPI(mux, cfg, logger, deps)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	// GeneratedRequestID rather than RequestID: on this service the request ID
	// becomes the correlation_id of an audit row, so it must be minted here and
	// not accepted from the caller. The rest of the platform keeps the
	// trace-propagating variant.
	return httputil.Recover(httputil.GeneratedRequestID(httputil.SecurityHeaders(CORS(cfg.AllowedOrigins)(obs(mux)))))
}

// registerAdminAPI assembles the privileged routes, and only when every part
// of the guard chain exists.
//
// The all-or-nothing shape is the point. A partially wired pod — no database,
// no JWT secret, a half-built session service — serves a refusal on these
// paths instead of an unguarded handler, so a configuration mistake cannot
// leave the Admin API reachable without its guards. This mirrors the same
// decision in auth-service's router.
func registerAdminAPI(mux *http.ServeMux, cfg config.Config, logger *slog.Logger, deps RouterDependencies) {
	createSession := adminUnavailable()
	destroySession := adminUnavailable()
	bootstrap := adminUnavailable()
	auditEvents := adminUnavailable()

	ready := deps.TokenValidator != nil && deps.Sessions != nil && deps.Authenticator != nil &&
		deps.CSRF != nil && deps.Audit != nil && deps.RateLimiter != nil
	if ready {
		requireSession := RequireAdminSession(deps.Authenticator, sessionCookieName)
		requireCSRF := RequireCSRF(deps.CSRF, cfg.AllowedOrigins)

		// The handshake is the one route authenticated by a bearer token
		// rather than by the administrative cookie, so it is also the one
		// route that carries its own rate limit.
		createSession = deps.RateLimiter.Middleware(
			BearerAuth(deps.TokenValidator)(
				CreateAdminSession(deps.Sessions, deps.Audit.Recorder, cfg, httputil.ParseCIDRs(cfg.TrustedProxyCIDRs)),
			),
		)
		destroySession = requireSession(requireCSRF(DestroyAdminSession(deps.Sessions, deps.Audit.Recorder, cfg, logger)))
		bootstrap = requireSession(Bootstrap(cfg, deps.Sessions))
		auditEvents = requireSession(
			RequireCapability(domain.CapabilityAuditRead, deps.Audit.Recorder)(
				ListAuditEvents(deps.Audit.Reader),
			),
		)
	}

	mux.Handle(RouteAdminSession, methodRouter(map[string]http.Handler{
		http.MethodPost:   createSession,
		http.MethodDelete: destroySession,
	}))
	mux.Handle(RouteAdminBootstrap, httputil.MethodNotAllowed(http.MethodGet, bootstrap))
	mux.Handle(RouteAdminAudit, httputil.MethodNotAllowed(http.MethodGet, auditEvents))

	registerManagementAPI(mux, cfg, deps, ready)
	registerConfigAPI(mux, cfg, deps, ready)
	registerObservabilityAPI(mux, cfg, deps, ready)
}

// observabilityRoute is one observability endpoint as the table below declares
// it.
type observabilityRoute struct {
	path       string
	method     string
	capability domain.Capability
	handler    func(*ObservabilityPorts) http.Handler
}

// registerObservabilityAPI wires the issue #581 surface.
//
// Same guard chain and same order as every other surface: administrative
// session, then — for the refresh, which is a POST — the origin and CSRF
// checks, then the one capability the route declares.
//
// All three require admin.infrastructure.read, and no new capability was
// introduced for them. The console's navigation map already declared that
// capability for the Health Center and the system section, the platform
// already defines it, the database CHECK in migration 000008 already allows
// it, and the RBAC matrix already documents it. Minting a capability to hold
// what an existing one is already for would have widened the model for
// nothing; widening admin.superuser instead would have been worse.
//
// Why a read is guarded as strictly as a write: the dashboard reports how many
// people are signed in and how much traffic the platform carries, and the
// Health Center names every dependency the deployment has. Both are
// reconnaissance for anyone who should not be holding this session.
func registerObservabilityAPI(mux *http.ServeMux, cfg config.Config, deps RouterDependencies, ready bool) {
	routes := []observabilityRoute{
		{RouteAdminOverview, http.MethodGet, domain.CapabilityInfrastructureRead, GetOverview},
		{RouteAdminHealth, http.MethodGet, domain.CapabilityInfrastructureRead, ListHealthChecks},
		{RouteAdminHealthRefresh, http.MethodPost, domain.CapabilityInfrastructureRead, RefreshHealth},
	}
	enabled := ready && deps.Observability != nil &&
		deps.Observability.Dashboard != nil && deps.Observability.Health != nil
	for _, route := range routes {
		handler := adminUnavailable()
		if enabled {
			handler = guardObservability(cfg, deps, route)
		}
		mux.Handle(route.path, httputil.MethodNotAllowed(route.method, handler))
	}
}

func guardObservability(cfg config.Config, deps RouterDependencies, route observabilityRoute) http.Handler {
	handler := RequireCapability(route.capability, deps.Audit.Recorder)(route.handler(deps.Observability))
	if !isSafeMethod(route.method) {
		handler = RequireCSRF(deps.CSRF, cfg.AllowedOrigins)(handler)
	}
	return RequireAdminSession(deps.Authenticator, sessionCookieName)(handler)
}

// registerConfigAPI wires the issue #580 surface.
//
// Same guard chain, same order, same table shape as the management routes:
// administrative session, then CSRF and origin for a mutation, then the one
// capability the route declares. Reading the configuration catalog is guarded
// exactly like changing it, one capability weaker, because the catalog names
// every integration and credential the deployment has.
//
// admin.config.manage is what the two write routes require. The additional
// admin.superuser check for a value that weakens the platform lives in the
// service and not here, because whether a value is dangerous is only knowable
// after it has been parsed against its definition — a route cannot decide it,
// and pretending otherwise would put half the rule in a place that never sees
// the value.
func registerConfigAPI(mux *http.ServeMux, cfg config.Config, deps RouterDependencies, ready bool) {
	routes := []configRoute{
		{RouteAdminConfig, http.MethodGet, domain.CapabilityConfigRead, GetConfiguration},
		{RouteAdminConfigPreview, http.MethodPost, domain.CapabilityConfigRead, PreviewConfiguration},
		{RouteAdminConfigApply, http.MethodPost, domain.CapabilityConfigManage, ApplyConfiguration},
		{RouteAdminConfigVersions, http.MethodGet, domain.CapabilityConfigRead, ListConfigurationVersions},
		{RouteAdminConfigRollbackPreview, http.MethodPost, domain.CapabilityConfigRead, PreviewConfigurationRollback},
		{RouteAdminConfigVersionRollback, http.MethodPost, domain.CapabilityConfigManage, RollbackConfiguration},
	}
	enabled := ready && deps.Configuration != nil
	for _, route := range routes {
		handler := adminUnavailable()
		if enabled {
			handler = guardConfig(cfg, deps, route)
		}
		mux.Handle(route.path, httputil.MethodNotAllowed(route.method, handler))
	}
}

// configRoute is one configuration endpoint as the table above declares it.
type configRoute struct {
	path       string
	method     string
	capability domain.Capability
	handler    func(ConfigAdmin) http.Handler
}

func guardConfig(cfg config.Config, deps RouterDependencies, route configRoute) http.Handler {
	handler := RequireCapability(route.capability, deps.Audit.Recorder)(route.handler(deps.Configuration))
	if !isSafeMethod(route.method) {
		handler = RequireCSRF(deps.CSRF, cfg.AllowedOrigins)(handler)
	}
	return RequireAdminSession(deps.Authenticator, sessionCookieName)(handler)
}

// managedRoute is one management endpoint as the table below declares it.
//
// The capability is a field rather than something applied at the call site
// because that is what makes the whole surface reviewable in one place: reading
// the table below answers "what does this endpoint require" for every route,
// and a route added without a capability does not compile.
type managedRoute struct {
	path       string
	method     string
	capability domain.Capability
	// mutating routes additionally pass the CSRF guard. It is derived from the
	// method rather than declared, so a new mutation cannot be added without
	// it.
	handler func(*ManagementPorts) http.Handler
}

// registerManagementAPI wires the issue #579 surface.
//
// Every route is guarded the same way and in the same order: the administrative
// session first, then — for a mutation — the CSRF and origin checks, then the
// one capability the route declares. There is no path that skips a step and no
// second, "internal" router: a request that reaches a management handler has
// passed all of them.
func registerManagementAPI(mux *http.ServeMux, cfg config.Config, deps RouterDependencies, ready bool) {
	routes := []managedRoute{
		{RouteAdminUsers, http.MethodGet, domain.CapabilityUsersRead,
			func(p *ManagementPorts) http.Handler { return ListUsers(p.Users) }},
		{RouteAdminUser, http.MethodGet, domain.CapabilityUsersRead,
			func(p *ManagementPorts) http.Handler { return GetUser(p.Users) }},
		{RouteAdminUserStatus, http.MethodPatch, domain.CapabilityUsersManage,
			func(p *ManagementPorts) http.Handler { return UpdateUserStatus(p.Users) }},
		{RouteAdminUserSessions, http.MethodDelete, domain.CapabilityUsersManage,
			func(p *ManagementPorts) http.Handler { return RevokeUserSessions(p.Users) }},
		// Changing who administers the platform requires administering all of
		// it: a principal may only confer authority it already holds, so
		// anything narrower than superuser here would be horizontal escalation.
		{RouteAdminUserRoles, http.MethodPost, domain.CapabilitySuperuser,
			func(p *ManagementPorts) http.Handler { return GrantAdminRole(p.Users) }},
		{RouteAdminUserRole, http.MethodDelete, domain.CapabilitySuperuser,
			func(p *ManagementPorts) http.Handler { return RevokeAdminRole(p.Users) }},

		{RouteAdminChannels, http.MethodGet, domain.CapabilityChannelsRead,
			func(p *ManagementPorts) http.Handler { return ListChannels(p.Channels) }},
		{RouteAdminChannel, http.MethodGet, domain.CapabilityChannelsRead,
			func(p *ManagementPorts) http.Handler { return GetChannel(p.Channels) }},
		{RouteAdminChannelStatus, http.MethodPatch, domain.CapabilityChannelsManage,
			func(p *ManagementPorts) http.Handler { return UpdateChannelStatus(p.Channels) }},
		// Membership is a mutation, so it requires the manage capability and not
		// the read one, and it passes the CSRF guard like every other mutation.
		{RouteAdminChannelCandidates, http.MethodGet, domain.CapabilityChannelsManage,
			func(p *ManagementPorts) http.Handler { return ListMemberCandidates(p.Channels) }},
		{RouteAdminChannelMembers, http.MethodPost, domain.CapabilityChannelsManage,
			func(p *ManagementPorts) http.Handler { return AddChannelMembers(p.Channels) }},
		{RouteAdminChannelMember, http.MethodDelete, domain.CapabilityChannelsManage,
			func(p *ManagementPorts) http.Handler { return RemoveChannelMember(p.Channels) }},
		{RouteAdminConversations, http.MethodGet, domain.CapabilityChannelsRead,
			func(p *ManagementPorts) http.Handler { return ListConversations(p.Channels) }},

		{RouteAdminAntiSpam, http.MethodGet, domain.CapabilitySecurityRead,
			func(p *ManagementPorts) http.Handler { return ListAntiSpamPolicies(p.Policies) }},
		{RouteAdminAntiSpamUpdate, http.MethodPatch, domain.CapabilitySecurityManage,
			func(p *ManagementPorts) http.Handler { return UpdateAntiSpamPolicy(p.Policies) }},
		{RouteAdminUploadPolicy, http.MethodGet, domain.CapabilityInfrastructureRead,
			func(p *ManagementPorts) http.Handler { return ListUploadPolicies(p.Policies) }},
		{RouteAdminUploadPolicyOne, http.MethodPatch, domain.CapabilityInfrastructureManage,
			func(p *ManagementPorts) http.Handler { return UpdateUploadPolicy(p.Policies) }},
	}

	enabled := ready && deps.Management != nil &&
		deps.Management.Users != nil && deps.Management.Channels != nil && deps.Management.Policies != nil
	for _, route := range routes {
		handler := adminUnavailable()
		if enabled {
			handler = guardManagement(cfg, deps, route)
		}
		mux.Handle(route.path, httputil.MethodNotAllowed(route.method, handler))
	}
}

// guardManagement assembles one route's guard chain.
//
// CSRF runs before the capability check on purpose: a forged cross-site request
// must be refused as a forgery, not recorded in the audit trail as somebody's
// authorization failure. The operator reading that trail should see real
// denials, not noise a hostile page can generate at will.
func guardManagement(cfg config.Config, deps RouterDependencies, route managedRoute) http.Handler {
	handler := RequireCapability(route.capability, deps.Audit.Recorder)(route.handler(deps.Management))
	if !isSafeMethod(route.method) {
		handler = RequireCSRF(deps.CSRF, cfg.AllowedOrigins)(handler)
	}
	return RequireAdminSession(deps.Authenticator, sessionCookieName)(handler)
}

func adminUnavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeUnavailable(w)
	})
}

// methodRouter dispatches one path across methods, answering 405 with an
// Allow header for the rest. Same shape as auth-service's.
func methodRouter(handlers map[string]http.Handler) http.Handler {
	allowed := make([]string, 0, len(handlers)+1)
	for method := range handlers {
		allowed = append(allowed, method)
	}
	allowed = append(allowed, http.MethodOptions)
	allowHeader := joinSorted(allowed)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := handlers[r.Method]; ok {
			handler.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Allow", allowHeader)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		httputil.WriteError(w, http.StatusMethodNotAllowed, httputil.ErrCodeBadRequest, "method not allowed")
	})
}

func joinSorted(values []string) string {
	sorted := make([]string, len(values))
	copy(sorted, values)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}
