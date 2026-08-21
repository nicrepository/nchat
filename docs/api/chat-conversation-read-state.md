# Conversation read state

The chat backend is the source of truth for unread counts. It stores a
server-timestamped read point per user, workspace, and conversation; the count
itself is derived from active messages after that point and excludes messages
sent by the requesting user.

Mention highlighting is intentionally not part of this contract. It remains a
client-side hint because mention parsing currently exists only in the web app.

## Sidebar response

`GET /api/chat/sidebar` always includes `unread_count` on every channel and DM
row. A zero is an authoritative value, not an omitted field.

| Field          | Type                 | Meaning                                                                |
| -------------- | -------------------- | ---------------------------------------------------------------------- |
| `unread_count` | non-negative integer | Active, non-own messages newer than the caller's last server read time |

## Mark as read

| Target             | Endpoint                                   |
| ------------------ | ------------------------------------------ |
| Channel            | `POST /api/chat/channels/{channelID}/read` |
| Direct or group DM | `POST /api/chat/dm/{conversationID}/read`  |

The body is optional. When supplied, its strict JSON shape is:

```json
{ "last_read_message_id": "00000000-0000-4000-8000-000000000000" }
```

The message ID is informational and intended for diagnostics or future cursor
work. Counting uses `last_read_at`, assigned by PostgreSQL; clients never send
the timestamp. Its precision is PostgreSQL `TIMESTAMPTZ` precision and clients
must not infer ordering from UUIDs. Successful repeated calls return `204 No
Content`. Missing and unauthorized conversations both return the same
non-enumerating `404`.

The web client may briefly render its user/workspace-scoped local cache while
loading. Any `unread_count` returned by the server replaces that cached value,
including a lower count caused by reading on another device.

Issue: not linked; no verified GitHub issue number exists for this task.
