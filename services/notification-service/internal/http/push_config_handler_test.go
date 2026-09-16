package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// Issue #862: the browser subscribes with the key the process that signs
// delivers with, and is told plainly when this deployment delivers nothing.

// configRouter wires the route exactly as app.New does: the subscription routes
// and the same notification worker probe readiness reads.
func configRouter(cfg config.Config, workerRunning func() bool) http.Handler {
	return NewRouter(cfg, platformlog.New("notification-service", "test"),
		WithNotificationWorkerProbe(workerRunning),
		WithPushSubscriptions(
			fakeValidator{identity: tokenIdentity{UserID: ownerUser, SessionID: ownerSession}},
			&fakeResolver{principals: map[string]domain.Principal{
				ownerUser: {UserID: ownerUser, WorkspaceID: ownerWorkspace},
			}},
			NewPushSubscriptionHandler(newFakeService())))
}

func running() bool { return true }

func requestPushConfig(router http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, RoutePushConfig, nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func getPushConfig(t *testing.T, cfg config.Config) *httptest.ResponseRecorder {
	t.Helper()
	response := requestPushConfig(configRouter(cfg, running))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	return response
}

func TestPushConfigOffersTheSigningPublicKeyWhenDeliveryIsReady(t *testing.T) {
	cfg := workingNotificationConfig()

	data := decodeEnvelope(t, getPushConfig(t, cfg))

	if data["vapid_public_key"] != cfg.WebPush.VAPIDPublicKey {
		t.Fatalf("vapid_public_key = %v, want the configured public key", data["vapid_public_key"])
	}
}

// The private half, the subject and the TTL are server configuration. The only
// field that may ever leave is the public key.
func TestPushConfigNeverCarriesThePrivateKey(t *testing.T) {
	cfg := workingNotificationConfig()

	response := getPushConfig(t, cfg)

	body := response.Body.String()
	if strings.Contains(body, cfg.WebPush.VAPIDPrivateKey) || strings.Contains(body, "mailto:") {
		t.Fatalf("response leaks server configuration: %s", body)
	}
	if data := decodeEnvelope(t, response); len(data) != 1 {
		t.Fatalf("response carries more than the public key: %v", data)
	}
}

func TestPushConfigDropsBase64Padding(t *testing.T) {
	cfg := workingNotificationConfig()
	want := cfg.WebPush.VAPIDPublicKey
	cfg.WebPush.VAPIDPublicKey = want + "="

	data := decodeEnvelope(t, getPushConfig(t, cfg))

	if data["vapid_public_key"] != want {
		t.Fatalf("vapid_public_key = %v, want %q", data["vapid_public_key"], want)
	}
}

// Each of these is a deployment in which a browser that subscribed would be
// told it is connected to a pipeline that delivers nothing.
func TestPushConfigReportsNoKeyWhenNothingDelivers(t *testing.T) {
	cases := map[string]func(*config.Config){
		"worker disabled": func(cfg *config.Config) { cfg.NotificationWorker.Enabled = false },
		"keys missing":    func(cfg *config.Config) { cfg.WebPush = config.WebPushConfig{} },
		"pair mismatched": func(cfg *config.Config) {
			cfg.WebPush.VAPIDPublicKey = workingWebPushConfig().VAPIDPublicKey
		},
		"no database": func(cfg *config.Config) { cfg.DatabaseURL = "" },
	}
	for name, breakConfig := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := workingNotificationConfig()
			breakConfig(&cfg)

			response := getPushConfig(t, cfg)

			if !strings.Contains(response.Body.String(), `"vapid_public_key":null`) {
				t.Fatalf("body = %s, want an explicit null key", response.Body.String())
			}
		})
	}
}

// Mounted without Authenticate by mistake, the handler still refuses: the key is
// public, but a route that answers without a principal is the first step to
// one that does more.
func TestPushConfigRefusesARequestWithoutAPrincipal(t *testing.T) {
	response := httptest.NewRecorder()

	PushConfig(workingNotificationConfig(), running).ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, RoutePushConfig, nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "vapid_public_key") {
		t.Fatalf("an unauthenticated request was served the key: %s", response.Body.String())
	}
}

// A deployment that is configured but whose worker is down is not "not
// configured", and it is not usable either. The probe is the one readiness
// reads, so the two answers cannot disagree.
func TestPushConfigReportsAStoppedWorkerAsUnavailableNotUnconfigured(t *testing.T) {
	cfg := workingNotificationConfig()
	alive := true
	router := configRouter(cfg, func() bool { return alive })

	if response := requestPushConfig(router); response.Code != http.StatusOK {
		t.Fatalf("status with a running worker = %d (%s)", response.Code, response.Body.String())
	}

	// The worker stops after boot: refused a lease, or its context ended.
	alive = false
	response := requestPushConfig(router)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status with a stopped worker = %d (%s)", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, errCodePushDeliveryUnavailable) {
		t.Fatalf("body = %s, want the %s code", body, errCodePushDeliveryUnavailable)
	}
	if strings.Contains(body, cfg.WebPush.VAPIDPublicKey) || strings.Contains(body, "vapid_public_key") {
		t.Fatalf("a stopped worker still offered a key to subscribe with: %s", body)
	}
	if code, _ := readinessBody(t, cfg, WithNotificationWorkerProbe(func() bool { return alive })); code == http.StatusOK {
		t.Fatal("readiness and push config disagree about the same stopped worker")
	}
}

// Disabled on purpose is a configuration fact, not an outage: the probe is not
// consulted, and the answer is the null key the client reads as not_configured.
func TestPushConfigReportsADisabledWorkerAsNotConfiguredWhateverTheProbeSays(t *testing.T) {
	cfg := workingNotificationConfig()
	cfg.NotificationWorker.Enabled = false

	response := requestPushConfig(configRouter(cfg, func() bool { return false }))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"vapid_public_key":null`) {
		t.Fatalf("status = %d body = %s, want 200 with a null key", response.Code, response.Body.String())
	}
}

// A worker that never started because its configuration is broken is also
// probed as not running; the configuration is what explains it, so the answer
// stays "not configured" rather than a transient outage a retry could fix.
func TestPushConfigReportsABrokenConfigurationAsNotConfiguredEvenWithTheWorkerDown(t *testing.T) {
	cfg := workingNotificationConfig()
	cfg.WebPush = config.WebPushConfig{}

	response := requestPushConfig(configRouter(cfg, func() bool { return false }))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"vapid_public_key":null`) {
		t.Fatalf("status = %d body = %s, want 200 with a null key", response.Code, response.Body.String())
	}
}
