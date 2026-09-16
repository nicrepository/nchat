package httpapi

import (
	"net/http"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

// pushConfigView tells a browser which application server key to subscribe
// with (issue #862).
//
// The public half of the VAPID pair and nothing else. It is public by
// construction — every push service reads it out of each signed request — and
// it is served from the process that holds the private half, so a browser can
// never subscribe with a key this deployment does not sign with. null means
// this deployment delivers no Web Push, which the client reports as
// not_configured instead of subscribing to a channel nothing drains.
type pushConfigView struct {
	VAPIDPublicKey *string `json:"vapid_public_key"`
}

// PushConfig handles GET /api/notifications/push/config.
//
// Two questions, answered from the two sources readiness already uses and no
// third one:
//
//   - is Web Push configured — the worker is enabled and NotificationWorkerReady
//     accepts its whole configuration. Fixed for the life of the process, so it
//     is decided once. No: 200 with a null key;
//   - is it running now — notificationWorkerAlive, the same probe the
//     notification-worker-running readiness check reads, asked per request
//     because a worker can stop after boot. No: 503, with no detail.
func PushConfig(cfg config.Config, workerProbe func() bool) http.HandlerFunc {
	configured := pushConfigView{VAPIDPublicKey: deliverableVAPIDPublicKey(cfg)}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePrincipal(w, r); !ok {
			return
		}
		if configured.VAPIDPublicKey != nil && !notificationWorkerAlive(cfg, workerProbe) {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodePushDeliveryUnavailable,
				"web push delivery is temporarily unavailable")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, configured)
	}
}

// deliverableVAPIDPublicKey is the key a browser should subscribe with, or nil
// when this deployment is not configured to deliver Web Push.
//
// Padding is dropped because `applicationServerKey` is read as unpadded
// base64url by browsers, while the server accepts either form.
func deliverableVAPIDPublicKey(cfg config.Config) *string {
	if !cfg.NotificationWorker.Enabled {
		return nil
	}
	if ready, _ := cfg.NotificationWorkerReady(); !ready {
		return nil
	}
	key := strings.TrimRight(cfg.WebPush.VAPIDPublicKey, "=")
	return &key
}
