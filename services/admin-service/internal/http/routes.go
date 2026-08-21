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
)
