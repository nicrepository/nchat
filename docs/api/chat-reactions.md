# Message reactions (RF-03)

A reaction is one complete Unicode emoji sequence attached to one message by one
user. The server decides what a reaction may be; the picker in the web client is
a convenience over the same data, never the policy.

## Emoji catalog

| Property        | Value                                                                    |
| --------------- | ------------------------------------------------------------------------ |
| Source          | Unicode RGI emoji set (`emoji-test.txt`), fully-qualified sequences      |
| Unicode version | **16.0**                                                                 |
| Names, keywords | CLDR **release-46** `annotations/pt.xml` and `annotationsDerived/pt.xml` |
| Locale          | Portuguese, falling back to the CLDR English short name                  |

Both halves of the product read from one generated artifact pair, written by a
single run of `scripts/emoji/generate-emoji-catalog.mjs`:

| Artifact                                                   | Consumer       | Contents                                                             |
| ---------------------------------------------------------- | -------------- | -------------------------------------------------------------------- |
| `services/chat-service/internal/service/emoji_catalog.txt` | Go, `go:embed` | Every RGI sequence, one per line — the validation set                |
| `apps/web/src/chat/emoji/emojiCatalog.json`                | Web, lazy      | Base emoji with label, search keywords, group and skin-tone variants |

Both are checked in, so neither build runs the generator or needs the network.
Regenerate them together when the project adopts a new Unicode version, and
update the table above in the same change.

Validation compares the **whole sequence** against the catalog. A ZWJ sequence, a
skin-tone variant and a variation selector are matched as the single sequence
they are: nothing counts code points, inspects a prefix, or accepts a shortcode
or markup. `chat.message_reactions.emoji` bounds the stored sequence at 32 code
points (migration `000040`), comfortably above the longest sequence Unicode 16.0
defines.

## `GET /api/chat/reactions/allowed-emojis`

Returns the quick-reaction row and the catalog version. It does **not** serve the
catalog: that is a fixed, versioned property of the build the client already
carries, so sending thousands of sequences on every chat open would be payload
for nothing.

```json
{ "data": { "emojis": ["👍", "❤️", "…"], "version": "16.0" } }
```

| Field     | Type            | Meaning                                                           |
| --------- | --------------- | ----------------------------------------------------------------- |
| `emojis`  | array of string | Curated quick-reaction row. A subset of the catalog, not a policy |
| `version` | string          | Unicode emoji version this deployment validates against           |

`emojis` keeps the name and meaning it had before the catalog existed, so a
client built against the older contract is unaffected.

The response carries `ETag: "emoji-catalog-<version>"` and
`Cache-Control: private, max-age=300`. The answer changes only when the
deployment adopts a new Unicode version, so a client that sends a matching
`If-None-Match` is answered `304 Not Modified` with no body.

## Reaction aggregates over HTTP

Every message DTO — the list endpoints and the single-message endpoint — carries
one aggregate per emoji:

```json
{
  "emoji": "👍",
  "count": 4,
  "reacted_by_me": true,
  "users": [{ "user_id": "…", "display_name": "Álvaro Neto" }]
}
```

| Field           | Meaning                                                                         |
| --------------- | ------------------------------------------------------------------------------- |
| `count`         | Everyone who reacted with this emoji                                            |
| `reacted_by_me` | Whether the requesting user is among them                                       |
| `users`         | The **first three** reactors by `(created_at, user_id)` — a prefix, not the set |

`users` exists so a client can name who reacted without a request per badge, per
hover, or per person; it is filled by the same batched statement that computes
the counts, one per page of messages. Three names is the smallest prefix that
always fills a tooltip showing two entries, one of which may be the reader.

A reactor with no display name is counted but not named — they appear in the
client's "e mais N" summary rather than as an id or an address. No other profile
data is exposed: a reader learns a name they can already see on messages in a
conversation they are already authorized to read.

## Reaction aggregates over WebSocket

`reaction.updated` carries the **same aggregate minus `reacted_by_me`**:

```json
{
  "type": "reaction.updated",
  "target_type": "channel",
  "target_id": "…",
  "message_id": "…",
  "reaction": {
    "message_id": "…",
    "actor_user_id": "…",
    "emoji": "👍",
    "added": true,
    "reactions": [{ "emoji": "👍", "count": 4, "users": [{ "user_id": "…", "display_name": "…" }] }]
  }
}
```

| Field                    | Meaning                                                                         |
| ------------------------ | ------------------------------------------------------------------------------- |
| `reaction.actor_user_id` | Who toggled — exposed like `sender_id`, so a client can recognise its own write |
| `reaction.emoji`         | The emoji that was toggled                                                      |
| `reaction.added`         | `true` for a reaction added, `false` for one removed                            |
| `reaction.reactions[]`   | `emoji`, `count`, and `users` — **no `reacted_by_me`**                          |

The omission is deliberate. One event is fanned out to every subscriber of the
target, so a per-reader field would be either wrong for everybody but the actor
or a separate event per reader. `users` is safe to share for the same reason it
is bounded: it names people, not the reader.

### How a client derives `reacted_by_me`

For each aggregate in the event:

- if `actor_user_id` is the reader **and** the aggregate's `emoji` is
  `reaction.emoji`, the reader's state is exactly `reaction.added`;
- otherwise it is carried over from what the client already held for that emoji.

The carried-over value is read from the client's **pre-optimistic baseline** —
the snapshot taken when its own toggle was sent, falling back to current state
when no toggle is in flight — so an event from another actor cannot promote the
reader's unconfirmed guess into ground truth.

That derivation is idempotent, which is what makes duplicate delivery safe:
replaying the same event recomputes the same value from the same baseline, and
`count` and `users` are absolute values from the server rather than increments,
so nothing accumulates and no author is listed twice. Each event's `reactions`
array replaces the previous one wholesale, so a reactor an event removed stays
removed.

Ordering is the connection's: events for one target travel a single WebSocket, so
a client never sees them out of the order the server published them. Delivery
that spans a _reconnect_ is where a client can have missed or re-received work,
and it is resolved by refetching rather than by ordering rules — after a
reconnect, or whenever a client refetches the message, the HTTP aggregate above
replaces the derived state, `reacted_by_me` included.

Cross-instance relay relies on the same refetch: the bus strips `reaction` from a
remote `reaction.updated` rather than re-trusting aggregates it did not compute,
so a subscriber on another pod receives the route and the flag and reads the
authoritative snapshot itself.

### Authorization

The aggregate adds no reach of its own. It is computed only over messages the
caller's own list or fetch query already resolved, under the same workspace,
channel, and DM membership predicates. The `reaction.updated` event is delivered
only to subscribers whose access to the target is re-checked at delivery time, so
a user who has lost access is never told that a reaction happened, let alone by
whom.

## Toggling

Toggles travel over the WebSocket as `reaction.toggle` (`message_id`, `emoji`)
and are idempotent per `(message, user, emoji)`: the server serializes the tuple
with an advisory lock and inserts or deletes in one statement, so a double click,
two tabs, or a retry after a timeout converge on the same count. The existing
per-user reaction rate limit applies unchanged.
