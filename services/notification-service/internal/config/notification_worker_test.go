package config

import (
	"testing"
	"time"
)

// Issue #742: the worker's configuration is a safety boundary, not a preference.
// Every test here is about a value that, unbounded, would break something the
// worker promises — a poll interval of zero is a busy loop, a lease shorter than
// a batch is a duplicated delivery.

func TestNotificationWorkerDefaultsAreCoherent(t *testing.T) {
	cfg := NotificationWorkerConfig{}.Normalized()

	if cfg.PollSeconds != notificationDefaultPollSeconds {
		t.Fatalf("poll seconds = %d, want %d", cfg.PollSeconds, notificationDefaultPollSeconds)
	}
	if cfg.BatchSize != notificationDefaultBatchSize {
		t.Fatalf("batch size = %d, want %d", cfg.BatchSize, notificationDefaultBatchSize)
	}
	if cfg.MaxConcurrency != notificationDefaultMaxConcurrency {
		t.Fatalf("max concurrency = %d, want %d", cfg.MaxConcurrency, notificationDefaultMaxConcurrency)
	}
	// The one invariant everything else exists to protect.
	if !cfg.LeaseCoversProcessing() {
		t.Fatalf("the default lease of %ds does not cover a pass of %ds",
			cfg.LeaseSeconds, cfg.ProtectedProcessingSeconds())
	}
}

// A zero or negative value is a missing value, not a request for zero. Zero poll
// seconds in particular would be a spin loop against the database.
func TestNotificationWorkerRefusesNonPositiveValues(t *testing.T) {
	cfg := NotificationWorkerConfig{
		PollSeconds:            0,
		BatchSize:              -1,
		MaxConcurrency:         0,
		LeaseSeconds:           -30,
		MaxAttempts:            0,
		RetryBaseSeconds:       -5,
		RetryMaxSeconds:        0,
		DeliveryTimeoutSeconds: -10,
	}.Normalized()

	if cfg.PollSeconds <= 0 || cfg.BatchSize <= 0 || cfg.MaxConcurrency <= 0 ||
		cfg.LeaseSeconds <= 0 || cfg.MaxAttempts <= 0 || cfg.RetryBaseSeconds <= 0 ||
		cfg.RetryMaxSeconds <= 0 || cfg.DeliveryTimeoutSeconds <= 0 {
		t.Fatalf("a non-positive value survived normalization: %+v", cfg)
	}
}

func TestNotificationWorkerClampsValuesToTheirBounds(t *testing.T) {
	cfg := NotificationWorkerConfig{
		PollSeconds:            1_000_000,
		BatchSize:              1_000_000,
		MaxConcurrency:         1_000_000,
		LeaseSeconds:           1_000_000,
		MaxAttempts:            1_000_000,
		RetryBaseSeconds:       1_000_000,
		RetryMaxSeconds:        100_000_000,
		DeliveryTimeoutSeconds: 1_000_000,
	}.Normalized()

	if cfg.BatchSize != 200 {
		t.Fatalf("batch size = %d, want it clamped to 200", cfg.BatchSize)
	}
	if cfg.MaxConcurrency != 64 {
		t.Fatalf("max concurrency = %d, want it clamped to 64", cfg.MaxConcurrency)
	}
	if cfg.MaxAttempts != 20 {
		t.Fatalf("max attempts = %d, want it clamped to 20", cfg.MaxAttempts)
	}
	if cfg.LeaseSeconds != 3600 {
		t.Fatalf("lease seconds = %d, want it clamped to 3600", cfg.LeaseSeconds)
	}
}

// A retry ceiling below the first step would make the backoff shrink as attempts
// grow. It is raised rather than accepted.
func TestNotificationWorkerKeepsBackoffMonotonic(t *testing.T) {
	cfg := NotificationWorkerConfig{RetryBaseSeconds: 120, RetryMaxSeconds: 30}.Normalized()

	if cfg.RetryMaxSeconds < cfg.RetryBaseSeconds {
		t.Fatalf("retry max %ds is below retry base %ds", cfg.RetryMaxSeconds, cfg.RetryBaseSeconds)
	}
}

// Normalizing twice must not move anything: the derived numbers are read on
// every pass, so a non-idempotent clamp would drift.
func TestNotificationWorkerNormalizationIsIdempotent(t *testing.T) {
	once := NotificationWorkerConfig{PollSeconds: 7, BatchSize: 3}.Normalized()
	twice := once.Normalized()

	if once != twice {
		t.Fatalf("normalization is not idempotent: %+v then %+v", once, twice)
	}
}

// A batch that cannot be delivered inside its lease is the configuration that
// lets two workers hold one event. It has to be visible, not silently accepted.
func TestNotificationWorkerDetectsALeaseTooShortForABatch(t *testing.T) {
	cfg := NotificationWorkerConfig{
		BatchSize:              200,
		MaxConcurrency:         1,
		DeliveryTimeoutSeconds: 120,
		LeaseSeconds:           60,
	}

	if cfg.LeaseCoversProcessing() {
		t.Fatal("a 60s lease was accepted for 200 sequential 120s deliveries")
	}
}

// ProtectedProcessingSeconds is arithmetic over configured values, so it must
// survive a struct nobody normalized — including a zero concurrency, which is a
// division.
func TestNotificationWorkerProtectedProcessingSurvivesAZeroValueStruct(t *testing.T) {
	seconds := NotificationWorkerConfig{}.ProtectedProcessingSeconds()

	if seconds <= 0 {
		t.Fatalf("protected processing seconds = %d, want a positive budget", seconds)
	}
}

func TestNotificationWorkerReadyReportsWhyItCannotRun(t *testing.T) {
	tests := map[string]struct {
		cfg      Config
		wantOK   bool
		wantSaid bool
	}{
		"disabled is always ready": {
			cfg:    Config{},
			wantOK: true,
		},
		"enabled without a database": {
			cfg: Config{NotificationWorker: NotificationWorkerConfig{
				Enabled: true,
			}.Normalized()},
			wantOK:   false,
			wantSaid: true,
		},
		"enabled with a lease that cannot cover a batch": {
			cfg: Config{
				DatabaseURL: "postgres://localhost/nchat",
				NotificationWorker: NotificationWorkerConfig{
					Enabled:                true,
					BatchSize:              200,
					MaxConcurrency:         1,
					DeliveryTimeoutSeconds: 120,
					LeaseSeconds:           60,
				},
			},
			wantOK:   false,
			wantSaid: true,
		},
		"enabled and coherent": {
			cfg: Config{
				DatabaseURL:        "postgres://localhost/nchat",
				NotificationWorker: NotificationWorkerConfig{Enabled: true}.Normalized(),
			},
			wantOK: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ok, reason := tc.cfg.NotificationWorkerReady()
			if ok != tc.wantOK {
				t.Fatalf("ready = %v (%q), want %v", ok, reason, tc.wantOK)
			}
			if tc.wantSaid && reason == "" {
				t.Fatal("a refusal must say why")
			}
		})
	}
}

func TestLoadReadsTheNotificationWorkerEnvironment(t *testing.T) {
	t.Setenv("NOTIFICATION_WORKER_ENABLED", "true")
	t.Setenv("NOTIFICATION_WORKER_BATCH_SIZE", "7")
	t.Setenv("NOTIFICATION_WORKER_MAX_ATTEMPTS", "3")

	cfg := Load()

	if !cfg.NotificationWorker.Enabled {
		t.Fatal("NOTIFICATION_WORKER_ENABLED was not read")
	}
	if cfg.NotificationWorker.BatchSize != 7 {
		t.Fatalf("batch size = %d, want 7", cfg.NotificationWorker.BatchSize)
	}
	if cfg.NotificationWorker.MaxAttempts != 3 {
		t.Fatalf("max attempts = %d, want 3", cfg.NotificationWorker.MaxAttempts)
	}
}

// ProcessingBudget is the single answer to "how long may a pass in flight still
// need?", and three places read it: the lease validation, the worker's protected
// pass context, and App's shutdown wait. A drift here is a shutdown that
// abandons work the configuration said was legitimate.
func TestProcessingBudgetCountsEveryWaveAndTheGrace(t *testing.T) {
	tests := map[string]struct {
		cfg  NotificationWorkerConfig
		want time.Duration
	}{
		// The configuration the third review named: five sequential waves of a
		// 10s delivery, plus the 5s grace. Valid, and past the fixed 40s the
		// lifecycle used to wait.
		"five sequential waves": {
			NotificationWorkerConfig{
				BatchSize: 5, MaxConcurrency: 1, DeliveryTimeoutSeconds: 10, LeaseSeconds: 60,
			},
			55 * time.Second,
		},
		// Concurrency divides the waves, so the same batch costs one wave.
		"one concurrent wave": {
			NotificationWorkerConfig{
				BatchSize: 5, MaxConcurrency: 5, DeliveryTimeoutSeconds: 10, LeaseSeconds: 60,
			},
			15 * time.Second,
		},
		// A partial wave still costs a whole one.
		"partial final wave": {
			NotificationWorkerConfig{
				BatchSize: 7, MaxConcurrency: 3, DeliveryTimeoutSeconds: 10, LeaseSeconds: 60,
			},
			35 * time.Second,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.cfg.ProcessingBudget(); got != tc.want {
				t.Fatalf("processing budget = %s, want %s", got, tc.want)
			}
			if got := tc.cfg.ProtectedProcessingSeconds(); time.Duration(got)*time.Second != tc.want {
				t.Fatalf("the seconds view (%d) disagrees with the budget (%s)", got, tc.want)
			}
		})
	}
}

// The lease validation and the budget must be the same computation, or a
// configuration could be accepted whose pass cannot finish inside its lease.
func TestLeaseCoversProcessingReadsTheProcessingBudget(t *testing.T) {
	cfg := NotificationWorkerConfig{
		BatchSize: 5, MaxConcurrency: 1, DeliveryTimeoutSeconds: 10, LeaseSeconds: 60,
	}.Normalized()

	if !cfg.LeaseCoversProcessing() {
		t.Fatalf("a 60s lease must cover a %s pass", cfg.ProcessingBudget())
	}

	// One second below the budget is refused.
	tooShort := cfg
	tooShort.LeaseSeconds = int(cfg.ProcessingBudget() / time.Second)
	if tooShort.LeaseCoversProcessing() {
		t.Fatalf("a %ds lease was accepted for a %s pass",
			tooShort.LeaseSeconds, cfg.ProcessingBudget())
	}
}

// The budget is bounded by the configuration clamps, so it can never overflow
// or become negative however hostile the environment is.
func TestProcessingBudgetStaysBounded(t *testing.T) {
	extreme := NotificationWorkerConfig{
		BatchSize:              1 << 30,
		MaxConcurrency:         1,
		DeliveryTimeoutSeconds: 1 << 30,
	}.ProcessingBudget()

	if extreme <= 0 {
		t.Fatalf("processing budget = %s, want a positive bound", extreme)
	}
	if extreme > 24*time.Hour {
		t.Fatalf("processing budget = %s, want it bounded by the configuration clamps", extreme)
	}
}
