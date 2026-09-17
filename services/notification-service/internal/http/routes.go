package httpapi

const (
	RouteHealthz = "/healthz"
	RouteReadyz  = "/readyz"
	RouteVersion = "/version"

	// RoutePushSubscriptions registers a browser's Web Push subscription (POST)
	// and reports the caller's own subscriptions for reconcile (GET).
	RoutePushSubscriptions = "/api/notifications/push/subscriptions"
	// RoutePushSubscription disables one of the caller's own subscriptions.
	RoutePushSubscription = "/api/notifications/push/subscriptions/{subscriptionID}"
	// RoutePushConfig tells a browser which VAPID public key to subscribe with,
	// or that this deployment delivers no Web Push (issue #862).
	RoutePushConfig = "/api/notifications/push/config"
)
