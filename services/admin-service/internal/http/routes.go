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

	// Management routes (issue #579).
	//
	// Each names one resource and one operation. There is no generic
	// "/admin/mutate" and no endpoint that takes the object type as a
	// parameter: an endpoint that can be aimed at anything is an endpoint whose
	// authorization nobody can review, and every route below is registered with
	// exactly one required capability in router.go.
	//
	// scripts/ci/admin-route-contract-check.sh reads these constants and
	// asserts, against every rendered overlay, that /api/admin<route> reaches
	// this pod as <route>. That is why nothing here carries the public prefix.
	RouteAdminUsers          = "/users"
	RouteAdminUser           = "/users/{userID}"
	RouteAdminUserStatus     = "/users/{userID}/status"
	RouteAdminUserSessions   = "/users/{userID}/sessions"
	RouteAdminUserRoles      = "/users/{userID}/admin-roles"
	RouteAdminUserRole       = "/users/{userID}/admin-roles/{roleSlug}"
	RouteAdminChannels       = "/channels"
	RouteAdminChannel        = "/channels/{channelID}"
	RouteAdminChannelStatus  = "/channels/{channelID}/status"
	RouteAdminChannelMembers = "/channels/{channelID}/members"
	// The picker behind the add. Same capability as the mutation it feeds, so
	// read access to the channel directory does not also enumerate people.
	RouteAdminChannelCandidates = "/channels/{channelID}/member-candidates"
	RouteAdminChannelMember     = "/channels/{channelID}/members/{userID}"
	RouteAdminConversations     = "/conversations"
	RouteAdminAntiSpam          = "/policies/anti-spam"
	RouteAdminAntiSpamUpdate    = "/policies/anti-spam/{workspaceID}"
	RouteAdminUploadPolicy      = "/policies/upload"
	RouteAdminUploadPolicyOne   = "/policies/upload/{workspaceID}"

	// Configuration routes (issue #580).
	//
	// Semantic, and none of them takes the thing being changed as a path
	// parameter. There is no PATCH /config/{key}: the key travels in a body
	// that is resolved against a server-side registry, so an unregistered key
	// is refused rather than reaching a store that would take it literally.
	//
	// The apply route is separate from the read rather than being a second
	// method on it. That keeps every route in this service exactly one method
	// with exactly one capability, which is the property that makes the guard
	// table above reviewable at a glance.
	RouteAdminConfig                = "/config"
	RouteAdminConfigPreview         = "/config/preview"
	RouteAdminConfigApply           = "/config/apply"
	RouteAdminConfigVersions        = "/config/versions"
	RouteAdminConfigVersionRollback = "/config/versions/{versionID}/rollback"
	// The rollback preview is its own route rather than a flag on the generic
	// preview, because a rollback is not an edit that happens to carry old
	// values: the change set and the preconditions are derived from the
	// recorded version, server-side, and a client names only which version.
	RouteAdminConfigRollbackPreview = "/config/versions/{versionID}/rollback/preview"

	// Observability routes (issue #581).
	//
	// Three routes, none of which takes a destination. The dashboard is one
	// aggregate read so the console makes one request instead of one per card;
	// the health listing is the same collection in full; the refresh is a POST
	// with no body, which is what puts it behind the CSRF and origin guards
	// like every other non-safe method.
	//
	// There is deliberately no /health/{service} and no endpoint that accepts a
	// URL, host, port or DSN. A caller may narrow the listing with ?service=,
	// and that value is resolved against the compile-time registry in
	// domain/health.go before anything reads it: the set of addresses this pod
	// is willing to contact comes from its own environment and from nowhere
	// else, which is what stops a health check from becoming an SSRF
	// primitive.
	RouteAdminOverview      = "/overview"
	RouteAdminHealth        = "/health/services"
	RouteAdminHealthRefresh = "/health/refresh"

	// Integration routes (issue #582).
	//
	// Three routes, and none of them names a destination. The listing takes no
	// parameter at all; the diagnostic takes one path segment resolved against
	// the compile-time registry in domain/integration.go; the test message takes
	// an empty body and delivers to the authenticated administrator's own
	// address.
	//
	// The test message has a literal path rather than travelling through a
	// generic /integrations/{id}/actions/{action}. An endpoint parameterised by
	// the action is an endpoint whose effect nobody can review at the route, and
	// this one sends mail — so it is registered by name, with its own capability
	// and its own rate limit, like every other privileged operation in this
	// service.
	RouteAdminIntegrations         = "/integrations"
	RouteAdminIntegrationDiagnose  = "/integrations/{integrationID}/diagnose"
	RouteAdminIntegrationTestEmail = "/integrations/smtp/test-email"
)
