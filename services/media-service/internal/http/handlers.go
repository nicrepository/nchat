package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
)

const (
	readinessTimeout  = 4 * time.Second
	liveKitAPITimeout = 3 * time.Second
)

func Healthz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.LivenessHandler(cfg.ServiceName, info.Version, info.Commit)
}

func Readyz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(cfg.ServiceName, info.Version, info.Commit, readinessChecks(cfg), readinessTimeout)
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

func readinessChecks(cfg config.Config) []health.Checker {
	checks := []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
	}
	if cfg.LiveKitURL != "" {
		checks = append(checks, liveKitAPIChecker{rawURL: cfg.LiveKitURL})
	}
	return checks
}

type liveKitAPIChecker struct {
	rawURL  string
	timeout time.Duration
}

func (liveKitAPIChecker) Name() string   { return "livekit-api" }
func (liveKitAPIChecker) Critical() bool { return true }

func (c liveKitAPIChecker) Check(ctx context.Context) health.CheckResult {
	result := health.CheckResult{Name: c.Name(), Critical: c.Critical(), Status: health.CheckFail}
	endpoint, err := url.Parse(c.rawURL)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		result.Message = "invalid LiveKit URL"
		return result
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = liveKitAPITimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		result.Message = "invalid LiveKit URL"
		return result
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			result.Message = "LiveKit API timeout"
		case errors.Is(err, context.Canceled):
			result.Message = "LiveKit API check canceled"
		default:
			result.Message = "LiveKit API unavailable"
		}
		return result
	}
	defer func() {
		_ = response.Body.Close()
	}()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Message = "LiveKit API returned non-success status"
		return result
	}
	result.Status = health.CheckPass
	return result
}
