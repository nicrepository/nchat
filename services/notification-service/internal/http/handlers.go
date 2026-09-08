package httpapi

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

const readinessTimeout = time.Second

func Healthz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.LivenessHandler(cfg.ServiceName, info.Version, info.Commit)
}

func Readyz(cfg config.Config, options routerOptions) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(cfg.ServiceName, info.Version, info.Commit,
		readinessChecks(cfg, options), readinessTimeout)
}

func Version(cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := buildinfo.Current()
		httputil.WriteJSON(w, http.StatusOK, map[string]string{
			"service": cfg.ServiceName,
			"version": info.Version,
			"commit":  info.Commit,
		})
	})
}

func readinessChecks(cfg config.Config, options routerOptions) []health.Checker {
	return []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
		smtpWorkerCheck(cfg),
		smtpWorkerLivenessCheck(cfg, options.smtpWorkerProbe),
		notificationWorkerCheck(cfg),
		notificationWorkerLivenessCheck(cfg, options.notificationWorkerProbe),
	}
}

// notificationWorkerCheck reports a configuration the outbox worker cannot run
// on — most usefully a lease too short to cover one batch of deliveries, which
// is the setting that would otherwise let two workers deliver one notification.
func notificationWorkerCheck(cfg config.Config) health.Checker {
	if !cfg.NotificationWorker.Enabled {
		return health.NewStaticChecker("notification-worker-config", true, health.CheckPass, "")
	}
	if ready, reason := cfg.NotificationWorkerReady(); !ready {
		return health.NewStaticChecker("notification-worker-config", true, health.CheckFail, reason)
	}
	return health.NewStaticChecker("notification-worker-config", true, health.CheckPass, "")
}

// notificationWorkerLivenessCheck answers "is the outbox worker actually
// running", which the configuration check cannot: a worker with no delivery
// channel, or one that stopped, leaves the pod Ready with nothing draining the
// backlog.
func notificationWorkerLivenessCheck(cfg config.Config, probe func() bool) health.Checker {
	if !cfg.NotificationWorker.Enabled || probe == nil {
		return health.NewStaticChecker("notification-worker-running", true, health.CheckPass, "")
	}
	if !probe() {
		return health.NewStaticChecker("notification-worker-running", true, health.CheckFail,
			"notification worker is enabled but not running")
	}
	return health.NewStaticChecker("notification-worker-running", true, health.CheckPass, "")
}

// smtpWorkerLivenessCheck answers "is the worker actually running", which the
// configuration check cannot: a valid configuration whose worker has since
// stopped used to leave the pod Ready with nothing sending mail.
func smtpWorkerLivenessCheck(cfg config.Config, probe func() bool) health.Checker {
	// Disabled on purpose is not a fault, and a caller with no worker to report
	// on gets no opinion rather than a failure.
	if !cfg.SMTPWorkerEnabled || probe == nil {
		return health.NewStaticChecker("smtp-worker-running", true, health.CheckPass, "")
	}
	if !probe() {
		return health.NewStaticChecker("smtp-worker-running", true, health.CheckFail,
			"smtp worker is enabled but not running")
	}
	return health.NewStaticChecker("smtp-worker-running", true, health.CheckPass, "")
}

func smtpWorkerCheck(cfg config.Config) health.Checker {
	if !cfg.SMTPWorkerEnabled {
		return health.NewStaticChecker("smtp-worker-config", true, health.CheckPass, "")
	}

	ready, reason := cfg.SMTPWorkerReady()
	if !ready {
		return health.NewStaticChecker("smtp-worker-config", true, health.CheckFail, reason)
	}
	return health.NewStaticChecker("smtp-worker-config", true, health.CheckPass, "")
}
