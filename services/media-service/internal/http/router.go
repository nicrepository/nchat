package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
)

const RouteMetrics = "/metrics"

func NewRouter(cfg config.Config, logger *slog.Logger) http.Handler {
	_ = logger

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())
	if cfg.MediaSpikeActive() {
		var issuer service.SpikeTokenIssuer
		liveKitIssuer, err := service.NewLiveKitSpikeTokenIssuer(service.SpikeTokenConfig{
			ServerURL: cfg.LiveKitURL,
			APIKey:    cfg.LiveKitAPIKey,
			APISecret: cfg.LiveKitAPISecret,
			Room:      cfg.MediaSpikeRoom,
			TTL:       time.Duration(cfg.MediaSpikeTokenTTLSeconds) * time.Second,
		})
		if err != nil {
			if logger != nil {
				logger.Warn("LiveKit spike token endpoint unavailable", "reason", "invalid_configuration")
			}
		} else {
			issuer = liveKitIssuer
		}
		mux.Handle(RouteSpikeToken, httputil.MethodNotAllowed(http.MethodPost, SpikeToken(issuer, cfg, logger)))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}
