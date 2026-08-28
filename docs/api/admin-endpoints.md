# Admin Service — Admin API

> **Scope:** the Admin Console foundation (issue #578), the management surface
> (issue #579), configuration management (issue #580), platform observability
> (issue #581) and integration diagnostics (issue #582). This document covers
> the endpoints that exist today: the administrative session handshake, the
> console bootstrap, the audit trail, the platform user directory, the channel
> and conversation directories, the two operational policies that are
> configurable at runtime, the platform configuration catalogue, the operational
> dashboard and Health Center, and the integration inventory with its active
> diagnostics. Integration **configuration** remains read-only here: every
> integration setting is class C or D, so changing one is a commit and a rollout
> or the Sealed Secrets runbook, never a write through this API.

Public base: `/api/admin`. The gateways rewrite `/api/admin/<rest>` to
`/<rest>` before the request reaches the pod, so the paths admin-service
registers carry no public prefix.

| Layer                             | Rule                                                                |
| --------------------------------- | ------------------------------------------------------------------- |
| Local Traefik                     | `strip-admin-prefix` in `infra/traefik/local/dynamic.yml`           |
| k3s-dev / k3s-staging / nchat-dev | `Middleware admin-api-prefix`, referenced by the Ingress annotation |

`scripts/ci/admin-route-contract-check.sh` renders every overlay and fails if a
middleware is missing, an overlay diverges, the backend adds an `/api/` alias,
or the console's base path drifts. Kubernetes probes reach `/healthz` and
`/readyz` on the pod directly and are unaffected.

Every response uses the platform envelope: `{"data": …}` on success,
`{"error": {"code": …, "message": …}}` on failure.

---

## Contents

| #   | Method | Path                       | Auth                              |
| --- | ------ | -------------------------- | --------------------------------- |
| 1   | POST   | `/api/admin/session`       | NChat access token (Bearer)       |
| 2   | DELETE | `/api/admin/session`       | Admin session cookie + CSRF token |
| 3   | GET    | `/api/admin/bootstrap`     | Admin session cookie              |
| 4   | GET    | `/api/admin/audit/events`  | Admin session cookie + capability |
| 5   | —      | Management (issue #579)    | Admin session cookie + capability |
| 6   | —      | Configuration (issue #580) | Admin session cookie + capability |
| 7   | —      | Observability (issue #581) | Admin session cookie + capability |
| 8   | —      | Integrations (issue #582)  | Admin session cookie + capability |

Every management route below is guarded in the same order: the administrative
session, then — for a mutation — the origin and CSRF checks, then the one
capability the route declares. There is no path that skips a step, no second
router, and no generic mutation endpoint that takes the object type as a
parameter.

---

## The credential

The console's credential is an opaque, server-generated value in a cookie:

```
__Host-nchat_admin_session=<opaque>; Path=/; HttpOnly; Secure; SameSite=Strict; Max-Age=900
```

- `HttpOnly` — unreadable from JavaScript, so an XSS foothold on the console
  cannot exfiltrate it.
- `SameSite=Strict` — withheld on every cross-site request, which is the primary
  CSRF defence.
- `__Host-` prefix — the browser refuses the cookie unless it is `Secure`,
  `Path=/` and carries no `Domain`. A sibling subdomain (the chat host, for
  instance) therefore cannot set or read it.

`X-NChat-Admin-Token` (`ADMIN_BOOTSTRAP_TOKEN`) plays no part in any endpoint
here. It remains a CLI-only bootstrap credential on the three `/admin/*` routes
of auth-service and must never be sent to a browser.

## CSRF

Mutating requests must echo the token returned in the bootstrap payload:

```
X-NChat-Admin-CSRF: <csrf_token>
```

The token is derived server-side from the session identifier, so it is bound to
exactly one session and cannot be transplanted. The request `Origin` (or
`Referer`, when the browser omits `Origin`) must also be the deployment's own or
one of `ADMIN_ALLOWED_ORIGINS`; a request carrying neither header is refused.

---

## 1. `POST /api/admin/session`

Exchanges a proven NChat identity for an administrative session. This is the
only endpoint that accepts a chat access token, and it accepts it for one
purpose: proving who is asking. It authorizes nothing on its own.

**Request**

```
Authorization: Bearer <nchat access token>
```

No body. Every other input — the actor, the originating login session, the
client address, the user agent — is derived server-side.

**Responses**

| Status | Meaning                                                                              |
| ------ | ------------------------------------------------------------------------------------ |
| `201`  | Session created. `Set-Cookie` carries the credential; body is the bootstrap payload. |
| `401`  | Missing, malformed, expired or revoked access token.                                 |
| `403`  | Authenticated, but not an active platform administrator, or holding no capability.   |
| `429`  | Rate limited (per client address).                                                   |
| `503`  | Admin API not configured on this pod.                                                |

## 2. `DELETE /api/admin/session`

Revokes the administrative session and expires the cookie. Idempotent: it never
reveals whether the presented credential matched anything.

| Status | Meaning                                |
| ------ | -------------------------------------- |
| `204`  | Session ended, cookie cleared.         |
| `401`  | No live administrative session.        |
| `403`  | Origin rejected or CSRF token invalid. |

## 3. `GET /api/admin/bootstrap`

What the console shell loads before rendering anything.

```json
{
  "data": {
    "identity": {
      "user_id": "…",
      "email": "admin@example.test",
      "display_name": "Admin",
      "avatar_url": "/api/auth/avatars/…"
    },
    "capabilities": ["admin.audit.read"],
    "environment": "PRODUCTION",
    "build": { "service": "admin-service", "version": "1.2.3", "commit": "abc1234" },
    "session": {
      "idle_expires_at": "2026-08-20T12:00:00Z",
      "absolute_expires_at": "2026-08-20T20:00:00Z"
    },
    "csrf_token": "…"
  }
}
```

The payload is an allowlist, not a filter: the response struct names every field
that may leave the service. It carries **no** access token, refresh token,
bootstrap token, service token, DSN, password, client secret, Kubernetes
credential, environment dump or infrastructure topology, and the shape is
asserted field-by-field by `TestBootstrap_LeaksNoCredentialOrInfrastructureDetail`.

`environment` comes from the deployment's own `APP_ENV` and is one of
`DEVELOPMENT`, `STAGING`, `PRODUCTION`. An unrecognised value resolves to
`PRODUCTION`. No request header, hostname, query parameter or stored preference
participates.

`capabilities` is presentation data. It tells the console which navigation
entries to render; it never tells an endpoint what to allow.

| Status | Meaning                                              |
| ------ | ---------------------------------------------------- |
| `200`  | Session live.                                        |
| `401`  | No session, or the session or its login was revoked. |
| `403`  | The principal is no longer an active administrator.  |
| `503`  | Admin API not configured on this pod.                |

## 4. `GET /api/admin/audit/events`

Requires the `admin.audit.read` capability.

**Query**

| Name      | Default | Notes                                                                                                            |
| --------- | ------- | ---------------------------------------------------------------------------------------------------------------- |
| `limit`   | `50`    | Positive integer, capped server-side at `200`.                                                                   |
| `user_id` | —       | Optional. A user id (UUID); narrows the trail to the events performed **on** that account. Malformed is a `400`. |

```json
{
  "data": {
    "events": [
      {
        "id": "7",
        "occurred_at": "2026-08-20T10:00:00Z",
        "actor_user_id": "…",
        "actor_email": "admin@example.test",
        "action": "admin.session.create",
        "resource": "admin.session",
        "result": "success",
        "correlation_id": "…"
      }
    ]
  }
}
```

`user_id` is the one narrowing this endpoint offers, and it exists because an
operator looking at somebody's record needs their history — the last fifty
platform-wide events are not it.

The filter runs in the database, in the `WHERE` clause, **before** the ordering
and the limit. That is the whole point: an event that is the two-hundredth most
recent on the platform is still the first one in its own subject's history, and
a filter applied after the limit — or in the browser — would never find it.

It is not an object reference to tamper with. The trail is platform-wide and
`admin.audit.read` authorizes all of it, so narrowing reaches nothing a caller
could not already read: no row becomes visible by naming an id, and none becomes
hidden. The value is validated as a UUID and converted by the service into the
canonical resource key `admin.user:<uuid>`, so what reaches the indexed
`resource` column is a string this service built.

"Performed on" is the deliberate reading. `actor_user_id` answers a different
question — what somebody _did_ — and is not what this filter compares. The four
mutations that act on an account (`admin.user.status.update`,
`admin.user.sessions.revoke`, `admin.user.role.grant`,
`admin.user.role.revoke`) all write that same resource key, so one filter finds
every one of them.

Channel membership events are filed under the **channel**
(`admin.channel:<uuid>`), because that is the object that changed; the affected
person travels in the metadata for the channel's own trail. Re-keying them to
the user would claim a user record was modified when a channel's membership was.

There is no filter on metadata, no JSON path, no pattern and no expression. The
API expresses one intent — this person's history — and nothing a caller composes
reaches the query.

Absent `user_id`, the response is the platform-wide trail exactly as before.

| Status | Meaning                                                                    |
| ------ | -------------------------------------------------------------------------- |
| `200`  | Events returned, newest first.                                             |
| `400`  | `limit` is not a positive integer, or `user_id` is not a valid identifier. |
| `401`  | No live administrative session.                                            |
| `403`  | Missing `admin.audit.read`.                                                |
| `503`  | Admin API not configured on this pod.                                      |

---

## Single sign-on

The console's sign-in link is `/api/auth/oidc/keycloak/login?app=admin`. `app`
is a closed enum (`chat` | `admin`) that selects which provider callback URI
auth-service uses; it is a label, never a destination, and an unknown value is
refused with `400 invalid_oidc_app`. The chosen context is stored server-side
with the OIDC state and recovered at the callback, so nothing the returning
request carries decides where the browser ends up.

`OIDC_ADMIN_REDIRECT_URL` must name this host's
`/api/auth/oidc/keycloak/callback`, and that URI must be registered on the same
Keycloak client as the chat one. Leaving it unset means the console has no
single sign-on — it never falls back to the chat host's URI.

A successful SSO login yields an ordinary NChat session. It grants no
administrative authority: `POST /api/admin/session` still refuses anyone who is
not an active principal.

## Capabilities

Declared in `services/admin-service/internal/domain/capability.go` and
constrained by a `CHECK` on `auth.admin_role_capabilities` (migration 000008),
so a capability the platform does not define cannot be granted:

`admin.superuser`, `admin.users.read`, `admin.users.manage`,
`admin.channels.read`, `admin.channels.manage`, `admin.security.read`,
`admin.security.manage`, `admin.integrations.read`,
`admin.integrations.manage`, `admin.infrastructure.read`,
`admin.infrastructure.manage`, `admin.audit.read`, `admin.config.read`,
`admin.config.manage`.

`admin.superuser` implies the rest. An unknown capability is refused even for a
superuser. See `docs/security/rbac-matrix.md` for the policy and
`docs/runbooks/task-admin-console-foundation.md` for how a principal is granted.

---

# Management surface (issue #579)

## Conventions

**Pagination.** Every listing is keyset-paginated and newest-first. There is no
page number and no total: a page costs its own rows, and a count would mean
scanning the whole table on every request.

| Query    | Default | Notes                                                |
| -------- | ------- | ---------------------------------------------------- |
| `limit`  | `25`    | Positive integer; values above `100` are capped.     |
| `cursor` | —       | Opaque token from the previous page's `next_cursor`. |

```json
"pagination": { "next_cursor": "MjAyNi0wOC0yMFQxMDowMDowMFp8dXVpZA", "has_more": true }
```

On the last page the field is present and null:

```json
"pagination": { "next_cursor": null, "has_more": false }
```

`has_more` is derived from `next_cursor`, so the two can never disagree. The
last page is always `null` and never `""` — the field is never omitted either,
so a client never has to tell "no more pages" from a key the server forgot to
send. A malformed cursor is a `400`, not a silent restart from page one.

The cursor is opaque but not secret and not a capability: it names a position in
an ordering the caller is already authorized to read, and the authorization is
re-evaluated on the request that carries it.

**Ordering.** There is no `sort` parameter on any endpoint. Each listing has one
fixed order, so there is no ordering expression a request can influence.

**Filters.** Every filter is drawn from a closed allowlist or is a bounded
scalar. An unrecognised value is a `400` rather than a filter that silently
matches nothing. No filter is ever concatenated into SQL: the queries bind their
parameters and use one prepared statement for every combination.

**Search.** `q` is matched with `ILIKE` against a bound pattern. `%`, `_` and
`\` are escaped before the pattern is built, and every predicate states
`ESCAPE E'\\'` rather than relying on the server default — so a term containing
them matches them literally instead of turning into a wildcard that quietly
stops filtering. The escaping is proved against a real PostgreSQL by the
`TestPostgreSQL_Search*` suite, which seeds decoy records that would be false
positives if either character were still a wildcard and asserts the rows the
query returned, not the pattern the helper produced.

**Bodies.** Mutating bodies are capped at 64 KiB, decoded with unknown fields
refused, and accept exactly one JSON document. A body carrying a field the
endpoint does not name — `role`, `capabilities`, `platform_admin`, a second
identifier — is a `400`, never a partially applied update.

**Statuses.** `400` malformed request; `401` no live administrative session;
`403` missing capability, rejected origin or invalid CSRF token; `404` the named
object does not exist; `409` the object is not in the state the command
requires, including a change another request made first; `503` the Admin API is
not configured on this pod.

---

## Users

| Method | Path                                               | Capability           |
| ------ | -------------------------------------------------- | -------------------- |
| GET    | `/api/admin/users`                                 | `admin.users.read`   |
| GET    | `/api/admin/users/{userID}`                        | `admin.users.read`   |
| PATCH  | `/api/admin/users/{userID}/status`                 | `admin.users.manage` |
| DELETE | `/api/admin/users/{userID}/sessions`               | `admin.users.manage` |
| POST   | `/api/admin/users/{userID}/admin-roles`            | `admin.superuser`    |
| DELETE | `/api/admin/users/{userID}/admin-roles/{roleSlug}` | `admin.superuser`    |

### `GET /api/admin/users`

| Query    | Values                                                                                   |
| -------- | ---------------------------------------------------------------------------------------- |
| `q`      | Free text, ≤128 chars; matches display name, full name or e-mail. Wildcards are escaped. |
| `status` | `active` \| `invited` \| `suspended` \| `locked`                                         |

`deleted` is a status `auth.users` can hold and is deliberately **not** an
accepted filter — `status=deleted` is a `400`. The directory excludes
soft-deleted accounts unconditionally, as every reader of `auth.users` on this
platform does, so the value could never return a row; making it return rows
would turn a contract fix into administrative access to erased and anonymized
people, which is retention work this issue does not do.
| `auth_source` | `manual` \| `oidc` \| `imported` |
| `platform_admin` | `true` \| `false`. Absent means "either", which is not the same as `false`. |
| `workspace_role` | `owner` \| `admin` \| `moderator` \| `member` \| `guest` |
| `inactivity` | `never` \| `7d` \| `30d` \| `90d` |

`workspace_role` is a **workspace** role, from `chat.workspace_members` as
migration 000022 widened it. It is not a platform role and not a capability:
`platform_admin` is a separate question, and the two combine rather than replace
each other — asking for both returns the workspace owners who are also platform
administrators.

The directory is platform-wide and no request names a workspace, so the filter
reads as _"holds at least one active membership with this role, in any active
workspace"_. It cannot mean "in workspace X" because there is no X in the
request. It is applied as an `EXISTS`, so somebody who owns two workspaces
appears once and the page size stays honest. A membership that has ended, or one
in a disabled workspace, does not select.

```json
{
  "data": {
    "users": [
      {
        "id": "…",
        "email": "ana@example.test",
        "display_name": "Ana",
        "full_name": "Ana Lima",
        "avatar_url": "",
        "status": "active",
        "auth_source": "oidc",
        "external_provider": "keycloak",
        "identity_managed_externally": true,
        "last_login_at": "2026-08-01T10:00:00Z",
        "created_at": "2026-01-01T00:00:00Z",
        "platform_admin": false,
        "admin_roles": [],
        "workspace_roles": [
          {
            "workspace_id": "…",
            "workspace_name": "NChat",
            "role": "member",
            "status": "active",
            "joined_at": "2026-01-01T00:00:00Z"
          }
        ],
        "active_sessions": 2
      }
    ],
    "pagination": { "next_cursor": null, "has_more": false }
  }
}
```

The payload is an allowlist, not a filter. `auth.users` carries more than this —
the subject identifier the identity provider knows the person by, the
soft-delete timestamp, the anonymization timestamp — and none of it can arrive
here by somebody adding a column. `identity_managed_externally` is derived
server-side so the console does not re-derive it.

The row aggregates (`workspace_roles`, `admin_roles`, `active_sessions`) come
from lateral joins evaluated once per row of the page, on indexes already keyed
by `user_id`. There is no per-row follow-up request.

### `GET /api/admin/users/{userID}`

Adds `memberships`, `channel_count`, `role_grants` (with `granted_at` and
`granted_by`) and `available_roles` — the catalogue of grantable roles and what
each confers. The catalogue travels with the record so the console does not need
a second endpoint for it; it names roles and capabilities, which are public
policy, and no principal.

### `PATCH /api/admin/users/{userID}/status`

```json
{ "status": "suspended" }
```

`active` and `suspended` only. `invited`, `locked` and `deleted` belong to other
flows and are not a switch an operator flips.

Suspension and session revocation are one transaction: the user row is locked,
the transition is validated under that lock, every live session is revoked with
`revoked_reason = 'admin_suspension'`, the matching refresh-token history is
marked revoked, and pending OIDC exchange codes are consumed so a code minted
before the suspension cannot be redeemed after it. Activation restores nothing —
the person signs in again.

```json
{
  "data": {
    "user_id": "…",
    "from_status": "active",
    "to_status": "suspended",
    "revoked_sessions": 2
  }
}
```

An administrator cannot change their own status: a `403`, recorded as a denial.
A transition onto the status the account already has is a `409`, so two
concurrent suspensions produce one change and one conflict rather than two audit
rows claiming the same thing.

### `DELETE /api/admin/users/{userID}/sessions`

Signs one account out everywhere without changing what it is allowed to do.
Answers `{ "data": { "user_id": "…", "revoked_sessions": 3 } }`. Same self
guard: revoking the operator's own logins would end the administrative session
riding on one of them from inside the request that asked for it.

### `POST` / `DELETE /api/admin/users/{userID}/admin-roles`

```json
{ "role_slug": "platform-auditor" }
```

Both answer `204`.

`admin.superuser` and not `admin.users.manage`. A principal may only confer
authority it holds in full; anything narrower granting a role would be
horizontal escalation — an administrator with `admin.users.manage` handing
somebody `admin.security.manage`.

Enforced invariants:

- **self-escalation and self-lockout** — an administrator cannot change their own
  roles (`403`);
- **target validity** — the account must exist, not be soft-deleted, and be
  active; a suspended account is a `409`;
- **principal suspension is not undone** — a role grant does not reactivate a
  principal suspended out of band (`409`);
- **the platform is never left without an administrator** — a revocation counts
  the remaining principals reaching `admin.superuser` _after_ the delete and
  _inside_ the transaction, under a transaction-scoped advisory lock, and rolls
  back if the count would reach zero (`409`). The lock is what makes it correct
  under concurrency: the rule is about a count across rows, and two transactions
  each deleting a different row would otherwise both see the other's row still
  present and both commit.

Removing a principal's last role leaves the principal row in place. It confers
nothing — the session service refuses a principal holding no capability on every
request, so the removal takes effect immediately — and deleting it would cascade
through `auth.admin_sessions`, destroying records the audit trail refers to.

---

## Channels and conversations

| Method | Path                                                | Capability              |
| ------ | --------------------------------------------------- | ----------------------- |
| GET    | `/api/admin/channels`                               | `admin.channels.read`   |
| GET    | `/api/admin/channels/{channelID}`                   | `admin.channels.read`   |
| PATCH  | `/api/admin/channels/{channelID}/status`            | `admin.channels.manage` |
| GET    | `/api/admin/channels/{channelID}/member-candidates` | `admin.channels.manage` |
| POST   | `/api/admin/channels/{channelID}/members`           | `admin.channels.manage` |
| DELETE | `/api/admin/channels/{channelID}/members/{userID}`  | `admin.channels.manage` |
| GET    | `/api/admin/conversations`                          | `admin.channels.read`   |

### `GET /api/admin/channels`

| Query             | Values                                  |
| ----------------- | --------------------------------------- |
| `q`               | Matches display name or slug.           |
| `workspace_id`    | UUID.                                   |
| `type`            | `public` \| `private`                   |
| `status`          | `active` \| `archived`                  |
| `min_members`     | Non-negative integer, ≤1000000.         |
| `active_within`   | `7d` \| `30d` \| `90d`                  |
| `administered_by` | A user id (UUID). Malformed is a `400`. |

`administered_by` selects the channels that person **administers**, which in
this domain means _created_ (`chat.channels.created_by`) **or** _moderates_
(`chat.channel_members.role = 'moderator'`). It is deliberately narrow: the chat
domain has no channel owner and no channel admin.

It does **not** include the workspace's owners and admins. Those govern the
workspace, not one channel, and folding them in would make every channel in a
workspace match its owner — a different question, and precisely the collapse
`docs/security/rbac-matrix.md` exists to prevent.

Each row carries the workspace, visibility, state, member and moderator counts,
who created it, and a last-activity **timestamp**. Not a preview: the console
lists where conversation happens, never what was said.

Private channels appear. That is not a widening of the channel read policy — the
row states that a private channel exists, how large it is and who administers
it, which is what `admin.channels.read` authorizes. No message and no member
name of a private channel is reachable from the listing, and
`chat.channel_visible_to_user` remains the only thing that decides who may read
one.

### `GET /api/admin/channels/{channelID}`

Adds `category_name`, `message_count` (a volume, not a listing) and two separate
people lists:

- `moderators` — `chat.channel_members.role = 'moderator'`, a per-channel role;
- `workspace_admins` — `chat.workspace_members.role IN ('owner','admin')`, which
  governs the workspace.

They are separate fields because they are separate authorities. Merging them
into one "admins" array is exactly the collapse `docs/security/rbac-matrix.md`
exists to prevent. Both lists are capped at 50.

A third list, `members`, is a bounded preview of the channel's membership so the
console has something to administer. It is capped at 50; `member_count` on the
summary is the real total, and capping the preview is what keeps a detail view
from doubling as a membership export. Being in a channel is not the same as
administering it, which is why it is its own field and not merged into either of
the two above.

### `PATCH /api/admin/channels/{channelID}/status`

```json
{ "status": "archived" }
```

Archive and unarchive, both directions. There is **no delete**: a channel holds
its members' history, and removing it is not an operation an administrative
click should perform. The workspace's `#geral` channel is refused with `403` —
chat-service treats it as immutable and this console must not become a second
way around that. A transition onto the current status is a `409`.

### `GET /api/admin/channels/{channelID}/member-candidates`

The picker behind the add: it answers "who could I add to this channel", so an
operator finds a colleague by name instead of knowing an identifier.

| Query | Notes                                                                         |
| ----- | ----------------------------------------------------------------------------- |
| `q`   | Matches display name, full name or e-mail, ≤128 chars. Wildcards are escaped. |

Requires **`admin.channels.manage`** — the capability of the mutation it feeds,
not `admin.channels.read`. Seeing that a channel exists must not also enumerate
the people in its workspace.

The workspace is derived from the channel inside the query, so no request can
point the search at another tenant's directory. That is the reason this is a
channel-scoped endpoint rather than a filter on `/users`.

The result set is the same population the add would admit — active member of the
channel's workspace, active workspace, active channel, active non-deleted
account — minus anybody already in the channel, excluded by a `NOT EXISTS` in
the same statement rather than by a lookup per candidate. Capped at 10.

```json
{
  "data": {
    "candidates": [
      {
        "user_id": "…",
        "display_name": "Ana",
        "full_name": "Ana Lima",
        "email": "ana@example.test",
        "avatar_url": "",
        "workspace_role": "member"
      }
    ]
  }
}
```

Deliberately narrower than a directory row: no administrative role, no
membership list, no session count, no identity-provider detail. A picker behind
a channel capability must not double as a second, wider user directory.

This is a convenience and never a control. `POST .../members` re-decides
eligibility for whoever is actually submitted, under the shared rule, in the
statement that writes — a client that skips this search gains nothing.

### `POST /api/admin/channels/{channelID}/members`

```json
{ "user_ids": ["…", "…"] }
```

User ids and nothing else. There is no `role` in the contract — every
administratively added member joins as an ordinary `member`, and an endpoint
that could create a channel moderator would be a privilege grant wearing the
shape of a membership change. There is no `workspace_id` either: it is derived
from the channel, so no request can aim the operation at another workspace. A
body carrying either is a `400`.

The list must be non-empty, at most 50 entries, and every entry a distinct
well-formed UUID; anything else is a `400` before a statement runs.

**Eligibility** is the shared rule in `libs/go/platform/channelmembership`,
embedded verbatim by this service _and_ by chat-service — the two writers of
`chat.channel_members` do not each carry their own copy. A target must be an
active member of the channel's workspace, in an active workspace, in an active
channel, with an active non-deleted account. Guests are eligible: being added to
a channel is the only way a guest reaches one.

What is **not** shared is the actor check, because it is genuinely different.
chat-service re-derives the caller's owner/admin/moderator workspace membership
inside its transaction; here the authority is the platform capability
`admin.channels.manage`, re-read from the database by the session guard on this
request, and the actor is typically not a member of the workspace at all.

The add is **all-or-nothing**: if any requested target is ineligible, nothing is
written and the answer is `409`. `409` rather than `403` on purpose — the caller
already holds the capability and can already list every channel and every user,
so there is nothing left to conceal, and `403` in this API means "you lack the
capability", which would be a lie.

Idempotent: a repeat adds nobody and says so.

```json
{
  "data": {
    "channel_id": "…",
    "workspace_id": "…",
    "added": 1,
    "already_members": 0,
    "removed": false,
    "member_count": 13
  }
}
```

`added` and `already_members` are separate because they are separate outcomes.
Reporting a retry as "1 added" would tell the operator something false.

`member_count` is the channel's total **after this operation**, and it is exact
under concurrency. Every writer of `chat.channel_members` — this endpoint, the
removal below, and chat-service's own add, single-add and remove — takes an
exclusive row lock on the channel as the first statement of its transaction, and
counts after its write and before its commit. Two administrators adding
different people to a channel of ten therefore answer 11 and 12, in whichever
order they win the lock, and the committed total is 12. The lock is per channel:
mutations on different channels never wait for each other.

This changes membership and grants the actor nothing: they do not become a
member, and no message becomes reachable anywhere in this service as a result.

### `DELETE /api/admin/channels/{channelID}/members/{userID}`

The target is a path segment, so the operation has the idempotent shape a
`DELETE` should have and cannot be re-aimed by a body the console did not send.

Removing somebody who is not a member answers `200` with `removed: false`: the
caller's intent already holds, and a `404` would make a safe retry look like a
failure.

An **archived** channel is not a refusal. Removal does not read the channel's
status — taking somebody out of a channel nobody uses needs no live channel, and
chat-service's own removal behaves the same way. Only _adding_ requires an
active channel, because the shared eligibility rule does.

`#geral` is refused with `403`, mirroring `ErrCannotLeaveGeneralChannel` in
chat-service. Every member of a workspace belongs to its general channel by
construction, and this console must not become a second way around that.
Additions to `#geral` are _not_ refused: a guest is not enrolled in it
automatically, so adding one is a real operation.

**Not offered, deliberately:** membership of a private DM group. A platform
administrator has no authority over a conversation they cannot read, and
`chat.dm_members` remains the only thing that decides who participates in one.
"Groups" in this console means the workspace's channels.

### `GET /api/admin/conversations`

| Query          | Values                 |
| -------------- | ---------------------- |
| `workspace_id` | UUID.                  |
| `type`         | `direct` \| `group`    |
| `status`       | `active` \| `archived` |

```json
{
  "data": {
    "conversations": [
      {
        "id": "…",
        "workspace_id": "…",
        "workspace_name": "NChat",
        "type": "group",
        "status": "active",
        "participant_count": 4,
        "message_count": 120,
        "created_at": "…",
        "updated_at": "…",
        "last_activity_at": "…"
      }
    ],
    "pagination": { "next_cursor": null, "has_more": false }
  }
}
```

**What is absent is the contract.** No body, no rich text, no attachment, no
quote, no reaction, no preview, no "most recent message", no title — a group's
title is written by its participants — and no participant identity. There is no
search over conversations, no per-conversation detail endpoint, and no
administrative message endpoint of any kind in this service.

Being a platform administrator does not make somebody a participant.
`chat.dm_members` remains the only thing consulted to decide who may read a
conversation, and no query added by this issue reads `chat.messages.body_text`.
`TestConversationQuery_NeverSelectsMessageContent` asserts that against the SQL
itself, and `TestListConversations_ExposesNoContentAndNoParticipants` asserts it
against the response.

There is no `q` parameter here on purpose: searching conversations would mean
searching their titles or their content.

---

## Operational policies

| Method | Path                                          | Capability                    |
| ------ | --------------------------------------------- | ----------------------------- |
| GET    | `/api/admin/policies/anti-spam`               | `admin.security.read`         |
| PATCH  | `/api/admin/policies/anti-spam/{workspaceID}` | `admin.security.manage`       |
| GET    | `/api/admin/policies/upload`                  | `admin.infrastructure.read`   |
| PATCH  | `/api/admin/policies/upload/{workspaceID}`    | `admin.infrastructure.manage` |

These are the **only two** operational policies that are configurable at
runtime. Both are columns on `chat.workspaces` with a database `CHECK`, read by
the enforcing service on the path that enforces them, so an administrative
change takes effect without a restart.

Everything else an operator might want to tune — burst windows, reaction and
edit limits, conversation-creation limits, link-scan budgets, malware scanner
behaviour, upload concurrency, the gateway body cap — is read from the
environment at boot by the service that enforces it. Changing one is a rollout,
not a click, and this API offers no field that would store a number nobody
reads. The upload listing names the ones that are real and fixed rather than
staying silent about them.

Both listings echo the server's own bounds so a client validates and renders
against them instead of restating limits it decided:

```json
"bounds": { "min": 1, "max": 600, "default": 60, "unit": "messages_per_minute" }
```

`unit` is named explicitly: a limit whose unit the screen has to guess is how
"60" becomes per-second on one side and per-minute on the other. `step`, present
on the upload bounds, is the granularity every accepted value must be an exact
multiple of.

### Anti-spam (RF-19)

```json
{ "message_rate_limit_per_minute": 30 }
```

Bounds `[1, 600]`, default `60`. The minimum is 1 and not 0 by design: an
anti-spam control must not double as a way to mute a workspace, so "unlimited"
and "disabled" are both inexpressible.

The same column chat-service's own workspace-admin endpoint writes, under the
same `CHECK`. What differs is the authorization: chat-service asks "does this
person administer this workspace", this service asks "does this principal hold
the platform capability". Propagation is unchanged — chat-service caches the
policy per workspace for five seconds, so an instance other than the one serving
the change picks it up within that window. This API claims no hot reload the
platform does not perform.

### Upload limit (RF-32)

```json
{ "max_upload_bytes": 104857600 }
```

Bounds `[1 MiB, 512 MiB]`, default 250 MiB, and every accepted value is an exact
multiple of 1 MiB. A value off that grid is **refused**, never rounded: the
administrative screen edits whole MiB, so a stored limit that is not one could
not be shown there without being changed, and an ordinary save would then write
a limit nobody typed.

Refused with `400`, at the HTTP boundary and before the service: decimals,
exponent notation, a number sent as a JSON string, `null`, an absent field, a
value too large for `int64`, and anything outside the bounds. A number that does
not fit `int64` cannot wrap into a small positive limit.

The listing also publishes what this policy is not:

```json
"gateway_hard_cap_bytes": 536879104,
"deployment_managed": ["malware_scanning", "upload_concurrency"]
```

`gateway_hard_cap_bytes` is the static infrastructure ceiling, derived from the
same constant the gateway configuration is, so the two cannot drift. No
workspace limit can exceed it. No value of this policy disables malware
scanning, bypasses admission control or makes an unavailable control mean
"allow unlimited": the bounds make that inexpressible and every failure path in
file-service keeps a limit in force.

---

## Audit

Every mutation above writes one row to `auth.admin_audit_events` through the
existing audit service, with the actor taken from the session and the
correlation id minted by this service — never accepted from a header.

| Action                         | Written by                                   |
| ------------------------------ | -------------------------------------------- |
| `admin.user.status.update`     | `PATCH /users/{id}/status`                   |
| `admin.user.sessions.revoke`   | `DELETE /users/{id}/sessions`                |
| `admin.user.role.grant`        | `POST /users/{id}/admin-roles`               |
| `admin.user.role.revoke`       | `DELETE /users/{id}/admin-roles/{slug}`      |
| `admin.channel.status.update`  | `PATCH /channels/{id}/status`                |
| `admin.channel.member.add`     | `POST /channels/{id}/members`                |
| `admin.channel.member.remove`  | `DELETE /channels/{id}/members/{userID}`     |
| `admin.policy.antispam.update` | `PATCH /policies/anti-spam/{workspaceID}`    |
| `admin.policy.upload.update`   | `PATCH /policies/upload/{workspaceID}`       |
| `admin.config.update`          | `POST /config/apply`                         |
| `admin.config.rollback`        | `POST /config/versions/{versionID}/rollback` |
| `admin.authorization.deny`     | Any capability refusal on any guarded route  |

Refusals are recorded too, and `result` distinguishes them: `denied` is the
platform saying no, `error` is the platform breaking. Collapsing the two would
make an attack look like an outage.

Metadata is an allowlist by construction — a `map[string]string` each producer
builds field by field from server-derived values. For a policy change it carries
the field, the unit, the previous value, the new value and the permitted range;
for a status change, the previous status and how many sessions the transition
closed; for a membership change, how many targets were requested, how many
were added, how many were already members, and — for a removal — whether
anything was actually removed, so the trail never claims a removal that did not
happen. No token, header, cookie, password, client secret or message content is
reachable from any producer.

---

## 6. Configuration (issue #580)

The platform configuration catalogue: what exists, where each value comes from,
and which part of it this API can change.

Five routes, each one method and one capability:

| Method | Path                                                      | Capability            |
| ------ | --------------------------------------------------------- | --------------------- |
| GET    | `/api/admin/config`                                       | `admin.config.read`   |
| POST   | `/api/admin/config/preview`                               | `admin.config.read`   |
| POST   | `/api/admin/config/apply`                                 | `admin.config.manage` |
| GET    | `/api/admin/config/versions`                              | `admin.config.read`   |
| POST   | `/api/admin/config/versions/{versionID}/rollback/preview` | `admin.config.read`   |
| POST   | `/api/admin/config/versions/{versionID}/rollback`         | `admin.config.manage` |

There is deliberately no `PATCH /config/{key}`. A key travels in a body and is
resolved against a server-side registry
(`internal/domain/config_catalog.go`); a key the registry does not declare is a
`400`, and one it declares as read-only is a `400` as well. Nothing in a request
names a service, an environment variable, a Secret, a file path, a namespace or
a Kubernetes resource.

### What is editable, and what is not

Every setting carries an explicit class. The full inventory, including the
settings this API does not expose, is
[`docs/security/config-inventory.md`](../security/config-inventory.md).

| Class | Meaning                        | Editable       |
| ----- | ------------------------------ | -------------- |
| A     | Runtime                        | **yes**        |
| B     | Runtime with a credential      | does not exist |
| C     | Read at boot; needs a rollout  | no             |
| D     | Infrastructure and credentials | no             |

Class A is exactly `auth.auth_policy_settings`, the single row auth-service
reads on the request that enforces it, so **persisting is applying**: there is
no rollout to follow, no "applying" state and no apply/rollback state machine —
a setting that needed one would not be editable at all.

There is no class B, because NChat has no secret backend this API can write.
Credentials arrive as environment variables from Sealed Secrets and rotate
through [`sealed-secrets-rotation.md`](../runbooks/sealed-secrets-rotation.md).

### Credentials

A stored credential is **never** returned. `GET /config` reports a sensitive
setting as a status and omits `value` entirely:

```json
{
  "key": "secret.smtp_password",
  "sensitive": true,
  "editable": false,
  "observable": true,
  "configured": true,
  "read_only_reason": "Credencial em Sealed Secret; a rotacao segue docs/runbooks/sealed-secrets-rotation.md."
}
```

`observable: false` means this pod does not receive the variable at all — a
Secret scoped to another workload — which is a different fact from
`configured: false` and is reported as such.

Because no sensitive setting is editable, none can appear in a change set, in a
diff or in the version history. The history table enforces that structurally:
`value_from` and `value_to` are JSONB constrained by CHECK to `number`,
`boolean` or `null`, so a string cannot be stored there at all.

### `GET /api/admin/config`

```json
{
  "data": {
    "documents": [{ "key": "auth.policy", "revision": 3 }],
    "settings": [
      {
        "key": "auth.password.min_length",
        "label": "Tamanho minimo da senha",
        "description": "…",
        "category": "authentication",
        "owner_service": "auth-service",
        "class": "A",
        "source": "database",
        "apply": "runtime",
        "type": "int",
        "unit": "caracteres",
        "min": 8,
        "max": 128,
        "nullable": false,
        "default": 12,
        "editable": true,
        "sensitive": false,
        "document": "auth.policy",
        "manage_capability": "admin.config.manage",
        "danger_note": "…",
        "rollbackable": true,
        "observable": true,
        "value": 12
      }
    ]
  }
}
```

`revision` is the concurrency token. It is echoed back on every write.

### `POST /api/admin/config/preview`

```json
{
  "document": "auth.policy",
  "expected_revision": 3,
  "changes": { "auth.password.min_length": 16 }
}
```

Writes nothing and answers `200` with the plan the server would act on:

```json
{
  "data": {
    "plan": {
      "document": "auth.policy",
      "revision": 3,
      "stale": false,
      "changes": [
        {
          "key": "auth.password.min_length",
          "label": "Tamanho minimo da senha",
          "category": "authentication",
          "owner_service": "auth-service",
          "apply": "runtime",
          "unit": "caracteres",
          "dangerous": false,
          "from": 12,
          "to": 16
        }
      ],
      "dangerous": false,
      "required_capability": "admin.config.manage",
      "authorized": true,
      "reason_required": false,
      "warnings": [],
      "errors": [],
      "affected_services": ["auth-service"],
      "apply": "runtime"
    }
  }
}
```

Field-level validation messages live here, in `errors`, and every invalid field
is reported at once. `stale: true` means the document moved since the form was
loaded. Values equal to what is stored are dropped from `changes`, so a form
that submits every field it rendered produces a diff of what actually changed.

### `POST /api/admin/config/apply`

Same body as the preview, plus an optional `reason`. Requires
`admin.config.manage`, and **additionally `admin.superuser`** when any resulting
value is dangerous — a change that weakens authentication is a change to who can
reach the platform. A dangerous change also requires a non-empty `reason`.

The write is a compare-and-swap:

```sql
UPDATE auth.auth_policy_settings
SET … , revision = revision + 1
WHERE id = 1 AND revision = $expected
```

so the check and the write are one statement and one snapshot. Two
administrators saving at once produce one write and one `409`; there is no
last-write-wins path and nothing is merged.

| Status | Meaning                                                               |
| ------ | --------------------------------------------------------------------- |
| `200`  | Applied, or nothing to apply.                                         |
| `400`  | Unknown key, read-only key, wrong type, out of range, missing reason. |
| `403`  | The capability the resulting value demands is not held.               |
| `409`  | The document moved since the form was loaded. Nothing was written.    |

Failures use the platform error envelope and carry no plan: the detailed,
per-field answer is what the preview endpoint exists to produce.

```json
{
  "data": {
    "applied": true,
    "document": "auth.policy",
    "revision": 4,
    "values": { "auth.password.min_length": 16 },
    "plan": { "…": "the plan that was applied" },
    "version": { "id": "8", "revision": 4, "reverts_revision": 0, "…": "…" }
  }
}
```

`applied: false` with `200` is the idempotent case: the requested values are
already the stored values, so nothing is written, no version is recorded and no
audit event is raised. A resubmitted form lands here instead of creating a
second version that changed nothing.

`values` and `revision` describe the row the write itself returned, not a
re-read of it. Once the transaction commits, the response, the audit event and
the recorded version all describe that commit; nothing that happens afterwards
can turn `applied` back to false or lose the version id. A client told a
mutation failed would send it again, so a committed change is never reported as
one that did not happen.

### `GET /api/admin/config/versions`

`?document=auth.policy&limit=25`. Newest first, limit clamped to 100. An unknown
document is a `400`, never an empty list.

Each version records the revision it produced, the actor, the correlation id,
the operator's stated reason, the fields it changed with their previous and new
values, and `reverts_revision` when it was a rollback. `rollbackable` says
whether the platform can still undo it.

### `POST /api/admin/config/versions/{versionID}/rollback/preview`

```json
{ "expected_revision": 12 }
```

Writes nothing, and answers `200` with the same `plan` shape the ordinary
preview returns.

A rollback has its own preview because it is not an edit that happens to carry
old values. The request names **only** the version and the revision this client
last read; the values to restore, the preconditions, the eligibility and the
verdict are all derived server-side from the recorded version, by the same code
the confirmed rollback runs. A client does not send `changes`, `from`, `to`,
preconditions or `superseded` — a body carrying any of them is a `400`.

The diff describes the version's own transition (`version.To -> version.From`),
not a diff against whatever the document holds now. While the version is still
in force the two are identical; when it is not, describing it against the
current value would present a different operation than the one requested.

### `POST /api/admin/config/versions/{versionID}/rollback`

```json
{ "expected_revision": 4, "reason": "reverter" }
```

Restores the values the named version replaced, as a **new** version that names
the one it reverts. The history is append-only: an apply/rollback sequence reads
as three changes rather than as a change that vanished.

A rollback is refused when the previous value is no longer acceptable under
today's registry, when a key no longer exists, or when the resulting value is
dangerous and the actor does not hold `admin.superuser`. Undoing a hardening is
producing a weakening, and is judged as one.

**A superseded version cannot be reverted.** `plan.superseded` is `true` when at
least one field of the target version no longer holds the value that version
set. The preview reports it in advance, the console shows why and refuses to
offer the confirmation, and the apply revalidates it atomically regardless — a
preview is informative and never an authorization to write, because the document
can move between the two requests.

Every field the version changed must still hold the value that version set. Reverting `10 -> 20` after somebody
has since moved the value to `30` would discard their change, and the revision
cannot catch it — the console loaded _after_ they wrote, so its revision is
current. The write asserts both, in the same statement:

```sql
UPDATE auth.auth_policy_settings
SET … , revision = revision + 1
WHERE id = 1
  AND revision = $expected
  AND max_devices_per_user IS NOT DISTINCT FROM $precondition
```

so the check and the write are one step with no window between them. A
superseded rollback matches no row, writes nothing, records no version and
answers `409`.

`plan.superseded` and `plan.stale` are different fields because they are
different facts with different remedies:

| Field        | Means                                                 | Remedy                        |
| ------------ | ----------------------------------------------------- | ----------------------------- |
| `stale`      | The document moved since **this client** read it.     | Reload and review again.      |
| `superseded` | The **target version** is no longer the one in force. | Revert a more recent version. |

A superseded rollback usually has `stale: false`: the console loaded _after_ the
change that superseded the version, so its revision is perfectly current. That
is precisely why optimistic locking alone cannot catch it.

Rollback is all or nothing. If one field of the version has moved on, the whole
rollback is refused rather than restoring the fields that still match.

### Audit

`admin.config.update` and `admin.config.rollback` carry, in metadata built field
by field: the document, both revisions, the version id, the changed keys with
their `from -> to`, whether the change was dangerous, the capability it
required, the validation outcome, and whether a reason was given. The reason
_text_ is deliberately not copied there — it is operator prose, it is already
persisted on the version row, and the version id is how the two are joined.

---

## 7. Observability — Dashboard and Health Center (issue #581)

| Method | Path                         | Capability                  |
| ------ | ---------------------------- | --------------------------- |
| GET    | `/api/admin/overview`        | `admin.infrastructure.read` |
| GET    | `/api/admin/health/services` | `admin.infrastructure.read` |
| POST   | `/api/admin/health/refresh`  | `admin.infrastructure.read` |

No new capability was introduced. `admin.infrastructure.read` already exists in
the platform, in the `CHECK` on `auth.admin_role_capabilities` (migration 000008) and in the console's navigation map, where it has named the Health
Center and the system section since the foundation issue. The refresh is a
`POST`, so it also passes the origin and CSRF guards like every other non-safe
method.

Reads are guarded exactly like writes here, for the same reason the audit trail
and the configuration catalogue are: the dashboard reports how many people are
signed in and how much traffic the platform carries, and the Health Center names
every dependency the deployment has. Both are reconnaissance for anyone who
should not be holding this session.

### What these endpoints do not accept

**No request to this surface names a destination.** There is no field, no query
parameter and no body anywhere in it that carries a URL, a hostname, an IP
address, a port, a DSN, a namespace or a path, and there is no
`/health/{service}` route. The refresh takes no body at all.

The listing accepts exactly one optional parameter, `?service=<id>`, and it is
resolved against a compile-time registry
(`services/admin-service/internal/domain/health.go`) before anything reads it:
an identifier the platform does not declare is a `400`, never a filter that
matches nothing and never — under any circumstance — an address. The addresses
the service is willing to contact come from the pod's own environment, resolved
at collection time from the ConfigMap and Secrets the deployment chose to mount.

That is what keeps a detailed health check from becoming an SSRF primitive, and
it is a property of the design rather than of a validator: there is no code path
from an HTTP request to a dial target.

Two further rules hold on the outbound side: TLS verification is never relaxed
(there is no `InsecureSkipVerify` in the service), and redirects are never
followed, so a dependency that answers `302` does not get to nominate a second
address for the pod to connect to.

### `GET /api/admin/overview`

The whole dashboard in one request. There is deliberately no per-card endpoint.

```json
{
  "data": {
    "summary": {
      "collected_at": "2026-08-22T12:00:00Z",
      "overall": "degraded",
      "state_counts": {
        "healthy": 5,
        "degraded": 1,
        "unavailable": 1,
        "disabled": 2,
        "unknown": 1
      },
      "metrics": [
        {
          "key": "users.active_now",
          "label": "Usuários ativos agora",
          "definition": "Contas distintas com ao menos uma sessão de chat viva…",
          "window": "instant",
          "unit": "count",
          "value": 3,
          "available": true
        }
      ],
      "metrics_available": true,
      "alerts": [
        {
          "service_id": "livekit",
          "severity": "warning",
          "title": "LiveKit indisponível",
          "impact": "Servidor de mídia das chamadas…",
          "action": "Verifique se a dependência está de pé…",
          "since": "2026-08-22T12:00:00Z",
          "runbook_path": "docs/runbooks/task-livekit-coturn-dev.md",
          "config_key": "calls.livekit.enabled"
        }
      ]
    }
  }
}
```

`collected_at` describes the **data**, not the request: it is when the health
snapshot was taken, which is what the console shows as "última atualização".

**`value` is absent when `available` is false.** A counter the aggregate could
not produce is never sent as `0`: "nothing happened" and "we could not find out"
look identical on a card and mean opposite things. `metrics_available` is a
single flag for the whole set because the counters come from one query — either
it ran or it did not.

Every metric carries its own `definition` and `window`. The full table, with the
source and cost of each counter, is in
[`../runbooks/task-admin-observability.md`](../runbooks/task-admin-observability.md).

The counters are aggregates only. No identifier, no message body, no filename,
no e-mail address and no URL is selected by the query that produces them, so the
endpoint reveals volume and never content.

A failing counter query does **not** fail the request: the summary is returned
with `metrics_available: false`, because the health section is what would tell
an operator that the database is the problem. A failing health collection _does_
fail the request, since without it there is no overall state, no counters by
state and no alerts.

### `GET /api/admin/health/services`

```json
{
  "data": {
    "collected_at": "2026-08-22T12:00:00Z",
    "overall": "degraded",
    "services": [
      {
        "id": "livekit",
        "display_name": "LiveKit",
        "category": "realtime",
        "impact": "Servidor de mídia das chamadas…",
        "state": "unavailable",
        "enabled": true,
        "observable": true,
        "critical": false,
        "latency_ms": 3001,
        "checked_at": "2026-08-22T12:00:00Z",
        "error_category": "connection_timeout",
        "detail": "A dependência não respondeu dentro do tempo limite do check.",
        "runbook_path": "docs/runbooks/task-livekit-coturn-dev.md",
        "config_key": "calls.livekit.enabled"
      }
    ]
  }
}
```

Rows arrive ordered by how much attention each demands. The console re-sorts and
filters locally — the payload is a dozen rows, so a round trip to hide three of
them would add a parameter and save nothing.

`latency_ms` is **absent** when no round trip was measured. It is never `0` as a
stand-in: a disabled integration has no latency, and reporting one would claim a
check that did not happen. `checked_at` is always present.

### The five states

| State         | Means                                                           |
| ------------- | --------------------------------------------------------------- |
| `healthy`     | The check ran and the dependency answered correctly.            |
| `degraded`    | It answered, and something about the answer warrants attention. |
| `unavailable` | The check ran and it did not answer.                            |
| `disabled`    | The deployment switched the integration off. **Not** a failure. |
| `unknown`     | No check ran, so the platform knows nothing. **Not** healthy.   |

Three invariants the API upholds and the tests pin:

- **`configured` is not `healthy`.** A dependency with complete configuration
  and no reachable process comes back `unavailable`. The state is produced by a
  connection attempt, never by the presence of a value.
- **`unknown` is never `healthy`.** It is what `enabled: true, observable:
false` produces, and the overall state degrades to `degraded` when any row is
  unknown rather than staying `healthy`.
- **`disabled` is not `unavailable`.** It raises no alert and contributes
  nothing to the overall state.

`enabled` and `observable` are two different facts, reported alongside the state
and never instead of it:

| `enabled` | `observable` | State                   | Why                                                       |
| --------- | ------------ | ----------------------- | --------------------------------------------------------- |
| `false`   | —            | `disabled`              | The deployment turned it off.                             |
| `true`    | `false`      | `unknown`               | This pod does not receive the config naming the endpoint. |
| `true`    | `true`       | the result of the check | It was really contacted.                                  |

The second row is not a defect: `admin-service` mounts the shared ConfigMap and
only the two Secrets it needs, so a target scoped to another workload is
genuinely invisible from here. That is the same `observable` / `configured`
distinction issue #580 established in the configuration catalogue, and reporting
it as `unknown` is the honest answer.

### `error_category`

A closed set. **No library error text, driver message, remote response body or
stack trace ever reaches a client** — a failure is classified into one of these
and the original is discarded, and `detail` is a hand-written sentence, not
something a dependency produced.

| Category                 | Means                                                    |
| ------------------------ | -------------------------------------------------------- |
| `connection_timeout`     | It did not answer in time.                               |
| `authentication_failed`  | It answered and refused the credential this pod holds.   |
| `tls_error`              | The transport could not be secured. Never worked around. |
| `dependency_unavailable` | Connection refused or reset, or a server error.          |
| `invalid_configuration`  | The deployment describes something unusable.             |
| `capacity_warning`       | It answered close to a limit, or well past its budget.   |
| `not_observable`         | No check ran; this pod cannot see the target.            |
| `protocol_error`         | It answered something this build cannot interpret.       |

The only field carrying text a dependency produced is `version`, and it passes a
character allowlist and a length cap first.

### `POST /api/admin/health/refresh`

No body. Answers with the same payload as the listing, from a freshly forced
collection.

Abuse is bounded server-side rather than by the button being disabled:

- concurrent requests share **one** in-flight collection;
- a forced collection below a minimum interval returns the current snapshot
  instead of recollecting;
- an ordinary read is served from a short-lived cached snapshot.

So a held-down refresh, or a dashboard open in ten tabs, costs one collection per
interval regardless of how many requests arrive.

### Checks, timeouts and isolation

Each dependency has its own short timeout — there is no single large budget for
the whole collection, because one stuck dependency would then consume everyone
else's. Concurrency is bounded, and the collection runs under a context detached
from the request that started it, so a browser tab that closes cannot cancel a
computation other waiters are sharing.

A probe that fails, times out or panics becomes **that service's row**. Nothing
propagates: the response is still `200` describing what was learned.

What the probes deliberately do not do: no real user login against the identity
provider, no e-mail sent through the relay, no persistent LiveKit room, no EICAR
sample written to the antimalware daemon, no listing of stored objects, and no
call that would spend a third party's quota.

### Errors

| Status | Meaning                                                               |
| ------ | --------------------------------------------------------------------- |
| `200`  | Collected. Individual dependencies may be in any of the five states.  |
| `400`  | `?service=` names an identifier the registry does not declare.        |
| `401`  | No live administrative session.                                       |
| `403`  | Missing `admin.infrastructure.read`, or a rejected origin/CSRF token. |
| `503`  | The observability surface is not wired on this pod.                   |

### Metrics exported by this surface

`nchat_admin_health_check_duration_seconds{service}`,
`nchat_admin_health_check_results_total{service,state}`,
`nchat_admin_health_cache_events_total{result}` and
`nchat_admin_dashboard_build_duration_seconds{outcome}`.

Every label value comes from a closed set declared in code — a registry service
id, one of the five states — so the series count is bounded by the size of two
literals. No user id, e-mail, URL, request id, channel id or file id appears in
a label. They are registered into the shared registry, so they are exported only
when `PROMETHEUS_METRICS_ENABLED` is set, exactly like the rest of the platform's.

---

## 8. Integrations — configuration and diagnostics (issue #582)

Three endpoints. They answer the two questions an operator has about an
integration that is not working: what does the platform currently know about it,
and what happens when we actually try.

The first question is answered from the passive collection of
[issue #581](#7-observability--dashboard-and-health-center-issue-581) — opening
the screen contacts nothing. The second is the only place in admin-service where
an operator's click causes an outbound connection, and everything below is about
bounding that.

### What these endpoints do not accept

No URL. No hostname. No IP address. No port. No credential. No namespace. No
Kubernetes resource. No file path. No SMTP recipient.

`GET /api/admin/integrations` takes no parameter at all. The diagnostic takes one
path segment, resolved against the compile-time registry in
`services/admin-service/internal/domain/integration.go` before anything reads it.
The test message takes an empty body.

That is what keeps "Testar configuração" from being a proxy: the set of things
this pod is willing to contact comes from its own environment, and a caller can
only choose **which** of them to contact, never **what** they are.

### Where the configuration lives

This surface writes nothing. Every setting it shows belongs to the registry of
issue #580 and is class C or D — a value in the Git-managed ConfigMap or a
credential in a Sealed Secret — so changing one is a commit and a rollout, or the
rotation runbook. There is no "substituir secret" field, because there is no
endpoint that would accept one; see
[`config-inventory.md`](../security/config-inventory.md) for why class B does
not exist.

`ValidateIntegrationRegistry` asserts that every key an integration claims is a
key the configuration registry already declares, so this screen cannot grow a
second configuration model with keys of its own.

### The integrations, and what can be checked

| Integration | Diagnostic | Stages                                                |
| ----------- | ---------- | ----------------------------------------------------- |
| `oidc`      | yes        | DNS, TCP, TLS, discovery, issuer, JWKS, credential¹   |
| `smtp`      | yes        | DNS, TCP, TLS, credential, ready — plus delivery²     |
| `livekit`   | yes        | DNS, TCP, TLS, credential, ready                      |
| `clamav`    | yes        | DNS, TCP, ready (`PING`/`VERSION`)                    |
| `storage`   | yes        | DNS, TCP, TLS, ready                                  |
| `turn`      | **no**     | no platform variable names a TURN server³             |
| `link_scan` | **no**     | the provider credential is scoped to other workloads³ |

¹ The client stage is reported as **skipped**, with the reason: verifying a
client without performing a real authentication is not something the protocol
offers, and a diagnostic that signed somebody in would put a login event in the
identity provider's trail on every click.

² Only when the test message endpoint was called. An ordinary diagnostic never
delivers anything.

³ Reported with the reason shown verbatim in the console, not as a missing
button. Inventing a target is the one thing this surface exists to prevent.

### `GET /api/admin/integrations`

Requires `admin.integrations.read`. Guarded like a mutation, one capability
weaker, for the same reason the configuration catalogue is: the list names every
integration the deployment has and whether each is reachable.

The `settings` array is the configuration projection of issue #580, with an
`advanced` flag. It is present only for an actor who **also** holds
`admin.config.read`; otherwise `settings_visible` is `false` and the array is
empty. "You may not see them" and "there are none" are different sentences on the
screen, so they are different fields on the wire.

A credential arrives exactly as it does on `/config`: `configured: true|false`,
no `value`, no `default`.

### `POST /api/admin/integrations/{integrationID}/diagnose`

Requires `admin.integrations.manage` — the manage capability and not the read
one, even though nothing is written. A diagnostic makes this pod open outbound
connections and sign a LiveKit credential; that is an action with a cost, and the
capability that authorizes it should be one an operator grants deliberately.

No body, and that is enforced rather than merely documented: a request carrying
any content at all is refused with `400` before the integration is resolved and
before anything is dialled. The rule is the absence of a body and not the absence
of meaningful JSON, so `{}` is refused alongside `{"unexpected":"payload"}` —
neither endpoint has a field a caller could send, and accepting one silently
would leave a future field free to arrive from a client nobody reviewed.

Being a POST, it passes the origin and CSRF guards like every other non-safe
method.

**A failed check answers `200` with the report.** "O relay recusou a credencial"
is the result the operator asked for, not a server fault, and a `502` would lose
every stage that did pass.

```json
{
  "data": {
    "report": {
      "integration": "oidc",
      "started_at": "2026-08-23T11:05:00Z",
      "status": "failed",
      "summary": "Ao menos uma etapa falhou. As etapas seguintes não foram executadas.",
      "steps": [
        { "stage": "resolve", "status": "passed", "latency_ms": 3 },
        { "stage": "connect", "status": "passed", "latency_ms": 8 },
        {
          "stage": "tls",
          "status": "failed",
          "category": "tls_error",
          "detail": "Não foi possível estabelecer TLS com a dependência.",
          "latency_ms": 41
        },
        {
          "stage": "jwks",
          "status": "skipped",
          "detail": "Não executada porque uma etapa anterior falhou."
        }
      ]
    }
  }
}
```

`status` is one of `passed`, `warning`, `failed`, `skipped`. A stage that did not
run is `skipped` and carries no `latency_ms` — it was not measured, and zero
would read as instantaneous. `category` is the same closed set the Health Center
uses (`error_category` above).

Nothing a dependency said is in that payload. Bodies are drained or read under a
byte cap and reduced to known fields; errors are classified by type and the
original text is dropped; a version string passes a character allowlist and a
length cap. A caller learns `tls_error`, never a certificate chain, an internal
hostname or a stack trace.

### `POST /api/admin/integrations/smtp/test-email`

Requires `admin.integrations.manage`, passes CSRF, and accepts **no body** —
enforced exactly as on the diagnostic above, and refused with `400` before the
relay is contacted.

The destination is the authenticated administrator's own address, read from the
session principal. There is no recipient field to send, so there is nothing for a
stolen session to aim: the worst it can do is mail the victim. That single
decision is the whole anti-relay control, and it is why no allowlist of
destinations had to be invented.

The message is fixed, marked `Auto-Submitted: auto-generated`, and carries no
platform data. It answers with the same staged report, ending in a `delivery`
stage.

### Network policy

Every diagnostic connection goes through a dialer whose `Control` hook inspects
**the address the kernel is about to connect to**, after resolution and
immediately before `connect(2)`. Checking there rather than resolving the name
separately is what closes the window DNS rebinding depends on.

Refused for every integration, with no opt-out:

- link-local (`169.254.0.0/16`, `fe80::/10`) — the cloud metadata range, which is
  `169.254.169.254` on AWS and Azure and what `metadata.google.internal` resolves
  to on GCP;
- the unspecified address, multicast and broadcast.

**Not** refused: RFC 1918, unique-local and loopback. Every NChat dependency is a
cluster service, so a blanket "no private addresses" rule would break the real
deployment while doing nothing about the range that matters. The policy is
declared per integration in the registry, and what differs between them is the
protocol and the accepted schemes.

Also enforced, on every integration:

- `http` and `https` only for a URL-shaped target; a bare `host:port` for the
  others. A ConfigMap holding `file://` or `gopher://` produces a configuration
  error, not a request;
- a URL carrying credentials is refused rather than stripped;
- **no redirect is ever followed**. A dependency that answers `302` is reported as
  reachable and nothing more;
- for OIDC, the `jwks_uri` the provider returns must share the issuer's scheme,
  host **and port**. Two services on one host are two origins, and a provider
  that could nominate either would be choosing an address this deployment never
  configured;
- TLS verification is never relaxed. There is no `InsecureSkipVerify` in the
  package and no setting that could introduce one.

### Rate limits and concurrency

| What                   | Budget                                                     |
| ---------------------- | ---------------------------------------------------------- |
| Diagnostic             | 6 per minute, burst 3, per **administrator × integration** |
| SMTP test message      | 1 per minute, no burst, per administrator                  |
| Concurrent diagnostics | 2 per pod, refused rather than queued                      |
| Whole run              | 20 s ceiling; each stage has its own short timeout         |

Per administrator and integration rather than per IP: two operators debugging one
outage must not throttle each other, and one operator holding a button must not
turn the console into a scanner. Exceeding a budget is `429` with `Retry-After`.

A diagnostic is cancelled with the request that asked for it — the opposite of the
health collection, which is shared and therefore detached. Navigating away stops
the outbound work.

### Errors

| Status | Meaning                                                                   |
| ------ | ------------------------------------------------------------------------- |
| `200`  | Ran. The report may describe a failure; that is the answer, not an error. |
| `400`  | The request carried a body. Both POSTs take none; see below.              |
| `401`  | No live administrative session.                                           |
| `403`  | Missing the capability, or a rejected origin/CSRF token.                  |
| `404`  | The path names an integration the registry does not declare.              |
| `409`  | The integration is declared and has no diagnostic on this deployment.     |
| `429`  | Over the rate limit, or the pod is already running two diagnostics.       |
| `503`  | The integration surface is not wired on this pod.                         |

### Audit

Every diagnostic and every test message writes one row, whether it succeeded,
was refused or failed.

- action: `admin.integration.diagnose` or `admin.integration.smtp.test_email`;
- resource: `admin.integration:<id>`;
- metadata, by allowlist: `integration`, `outcome`, and `failed_stage` when one
  failed.

No target, no response, no credential and no recipient reaches the trail — the
recipient in particular is redundant, since the actor column already identifies
whose mailbox it was.
