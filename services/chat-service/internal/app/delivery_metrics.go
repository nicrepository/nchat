package app

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// hubDeliveryMetrics adapts the hub's realtime delivery outcomes to Prometheus
// (issue #947). Every label is a closed set; no user, connection, target or
// message ever becomes one.
type hubDeliveryMetrics struct {
	rechecks    *prometheus.CounterVec
	recoveries  *prometheus.CounterVec
	disconnects *prometheus.CounterVec
}

func newHubDeliveryMetrics(metrics *observability.Metrics) ws.DeliveryMetrics {
	rechecks := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "chat_ws_auth_recheck_total",
		Help: "Per-recipient authorization re-checks before realtime delivery, by target type and outcome (allowed, denied, timeout, error, client_closed).",
	}, []string{"target_type", "outcome"})
	recoveries := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "chat_ws_sync_recovery_total",
		Help: "Delivery gaps recorded after an unprovable re-check, and how recovery attempts ended (recorded, signalled, revoked, deferred).",
	}, []string{"outcome"})
	disconnects := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "chat_ws_disconnect_total",
		Help: "WebSocket connections dropped by the server, by reason (send_error, heartbeat, slow_client).",
	}, []string{"reason"})
	if !metrics.Register(rechecks, recoveries, disconnects) {
		return nil
	}
	return hubDeliveryMetrics{rechecks: rechecks, recoveries: recoveries, disconnects: disconnects}
}

func (m hubDeliveryMetrics) AuthRecheck(targetType, outcome string) {
	m.rechecks.WithLabelValues(targetType, outcome).Inc()
}

func (m hubDeliveryMetrics) SyncRecovery(outcome string) {
	m.recoveries.WithLabelValues(outcome).Inc()
}

func (m hubDeliveryMetrics) Disconnect(reason string) {
	m.disconnects.WithLabelValues(reason).Inc()
}
