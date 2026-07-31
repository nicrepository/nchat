package domain

import (
	"errors"
	"fmt"

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
func RoomName(kind ResourceKind, resourceID string) (string, error) {
	if !kind.Valid() {
		return "", fmt.Errorf("%w: invalid resource kind", ErrInvalidInput)
	}
	id, err := uuid.Parse(resourceID)
	if err != nil {
		return "", fmt.Errorf("%w: invalid resource id", ErrInvalidInput)
	}
	return string(kind) + ":" + id.String(), nil
}
