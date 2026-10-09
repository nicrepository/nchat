# Conversation read state

The chat backend is the source of truth for unread counts. It stores one read
state per user, workspace and conversation, and derives the unread count from
it.

Mention highlighting is intentionally not part of this contract. It remains a
client-side hint because mention parsing currently exists only in the web app.

Issue: #1082 (read state decoupled from the viewport tail).

## Canonical order

Every message listing returns messages in `(created_at, id)` order, and the read
cursor is a position in that same order. `created_at` is compared as an instant
(never as text — RFC 3339 drops trailing zeros, so `10:00:00Z` and
`10:00:00.1Z` sort the wrong way as strings, and two notations of one instant
are equal); ties fall to the message id. Clients must not infer order from the
UUID alone.

## Read state rows

A row of `chat.conversation_read_state` holds two independent claims. Migration
`000069` adds the second one; nothing is inferred from the values.

| Claim           | Columns                                                       | Meaning                                                                                                                           |
| --------------- | ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| Legacy boundary | `last_read_at` (`last_read_message_id` is informational only) | Everything created at or before `last_read_at` is read. Every row written before #1082, and what a previous release writes.       |
| Message cursor  | `cursor_created_at`, `cursor_message_id` (both or neither)    | That message and everything before it in canonical order are read. Only this release writes it; a previous release never sees it. |

A message is read when **either** claim covers it. Both are prefixes of the same
total order, so their union is simply the later of the two, and since neither
ever moves back, neither does the union. A writer that only knows the boundary
can therefore neither corrupt nor reinterpret the cursor: whatever it writes
can only add to what is read, never turn `(now(), id)` into a message position.

When this release advances the cursor to message `c`, it also raises the
boundary to `c.created_at − 1µs` (never lowering it). `created_at` has
microsecond resolution, so that boundary covers exactly the messages strictly
before `c`'s instant — a subset of what the cursor covers. A reader that only
knows the boundary sees a conservative read point, never a message that was not
read.

## Sidebar response

`GET /api/chat/sidebar` includes, on every channel and DM row:

| Field          | Type                 | Meaning                                                                    |
| -------------- | -------------------- | -------------------------------------------------------------------------- |
| `unread_count` | non-negative integer | Active messages from other users neither claim covers                      |
| `read_through` | object or `null`     | The read point `unread_count` is counted from; `null` when never read here |

and, once at the top level, `"precise_read_cursor": true` — the capability that
says this server takes message positions (below). A server without it omits the
field.

`read_through` is `{ "created_at": "<RFC 3339>", "message_id": "<uuid>" | null }`:
the later of the two claims — the cursor as a message position, or, when the
boundary is later, the boundary as an instant (`message_id: null`, everything
created at or before it — its informational id is never exposed as a
position). `created_at` keeps its full sub-second fraction.

No field says which messages a count included or how fresh it is: the newest
message a count happened to see is no clock — a late commit can land before
it, and a deletion can move it back. The one ordering a client can rely on is
the read point's: it never moves back (see Web client).

Messages sent by the caller never count. Messages withheld by link scanning
(`pending_link_scan`) and removed messages do not count. Channels, direct
conversations and groups use the same rule:
`created_at > last_read_at AND (cursor IS NULL OR (created_at, id) > cursor)`.

## Advancing the read point

| Target             | Endpoint                                   |
| ------------------ | ------------------------------------------ |
| Channel            | `POST /api/chat/channels/{channelID}/read` |
| Direct or group DM | `POST /api/chat/dm/{conversationID}/read`  |

The body is optional. When supplied, its strict JSON shape is:

```json
{ "last_read_message_id": "00000000-0000-4000-8000-000000000000", "read_cursor": "message" }
```

`read_cursor: "message"` is the protocol marker: the client means "a message
position". Any other value is `400`, and a previous release — whose decoder
refuses unknown fields — answers `400` to the marker itself instead of
recording `now()` for a request it does not understand. The marker is optional
for this release.

`last_read_message_id` means "I have read through this message". In one
statement the server authorizes the caller for the conversation, resolves the
message inside that conversation and workspace and visible to the caller, and
moves the cursor to that message's own `(created_at, id)` — never the request
time or anything else the client sends.

Without `last_read_message_id` the request means "mark the whole conversation
read": the server resolves the newest message visible to the caller at that
moment and advances to it under the same rule. Messages that arrive afterwards
— including one at the same microsecond with a later id — stay unread. Marking
an empty conversation read succeeds and writes nothing.

The cursor only moves forward in canonical order, and the boundary only moves
up. The comparison runs in PostgreSQL against the latest committed row
(`INSERT … ON CONFLICT … DO UPDATE … WHERE`), so duplicate, out-of-order and
concurrent requests from several tabs, devices or releases all leave the
greatest point: tab A at 120, tab B at 150 and a late tab A at 130 end at 150.

A successful call answers `200 OK` with the read state as it stands after the
write — including a call that moved nothing:

```json
{
  "data": {
    "unread_count": 2,
    "read_through": { "created_at": "2026-07-15T10:03:00.123456Z", "message_id": "…" }
  }
}
```

A missing conversation, an unauthorized one, and a message outside it, not
visible to the caller or nonexistent all return the same non-enumerating `404`.
A malformed id or an unknown `read_cursor` is `400`.

`POST …/read` has a per-user budget of its own — 180 requests a minute, per
replica — sized from the web client's write cadence (below) and separate from
the ten-a-minute budget of pin, mute and ownership actions. Past it, the
answer is `429` with `Retry-After: 60`.

### Rollout

The migration is expand-only (two nullable columns and a pair check), so both
releases run against it during blue/green:

- **Old writes after new.** A previous release updates only the boundary
  (`now()`, informational id). The cursor is untouched, and the union is still
  the later of the two — a write can only add reads, never reinterpret one.
- **Old reads after new.** A previous release reads the boundary, which this
  release keeps at `cursor − 1µs`: what it reports read is a subset of what
  was read.
- **Rollback.** Rolling the migration back drops the cursor columns; every row
  reads by its boundary, which never covers a message the reader did not read.
- **Old frontend, new backend.** No marker, same body: accepted as before.
- **New frontend, old backend.** The sidebar carries no `precise_read_cursor`,
  so the client preserves the server count and sends no automatic read writes.
  Explicit manual "mark the whole conversation read" remains available. A
  loaded timeline may contain gaps, so seeing all loaded rows is insufficient.
  Were a position
  sent anyway, the marker makes the old backend refuse it.

## Web client

The server is authoritative; the client keeps a projection in between.

- **Seen, not opened.** Opening a conversation reads nothing. A row counts as
  read when the tab is visible, the window has focus, the opening position has
  settled, nothing but the reader owns the scroll — no navigation, prepend
  restoration or jump (a deep link, a quote, a pin) is carrying it — and at
  least half of the row, or half of the viewport for a row taller than it, is
  exposed in the timeline's scroll area. Only mounted rows are measured.
- **Events by their own box.** Every conversation event draws a row — one this
  build has no copy for falls back to "Evento da conversa" — and is
  read only by its own exposure, never by a neighbour's. The reader's own
  messages never move the cursor.
- **Monotonic.** The client cursor is the latest position seen. Scrolling up,
  restoring an older viewport position, a deep link into history and row reflow
  never move it back.
- **Two views of one cursor.** The timeline's scroll control counts the unread
  messages it holds past the cursor; the sidebar row counts against the
  server's base (below), so the two can differ while a timeline holds a gap. The
  timeline also adopts a later server `read_through` (read on another device)
  without writing it back; the count at opening only places the "Novas
  mensagens" boundary.
- **Capability.** Progressive positions are sent only to a server that
  advertises `precise_read_cursor`, as the newest sidebar payload says — any
  refetch, a reconnect's included, can change it. It is checked again when a
  queued position is about to be sent, and a position the server no longer
  takes is dropped there. A `400` to a position already on the wire is taken as
  that downgrade: nothing is acknowledged or retried, and the sidebar is
  fetched again. Against a server without the capability, the server count
  stands and only an explicit manual action marks the whole conversation read.
- **Downgrade and upgrade.** A sidebar payload without `precise_read_cursor`
  replaces the row's precise state wholesale: its `unread_count` becomes the
  count, and no earlier snapshot, local read or pending "mark as read" is
  carried into it — showing more unread for a while is the safe side. The
  first precise payload afterwards starts a fresh precise state.
- **Coalesced writes.** Per conversation, writes are debounced (400 ms): the
  window is armed by the first position after a quiet spell and never pushed
  back, so a reader scrolling without pause still gets a write every window.
  At most one write is in flight; the greatest position read meanwhile is sent
  one window after it settles — at most one write per window plus a round
  trip, never one per message, however fast the network. "Mark as read" is
  tracked separately from the cursor and invents no position: the count shows
  zero until the server answers with the point it resolved. A failed write is
  not retried; the next read writes again. A `429` is not an acknowledgement:
  the greatest position (or the "mark as read") is kept, and nothing is sent
  for the 60 s the server asked for. Then it is sent once by itself, so an idle
  reader's cursor is still saved; if that retry is refused too, only the next
  read, flush or page exit sends it — one automatic retry until a write
  succeeds, never a loop. A logout or account switch drops it.
- **Identity on refresh.** The refresh token is a cookie every tab shares, so
  a refresh can hand a tab another account's access token. After a refresh,
  writes wait until a sidebar fetch confirms the same `current_user_id`; a
  different user, or no answer, is handled as a session change — pending
  positions are dropped and the sidebar loads again.
- **Confirmed read frontier.** The furthest read point any server answer
  confirmed never moves back; the writer holds the same rule for its
  acknowledgements. An answer — a refetch's or a write's, normal or terminal —
  whose point is behind it was computed before that point was written,
  whatever order the requests started in: its count is ignored. An answer from
  further ahead becomes the row's base — the answer to each of this session's
  writes included, even while the reader is already further on. At
  the base's point, an answer to a request started after the base arrived is
  taken whatever it says (its count may fall after a deletion, or grow); two
  concurrent answers cannot be ordered, so the higher count stands.
- **Reconciliation.** Whenever the row holds a count it could not settle — a
  refused answer, the higher of two concurrent ones, or a realtime message
  that arrived while the request that answered was out — it asks for one
  sidebar refetch, sent after it, which settles it. Refetches are coalesced,
  and the refetch's own answer asks again only if something new happened
  while it was out: no polling, no loop.
- **Page lifecycle.** When the tab is hidden, a debounced write is sent at once
  (`keepalive`). On `pagehide`, every pending write is sent as `keepalive` even
  beside one already in flight — safe because the server never moves a point
  backwards and the acknowledgement above drops whichever answer is older.
  A `pagehide` with `persisted` (back/forward cache) does not end the writer;
  the matching `pageshow` refetches the sidebar. `sendBeacon` is not used: the
  endpoint needs the Authorization header.
- **Session scope.** The writer and the sidebar's read state live for one
  authenticated session: a login, logout or account switch (`onAuthChange` with
  `"session"`) disposes the writer — dropping its timers and pending writes —
  and reloads the sidebar from scratch, so nothing read, projected or pending
  under the previous identity (even the same user signing in again) reaches the
  new one; an answer to a request the previous session started is ignored. A
  refresh rotation (`"refresh"`) is the same session with a new token: the
  writer, its pending writes and the read state carry on.
- **The count is the server's.** The row shows `unread_count` plus realtime
  arrivals since, until the reader's cursor passes them or a later base
  accounts for them. Reading takes nothing off it: the browser cannot prove
  which messages a count included — a message it holds may have been deleted
  without an event reaching it, and a late commit or a deep link across a gap
  holds messages the count never saw. Reading moves the cursor; the writer
  persists it; the server's answer to that write is the next count. So the
  badge follows the server's acknowledgement of what was read, one window
  plus a round trip behind the reader (400 ms + RTT for the first read after
  a quiet spell, then a write per window while reading continues) — never the
  tail, and never lower than what is unread. Examples: 5 unread, 2 read → 5
  until the write answers 3. m1 deleted without an event, a refetch says 4, m1
  to m3 read → 4 until the write answers 2. A base of 3 (m3b, m4, m5) with m3
  read across a gap → 3.
- **Mentions.** A mention known with its position clears when a read point
  passes it. A mention without a position — restored from the local cache — is
  kept as unknown, and only a zero the server stated — its own count, or the
  reader's explicit "mark as read" — clears it; a projected zero does not.
