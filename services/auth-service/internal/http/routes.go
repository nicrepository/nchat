package httpapi

const (
	RouteHealthz                  = "/healthz"
	RouteReadyz                   = "/readyz"
	RouteVersion                  = "/version"
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
	// Authenticated workspace-admin invitation endpoint. Its sibling
	// RouteAdminInvites above is the initialization-only route, reachable with
	// the bootstrap credential until the workspace has its first owner.
	//
	// RouteAdminInvites is the *only* route the bootstrap credential reaches.
	// It once had two siblings — POST /admin/users and PATCH
	// /admin/users/{id}/status — which created and suspended global identities
	// and, unlike this one, never consulted the bootstrap lifecycle: they
	// stayed reachable with the pre-shared credential long after the workspace
	// had an owner. They are gone rather than guarded, because bootstrapping
	// needs exactly one door and every additional one is a standing risk with
	// no remaining purpose.
	RouteAuthAdminInvites = "/auth/admin/invites"
)
