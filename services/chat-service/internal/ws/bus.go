package ws

import "context"

// BroadcastBus is an optional distributed event bus for the hub.
//
// It distributes ws.Event payloads across chat-service instances so that
// a message published on one instance is also delivered to local subscribers
// on all other instances.
//
// Delivery contract: best-effort only.
//   - No durability, no replay, no at-least-once guarantee.
//   - Valkey Pub/Sub is fire-and-forget; events missed during a disconnect
//     are not recovered.
//   - Durable notification delivery (PostgreSQL notification_outbox / Valkey
//     Streams) is a separate, future concern and is NOT in scope for this bus.
//
// The hub maintains in-process subscription state. Every event received from
// the bus goes through the same authorization re-check as locally published
// events before delivery to local subscribers.
//
// Implementations must be safe for concurrent use.
type BroadcastBus interface {
	// Publish sends evt to remote chat-service instances.
	// A non-nil error means the event was not sent (or may not have been
	// sent). Callers must not treat publish failure as fatal — local in-process
	// delivery is independent and must succeed regardless of bus state.
	Publish(ctx context.Context, evt Event) error

	// Subscribe registers handler for events received from remote instances.
	// The handler is called from a goroutine owned by the bus; callers must
	// not block inside handler. Subscribe starts background goroutines that
	// exit when ctx is cancelled or Close is called.
	// Returns ErrBusClosed if the bus has already been closed.
	Subscribe(ctx context.Context, handler func(Event)) error

	// Close stops subscriber goroutines and releases resources.
	// Safe to call multiple times.
	Close()
}

// NopBus is a no-op BroadcastBus for single-instance deployments and tests
// that do not require distributed broadcast. Local hub delivery is unaffected.
type NopBus struct{}

func (NopBus) Publish(_ context.Context, _ Event) error         { return nil }
func (NopBus) Subscribe(_ context.Context, _ func(Event)) error { return nil }
func (NopBus) Close()                                           {}
