package domain

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Environment is an APP_ENV validated for use as a LiveKit namespace.
type Environment string

func (e Environment) String() string { return string(e) }

// ParseEnvironment trims APP_ENV but does not otherwise normalize it.
func ParseEnvironment(raw string) (Environment, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: environment is required", ErrInvalidInput)
	}
	if !environmentPattern.MatchString(trimmed) {
		return "", fmt.Errorf(
			"%w: environment %q must match %s",
			ErrInvalidInput, trimmed, environmentPattern.String(),
		)
	}
	return Environment(trimmed), nil
}

// ParticipantIdentity derives a canonical namespaced LiveKit identity.
func ParticipantIdentity(environment Environment, userID string) (string, error) {
	environment, err := ParseEnvironment(environment.String())
	if err != nil {
		return "", err
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return "", fmt.Errorf("%w: invalid user id", ErrInvalidInput)
	}
	return environment.String() + ":" + id.String(), nil
}

// ParseParticipantIdentity validates an identity against an exact environment.
func ParseParticipantIdentity(environment Environment, identity string) (string, error) {
	environment, err := ParseEnvironment(environment.String())
	if err != nil {
		return "", err
	}
	namespace, rest, found := strings.Cut(identity, ":")
	if !found || namespace != environment.String() {
		return "", fmt.Errorf("%w: identity is not namespaced to this environment", ErrInvalidInput)
	}
	want, err := ParticipantIdentity(environment, rest)
	if err != nil || want != identity {
		return "", fmt.Errorf("%w: invalid participant identity", ErrInvalidInput)
	}
	return rest, nil
}
