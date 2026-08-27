package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrUnauthorized = errors.New("unauthorized")
	ErrNotFound     = errors.New("not found")
	ErrUnavailable  = errors.New("service unavailable")

	ErrIntegrationDisabled     = fmt.Errorf("%w: integration disabled", ErrUnavailable)
	ErrDependenciesUnavailable = fmt.Errorf("%w: dependencies unavailable", ErrUnavailable)
)

type ResourceKind string

const (
	ResourceKindChannel ResourceKind = "channel"
	ResourceKindDM      ResourceKind = "dm"
	ResourceKindCall    ResourceKind = "call"
)

func (k ResourceKind) Valid() bool {
	return k == ResourceKindChannel || k == ResourceKindDM || k == ResourceKindCall
}

// RoomName derives a canonical LiveKit room without user-controlled text.
func RoomName(environment Environment, kind ResourceKind, resourceID string) (string, error) {
	environment, err := ParseEnvironment(environment.String())
	if err != nil {
		return "", err
	}
	if !kind.Valid() {
		return "", fmt.Errorf("%w: invalid resource kind", ErrInvalidInput)
	}
	id, err := uuid.Parse(resourceID)
	if err != nil {
		return "", fmt.Errorf("%w: invalid resource id", ErrInvalidInput)
	}
	return environment.String() + ":" + string(kind) + ":" + id.String(), nil
}

// ParseRoomName validates a canonical room against an exact environment.
func ParseRoomName(environment Environment, room string) (ResourceKind, string, error) {
	environment, err := ParseEnvironment(environment.String())
	if err != nil {
		return "", "", err
	}
	namespace, rest, found := strings.Cut(room, ":")
	if !found || namespace != environment.String() {
		return "", "", fmt.Errorf("%w: room is not namespaced to this environment", ErrInvalidInput)
	}
	rawKind, resourceID, found := strings.Cut(rest, ":")
	if !found {
		return "", "", fmt.Errorf("%w: invalid room", ErrInvalidInput)
	}
	kind := ResourceKind(rawKind)
	want, err := RoomName(environment, kind, resourceID)
	if err != nil || want != room {
		return "", "", fmt.Errorf("%w: invalid room", ErrInvalidInput)
	}
	return kind, resourceID, nil
}
