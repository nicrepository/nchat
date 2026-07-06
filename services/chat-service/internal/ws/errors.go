package ws

import "errors"

// ErrSubscribeForbidden is returned when a client is not authorized to subscribe
// to a target. The error is intentionally non-enumerating: it does not reveal
// whether the target exists or the exact reason for denial.
var ErrSubscribeForbidden = errors.New("subscribe forbidden")

// ErrHubShutdown is returned by Hub.Subscribe when the hub is shutting down.
var ErrHubShutdown = errors.New("hub is shutting down")

// ErrBusClosed is returned by BroadcastBus.Subscribe when the bus is already closed.
var ErrBusClosed = errors.New("bus is closed")

var ErrReactionRateLimited = errors.New("reaction rate limited")

// ErrReactionFeatureDisabled is an internal deployment/configuration guard.
// It must be mapped to a neutral client response rather than serialized.
var ErrReactionFeatureDisabled = errors.New("reaction feature disabled")
