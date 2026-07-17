package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
)

const (
	serviceName = "media-service"
	defaultPort = 8087

	defaultMediaSpikeTokenTTLSeconds = 300
	minMediaSpikeTokenTTLSeconds     = 60
	maxMediaSpikeTokenTTLSeconds     = 600
)

var safeSpikeRoomPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Config struct {
	ServiceName               string
	Env                       string
	Port                      int
	ReadHeaderTimeoutSeconds  int
	ReadTimeoutSeconds        int
	WriteTimeoutSeconds       int
	MediaSpikeEnabled         bool
	MediaSpikeLocalOnly       bool
	LiveKitURL                string
	LiveKitAPIKey             string
	LiveKitAPISecret          string
	MediaSpikeRoom            string
	MediaSpikeAllowedOrigins  string
	MediaSpikeTokenTTLSeconds int
}

func Load() Config {
	return Config{
		ServiceName:               serviceName,
		Env:                       platformconfig.GetString("APP_ENV", "development"),
		Port:                      platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:  platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		ReadTimeoutSeconds:        positiveInt("READ_TIMEOUT_SECONDS", 10),
		WriteTimeoutSeconds:       positiveInt("WRITE_TIMEOUT_SECONDS", 10),
		MediaSpikeEnabled:         platformconfig.GetBool("MEDIA_SPIKE_ENABLED", false),
		MediaSpikeLocalOnly:       platformconfig.GetBool("MEDIA_SPIKE_LOCAL_ONLY", false),
		LiveKitURL:                strings.TrimSpace(platformconfig.GetString("LIVEKIT_URL", "")),
		LiveKitAPIKey:             strings.TrimSpace(platformconfig.GetString("LIVEKIT_API_KEY", "")),
		LiveKitAPISecret:          platformconfig.GetString("LIVEKIT_API_SECRET", ""),
		MediaSpikeRoom:            strings.TrimSpace(platformconfig.GetString("MEDIA_SPIKE_ROOM", "")),
		MediaSpikeAllowedOrigins:  strings.TrimSpace(platformconfig.GetString("MEDIA_SPIKE_ALLOWED_ORIGINS", "")),
		MediaSpikeTokenTTLSeconds: configuredInt("MEDIA_SPIKE_TOKEN_TTL_SECONDS", defaultMediaSpikeTokenTTLSeconds),
	}
}

func (c Config) Validate() error {
	if !c.MediaSpikeEnabled {
		return nil
	}
	if c.Env != "development" {
		return errors.New("media spike is restricted to the development environment")
	}
	if !c.MediaSpikeLocalOnly {
		return errors.New("media spike requires MEDIA_SPIKE_LOCAL_ONLY=true")
	}
	if c.LiveKitAPIKey == "" || c.LiveKitAPISecret == "" {
		return errors.New("media spike LiveKit credentials are not configured")
	}
	if !validLocalLiveKitURL(c.LiveKitURL) {
		return errors.New("media spike LiveKit URL must be a valid local ws or wss URL")
	}
	if !safeSpikeRoomPattern.MatchString(c.MediaSpikeRoom) {
		return errors.New("media spike room must use 1-64 ASCII letters, digits, underscores, or hyphens")
	}
	if c.MediaSpikeTokenTTLSeconds < minMediaSpikeTokenTTLSeconds || c.MediaSpikeTokenTTLSeconds > maxMediaSpikeTokenTTLSeconds {
		return fmt.Errorf("media spike token TTL must be between %d and %d seconds", minMediaSpikeTokenTTLSeconds, maxMediaSpikeTokenTTLSeconds)
	}
	if !validAllowedOrigins(c.MediaSpikeAllowedOrigins) {
		return errors.New("media spike allowed origins must contain valid HTTP origins")
	}
	return nil
}

func (c Config) MediaSpikeActive() bool {
	if !c.MediaSpikeEnabled || !c.MediaSpikeLocalOnly || c.Env != "development" {
		return false
	}
	return c.Validate() == nil
}

func validLocalLiveKitURL(raw string) bool {
	if !validLiveKitURL(raw) {
		return false
	}
	parsed, _ := url.Parse(raw)
	return isLoopbackHost(parsed.Hostname())
}

func validLiveKitURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/"
}

func validAllowedOrigins(raw string) bool {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		parsed, err := url.Parse(strings.TrimSpace(part))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !isApprovedLocalOriginHost(parsed.Hostname()) {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func isApprovedLocalOriginHost(host string) bool {
	return isLoopbackHost(host) || strings.EqualFold(host, "nchat.local")
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

func configuredInt(key string, fallback int) int {
	raw, configured := os.LookupEnv(key)
	if !configured {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}
