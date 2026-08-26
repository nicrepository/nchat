# Runbook — Production Blue & Green deployment

Issue #626. This is the operational procedure for releasing NChat to production.
Every command below exists in the repository and is the one an operator actually
runs; nothing here is illustrative.

Scope note: this runbook describes infrastructure **defined in this repository**.
It does not assert that a production cluster has been provisioned, that DNS
resolves, or that any of it has been launched. The prerequisites section says
what must exist first, and none of it is created by these scripts.

---

## 1. Architecture

```text
nchat-prod                      one namespace, one logical environment
├── shared (deployed once)
│   ├── nchat-config            one ConfigMap, read by both slots
│   ├── ServiceAccounts, quota, LimitRange, NetworkPolicies
│   ├── stable Services         auth-service, chat-service, nchat-web, …
│   ├── Ingress + Traefik middlewares + TLS certificate
│   ├── clamav, upload-guard    shared, never duplicated per slot
│   └── preview Ingresses       blue.preview.<host>, green.preview.<host>
├── blue    ── nine Deployments + nine per-slot Services + eight PDBs
└── green   ── the same, independently deployable
```

**Blue and Green are release slots, not environments.** They share one database,
one Valkey, one object store, one Keycloak, one LiveKit and one ClamAV. The only
thing that legitimately differs between them is the release image, which
`prod-blue-green-check` asserts.

A stable Service keeps its name and its Ingress backend forever. A release moves
one field:

```yaml
selector:
  app.kubernetes.io/component: chat
  nchat.io/release-slot: blue # → green at cutover
```

That is why Traefik needs no reconfiguration per release and why rollback is a
selector write rather than a redeploy.

---

## 2. Shared dependencies

| Dependency                 | Shared | Notes                                                                                                                             |
| -------------------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------- |
| PostgreSQL                 | yes    | one schema, both slots. Migrations must stay compatible with the previous slot.                                                   |
| Valkey                     | yes    | `VALKEY_WS_BROADCAST_ENABLED=true` — without it chat-service falls back to pod-local presence and messages do not cross replicas. |
| Object storage (SeaweedFS) | yes    | an attachment uploaded on Blue must be readable on Green.                                                                         |
| Keycloak / OIDC            | yes    | one client. Sessions survive cutover because nothing session-related is pod-local.                                                |
| LiveKit / TURN             | yes    | deliberately **not** part of the rotation.                                                                                        |
| ClamAV                     | yes    | one scanner, `clamav:3310`.                                                                                                       |
| auth-service avatar PVC    | yes    | ReadWriteOnce — see "Known limitation — auth-service".                                                                            |

---

## 3. Prerequisites

None of these are created by the scripts. `bootstrap.sh` checks for them and
stops with the exact missing name.

- **kube context** `nchat-prod-deployer` (override with `NCHAT_PROD_CONTEXT`).
  Every script refuses an unexpected context.
- **Namespace** `nchat-prod` must exist.
- **Secrets**, provisioned via `docs/runbooks/sealed-secrets-rotation.md`:
  `nchat-secrets`, `nchat-postgres-runtime`, `nchat-postgres-migrator`,
  `nchat-file-encryption`, `ghcr-pull`.
  `nchat-postgres-migrator` is required by `infra/k8s/base/migrations/job.yaml`;
  without it the migration Job fails after the namespace is already half set up.
- **Stateful layer** publishing the Services `postgres`, `valkey`,
  `seaweedfs-filer`.
- **Topology file** (not committed), passed as `NCHAT_PROD_TOPOLOGY_FILE`:

  ```ini
  NCHAT_PROD_HOST=nchat.nic-labs.com
  NCHAT_PROD_PUBLIC_URL=https://nchat.nic-labs.com
  NCHAT_PROD_PREVIEW_ALLOW_CIDR=<operator network>/24
  NCHAT_PROD_LIVEKIT_CONNECT_SRC=wss://<livekit host> https://<livekit host>
  ```

  The committed `topology.env` holds `REPLACE_ME_*` placeholders. A manifest
  still carrying one is refused before `kubectl` runs.

- **DNS and TLS** for the stable host, the `admin.` host and both preview hosts.
  The `Certificate` resource assumes a `letsencrypt-prod` ClusterIssuer; **this
  branch does not install cert-manager**.
- **Keycloak** with the production callback plus both preview callbacks as
  additional valid redirect URIs.
- **Database backup taken and a restore rehearsed.** Application rollback does
  not undo a migration — see "A migration that has to be undone".
- **Release images** built and pushed, with `artifacts/digest-<image>.txt` for
  all ten images (nine services plus `migrations`).

---

## 4. First production — bootstrap

Blue is the baseline by definition; Green is the next candidate and is not
deployed.

```bash
NCHAT_PROD_RELEASE_SHA=<40-hex commit sha> \
NCHAT_PROD_TOPOLOGY_FILE=/secure/path/topology.env \
ARTIFACTS_DIR=./artifacts \
make prod-blue-green-bootstrap
```

It validates the context, the namespace, every required Secret and the stateful
layer **before** applying anything; then applies the shared half, runs the
migration Job once, deploys Blue and waits for every workload.

The stable Services render selecting `blue`, so Blue is live the moment the
shared half is applied — which is why bootstrap ends by telling you **not** to
publish the address until the baseline smoke and the authenticated smoke below
are complete.

The full first-production sequence:

```bash
# 1. establish Blue
NCHAT_PROD_RELEASE_SHA=<sha> NCHAT_PROD_TOPOLOGY_FILE=... ARTIFACTS_DIR=./artifacts \
  make prod-blue-green-bootstrap

# 2. confirm what the cluster now holds
make prod-blue-green-status

# 3. automated baseline smoke — note --baseline
make prod-blue-green-smoke ARGS="--target blue --baseline"

# 4. complete the authenticated checklist it prints, on the preview hosts

# 5. only then publish the address
```

### Why `--baseline` exists, and when it does not apply

A candidate smoke refuses a slot that already holds production traffic: smoking
the live slot proves nothing about the release being validated. On the first
launch there is no previous slot to serve while Blue is checked, so Blue is both
the baseline and the thing under test, and that rule would make the launch
impossible to complete.

`--baseline` is accepted only when **all** of these hold:

- the target is `blue`, the baseline slot;
- every stable Service selects it, with no mixed state;
- Green has never been deployed.

Once Green exists the namespace is past its baseline and the flag stops being
available. It does not promote anything, it does not relax any other check, and
it is **not** used in normal releases — see "Automated smoke".

---

## 5. Identifying a release

Every workload of a slot is stamped with the same annotation:

```yaml
metadata:
  annotations:
    nchat.io/release-sha: <commit sha>
```

Images are pinned by digest (`image@sha256:…`). Mutable tags — `latest`, `main`,
`develop`, `stable`, `prod` — are rejected by `prod-blue-green-check`.

```bash
make prod-blue-green-status
```

reports each slot as `CONSISTENT <sha>`, `NOT DEPLOYED`, or `MIXED` with a
per-workload breakdown. A slot is only promotable when it is CONSISTENT.

---

## 6. Migrations

**Expand/contract.** Blue and Green share one schema, so an up migration may add
freely but must not take anything away from the release still running. The gate:

```bash
make migrations-check
```

It rejects `DROP TABLE/COLUMN/TYPE/CONSTRAINT/DEFAULT`, `RENAME
TO/COLUMN/CONSTRAINT/VALUE/ATTRIBUTE`, `SET NOT NULL` and column type changes
unless the file declares:

```sql
-- nchat:blue-green contract-phase <why this is safe now>
```

**Historical migrations are never edited.** The runner stores a SHA-256 of each
file (`scripts/db/migrate.sh`) and refuses to continue when it changes, so
annotating an applied migration would block every deployment that has already
run it. Migrations written before this policy are listed in
`scripts/ci/blue-green-migration-exceptions.txt` instead. That list is closed.

Not enforced: a `CREATE INDEX` that locks a large table. `CONCURRENTLY` cannot
run inside a transaction and every migration here must be transactional, so this
stays a review item — check it by hand for large tables.

**Execution is once per release**, as a Job named after that release
(`nchat-migrations-<first 12 characters of the release SHA>`, with the full SHA
in the `nchat.io/release-sha` annotation) and with
`backoffLimit: 0`. An init container would run once per replica and once per
slot. A failed migration blocks the release rather than being discovered later.

---

## 7. Deploying the candidate

```bash
NCHAT_PROD_RELEASE_SHA=<40-hex commit sha> \
NCHAT_PROD_TOPOLOGY_FILE=/secure/path/topology.env \
ARTIFACTS_DIR=./artifacts \
make prod-blue-green-deploy
```

In order: validate context and namespace → read the active slot from the cluster
→ pick the opposite as candidate → render and pin digests → capacity preflight →
run migrations → apply the candidate → wait for every workload to be Ready.

It does **not** cut over, and it never patches a Service.

### Capacity preflight

Compares what the candidate _adds_ against the namespace ResourceQuota (cpu,
memory, pods) and the cluster's allocatable capacity minus what is already
committed. Pods that are `Pending` but already bound to a node are counted —
they hold their reservation while pulling images or attaching volumes.
`Succeeded` and `Failed` pods do not.

How much a candidate adds depends on whether its slot already exists:

| Situation                                                                   | Additional demand                                     |
| --------------------------------------------------------------------------- | ----------------------------------------------------- |
| **First deploy of a slot** — the other slot is live, this one has never run | the whole slot: replicas × per-pod requests           |
| **Redeploy of an existing slot** — normal second and later cycles           | the rollout's peak, minus what the slot already holds |

For a redeploy the preflight reads the slot's **current** state from the
cluster — each Deployment's replica count _and_ its per-pod CPU and memory
requests — and models the worst instant of the rolling update:

- Kubernetes caps a Deployment's total pods at `replicas + maxSurge` while the
  replacement runs;
- the new ReplicaSet never exceeds `replicas`, the old one never exceeds what is
  already running;
- within those limits the peak is the arrangement holding as many of the
  expensive pods as they allow.

Additional demand is that peak minus what the slot already contributes, per
resource, never below zero.

Reading the current requests is what makes an increase correct. A release going
from 2 × 100m to 2 × 1000m with `maxSurge: 1` needs **+1900m**, not the +1000m
that costing one surge pod at the new price would suggest — its final state
alone is 1800m more than the cluster holds. A release that _lowers_ its requests
is not charged as though it were all new, and one that scales a slot **down**
adds nothing at all.

`maxSurge` is read from the manifest, as an integer or a percentage (rounded up,
as Kubernetes does).

### Dimensions checked

Four, each against both the namespace ResourceQuota and the cluster's
allocatable capacity minus what is already committed:

| Dimension                    | Source                                                                                                 |
| ---------------------------- | ------------------------------------------------------------------------------------------------------ |
| pods                         | `spec.replicas` per workload, against the namespace quota **and** the nodes' `status.allocatable.pods` |
| `requests.cpu`               | summed per pod across containers and init containers                                                   |
| `requests.memory`            | same                                                                                                   |
| `requests.ephemeral-storage` | same — file-service requests 64Mi per pod, so a second slot needs its own                              |

Pods are checked twice on purpose. A namespace quota with room to spare says
nothing about whether the nodes still have slots: the kubelet caps each node at
`status.allocatable.pods`, and a cluster can be full while the quota is not.
Both conditions must pass.

The ResourceQuota in the overlay declares `requests.ephemeral-storage` (8Gi),
derived from what the manifests actually request at the rollout peak — see the
comment in `shared/quota-patch.yaml`. It has to be declared: the preflight
reports an undeclared dimension `INCONCLUSIVE`, and that blocks a deploy, so
omitting it would make every release wait on a hand-patched cluster.
`scripts/ci/prod-blue-green-check.sh` fails if it is missing, zero or a
placeholder.

A node that does not report `status.allocatable.ephemeral-storage` or
`status.allocatable.pods` is treated as unknown rather than as having no room.

What it does **not** guarantee: per-node fragmentation, taints, affinity,
anti-affinity, topology spread, PVC scheduling, preemption, image pull time or
dynamic node pressure. It reasons about **requests**, which is what the
scheduler reserves — not about the disk or memory a pod will actually use at
runtime, which no preflight can predict. It is a conservative check on the
deterministic dimensions, not a scheduler. The rollout remains the second and
final barrier.

If the cluster does not report a dimension it prints `INCONCLUSIVE` and the
deploy stops. Verify by hand and re-run with
`NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY=1`.

---

## 8. Preview

There are four preview hosts — chat and console, for each slot — all behind
the `preview-allowlist` Traefik
middleware restricted to `NCHAT_PROD_PREVIEW_ALLOW_CIDR`, both on the same TLS
certificate:

| Host                          | Reaches                                                                                                           |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `<slot>.preview.<host>`       | the chat surface of that slot (`chat-service-<slot>`, `nchat-web-<slot>`, …)                                      |
| `admin-<slot>.preview.<host>` | the administrative console of that slot (`nchat-admin-web-<slot>`, `admin-service-<slot>`, `auth-service-<slot>`) |

The administrative preview exists because `admin-web` and `admin-service` are
part of every release, and the stable admin host serves the **active** slot by
definition — so without it a candidate console could only be checked after the
cutover, which is where regressions would then be discovered.

Both route to per-slot Services, so neither can promote anything, and neither
carries normal traffic: `<host>` and `admin.<host>` keep doing that against the
stable Services. The console's own authentication is untouched — there is no
bypass for validation.

Keycloak must list all four preview callbacks as additional valid redirect URIs
on the production client — the two administrative hosts as much as the two chat
ones, since the console signs in through the same provider.

To disable previews:

```bash
kubectl delete ingress -n nchat-prod \
  nchat-prod-preview-blue nchat-prod-preview-green \
  nchat-prod-preview-admin-blue nchat-prod-preview-admin-green
```

The candidate stays deployable and smoke-testable from inside the cluster.

---

## 9. Automated smoke

```bash
# a normal release candidate — the slot must carry no production traffic
make prod-blue-green-smoke ARGS="--target green"
```

`--baseline` is only for the first launch ("Why `--baseline` exists"). Using it
on a release candidate is refused, because by then the other slot is deployed.

Checks: the slot carries no production traffic; it carries exactly one release;
every Deployment is fully Ready; `/healthz` and `/readyz` on all nine workloads;
and the configuration keys whose absence silently disables whole features
(`VALKEY_WS_BROADCAST_ENABLED`, `FILE_UPLOADS_ENABLED`, `LIVEKIT_ENABLED`,
`FILE_MALWARE_SCANNER_ADDRESS`, `NCHAT_WEB_LIVEKIT_CONNECT_SRC`).

It fails if the candidate carries more than one release, and it **never prints
"smoke passed"**. Its verdict is:

```text
Automated smoke            : PASS
Release validated          : <sha>
Authenticated release smoke: REQUIRED / NOT CONFIRMED BY THIS COMMAND
Cutover eligibility        : BLOCKED until the checklist above is recorded
```

---

## 10. Authenticated smoke (manual, required)

Not automated and not claimed to be: a shell against an in-cluster Service
cannot sign in through Keycloak, watch a message arrive for a second user,
upload past authorization, place a call, or reload a session.

Two accounts, two browser profiles, against the preview hosts — the chat surface
on `<slot>.preview.<host>` and the console on `admin-<slot>.preview.<host>`.
Record every result in the release ticket; unrecorded counts as failed.

The administrative checks are read-only by design: load the console, sign in,
render the overview, open one listing. Never use an administrative mutation as a
smoke. The command prints
the full checklist — sign-in, session across reload, sidebar, DM, cross-account
message delivery, WebSocket (not polling), reaction, public/private channel,
authorization refusal for a non-member, group, presence, upload/download/preview,
EICAR rejection, search, call, screen share, logout.

---

## 11. Cutover

```bash
NCHAT_PROD_SMOKE_CONFIRMED=green:<sha> \
  make prod-blue-green-cutover ARGS="--target green"
```

The target is **named, never derived**. Deriving it from the current state makes
a repeat run reverse the previous one instead of confirming it.

Gates, all before any mutation:

1. context and namespace;
2. the target is fully Ready;
3. the target carries exactly one release (Ready is not enough — a deploy that
   failed part-way leaves untouched workloads Ready on the _previous_ release);
4. the smoke evidence matches `<slot>:<release now on that slot>`, recomputed at
   this moment. Redeploy the candidate and the earlier evidence stops matching.

If the target is already fully active it reports a no-op and changes nothing.

**The cutover is sequential, not atomic.** Kubernetes has no transaction across
nine Services. Each is patched and read back, and the run stops at the first
that does not take, leaving a described partial state rather than an assumed
complete one. The window is a few hundred milliseconds of two slots serving at
once — which the release contract already requires to be safe, since both run
against one database and one event bus.

The old slot keeps running. It is the rollback.

---

## 12. Mixed state

```bash
make prod-blue-green-status
```

prints every Service with its own state and exits non-zero:

```text
web          -> green
auth         -> green
chat         -> blue        ← not converged
file         -> MISSING     ← the Service does not exist
search       -> UNSET       ← no release-slot selector
```

`MISSING`, `UNSET` and an unrecognised selector are three different problems and
none of them is "mixed". Converge by naming the destination:

```bash
NCHAT_PROD_SMOKE_CONFIRMED=green:<sha> \
  make prod-blue-green-cutover ARGS="--target green"
# or
make prod-blue-green-rollback ARGS="--target blue 'partial cutover'"
```

Re-running with the same target continues converging to that target.

---

## 13. Observation window

Keep the previous slot running and do not run `drain-old`. Minimum 30–60 minutes
of active observation; several hours for the first general release. Watch:

- HTTP 5xx and latency at the edge, per slot;
- login failures and OIDC callback errors;
- WebSocket connects and reconnect rate;
- message delivery between two accounts;
- upload and download;
- ClamAV scan outcomes;
- worker progress and lease churn;
- LiveKit token issuance and call setup;
- pod restart counts, readiness flapping, CPU/memory.

Slots are distinguishable by the label `nchat.io/release-slot` on every pod:

```bash
kubectl get pods -n nchat-prod -l nchat.io/release-slot=green -o wide
kubectl logs -n nchat-prod -l nchat.io/release-slot=green --all-containers --tail=200
```

---

## 14. Rollback

```bash
make prod-blue-green-rollback ARGS="--target blue 'reason recorded in the log'"
```

No build, no image, no migration — the same nine selectors move back. The reason
is mandatory and is recorded.

The target is named for the same reason as cutover, and here the cost of getting
it wrong is higher: a rollback that derived its destination would, on a second
run, send production back to the release it had just been rescued from. Running
it twice is a no-op.

If the named target is not Ready the command **stops**. It never picks a
different slot: moving traffic onto a slot that cannot serve it turns one
incident into two.

The failed slot is left running for investigation.

---

## 15. Rollback criteria

Roll back rather than fixing forward when any of these is observed and
attributable to the release:

- login broadly failing, or sessions not surviving the cutover;
- WebSocket handshake failing or clients not converging on the new slot;
- messages not delivered between accounts;
- uploads or downloads unavailable;
- a critical worker stopped or looping;
- authorization decisions wrong (a non-member reaching a private channel);
- calls not starting;
- any workload in CrashLoopBackOff;
- 5xx persistently elevated against the pre-cutover baseline;
- latency severely degraded;
- any sign of data inconsistency.

The project has no measured SLO baseline yet, so the numeric thresholds are
deliberately not invented here: record the pre-cutover rates during the
observation window and compare against those.

---

## 16. Retiring the old slot

Only after the observation window, and never automatically:

```bash
make prod-blue-green-drain-old ARGS="--target blue"
```

Refuses to scale down the slot serving traffic, and refuses while the active
slot is not fully Ready.

Scaling to zero rather than deleting is what makes WebSockets close through the
application's own path. On SIGTERM every pod runs the same sequence, under **one
45-second budget** that all of it shares:

1. a 5-second window in which the pod keeps serving, while the cluster finishes
   removing it from its Services' endpoints;
2. the HTTP server stops accepting and finishes in-flight requests;
3. concurrently — not afterwards — any background worker stops taking new work
   and finishes what it holds;
4. whatever remains of the budget is what the pod gets for closing its hub
   connections, database pool and tracing exporter.

The budget is deliberately shorter than `terminationGracePeriodSeconds: 60`, so
the kubelet's SIGKILL is not what ends the sequence. Every wait inside it is
bounded: a worker or hub goroutine that will not return is reported and the
process still exits.

Clients reconnect to the stable host, which already points at the new slot.

For notification-service specifically, the outbox worker claims **one** row at a
time under a lease that outlives the send plus the write recording it, so a pod
stopping mid-delivery finalises the message it holds rather than leaving it for
the lease to expire and be re-sent. When a backlog exists it claims the next row
immediately rather than waiting for the next poll; when the queue is empty it
waits. Delivery remains at-least-once — SMTP offers nothing stronger.

After this, rollback needs a redeploy — it is no longer instant.

---

## 17. Troubleshooting

| Symptom                             | What it means                                 | Action                                                                        |
| ----------------------------------- | --------------------------------------------- | ----------------------------------------------------------------------------- |
| `missing Secret: <name>`            | a prerequisite is absent; nothing was applied | provision it, re-run                                                          |
| migration Job fails                 | schema not advanced; release blocked          | find the Job by label and read its logs (below); fix forward, do not cut over |
| `slot X is MIXED`                   | a deploy reached only some workloads          | re-run deploy for that slot; do not promote                                   |
| `slot X is not deployed`            | no workloads exist                            | deploy the candidate first                                                    |
| candidate Ready but cutover blocked | release inconsistent, or evidence stale       | `status`, then re-smoke                                                       |
| smoke evidence rejected             | candidate changed after smoke                 | re-run smoke, use the new `slot:sha`                                          |
| `-> MISSING` in status              | a stable Service was deleted                  | re-apply the shared half                                                      |
| `-> UNSET` in status                | a Service never got its slot selector         | re-apply shared, or converge with cutover                                     |
| `preflight capacity inconclusive`   | cluster did not report a dimension            | check by hand, then `NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY=1`                |
| `cannot hold a second slot`         | quota or nodes genuinely too small            | raise quota or free capacity; do not force                                    |
| cutover stopped part-way            | mixed state                                   | re-run with the **same** `--target`                                           |

### Finding the migration Job

Jobs are named per release, so there is no fixed `nchat-migrations` to address.
Find them by label:

```bash
kubectl -n nchat-prod get jobs \
  -l app.kubernetes.io/component=migrations \
  -o custom-columns=\
NAME:.metadata.name,\
RELEASE:.metadata.annotations.nchat\\.io/release-sha,\
SUCCEEDED:.status.succeeded,\
FAILED:.status.failed
```

Then read the one you want:

```bash
JOB=nchat-migrations-<first 12 characters of the release SHA>
kubectl -n nchat-prod logs "job/$JOB" --all-containers --tail=200
kubectl -n nchat-prod describe "job/$JOB"
```

A failed Job is deliberately left in place: it is the evidence, and the deploy
refuses to run again for that release until someone has looked at it. A Job that
completed is reused rather than re-run, which is what makes "once per release"
true.

### A migration that has to be undone

Application rollback is a selector change and does **not** touch the schema.
Reverting a migration is a separate, deliberate procedure with its own review
and its own backup restore point; never trigger it implicitly by swapping slots.
The reason the expand/contract rule is enforced is precisely so this is almost
never needed.

---

## 18. First production checklist

```text
[ ] release commit SHA frozen
[ ] CI green on that SHA
[ ] security and quality gates green
[ ] migrations reviewed for Blue/Green compatibility
[ ] PostgreSQL backup taken
[ ] restore rehearsed from that backup
[ ] namespace nchat-prod exists
[ ] Secrets provisioned: nchat-secrets, nchat-postgres-runtime,
    nchat-postgres-migrator, nchat-file-encryption, ghcr-pull
[ ] stateful layer running: postgres, valkey, seaweedfs-filer
[ ] topology.env prepared with real hosts and preview allowlist
[ ] DNS resolves for the stable, admin and preview hosts
[ ] TLS certificate issued and chain valid
[ ] Keycloak production client configured, preview callbacks registered
[ ] cluster capacity sufficient for two slots
[ ] bootstrap run; Blue Ready
[ ] automated smoke passed on Blue
[ ] authenticated smoke completed with two accounts in two browsers
[ ] WebSocket delivery verified in both directions
[ ] upload, download, preview and ClamAV rejection verified
[ ] LiveKit call and screen share verified
[ ] monitoring dashboards reachable and showing both slots
[ ] this runbook accessible to whoever is on call
[ ] named owner for the rollback decision
```

Nothing above is pre-ticked: each depends on an operation that has not happened.

---

## 19. Known limitation — auth-service

`auth-service` runs **one** replica per slot. Its avatar volume is
ReadWriteOnce and a single replica owns writes
(`infra/k8s/base/services/auth-service/avatar-pvc.yaml`). Consequences:

- it has **no** PodDisruptionBudget. Over one replica, `minAvailable: 1` would
  block every voluntary eviction and make node drains impossible, while
  `maxUnavailable: 1` would permit exactly the eviction it appears to guard.
  Neither is a guarantee, so none is claimed.
- during a release both slots' auth-service pods mount the same claim. The
  volume's node affinity co-locates them and they only create distinct files, so
  it is safe but not scalable.
- a node drain will briefly interrupt authentication.

The fix is RWX or avatars in object storage — a data-model change, not part of
issue #626.
