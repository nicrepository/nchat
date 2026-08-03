package config

import "testing"

func TestLoadCallConfiguration(t *testing.T) {
	t.Setenv("CALL_RING_TIMEOUT_SECONDS", "")
	t.Setenv("CALL_START_RATE_LIMIT_MAX_ACTIONS", "")
	t.Setenv("CALL_START_RATE_LIMIT_WINDOW_SECONDS", "")
	defaults := Load()
	if defaults.CallRingTimeoutSeconds != 30 || defaults.CallStartRateLimitMaxActions != 10 ||
		defaults.CallStartRateLimitWindowSeconds != 60 {
		t.Fatalf("unsafe call defaults: %+v", defaults)
	}

	t.Setenv("CALL_RING_TIMEOUT_SECONDS", "45")
	t.Setenv("CALL_START_RATE_LIMIT_MAX_ACTIONS", "4")
	t.Setenv("CALL_START_RATE_LIMIT_WINDOW_SECONDS", "90")
	configured := Load()
	if configured.CallRingTimeoutSeconds != 45 || configured.CallStartRateLimitMaxActions != 4 ||
		configured.CallStartRateLimitWindowSeconds != 90 {
		t.Fatalf("call config not loaded: %+v", configured)
	}
}
