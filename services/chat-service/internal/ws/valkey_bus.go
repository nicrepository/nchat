package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	valkey "github.com/valkey-io/valkey-go"
)

// valkeyChannelPattern is the PSUBSCRIBE pattern for distributed ws broadcast.
// All workspace channels match; auth re-check enforces per-subscriber access.
const valkeyChannelPattern = "nchat:chat:ws:broadcast:*"

// publishTimeout caps a single Valkey PUBLISH call.
// Long enough for normal network round-trips; short enough to not block callers.
const publishTimeout = 2 * time.Second

// subscribeRetryMin / subscribeRetryMax bound the reconnect backoff for the
// PSubscribe loop. Reconnect is silent-fail: local delivery is unaffected.
const (
	subscribeRetryMin = 100 * time.Millisecond
	subscribeRetryMax = 30 * time.Second
)

// pubsubAdapter abstracts Valkey pub-sub I/O.
// The production implementation wraps valkey-io/valkey-go.
// Tests use fakePubSub (defined in valkey_bus_test.go).
type pubsubAdapter interface {
	// Publish sends payload to channel. Returns an error if the call fails.
	Publish(ctx context.Context, channel, payload string) error
	// PSubscribe subscribes to channels matching pattern. Blocks until ctx is
	// cancelled or a fatal error occurs; calls handler for each message received.
	PSubscribe(ctx context.Context, pattern string, handler func(channel, payload string)) error
	// Close releases resources.
	Close()
}

// ValkeyBus implements BroadcastBus using Valkey Pub/Sub (pattern subscribe).
//
// Channel naming: nchat:chat:ws:broadcast:{workspace_id}
//   - Workspace-scoped publish; pattern-scoped subscribe.
//   - workspace_id must not contain special characters; validated before use.
//   - No user-controlled raw values appear in channel names.
//   - No secrets or tokens in channel names.
//
// Delivery contract: best-effort only.
//   - PUBLISH is fire-and-forget; no at-least-once guarantee.
//   - Clients that are disconnected during a publish miss the event.
//   - No replay, no durability, no missed-event recovery.
//   - Valkey Streams / PostgreSQL notification_outbox are separate future scope.
//
// Authorization: every event received from Valkey goes through hub's existing
// auth re-check before local delivery. Valkey messages are not trusted.
//
// Resilience: Valkey unavailability does not affect in-process hub delivery.
// The PSubscribe loop reconnects with bounded backoff. Publish failures are
// returned to the caller but must not propagate to MessageService as fatal.
type ValkeyBus struct {
	ps         pubsubAdapter
	instanceID string
	logger     *slog.Logger
	// closeCtx is cancelled by Close(), stopping all subscriber goroutines.
	closeCtx  context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// newValkeyBusWithAdapter constructs a ValkeyBus with an injected pubsubAdapter.
// Used in tests; production code uses NewValkeyBus.
func newValkeyBusWithAdapter(ps pubsubAdapter, instanceID string, logger *slog.Logger) *ValkeyBus {
	closeCtx, cancel := context.WithCancel(context.Background())
	return &ValkeyBus{
		ps:         ps,
		instanceID: instanceID,
		logger:     logger,
		closeCtx:   closeCtx,
		cancel:     cancel,
	}
}

// NewValkeyBus constructs a ValkeyBus backed by a real Valkey connection.
// valkeyURL must be a valid Valkey address (e.g. "valkey://localhost:6379" or
// "localhost:6379"). Returns an error if the client cannot be initialised.
func NewValkeyBus(valkeyURL, instanceID string, logger *slog.Logger) (*ValkeyBus, error) {
	if valkeyURL == "" {
		return nil, errors.New("ws: valkeyURL must not be empty")
	}
	// Strip scheme if present (valkey-go expects host:port).
	addr := strings.TrimPrefix(valkeyURL, "valkey://")
	addr = strings.TrimPrefix(addr, "redis://")

	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{addr},
	})
	if err != nil {
		return nil, fmt.Errorf("ws: create valkey client: %w", err)
	}

	closeCtx, cancel := context.WithCancel(context.Background())
	return &ValkeyBus{
		ps:         &valkeyPubSub{client: client},
		instanceID: instanceID,
		logger:     logger,
		closeCtx:   closeCtx,
		cancel:     cancel,
	}, nil
}

// Publish serialises evt as JSON and sends it to the workspace-scoped Valkey
// channel. A bounded context (publishTimeout) prevents indefinite blocking.
// Returns an error on publish failure; callers must treat this as non-fatal.
func (b *ValkeyBus) Publish(ctx context.Context, evt Event) error {
	ch, err := channelForWorkspace(evt.WorkspaceID)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("ws: marshal event for bus: %w", err)
	}

	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	if err := b.ps.Publish(pubCtx, ch, string(payload)); err != nil {
		return fmt.Errorf("ws: valkey publish: %w", err)
	}
	return nil
}

// Subscribe starts a background goroutine that pattern-subscribes to all
// workspace channels and calls handler for each received event. The handler
// is responsible for self-echo suppression and validation (handled by hub).
// Reconnects with bounded backoff on error. Exits when either callerCtx is
// cancelled OR Close is called — whichever happens first.
func (b *ValkeyBus) Subscribe(callerCtx context.Context, handler func(Event)) {
	// Derive a context cancelled by either the bus lifecycle (Close) or the caller.
	subCtx, subCancel := context.WithCancel(b.closeCtx)

	// Propagate caller cancellation into subCtx.
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer subCancel()
		select {
		case <-callerCtx.Done():
		case <-subCtx.Done():
		}
	}()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer subCancel()
		b.subscribeLoop(subCtx, handler)
	}()
}

// Close cancels the bus lifecycle context, stopping all subscriber goroutines,
// then waits for them to finish and closes the underlying pub-sub connection.
// Safe to call multiple times — subsequent calls are no-ops (idempotent).
func (b *ValkeyBus) Close() {
	b.closeOnce.Do(func() {
		b.cancel()
		b.wg.Wait()
		b.ps.Close()
	})
}

func (b *ValkeyBus) subscribeLoop(ctx context.Context, handler func(Event)) {
	delay := subscribeRetryMin
	for {
		err := b.ps.PSubscribe(ctx, valkeyChannelPattern, func(_, payload string) {
			var evt Event
			if err := json.Unmarshal([]byte(payload), &evt); err != nil {
				b.logger.WarnContext(ctx, "ws: drop malformed bus message", "error", err)
				return
			}
			handler(evt)
		})

		if ctx.Err() != nil {
			return // clean exit
		}

		b.logger.WarnContext(ctx, "ws: bus psubscribe error; reconnecting",
			"error", err,
			"retry_in", delay,
		)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, subscribeRetryMax)
	}
}

// channelForWorkspace returns the Valkey channel name for a workspace.
// workspace_id must be a valid UUID; it is parsed and canonicalized to prevent
// injection of glob characters or other unsafe values into the channel name.
func channelForWorkspace(workspaceID string) (string, error) {
	id, err := uuid.Parse(workspaceID)
	if err != nil {
		return "", fmt.Errorf("ws: workspace_id is not a valid UUID: %w", err)
	}
	return "nchat:chat:ws:broadcast:" + id.String(), nil
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// valkeyPubSub is the production pubsubAdapter backed by valkey-io/valkey-go.
type valkeyPubSub struct {
	client valkey.Client
}

func (v *valkeyPubSub) Publish(ctx context.Context, channel, payload string) error {
	return v.client.Do(ctx,
		v.client.B().Publish().Channel(channel).Message(payload).Build(),
	).Error()
}

func (v *valkeyPubSub) PSubscribe(ctx context.Context, pattern string, handler func(channel, payload string)) error {
	return v.client.Receive(ctx,
		v.client.B().Psubscribe().Pattern(pattern).Build(),
		func(msg valkey.PubSubMessage) {
			handler(msg.Channel, msg.Message)
		},
	)
}

func (v *valkeyPubSub) Close() {
	v.client.Close()
}
