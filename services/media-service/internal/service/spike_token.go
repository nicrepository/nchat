package service

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/livekit/protocol/auth"
)

const maxSpikeParticipantFieldLength = 64

var (
	ErrInvalidSpikeConfig   = errors.New("invalid LiveKit spike configuration")
	ErrInvalidSpikeRoom     = errors.New("invalid spike room")
	ErrInvalidSpikeIdentity = errors.New("invalid spike identity")
	ErrInvalidSpikeName     = errors.New("invalid spike participant name")

	safeSpikeIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

type SpikeTokenConfig struct {
	ServerURL string
	APIKey    string
	APISecret string
	Room      string
	TTL       time.Duration
}

type SpikeToken struct {
	ServerURL        string `json:"serverUrl"`
	Token            string `json:"token"`
	Room             string `json:"room"`
	Identity         string `json:"identity"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}

type SpikeTokenIssuer interface {
	Issue(room, identity, name string) (SpikeToken, error)
}

type LiveKitSpikeTokenIssuer struct {
	config SpikeTokenConfig
}

func NewLiveKitSpikeTokenIssuer(cfg SpikeTokenConfig) (*LiveKitSpikeTokenIssuer, error) {
	if strings.TrimSpace(cfg.ServerURL) == "" || strings.TrimSpace(cfg.APIKey) == "" || cfg.APISecret == "" || !safeSpikeIdentifierPattern.MatchString(cfg.Room) || cfg.TTL <= 0 {
		return nil, ErrInvalidSpikeConfig
	}
	return &LiveKitSpikeTokenIssuer{config: cfg}, nil
}

func (i *LiveKitSpikeTokenIssuer) Issue(room, identity, name string) (SpikeToken, error) {
	if !safeSpikeIdentifierPattern.MatchString(room) || room != i.config.Room {
		return SpikeToken{}, ErrInvalidSpikeRoom
	}
	if !safeSpikeIdentifierPattern.MatchString(identity) {
		return SpikeToken{}, ErrInvalidSpikeIdentity
	}
	name = strings.TrimSpace(name)
	if !validSpikeName(name) {
		return SpikeToken{}, ErrInvalidSpikeName
	}

	grant := &auth.VideoGrant{
		RoomJoin:          true,
		Room:              room,
		CanPublishSources: []string{"camera", "microphone"},
	}
	grant.SetCanPublish(true)
	grant.SetCanSubscribe(true)
	grant.SetCanPublishData(false)
	grant.SetCanUpdateOwnMetadata(false)

	accessToken := auth.NewAccessToken(i.config.APIKey, i.config.APISecret).
		SetIdentity(identity).
		SetValidFor(i.config.TTL).
		SetVideoGrant(grant)
	if name != "" {
		accessToken.SetName(name)
	}
	token, err := accessToken.ToJWT()
	if err != nil {
		return SpikeToken{}, errors.New("could not issue LiveKit spike token")
	}

	return SpikeToken{
		ServerURL:        i.config.ServerURL,
		Token:            token,
		Room:             room,
		Identity:         identity,
		ExpiresInSeconds: int(i.config.TTL / time.Second),
	}, nil
}

func validSpikeName(name string) bool {
	if name == "" {
		return true
	}
	if utf8.RuneCountInString(name) > maxSpikeParticipantFieldLength {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != ' ' && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}
