package httpapi

const (
	RouteHealthz                  = "/healthz"
	RouteReadyz                   = "/readyz"
	RouteVersion                  = "/version"
	RouteAdminUsers               = "/admin/users"
	RouteAdminInvites             = "/admin/invites"
	RouteAuthRefresh              = "/auth/refresh"
	RouteAuthLogout               = "/auth/logout"
	RouteAuthLogin                = "/auth/login"
	RouteAuthPasswordForgot       = "/auth/password/forgot"
	RouteAuthPasswordReset        = "/auth/password/reset"
	RouteAuthInvitesAccept        = "/auth/invites/accept"
	RouteAuthMe                   = "/auth/me"
	RouteAuthMeLoginAttempts      = "/auth/me/login-attempts"
	RouteAuthMeSessions           = "/auth/me/sessions"
	RouteAuthMeSessionByID        = "/auth/me/sessions/{session_id}"
	RouteAuthMeDevices            = "/auth/me/devices"
	RouteAuthMeDeviceByID         = "/auth/me/devices/{device_id}"
	RouteAuthMeAvatar             = "/auth/me/avatar"
	RouteAuthAvatarByName         = "/auth/avatars/{name}"
	RouteAuthOIDCKeycloakLogin    = "/auth/oidc/keycloak/login"
	RouteAuthOIDCKeycloakCallback = "/auth/oidc/keycloak/callback"
	RouteAuthOIDCKeycloakExchange = "/auth/oidc/keycloak/exchange"
	RouteAdminUserStatus          = "/admin/users/{id}/status"
	// RF-74 workspace administration (issue #425). This lives under /auth, not
	// under /admin, because /auth is the only prefix a browser can reach on
	// this service: both the local Traefik gateway and the deployed Ingress
	// rewrite /api/auth/* to /auth/*, while /api/admin/* is routed to
	// admin-service instead. The sibling /admin/* routes above keep the
	// bootstrap-token guard and stay CLI-only.
	RouteAuthAdminUsers = "/auth/admin/users"
)
