# Chat Service — Anti-spam Policy (RF-19)

> **Issue:** [#419](https://github.com/nicrepository/nchat/issues/419) — Anti-spam
> configurável pelo Admin.
> **Scope:** the per-user message rate limit, made configurable per workspace.
> RF-20 (automatic flood detection on identical messages) is **not** implemented.

Base URL resolved at runtime via `VITE_CHAT_WORKSPACE_API_BASE_URL`
(default: `/api/v1/workspaces`).

---

## Endpoints

| Method | Path                                     | Auth                                          |
| ------ | ---------------------------------------- | --------------------------------------------- |
| GET    | `/v1/workspaces/{workspaceID}/anti-spam` | Bearer JWT + active session + workspace admin |
| PATCH  | `/v1/workspaces/{workspaceID}/anti-spam` | Bearer JWT + active session + workspace admin |

Both verbs require the caller to be an **active `owner` or `admin` of the
workspace named in the path**. The path ID is never trusted: the handler checks
membership, and the `UPDATE` re-checks it in the same statement, so a caller who
administers a different workspace changes nothing. A caller lacking the role and
a caller aiming at someone else's workspace both receive the same `403`.

### Response body (both verbs)

```json
{
  "data": {
    "workspace_id": "…",
    "message_rate_limit_per_minute": 60,
    "min": 1,
    "max": 600
  }
}
```

`min` and `max` are returned so clients render and validate against the server's
bounds instead of restating them.

### Request body (PATCH)

```json
{ "message_rate_limit_per_minute": 30 }
```

Only this field is accepted. Unknown fields, non-integers, decimals, `null`,
values outside `[min, max]` and bodies over 64 KiB are rejected with `400`.

### Status codes

| Code | When                                                            |
| ---- | --------------------------------------------------------------- |
| 200  | Success                                                         |
| 400  | Malformed workspace ID, malformed body, or out-of-range value   |
| 401  | No authenticated user                                           |
| 403  | Caller does not administer this workspace                       |
| 404  | Workspace does not exist                                        |
| 500  | Persistence failure (no internal detail is exposed in the body) |
| 503  | Workspace settings not wired                                    |

---

## Policy semantics

| Property | Value                                                                                                         |
| -------- | ------------------------------------------------------------------------------------------------------------- |
| Scope    | one counter per **workspace + user**                                                                          |
| Window   | one minute (fixed, not configurable)                                                                          |
| Default  | **60** — the value the limit was hardcoded to before RF-19                                                    |
| Minimum  | **1** — never 0; anti-spam must not double as a workspace mute                                                |
| Maximum  | **600** — 10 msg/s per user; there is no "unlimited" value                                                    |
| Storage  | `chat.workspaces.message_rate_limit_per_minute`, `NOT NULL`, `CHECK (BETWEEN 1 AND 600)` (migration `000018`) |

Channels and DMs share one budget per user: the policy governs how much a person
may say, not where. Forwarding a message spends the same budget — it creates a
message — while keeping its own tighter dedicated cap on top.

Rejected sends still increment the counter (the existing fixed-window limiter's
semantics), so a client that keeps retrying after a `429` stays blocked for the
remainder of the window. A rejected message is neither persisted nor published:
the check runs before the handler.

Blocked requests answer `429` with `Retry-After: 60` and error code
`rate_limited`, the same shape clients already handle.

Message creation is HTTP-only — the WebSocket hub accepts
`subscribe`/`unsubscribe`/`ping`/`reaction_toggle` and has no send frame — so
these three routes are the complete set of enforcement points:

- `POST /api/chat/channels/{channelID}/messages`
- `POST /api/chat/dm/{conversationID}/messages`
- `POST /api/chat/channels/{channelID}/messages/forward`

---

## Enforcement and propagation

Counting uses the **existing shared Lua/Valkey limiter** (the one already behind
reactions, message edits and channel/DM creation). RF-19 adds no second rate
limiting mechanism; it feeds a per-workspace budget into that one. Because the
counter lives in Valkey, the limit holds across chat-service replicas — the
previous in-memory limiter granted each replica its own full budget.

### Where the workspace comes from

The workspace a send is counted against is resolved **server-side, once per
request**, and published in the request context; the message handler then writes
to that same value instead of resolving again. No client-supplied path, query,
body or header value participates — none of the three send routes even carries a
workspace segment.

Everything downstream is scoped to that one ID: the policy is loaded **by** it,
cached **under** it, and spent against a counter keyed **by** it. Two workspaces
can neither share a budget nor read each other's policy, and a workspace with no
cached policy never borrows another's.

Sending into a channel or DM that belongs to a different workspace than the
caller's is rejected by the message service's own membership checks, so no
message is created; the rejected attempt spends only the caller's own budget.

### Cache

Policy is read from PostgreSQL and cached in-process **per workspace** for
**5 seconds**:

- the instance serving the `PATCH` invalidates **that workspace's entry only**,
  after the write is confirmed, so the change applies to it immediately and no
  other workspace is forced back to a database read;
- every other instance picks it up within the TTL.

A failed write invalidates nothing. No restart is required, and no database read
happens on the per-message path.

### Failure behaviour

| Failure                                                    | Behaviour                                                      |
| ---------------------------------------------------------- | -------------------------------------------------------------- |
| Workspace unresolvable                                     | `503`; no counter is touched, so no other workspace is charged |
| PostgreSQL unreachable, **this** workspace's policy cached | that workspace's last known policy stays enforced past its TTL |
| PostgreSQL unreachable, nothing ever read for it           | `503`; the send is refused, not admitted unmetered             |
| Valkey unreachable                                         | `503`; message neither persisted nor published                 |
| Workspace row predates migration `000018`                  | falls back to the default of 60                                |
| Valkey unconfigured at boot                                | send routes answer `503`                                       |

Stale is per workspace: a workspace whose policy has never been read is refused,
never served from another workspace's stale entry.

The last row is deliberate. Falling back to an in-process limiter would hand each
replica its own full budget — the cross-instance bypass RF-19 exists to close —
so the routes refuse instead of degrading quietly. A chat-service without
`VALKEY_URL` already fails its readiness probe
(`reaction-rate-limiter-configured`) and receives no traffic in a cluster.

There is no configuration that disables the protection: the bounds make
"unlimited" inexpressible, and every failure path keeps a limit in force.

---

## Known limitations

- **No administrative audit trail.** chat-service has no admin audit
  infrastructure, and RF-19 did not add one. Who changed the policy, and from
  what to what, is not recorded.
- The fixed window permits roughly twice the limit across a window boundary.
  This is the pre-existing behaviour of the shared limiter and was not changed.
- Up to 5 seconds of propagation lag on instances other than the one that served
  the change.
