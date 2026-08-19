# Issue 546 Floating Call and Tab Handoff Design

## Goal

Keep one logical NChat call alive independently of the conversation route,
present it as an incoming popup, a persistent floating window, or a dedicated
authenticated tab, and guarantee that only one browser tab publishes local
media at a time.

## Scope

The implementation covers direct messages, group DMs, and channels; incoming
presentation; persistent floating presentation; responsive dedicated call
presentation; drag, clamp, resize, snap, and keyboard repositioning; real
LiveKit participant state, active speaker, and screen share; cross-tab media
ownership, handoff, rollback, and recovery; technical observability; and final
device/listener/timer cleanup.

Recording, transcription, backgrounds, reactions, raise hand, breakout rooms,
PSTN/SIP, multiple simultaneous calls per user, native operating-system
picture-in-picture, and general chat refactors remain out of scope.

## Current System

`ChatShell` currently owns `useCallMedia`, `useCallSignaling`, and
`useResourceCallSession`. This already preserves one LiveKit `Room` while the
user moves between routes nested under `/chat`, but unmounting the shell still
owns and destroys the media session. `CallPanel` renders both direct calls and
resource-call grids as fullscreen modal dialogs.

`useCallMedia` is the single existing media controller. It lazily creates the
LiveKit adapter, serializes disconnect/connect, retains participant elements
outside React state, and exposes reconnect and device-control state. The adapter
already enables `adaptiveStream` and `dynacast`.

Direct calls have an authoritative PostgreSQL/chat-service lifecycle and a
`call_id`. Channel and group calls currently connect directly to the room
derived from the resource, with no persisted call instance or incoming event.
The latter cannot satisfy a global incoming popup or a dedicated `/call/:callId`
route without a small lifecycle extension.

Media-service already has the required trust boundary. It authenticates the
HTTP session, derives participant identity and room server-side, applies the
canonical channel/DM authorization policy, and emits a short-lived LiveKit
token. A new tab can authenticate normally through the existing HttpOnly
refresh cookie and request its own token.

## Chosen Architecture

### Global call session

An authenticated call-session provider sits above the authenticated chat,
profile, admin, and dedicated-call routes. It owns the existing media and
signaling hooks and is the only code allowed to create, connect, or stop a
LiveKit session. `ChatShell` consumes this provider for sidebar/outlet actions
instead of constructing call state itself.

The provider exposes a small view/controller contract. Presentation components
receive serializable state and callbacks; they never receive or construct a
LiveKit `Room`. Moving, minimizing, navigating, or rerendering therefore cannot
replace the Room.

The provider is mounted once per document. A second document reconstructs its
own LiveKit connection only after cross-tab ownership has been transferred. A
JavaScript `Room` is never transferred between documents.

### State separation

Four independent state domains remain explicit:

1. backend call status and version;
2. LiveKit connection/media status;
3. presentation state;
4. cross-tab ownership state.

The presentation reducer supports:

- `idle`
- `incoming`
- `connecting`
- `active_floating`
- `handoff_to_tab`
- `active_dedicated_tab`
- `recovering_to_floating`
- `reconnecting`
- `ended`
- `failed`

Every transition is event-driven. Invalid transitions return the existing
state and are observable in development without throwing from rendering. A
reconnect event records the previous active presentation so `reconnected`
returns to floating or dedicated deterministically. Terminal cleanup is
idempotent.

Ownership is separately represented as `none`, `local`, `remote`, or
`transferring`. An active presentation with remote ownership renders state and
controls that do not operate local tracks.

### Resource-call lifecycle extension

The existing `chat.calls` authority is evolved instead of creating a second
call service. A migration adds a server-derived target kind and target ID while
preserving existing direct-call rows. Direct-call transition rules stay
unchanged.

A resource call targets one channel or group DM, starts active after the
authenticated initiator passes the existing canonical visibility/membership
policy, and is unique per active resource. Its start event is delivered through
the existing resource-targeted WebSocket subscription. Receiving members show
the non-modal incoming popup; ignoring it is local and does not end the shared
call. Joining requests media only after an explicit user gesture.

The originator may explicitly end a resource call for everyone. Other members
leave locally. While its LiveKit connection is healthy, the owning tab renews
an authenticated, short server-side participation lease over the existing chat
WebSocket. The lease is liveness bookkeeping only: it grants no membership or
media permission and is never used for the participant count. The existing
bounded expiry worker is extended to end an active resource call after every
participant lease has expired, so a crashed last participant cannot leave a
permanent incoming popup. The expiry grace is longer than the cross-tab handoff
timeout, allowing a controlled reconnect without ending the logical call.

Participant count shown after connection always comes from the actual LiveKit
roster; the incoming popup omits the count when no authoritative live roster is
available. A client cannot increase the displayed count by sending a liveness
heartbeat.

Media-service accepts the authoritative resource call ID through the existing
token endpoint, authorizes its current target and membership server-side, and
derives `call:<call-id>`. It does not accept a conversation, workspace, room,
identity, grants, or presentation claim from the client.

### Presentation surfaces

`IncomingCallPopup` is a non-modal section mounted globally. It does not call
navigation, mark a conversation read, open a tab, or move focus from the
composer. It has accessible accept/join and decline/ignore actions and a
separate explicit expand action. One call ID maps to one popup.

`FloatingCallWindow` reuses the current participant rendering and controls in a
compact surface. It shows connection state, real participant count, a bounded
avatar summary, stabilized active speaker, screen-share state, mic/camera/end,
and expand. When another tab owns media, it shows “Chamada aberta em outra aba”
and an explicit takeover action instead of enabled media controls.

`DedicatedCallStage` is the authenticated `/call/:callId` route. It reuses the
existing responsive grid, direct-call main tile/local preview, reconnect state,
and media controls. Screen share becomes the primary tile. Minimize requests a
handoff back to an eligible main tab; closing the tab alone never sends an end
command.

`GlobalCallIndicator` is always present in the authenticated shell while a call
exists, including when floating is minimized or remotely owned. It displays
call status, target, duration when `accepted_at` exists, participant count when
connected, screen share, and an explicit open/return action.

The visual treatment keeps the existing purple NChat call surfaces and Inter/
Material Symbols assets. No design system or UI dependency is added.

### Drag and responsive behavior

Desktop drag uses Pointer Events and pointer capture only on the designated
header. Interactive descendants never begin a drag. Pure geometry functions
clamp the window to the viewport and snap to one of four corners only within a
small threshold. Resize reclamps the stored position.

The last valid corner/position is stored under a call-presentation preference
key containing no call, conversation, participant, or credential data. A menu
offers the four corner positions for keyboard and assistive-technology users.
Motion is disabled under `prefers-reduced-motion`.

At the existing small-screen breakpoint the surface becomes a fixed mini-player
or banner; free drag is disabled. Controls retain touch targets and remain
usable at 200% zoom and 320 CSS pixels.

### Active speaker and screen share

The LiveKit adapter forwards `ActiveSpeakersChanged`, remote track source, and
local/remote screen-share state through typed callbacks. `useCallMedia` keeps
high-frequency speaker candidates outside the message tree and commits a new
visible speaker only after a short stability interval. Disconnecting a speaker
or a terminal cleanup clears it immediately.

Screen share is an explicit local control backed by LiveKit’s existing
`setScreenShareEnabled`. It never starts during join, handoff, reconnect, or
recovery. Native termination of a shared track updates the controller. Remote
screen-share tracks are identified from the real publication source and
prioritized only in the call presentation.

### Cross-tab ownership

Each mounted provider creates an in-memory `tabId` with `crypto.randomUUID()`.
It opens one versioned `BroadcastChannel` and coordinates a short owner lease in
`localStorage`. The stored record contains only schema version, call ID, tab ID,
lease epoch, role, and expiry. Tokens, SDP, device IDs, participant names, and
message content are prohibited.

When Web Locks is available it serializes lease mutation. It is an optimization,
not a compatibility requirement. The fallback uses an announce/election window,
the deterministic tuple `(epoch, tabId)`, write-read confirmation, and a settle
period before any media publication. The fallback does not claim impossible
atomicity; ambiguity fails closed and no candidate connects until one lease is
stable.

Typed version-1 messages cover hello, ready, handoff request, media released,
claim, acknowledgement, heartbeat, takeover, failure, and end. Parsers reject
unknown versions, missing fields, oversized strings, mismatched call IDs, stale
epochs, and unexpected transitions. These messages coordinate state only and
never authorize access.

The owner renews its lease while healthy. A non-owner may take over only after
expiry or an explicit handoff grant. Conflicting current-owner heartbeats are
resolved by epoch and then tab ID; the loser immediately stops local media and
reports duplicate-owner detection.

### Expand handoff

1. The local owner receives an explicit expand click.
2. It opens `/call/:callId` with `noopener`.
3. The dedicated tab passes `RequireAuth`, resolves the call server-side, and
   announces `ready` without requesting media.
4. The old owner enters `handoff_to_tab`, records the current mic/camera intent,
   stops screen share, disconnects LiveKit, and confirms cleanup.
5. Only after cleanup succeeds does it grant the next lease epoch.
6. The dedicated tab confirms the stable lease, reacquires a LiveKit token from
   media-service, connects, restores safe mic/camera intent, and sends `ack`.
7. The main tab becomes remote-owned and shows the global indicator.

If opening is blocked, `ready` times out, cleanup fails, lease confirmation
fails, token issuance fails, or media connection fails, the target releases its
claim and the old owner performs bounded rollback to floating. The two tabs are
never intentionally connected concurrently.

### Recovery and minimize

Dedicated minimize runs the same protocol in reverse and does not depend on
`window.opener`. A healthy main tab announces eligibility, the dedicated owner
stops media, and the main tab claims the next epoch before reconnecting.

Unexpected dedicated-tab closure is detected by lease expiry, not solely by
`beforeunload`. Eligible tabs elect one owner and recover to floating. Mic and
camera restore only their last confirmed intent; screen share stays off.
Explicit end broadcasts terminal state, invokes backend end/leave as
appropriate, stops every local track, cancels timers, removes listeners,
releases locks/leases, closes the channel, and clears presentation state.

## Security Model

The browser coordinator is not an authentication or authorization boundary.
Every document passes normal authentication, and every new LiveKit connection
uses a newly issued token after current server-side membership validation.

No LiveKit token may appear in the route, query, fragment, `localStorage`,
`sessionStorage`, BroadcastChannel, `postMessage`, logs, errors, analytics, or
test snapshots. The existing HttpOnly refresh cookie is the only cross-document
authentication continuity mechanism.

Direct calls remain participant-authorized. Resource calls validate the call,
workspace, target conversation, current membership, and canonical channel
visibility. Private channels and group DMs do not become discoverable through a
known call ID. Unauthorized and absent calls remain non-enumerating.

## Threat Model

- Token disclosure is mitigated by per-tab backend issuance and a strict
  prohibition on cross-tab/token persistence.
- BOLA and cross-workspace join are mitigated by server-derived identity, room,
  target, grants, and current membership checks.
- Split brain is mitigated by stop-before-grant handoff, lease epochs,
  deterministic election, stable-claim confirmation, and loser cleanup.
- Forged or stale coordination messages are mitigated by typed validation,
  call/epoch correlation, and the rule that coordination grants no authority.
- Reverse tabnabbing is mitigated by `noopener`; the protocol never reads
  `window.opener`.
- Silent device reactivation is mitigated by confirmed intent state, owner-only
  controls, explicit initial consent, and screen-share opt-in on every start.
- Dead or suspended tabs are mitigated by expiring leases and recovery that does
  not rely on unload events.
- Resource exhaustion is mitigated by one channel and bounded timers/listeners
  per provider, existing token rate limits, and idempotent cleanup.
- Sensitive telemetry is mitigated by an allowlist of technical event names and
  safe correlation IDs only.

## Observability

A small call telemetry adapter emits only allowlisted technical events:
incoming shown, accepted/rejected, join success/failure, reconnect, floating
activated, dedicated opened, handoff start/success/failure, ownership takeover,
duplicate-owner detected, end, and track cleanup. Production integration is a
single replaceable sink; development logs never include payloads, names,
tokens, SDP, credentials, message content, or device labels.

## Testing Strategy

Pure Vitest unit tests cover every valid/invalid presentation transition,
speaker stabilization, drag geometry, lease expiry, deterministic competition,
handoff timeout, rollback, recovery, message parsing, and idempotent cleanup.

React Testing Library covers DM/resource incoming popups, floating controls and
participant summaries, global indicator, remote-owned and reconnect states,
screen share, pointer capture, keyboard positioning, reduced motion, and
accessible names/state.

Hook/adapter integration tests prove route changes do not recreate a Room,
screen share survives textual navigation, dedicated handoff never overlaps
connections, non-owner reload does not publish, recovery reacquires credentials,
and devices are released on explicit end.

Playwright extends the current call suite with navigation during a call, drag,
dedicated-tab handoff, unique participant identity, dedicated close/recovery,
explicit end, desktop/tablet/mobile presentation, and deterministic event-based
waits. Real multi-user/LiveKit cases run only where the environment exposes the
required media stack; mocked browser tests remain deterministic.

## Failure Semantics

Popup blocking leaves the owner and floating call unchanged. Authentication or
authorization failure in the dedicated tab cannot claim ownership. A failed old
owner cleanup blocks transfer. A failed new owner join releases its lease and
requests rollback. A terminal backend call always wins over presentation or
ownership state. Cleanup is safe to repeat after partial failure.

## Documentation

This design becomes the implementation contract. The existing call lifecycle
and media token documentation will be updated for resource call IDs and
cross-tab reissuance. An ADR will record global call-session ownership and the
logical handoff decision because it changes the lifetime boundary of LiveKit
media across the application.
