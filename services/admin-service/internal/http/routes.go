package httpapi

const (
	RouteHealthz = "/healthz"
	RouteReadyz  = "/readyz"
	RouteVersion = "/version"

	// The Admin API paths, as admin-service registers them.
	//
	// The public contract is /api/admin/*: both the local Traefik gateway and
	// every Kubernetes overlay strip that prefix before the request reaches
	// this pod (see infra/traefik/local/dynamic.yml and the strip-admin-prefix
	// middleware in the overlays). Registering the stripped paths here is what
	// keeps the two halves of that contract in one shape instead of two that
	// can drift.
	RouteAdminSession   = "/session"
	RouteAdminBootstrap = "/bootstrap"
	RouteAdminAudit     = "/audit/events"
)
