package config

import platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"

const (
	serviceName = "notification-service"
	defaultPort = 8084
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	DatabaseURL              string
	DBConnectTimeoutSeconds  int
	AuthEmailOutboxEncKey    string
	AuthPublicWebBaseURL     string

	SMTPHost              string
	SMTPPort              int
	SMTPUsername          string
	SMTPPassword          string
	SMTPFrom              string
	SMTPFromName          string
	SMTPTLSMode           string
	SMTPTimeoutSeconds    int
	SMTPMaxAttempts       int
	SMTPBackoffSeconds    int
	SMTPWorkerEnabled     bool
	SMTPWorkerPollSeconds int
}

func Load() Config {
	return Config{
		ServiceName:              serviceName,
		Env:                      platformconfig.GetString("APP_ENV", "development"),
		Port:                     platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds: platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:              platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:  platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AuthEmailOutboxEncKey:    platformconfig.GetString("AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY", ""),
		AuthPublicWebBaseURL:     platformconfig.GetString("AUTH_PUBLIC_WEB_BASE_URL", ""),

		SMTPHost:              platformconfig.GetString("SMTP_HOST", ""),
		SMTPPort:              platformconfig.GetInt("SMTP_PORT", 587),
		SMTPUsername:          platformconfig.GetString("SMTP_USERNAME", ""),
		SMTPPassword:          platformconfig.GetString("SMTP_PASSWORD", ""),
		SMTPFrom:              platformconfig.GetString("SMTP_FROM", ""),
		SMTPFromName:          platformconfig.GetString("SMTP_FROM_NAME", "NChat"),
		SMTPTLSMode:           platformconfig.GetString("SMTP_TLS_MODE", "starttls"),
		SMTPTimeoutSeconds:    platformconfig.GetInt("SMTP_TIMEOUT_SECONDS", 10),
		SMTPMaxAttempts:       platformconfig.GetInt("SMTP_MAX_ATTEMPTS", 5),
		SMTPBackoffSeconds:    platformconfig.GetInt("SMTP_BACKOFF_SECONDS", 60),
		SMTPWorkerEnabled:     platformconfig.GetBool("SMTP_WORKER_ENABLED", false),
		SMTPWorkerPollSeconds: platformconfig.GetInt("SMTP_WORKER_POLL_SECONDS", 10),
	}
}

// SMTPLeaseSeconds is how long a claimed outbox row is held. SMTPFinaliseGrace
// is what is reserved, after the send returns, to record the outcome.
//
// They live here rather than in the worker because three places need the same
// rule — the worker before it starts, App before it starts the worker, and
// readiness — and only config is below all three. Duplicating the arithmetic is
// how a lease and a timeout drift into incompatibility unnoticed.
const (
	SMTPLeaseSeconds         = 30
	SMTPFinaliseGraceSeconds = 5
)

// SMTPProtectedProcessingSeconds is the longest one message may occupy its
// lease: the send, plus recording what happened.
func (c Config) SMTPProtectedProcessingSeconds() int {
	seconds := c.SMTPTimeoutSeconds
	if seconds <= 0 {
		seconds = 10
	}
	return seconds + SMTPFinaliseGraceSeconds
}

// SMTPLeaseCoversProcessing reports whether a claimed row stays claimed for at
// least as long as the worker may spend on it.
//
// When it does not, a second worker can reclaim a row the first is still
// sending — and with Blue/Green there is always a second worker. The worker
// refuses to run in that state, so the condition has to be visible to
// readiness too, or the pod stays green with no worker behind it.
func (c Config) SMTPLeaseCoversProcessing() bool {
	return SMTPLeaseSeconds > c.SMTPProtectedProcessingSeconds()
}

// SMTPWorkerReady returns (true, "") when the worker is enabled and properly
// configured. Returns (false, reason) otherwise.
func (c Config) SMTPWorkerReady() (bool, string) {
	if !c.SMTPWorkerEnabled {
		return true, "" // disabled is always "ready" (not a misconfiguration)
	}
	if c.SMTPHost == "" {
		return false, "SMTP_HOST is required"
	}
	if c.SMTPFrom == "" {
		return false, "SMTP_FROM is required"
	}
	if c.SMTPTLSMode == "none" && c.Env != "development" && c.Env != "test" && c.Env != "local" {
		return false, "SMTP_TLS_MODE=none is only allowed in development/test/local environments"
	}
	if !c.SMTPLeaseCoversProcessing() {
		return false, "SMTP_TIMEOUT_SECONDS leaves no room to record a delivery inside the outbox lease"
	}
	return true, ""
}
