package httpapi

const (
	RouteHealthz             = "/healthz"
	RouteReadyz              = "/readyz"
	RouteVersion             = "/version"
	RouteAdminUsers          = "/admin/users"
	RouteAdminInvites        = "/admin/invites"
	RouteAuthRefresh         = "/auth/refresh"
	RouteAuthLogout          = "/auth/logout"
	RouteAuthLogin           = "/auth/login"
	RouteAuthPasswordForgot  = "/auth/password/forgot"
	RouteAuthPasswordReset   = "/auth/password/reset"
	RouteAuthInvitesAccept   = "/auth/invites/accept"
	RouteAuthMeLoginAttempts = "/auth/me/login-attempts"
)
