package app

import (
	"log/slog"
	"net/http"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
)

type App struct {
	Config  config.Config
	Logger  *slog.Logger
	Handler http.Handler
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	return &App{Config: cfg, Logger: logger, Handler: httpapi.NewRouter(cfg, logger)}
}
