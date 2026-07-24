package httpapi

const (
	RouteHealthz      = "/healthz"
	RouteReadyz       = "/readyz"
	RouteVersion      = "/version"
	RouteLiveKitToken = "/media/livekit/token" //nolint:gosec // Public HTTP route path, not a credential.
)
