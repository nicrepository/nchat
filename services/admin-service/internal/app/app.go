package app

import (
	"context"
	"log/slog"
	"net/http"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/admin-service/internal/http"
)

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)
	return &App{Config: cfg, Logger: logger, Handler: httpapi.NewRouter(cfg, logger), TracingShutdown: shutdown}
}
