package app

import (
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/config"
)

func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "file-service", Env: "test", Port: 8083, ReadHeaderTimeoutSeconds: 5}
	app := New(cfg)
	if app == nil || app.Logger == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
	if app.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, app.Config)
	}
}
