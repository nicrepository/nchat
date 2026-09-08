package config

import (
	"time"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
)

// Issue #742: the notification outbox worker's configuration.
//
// It lives in its own file, and in its own struct inside Config, because it is a
// second worker with a second set of knobs and folding nine more fields into the
// flat SMTP block would leave nothing saying which belongs to which.
//
// Every value is bounded. A poll interval of zero is a busy loop, a batch of a
// hundred thousand is an unbounded claim, and a delivery timeout longer than the
// lease hands a row to a second worker mid-send — so none of the three can be
// configured. Out-of-range values are clamped rather than refused: the process
// must keep starting, and a clamped worker that says so is better operationally
// than a pod that will not boot because somebody typed a zero.

const (
	// notificationFinaliseGraceSeconds is what a pass reserves, after the last
	// delivery returns, to record the outcomes.
	//
	// Without it a pass that spent its whole budget delivering would try to write
	// the results through a context that had already expired, every row would
	// stay unfinalised until its lease ran out, and every one of them would be
	// delivered again. The SMTP worker reserves the same thing for the same
	// reason.
	notificationFinaliseGraceSeconds = 5

	notificationDefaultPollSeconds      = 5
	notificationDefaultBatchSize        = 10
	notificationDefaultMaxConcurrency   = 5
	notificationDefaultLeaseSeconds     = 60
	notificationDefaultMaxAttempts      = 6
	notificationDefaultRetryBaseSeconds = 30
	notificationDefaultRetryMaxSeconds  = 900
	notificationDefaultDeliverySeconds  = 10
)

// NotificationWorkerConfig bounds how the outbox worker consumes the backlog.
type NotificationWorkerConfig struct {
	// Enabled defaults to false. The worker has no delivery channel registered
	// yet, and a fail-safe default is what keeps an unconfigured deployment from
	// claiming events nothing can deliver.
	Enabled bool

	// PollSeconds is the idle interval between passes. Bounded below by one
	// second: the whole point of a poll interval is that an empty queue costs one
	// query per tick rather than a spinning loop.
	PollSeconds int

	// BatchSize bounds one claim, which bounds how much work a single worker can
	// hold at once and therefore how much a crash leaves to lease expiry.
	BatchSize int

	// MaxConcurrency bounds the deliveries in flight. It is what stops a backlog
	// of ten thousand from becoming ten thousand goroutines, and together with
	// BatchSize it is what makes the lease arithmetic below decidable.
	MaxConcurrency int

	// LeaseSeconds is how long a claimed event stays claimed. It must outlive the
	// work it protects; LeaseCoversProcessing is where that is checked.
	LeaseSeconds int

	// MaxAttempts is the ceiling after which a notification is failed rather than
	// retried. It is the difference between a retry policy and a retry loop.
	MaxAttempts int

	// RetryBaseSeconds and RetryMaxSeconds bound the exponential backoff.
	RetryBaseSeconds int
	RetryMaxSeconds  int

	// DeliveryTimeoutSeconds bounds one call into a delivery adapter, so a
	// provider that never answers cannot hold a lease open until it expires.
	DeliveryTimeoutSeconds int
}

// loadNotificationWorker reads the worker's environment, already normalized.
func loadNotificationWorker() NotificationWorkerConfig {
	return NotificationWorkerConfig{
		Enabled:                platformconfig.GetBool("NOTIFICATION_WORKER_ENABLED", false),
		PollSeconds:            platformconfig.GetInt("NOTIFICATION_WORKER_POLL_SECONDS", notificationDefaultPollSeconds),
		BatchSize:              platformconfig.GetInt("NOTIFICATION_WORKER_BATCH_SIZE", notificationDefaultBatchSize),
		MaxConcurrency:         platformconfig.GetInt("NOTIFICATION_WORKER_MAX_CONCURRENCY", notificationDefaultMaxConcurrency),
		LeaseSeconds:           platformconfig.GetInt("NOTIFICATION_WORKER_LEASE_SECONDS", notificationDefaultLeaseSeconds),
		MaxAttempts:            platformconfig.GetInt("NOTIFICATION_WORKER_MAX_ATTEMPTS", notificationDefaultMaxAttempts),
		RetryBaseSeconds:       platformconfig.GetInt("NOTIFICATION_WORKER_RETRY_BASE_SECONDS", notificationDefaultRetryBaseSeconds),
		RetryMaxSeconds:        platformconfig.GetInt("NOTIFICATION_WORKER_RETRY_MAX_SECONDS", notificationDefaultRetryMaxSeconds),
		DeliveryTimeoutSeconds: platformconfig.GetInt("NOTIFICATION_DELIVERY_TIMEOUT_SECONDS", notificationDefaultDeliverySeconds),
	}.Normalized()
}

// Normalized returns the configuration with every value inside its bound.
//
// Idempotent, and called wherever the numbers are used rather than only at load
// time, so a struct built by hand in a test cannot divide by a zero concurrency
// or produce a negative interval.
func (c NotificationWorkerConfig) Normalized() NotificationWorkerConfig {
	c.PollSeconds = clampSeconds(c.PollSeconds, 1, 300, notificationDefaultPollSeconds)
	c.BatchSize = clampSeconds(c.BatchSize, 1, 200, notificationDefaultBatchSize)
	c.MaxConcurrency = clampSeconds(c.MaxConcurrency, 1, 64, notificationDefaultMaxConcurrency)
	c.LeaseSeconds = clampSeconds(c.LeaseSeconds, 5, 3600, notificationDefaultLeaseSeconds)
	c.MaxAttempts = clampSeconds(c.MaxAttempts, 1, 20, notificationDefaultMaxAttempts)
	c.RetryBaseSeconds = clampSeconds(c.RetryBaseSeconds, 1, 600, notificationDefaultRetryBaseSeconds)
	c.RetryMaxSeconds = clampSeconds(c.RetryMaxSeconds, 1, 86400, notificationDefaultRetryMaxSeconds)
	c.DeliveryTimeoutSeconds = clampSeconds(c.DeliveryTimeoutSeconds, 1, 120, notificationDefaultDeliverySeconds)
	if c.RetryMaxSeconds < c.RetryBaseSeconds {
		// A ceiling below the first step is not a policy, it is a typo. Raising
		// the ceiling keeps the backoff monotonic instead of inverting it.
		c.RetryMaxSeconds = c.RetryBaseSeconds
	}
	return c
}

// clampSeconds bounds a configured integer, falling back to the default when it
// is not positive at all.
func clampSeconds(value, minimum, maximum, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return min(max(value, minimum), maximum)
}

// ProcessingBudget is the longest one pass may legitimately take: every wave of
// concurrent deliveries at the full timeout, plus the grace reserved for writing
// down what happened.
//
// This is the single answer to "how long may a pass in flight still need?", and
// three places have to agree on it or they disagree about what a valid
// configuration is:
//
//   - LeaseCoversProcessing, which refuses a lease shorter than this;
//   - the worker, which bounds its protected pass context by it;
//   - App's shutdown, which waits this long for a pass to drain.
//
// The lifecycle used a fixed 40s instead, and a perfectly valid configuration
// could exceed it — batch 5, concurrency 1, a 10s delivery timeout and a 60s
// lease is accepted by LeaseCoversProcessing and budgets 55s. Shutdown then
// stopped waiting 15s before the pass was entitled to finish, and a process
// that exited at that point could leave a delivery the provider had accepted
// unrecorded, to be retried after its lease expired. One function, so that
// cannot drift again.
//
// Bounded by construction: Normalized caps the batch at 200, concurrency at 1
// and the timeout at 120s, so the largest reachable budget is a little under
// seven hours — far from overflowing a Duration.
func (c NotificationWorkerConfig) ProcessingBudget() time.Duration {
	normalized := c.Normalized()
	waves := (normalized.BatchSize + normalized.MaxConcurrency - 1) / normalized.MaxConcurrency
	seconds := waves*normalized.DeliveryTimeoutSeconds + notificationFinaliseGraceSeconds
	return time.Duration(seconds) * time.Second
}

// ProtectedProcessingSeconds is ProcessingBudget in whole seconds, for the log
// lines and readiness messages that report it.
func (c NotificationWorkerConfig) ProtectedProcessingSeconds() int {
	return int(c.ProcessingBudget() / time.Second)
}

// LeaseCoversProcessing reports whether a claimed event stays claimed for at
// least as long as the worker may spend on it.
//
// When it does not, a second worker reclaims rows the first is still delivering
// — and with Blue/Green there is always a second worker. The worker refuses to
// run in that state rather than duplicating deliveries quietly, so readiness has
// to be able to see the same condition.
func (c NotificationWorkerConfig) LeaseCoversProcessing() bool {
	return c.Normalized().LeaseSeconds > c.ProtectedProcessingSeconds()
}

// NotificationWorkerReady returns (true, "") when the worker is either disabled
// or configured coherently, and (false, reason) when it is enabled on numbers
// that cannot work.
func (c Config) NotificationWorkerReady() (bool, string) {
	if !c.NotificationWorker.Enabled {
		return true, ""
	}
	if c.DatabaseURL == "" {
		return false, "DATABASE_URL is required by the notification worker"
	}
	if !c.NotificationWorker.LeaseCoversProcessing() {
		return false, "NOTIFICATION_WORKER_LEASE_SECONDS is shorter than one batch of deliveries"
	}
	return true, ""
}
