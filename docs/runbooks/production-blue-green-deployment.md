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

nchat-prod, underneath all of it   applied once, by its own command
└── stateful
    ├── postgres, valkey, seaweedfs   StatefulSets, one each, on retained local PVs
    ├── postgres-bootstrap            creates nchat_migrator and nchat_app
    └── 4 PersistentVolumes           /mnt/hdd-geral/k3s/nchat-prod/*, reclaim Retain

media plane, outside this cluster entirely   not applied, not provisioned here
└── LiveKit on AWS
    ├── shared with nchat-dev         one deployment serves dev, Blue and Green
    ├── media-service → it            HTTPS/443, token signing, readiness probe
    └── browser → it                  WSS/HTTPS direct, never through Traefik
```

Public addresses: <https://nchat.nic-labs.com> and
<https://admin.nchat.nic-labs.com>.

**Blue and Green are release slots, not environments.** They share one database,
one Valkey, one object store, one Keycloak, one ClamAV — and one media plane,
which is external: LiveKit runs on AWS, is shared with nchat-dev, and is not
provisioned by this repository at all. The only
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

| Dependency                 | Shared | Notes                                                                                                                                                                                                                                                     |
| -------------------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| PostgreSQL                 | yes    | one schema, both slots. Migrations must stay compatible with the previous slot.                                                                                                                                                                           |
| Valkey                     | yes    | `VALKEY_WS_BROADCAST_ENABLED=true` — without it chat-service falls back to pod-local presence and messages do not cross replicas.                                                                                                                         |
| Object storage (SeaweedFS) | yes    | an attachment uploaded on Blue must be readable on Green.                                                                                                                                                                                                 |
| Keycloak / OIDC            | yes    | one client. Sessions survive cutover because nothing session-related is pod-local.                                                                                                                                                                        |
| LiveKit (AWS)              | yes    | **external.** One shared deployment, also used by nchat-dev. Both slots hand the browser the same URL and sign with the same key, so a cutover cannot move a call in progress to another media server. TURN strategy is not yet established — see 3b.3.4. |
| ClamAV                     | yes    | one scanner, `clamav:3310`.                                                                                                                                                                                                                               |
| auth-service avatar PVC    | yes    | ReadWriteOnce — see "Known limitation — auth-service".                                                                                                                                                                                                    |

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
  `seaweedfs-filer` — applied by `make prod-stateful-apply`, once, before
  anything else. Section 3b below is the whole procedure, including the host
  directories that must exist first. `bootstrap.sh` refuses to run without it.
- **Secret `nchat-postgres-admin`**, additionally, for the stateful layer: it is
  the superuser PostgreSQL starts as and the one `postgres-bootstrap` connects
  as to create the runtime and migration roles. `bootstrap.sh` does not check
  for it because the release does not use it; `stateful.sh` does.
- **Topology file** (not committed), passed as `NCHAT_PROD_TOPOLOGY_FILE`:

  ```ini
  NCHAT_PROD_HOST=nchat.nic-labs.com
  NCHAT_PROD_PUBLIC_URL=https://nchat.nic-labs.com
  NCHAT_PROD_PREVIEW_ALLOW_CIDR=<operator network>/24
  NCHAT_PROD_LIVEKIT_URL=wss://<aws livekit host>
  NCHAT_PROD_LIVEKIT_CONNECT_SRC=wss://<aws livekit host> https://<aws livekit host>
  ```

  The two LiveKit values name the **external** AWS media plane and must carry
  the same host; `NCHAT_PROD_LIVEKIT_URL` is scheme-and-host only, because the
  LiveKit SDK appends its own paths. Neither may name `NCHAT_PROD_HOST`.

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

## 3b. The stateful layer

Everything under this heading is applied **once**, by its own command, before
the namespace is bootstrapped. It is deliberately not part of any release: a
`kubectl apply` over the database must never be in the blast radius of shipping
a new web image.

```text
stateful  →  shared  →  migrations  →  Blue
```

Manifests: `infra/k8s/overlays/k3s-prod/stateful/`.

### 3b.1 What it contains, and why exactly one of each

Blue and Green are release slots, not environments. Duplicating any of these per
slot would turn a cutover into a data migration.

| Object                          | Kind        | Shared by both slots because                                       |
| ------------------------------- | ----------- | ------------------------------------------------------------------ |
| `postgres`                      | StatefulSet | one schema; migrations must stay compatible with the previous slot |
| `postgres-bootstrap`            | Job         | creates `nchat_migrator` and `nchat_app` with separate grants      |
| `valkey`                        | StatefulSet | the WebSocket bus both slots' replicas subscribe to at once        |
| `seaweedfs` + `seaweedfs-filer` | StatefulSet | an attachment uploaded on Blue must be readable on Green           |
| `auth-service-avatars`          | PVC         | rendered by `shared`; bound here to `nchat-prod-auth-avatars`      |

`prod-stateful-check` fails if any of these is duplicated, carries a
`nchat.io/release-slot` label, or is named `*-blue` / `*-green`.

Two SeaweedFS Services, one workload: `seaweedfs` is the name the volume server
announces itself under (`-ip=seaweedfs`) and the filer dials to persist a chunk;
`seaweedfs-filer` exposes only port 8888 and is the name
`infra/k8s/base/configmap.yaml` and `bootstrap.sh` already use.

### 3b.2 Storage — physical paths and permissions

Four local PersistentVolumes, pinned to `srv-apps-01`, storage class
`local-hdd-geral`, **reclaim policy `Retain`** on all four.

| PersistentVolume          | Size | Host path                                    |
| ------------------------- | ---- | -------------------------------------------- |
| `nchat-prod-postgres`     | 30Gi | `/mnt/hdd-geral/k3s/nchat-prod/postgres`     |
| `nchat-prod-seaweedfs`    | 60Gi | `/mnt/hdd-geral/k3s/nchat-prod/seaweedfs`    |
| `nchat-prod-valkey`       | 10Gi | `/mnt/hdd-geral/k3s/nchat-prod/valkey`       |
| `nchat-prod-auth-avatars` | 1Gi  | `/mnt/hdd-geral/k3s/nchat-prod/auth-avatars` |

None of these directories is created by a manifest. Kubernetes does not create
the target of a `local` volume, and a manifest that pretended to would fail
later as a confusing mount error. Run this **on `srv-apps-01`, as root, before
the first apply**:

```bash
install -d -m 0750 -o 70    -g 70    /mnt/hdd-geral/k3s/nchat-prod/postgres
install -d -m 0750 -o 999   -g 999   /mnt/hdd-geral/k3s/nchat-prod/valkey
install -d -m 0750 -o 65532 -g 65532 /mnt/hdd-geral/k3s/nchat-prod/seaweedfs
install -d -m 0750 -o 65532 -g 65532 /mnt/hdd-geral/k3s/nchat-prod/auth-avatars
```

The owners are the UIDs each container runs as: PostgreSQL 70, Valkey 999,
SeaweedFS 65532, and auth-service 65532. `fsGroup` fixes up the group on mount
for the three StatefulSets, but the directory must already belong to the right
user or the first write fails.

Nothing here touches `/mnt/hdd-geral/k3s/nchat-dev/*`, and
`prod-stateful-check` fails on any path containing `nchat-dev`.

### 3b.3 Media plane — external, shared with nchat-dev

**Production provisions no media plane.** One LiveKit deployment runs on AWS,
outside this cluster; nchat-dev already uses it and nchat-prod uses the same
one. There is nothing to apply, no host port to reserve on `srv-apps-01`, and no
firewall rule to add there for media.

The endpoint is confirmed, not pending:

```ini
NCHAT_PROD_LIVEKIT_URL=wss://livekit-dev.nic-labs.com
NCHAT_PROD_LIVEKIT_CONNECT_SRC="wss://livekit-dev.nic-labs.com https://livekit-dev.nic-labs.com"
```

The `-dev` in that hostname is historical and does **not** mean production is
pointed at a development environment. It is the name of the one shared media
plane, which serves nchat-dev and both production slots alike. Renaming it is a
DNS change to schedule later, not a blocker for this release.

On the AWS host:

| Layer                 | What it does                                                                           |
| --------------------- | -------------------------------------------------------------------------------------- |
| Caddy                 | terminates TLS for `livekit-dev.nic-labs.com:443`, reverse-proxies to `localhost:7880` |
| LiveKit Server 1.9.12 | signalling and API on 7880/TCP, behind Caddy                                           |
| LiveKit RTC           | 7881/TCP and 7882/UDP, on the AWS host directly                                        |

That table is what a diagnostic session on the instance actually found. It found
**no TURN server**: no `coturn.service`, no coturn process, no listener on 3478
or 5349, and no `turn:` block in the `livekit.yaml` that was inspected. This
runbook therefore makes no claim that the media plane provides TURN — see
3b.3.4, which is an open item rather than a completed one.

Those ports are facts about that host, not configuration this repository sets —
the only thing production is configured with is the `wss://` address above,
which Caddy answers on 443. The instance's public address was observed as
`56.126.170.1` during diagnosis; nothing here depends on it, and nothing should
be configured with it, since it can change without notice.

That is a deliberate architecture, not a simplification:

- **No host-port contention.** A media server needs the host's own ports.
  srv-apps-01 has one set of them and nchat-dev holds them. A second local one
  would not get its own — it would fail to bind, or win the race after a restart
  and take development's media plane away.
- **WebRTC does not traverse Traefik.** The browser is handed the AWS host and
  dials it directly. Signalling and media never enter this cluster's gateway,
  which has no reason to carry them and would add a hop to every call.
- **A cutover cannot break a call.** Blue and Green are configured from one
  shared ConfigMap and one Secret, so both hand the browser the same URL and
  sign tokens with the same key. Promoting a release does not move anyone
  between media servers — see 3b.3.2.

#### 3b.3.1 How the pieces connect

```
browser
  └─ POST https://nchat.nic-labs.com/api/media/media/livekit/token
       └─ Traefik → media-service (Blue or Green, whichever the stable
            Service selects)
            └─ signs a JWT with LIVEKIT_API_KEY / LIVEKIT_API_SECRET
            └─ answers with { token, serverUrl: <LIVEKIT_API_URL> }
  └─ connects DIRECTLY to serverUrl over WSS
       └─ WebRTC handled entirely by the AWS deployment
```

Only the token request touches this cluster. Everything after it is between the
browser and AWS.

Three values, three different jobs — do not collapse them:

| Value                           | Where it lives                | What it is                                                                                                                                                          |
| ------------------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `LIVEKIT_API_URL`               | `nchat-config`, from topology | the AWS host. media-service dials it for the API and readiness, AND returns it to the browser as `serverUrl`. Scheme and host only — the SDK appends its own paths. |
| `NCHAT_WEB_LIVEKIT_CONNECT_SRC` | `nchat-config`, from topology | the browser CSP allowlist for that same host, both schemes (`wss://` and `https://`).                                                                               |
| `LIVEKIT_API_KEY` / `_SECRET`   | `nchat-secrets` (sealed)      | the signing credentials. Never in a ConfigMap, never in topology.                                                                                                   |

`prod-blue-green-check` asserts that the first two name the same host, that the
host is **not** `nchat.nic-labs.com`, that the URL is `wss://` and carries no
path, that the CSP allows **both** `wss://` and `https://` for that host, and
that both slots read them from the shared ConfigMap. A render still carrying
`REPLACE_ME_PROD_LIVEKIT_URL` is refused by the deploy scripts before `kubectl`
runs.

#### 3b.3.2 Why Blue and Green share it

Both slots take the whole `nchat-config` through `envFrom` and the same
`nchat-secrets`. So:

- both hand the browser the identical `serverUrl`;
- both sign with the identical API key, so a token issued by Blue is honoured by
  the same LiveKit server after the cutover;
- a call established before a cutover is a browser↔AWS connection that neither
  slot is in the path of, so promoting Green does not interrupt it.

The only thing a cutover changes for media is which pod signs the _next_ token.

#### 3b.3.3 Prerequisites the operator owns

These are outside this repository and cannot be verified by any gate here. Do
them before the first release:

- [ ] **Topology file** carries the two confirmed values from 3b.3, verbatim.
- [ ] **API key/secret** sealed into `nchat-prod/nchat-secrets` — for the first
      release, the same pair dev uses, in production's own Secret. See 3b.4.
- [ ] **WebSocket reachability** from a client network:
      `curl -sSf https://livekit-dev.nic-labs.com/ -o /dev/null` and a WSS
      handshake.
- [ ] **TURN strategy determined** — see 3b.3.4. This is an open question, not
      a box to tick from this repository.
- [ ] **AWS security group / firewall** allows the client networks that will
      place calls, on the ports that deployment publishes (7880 via Caddy on
      443, 7881/TCP, 7882/UDP, plus whatever TURN ends up needing). Nothing is
      forwarded on srv-apps-01 for media.
- [ ] **Audio call** between two participants on production.
- [ ] **Video call** between two participants on production.
- [ ] **Screen share** verified.
- [ ] **Reconnect** verified: drop the network on one participant, restore it,
      and confirm the session recovers.
- [ ] **A call over a relay-forcing network** verified — see 3b.3.4. A direct
      call succeeding does not establish that WebRTC works for everyone.

Verify from inside the cluster that both slots can actually reach it — this is
the check that proves the media plane works, and no gate in CI can do it:

```bash
for slot in blue green; do
  kubectl -n nchat-prod exec deploy/media-service-$slot -- \
    wget -qO- --timeout=5 "$(kubectl -n nchat-prod get cm nchat-config \
      -o jsonpath='{.data.LIVEKIT_API_URL}' | sed 's#^wss://#https://#')/" \
    >/dev/null && echo "$slot → LiveKit reachable"
done
```

media-service's own `livekit-api` readiness probe does the equivalent on
startup: a slot that cannot reach the AWS deployment never becomes Ready, which
is the intended loud failure.

#### 3b.3.4 TURN — an open item, not a solved one

A diagnostic session on the AWS instance found LiveKit and Caddy and **no TURN
server**: no `coturn.service`, no coturn process, nothing listening on 3478 or
5349, and no `turn:` block in the `livekit.yaml` inspected. Nothing in this
repository configures TURN either, for production or for dev.

What that means, precisely: it is not established that clients behind a
restrictive NAT can complete a call. It is also not established that they
cannot — LiveKit may be reaching them over its own RTC ports, and dev has been
working. The honest statement is that the relay path is untested.

Why it matters: a peer behind symmetric NAT, or on a network that blocks UDP,
cannot connect to LiveKit's RTC ports directly. Such a client needs a relay, and
a WebRTC deployment without one fails for a fraction of users that is invisible
until they report it — corporate networks and some mobile carriers first.

So, before treating calls as production-ready:

- [ ] **Determine the AWS deployment's TURN strategy.** Ask what the media plane
      is expected to do for clients that cannot connect directly: LiveKit's
      embedded TURN (`turn:` in its config), a separate relay, a managed
      service, or nothing by design.
- [ ] **If TURN is needed, configure it on the AWS media plane** — there, not
      here. Production must not grow a local TURN server: `prod-stateful-check`
      rejects one, for the same host-port reason it rejects a local LiveKit.
- [ ] **Test a call across a network that forces relaying** — a mobile hotspot
      with UDP restricted, or a client firewalled off LiveKit's RTC ports. The
      point is to exercise the relay path, not to confirm the direct one.
- [ ] **Do not read a successful direct call as coverage.** Two participants on
      a permissive network prove the signalling and the media plane work; they
      prove nothing about the users who need a relay.

Until those are answered, calls are verified for direct-path clients only, and
this runbook says nothing stronger.

### 3b.4 Secrets

None are created by this repository or by any script here. The stateful layer
reads four:

| Secret                    | Keys it must carry                                                                                          |
| ------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `nchat-secrets`           | `VALKEY_PASSWORD` (stateful); `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` (read by media-service in both slots) |
| `nchat-postgres-admin`    | `POSTGRES_ADMIN_USER`, `POSTGRES_ADMIN_PASSWORD`                                                            |
| `nchat-postgres-runtime`  | `POSTGRES_APP_PASSWORD` (and `DATABASE_URL` for the services)                                               |
| `nchat-postgres-migrator` | `POSTGRES_MIGRATOR_PASSWORD` (and `MIGRATIONS_DATABASE_URL`)                                                |

Provision them with `docs/runbooks/sealed-secrets-rotation.md`. **Do not copy
values from nchat-dev**: a shared credential makes the two environments one
blast radius.

`LIVEKIT_CONFIG` and `COTURN_CONFIG` are **not** production keys: they
configure a media-server process, and production runs none. Production
needs only the client credentials, `LIVEKIT_API_KEY` and `LIVEKIT_API_SECRET`,
in the shared `nchat-secrets` — the template
`infra/k8s/secrets/templates/nchat-secrets.template.yaml` already carries both.

#### 3b.4.1 LiveKit credentials — shared for the first release, by decision

The AWS LiveKit server currently has **exactly one API key configured**. So for
the first production release:

|                                                     |                                                                           |
| --------------------------------------------------- | ------------------------------------------------------------------------- |
| LiveKit server                                      | the same one dev uses                                                     |
| `LIVEKIT_API_KEY` / `LIVEKIT_API_SECRET` **values** | the same pair dev uses                                                    |
| Kubernetes Secret holding them                      | **separate**: `nchat-prod/nchat-secrets`, never `nchat-dev/nchat-secrets` |

The distinction matters and is not a formality. Production reads its own Secret
in its own namespace, sealed for its own cluster scope; nothing in `nchat-prod`
references `nchat-dev`, and `prod-blue-green-check` fails on any manifest that
does. Only the two credential _values_ coincide, and only because the server
offers one key to authenticate against.

**No other value may be copied from dev.** `VALKEY_PASSWORD`, the PostgreSQL
credentials, `nchat-file-encryption` and the rest are production's own; a shared
one of those would make the two environments a single blast radius. Seal
production's `nchat-secrets` following
`docs/runbooks/sealed-secrets-rotation.md`, filling only these two keys with
dev's values and generating everything else fresh.

**This is a temporary accepted risk, not the target state.** While it holds, a
leaked dev key can mint tokens that production's LiveKit honours, and revoking
the key revokes it for both environments at once.

#### 3b.4.2 Rotation plan — a dedicated production key

Do this once production has stabilised, and treat it as closing the risk above.

**What is settled, and what is not.** LiveKit supports several key/secret pairs
at once, and adding a new one does not invalidate tokens already signed with the
old one. That is what makes the _credential_ side of this safe: the two keys
overlap, so nothing signed before the change stops being honoured at the moment
of the change.

That property is about the credentials, not about the server. Whether LiveKit
Server 1.9.12 re-reads `/etc/livekit/livekit.yaml` without restarting **has not
been verified**, so do not plan this as a zero-downtime operation. If the
installed version needs a restart to pick up the new `keys:` entry, calls in
progress on that server are dropped when it restarts — and that server is shared
with nchat-dev.

Before touching the server:

- [ ] Confirm from the installed version's own documentation or configuration
      whether it reloads `keys:` without a restart.
- [ ] If a restart is required, schedule a maintenance window.
- [ ] Notify users — of production **and** of nchat-dev — that calls in progress
      may be interrupted, since one server serves both.

Then:

1. **Add** a second key/secret pair to the AWS LiveKit server's `keys:` map,
   alongside the existing one, and apply it by whichever mechanism the previous
   step established: a reload if the version supports one, a restart inside the
   window otherwise. Confirm the server is serving again before continuing.
2. **Reseal** `nchat-prod/nchat-secrets` with the new pair, per
   `docs/runbooks/sealed-secrets-rotation.md`. Change nothing in `nchat-dev`.
3. **Roll out** both media-service slots so they pick it up:

   ```bash
   kubectl -n nchat-prod rollout restart deploy/media-service-blue deploy/media-service-green
   kubectl -n nchat-prod rollout status  deploy/media-service-blue deploy/media-service-green
   ```

   Restart both, not only the active slot: the standby must be able to sign
   valid tokens the moment a cutover selects it.

4. **Verify** a new call connects on production, and that dev is unaffected.
5. **Keep the old key in place** until every token signed with it has expired —
   they are short-lived, see `LIVEKIT_TOKEN_TTL_SECONDS`. Removing it earlier
   would reject sessions that are still legitimately authenticated.
6. **Only then** remove or restrict the old key on the server, per the
   operational plan agreed for that window. Note that this is a second change to
   the server's configuration and carries the same reload-or-restart question as
   step 1.

Rollback at any point before step 6 is resealing the previous pair and repeating
step 3; the old key is still on the server, so this needs no server-side change.

### 3b.5 Applying it

```bash
make prod-stateful-check          # offline; no cluster needed
make prod-stateful-apply          # requires the nchat-prod-deployer context
```

`stateful.sh` validates before it writes: the kube context, the namespace, the
four Secrets, and that any PersistentVolume that already exists matches this
overlay's path, storage class and reclaim policy. It then applies and waits for
all three workloads plus the bootstrap Job.

It **never** deletes a PersistentVolume, a claim or a StatefulSet. Its only
`delete` is the completed `postgres-bootstrap` Job, whose pod template is
immutable once finished; every statement that Job runs is idempotent.

### 3b.6 Verifying it

```bash
kubectl get pv | grep nchat-prod          # Bound, RECLAIM POLICY Retain
kubectl -n nchat-prod get pvc             # data-postgres-0, data-valkey-0,
                                          # data-seaweedfs-0, auth-service-avatars
kubectl -n nchat-prod get svc postgres valkey seaweedfs seaweedfs-filer
kubectl -n nchat-prod get statefulset,deployment,job
```

Each dependency, individually:

```bash
# PostgreSQL — and that the two roles exist with the right grants
kubectl -n nchat-prod exec statefulset/postgres -- pg_isready
kubectl -n nchat-prod logs job/postgres-bootstrap

# Valkey — authenticated, so an unauthenticated PING must be refused
kubectl -n nchat-prod exec statefulset/valkey -- valkey-cli ping   # expect NOAUTH

# SeaweedFS — the filer is what file-service consumes, not the master
kubectl -n nchat-prod exec statefulset/seaweedfs -- wget -qO- http://localhost:8888/
kubectl -n nchat-prod exec statefulset/seaweedfs -- wget -qO- http://localhost:9333/cluster/status

# The media plane is NOT here — nothing to check in this namespace. Its
# reachability is verified from inside the slots instead; see 3b.3.3.
```

A `PersistentVolumeClaim` stuck in `Pending` almost always means the host
directory from 3b.2 does not exist, or is owned by the wrong UID.

### 3b.7 Backup and restore

`Retain` protects against a deleted claim. It does not protect against a corrupt
database, a bad migration or a failed disk, and none of it is a backup.

Take a dump before every release, keep it off `srv-apps-01`, and rehearse the
restore. The first-production checklist requires both.

#### 3b.7.1 Why the roles matter here

This database runs a least-privilege model: `nchat_migrator` owns the `auth`,
`chat` and `files` schemas and everything in them and is the only role that runs
DDL; `nchat_app` has DML and nothing more; the admin role owns neither and is
used only to administer the server.

Restoring ignores that model unless you make it not. `pg_restore` gives every
restored object to **whoever runs it**, so a dump restored by the admin produces
a database where the admin owns all 51 tables. `nchat_migrator` can then no
longer `ALTER` them, so the next migration fails — and `grant-runtime.sql` fails
with it, which takes `nchat_app`'s access down too. Nothing reports this at
restore time; the database looks complete and is unmaintainable.

The procedure below therefore **dumps as the admin and restores as
`nchat_migrator`**, which puts ownership back exactly where the model expects
it. `scripts/db/postgres-restore-test.sh` proves both halves on a real
PostgreSQL, including the admin-restore failure, so the reasoning here cannot
drift from the behaviour.

#### 3b.7.2 Backup

```bash
kubectl -n nchat-prod exec statefulset/postgres -- sh -c '
  PGPASSWORD="$POSTGRES_PASSWORD" pg_dump \
    --username="$POSTGRES_USER" \
    --format=custom --no-owner --no-privileges \
    --dbname=nchat
' > "nchat-prod-$(date +%F).dump"
```

Read that command in three parts:

- **`--username="$POSTGRES_USER"`** names the role explicitly. Without it
  `pg_dump` connects as the container's Unix user, `postgres`, which is not the
  role this deployment configures — the admin user comes from
  `nchat-postgres-admin/POSTGRES_ADMIN_USER`. The admin role is the right one to
  dump with because it can read every object regardless of owner; `nchat_app`
  cannot read what it has no grant on, and dumping as `nchat_migrator` would
  miss anything outside its schemas.
- **`PGPASSWORD="$POSTGRES_PASSWORD"`** takes the password from the environment
  the pod already has, inside the container. It is never typed, never passed as
  an argument where `ps` would show it, and never printed. Both variables are
  expanded by the shell **inside** the pod — hence the single quotes, which stop
  your own shell from touching them.
- **`--no-owner --no-privileges`** strips ownership and grants from the dump.
  They are re-established on restore by the role that runs it and by
  `grant-runtime.sql`, which is what makes the restore reproducible rather than
  dependent on the roles happening to exist with the same names.

#### 3b.7.3 Restore

Into an empty database, with the roles already bootstrapped:

```bash
# 1. Recreate the database. Destructive and deliberate.
kubectl -n nchat-prod exec statefulset/postgres -- sh -c '
  PGPASSWORD="$POSTGRES_PASSWORD" psql --username="$POSTGRES_USER" --dbname=postgres \
    -c "DROP DATABASE IF EXISTS nchat" -c "CREATE DATABASE nchat"
'

# 2. Recreate the roles and schema ownership by re-running the stateful layer.
#    Its bootstrap Job is idempotent, and stateful.sh refuses to touch a Job
#    that is still running rather than interrupting one.
make prod-stateful-apply

# 3a. Put the migrator credential inside the pod as a 0600 pgpass file. It
#     arrives on stdin, so it never appears in an argument list, in your shell
#     history, or in the pod's process table.
kubectl -n nchat-prod get secret nchat-postgres-migrator \
  -o jsonpath='{.data.POSTGRES_MIGRATOR_PASSWORD}' | base64 -d |
  kubectl -n nchat-prod exec -i statefulset/postgres -- sh -c '
    umask 077 && { printf "*:*:nchat:nchat_migrator:"; cat; printf "\n"; } > /tmp/.pgpass'

# 3b. Restore AS nchat_migrator, so it owns what it restores.
kubectl -n nchat-prod exec -i statefulset/postgres -- sh -c '
  PGPASSFILE=/tmp/.pgpass pg_restore \
    --username=nchat_migrator --host=127.0.0.1 --dbname=nchat \
    --no-owner --exit-on-error
' < nchat-prod-YYYY-MM-DD.dump

# 3c. Remove it, whether or not the restore succeeded.
kubectl -n nchat-prod exec statefulset/postgres -- rm -f /tmp/.pgpass
```

`POSTGRES_MIGRATOR_PASSWORD` is not in the postgres pod's environment — it
belongs to `nchat-postgres-migrator`, which only the migration Job mounts. Step
3a is how it reaches one command without being typed, echoed, or passed as an
argument: `kubectl exec` arguments are visible in the pod's process table and in
the API server's audit log, so a `PGPASSWORD=...` on the command line would put
a production credential in both.

`--host=127.0.0.1` forces a TCP connection, so PostgreSQL performs password
authentication. Over the Unix socket the container's `trust`/`peer` rules can
apply instead, and the restore could silently run as the wrong role — which is
the exact failure this whole procedure exists to prevent.

Then re-apply the runtime grants. `run-migrations.sh up` does this as
`nchat_migrator`: it applies any pending migrations and then
`grant-runtime.sql`, in one connection, as the role that now owns the restored
objects — which is why its `GRANT`s succeed where the admin's restore would have
left them failing.

```bash
make migrations-up
```

#### 3b.7.4 Verifying a restore

A restore is not finished until these hold. Run them before letting traffic in:

```bash
# Every schema and table belongs to nchat_migrator — not to the admin role.
kubectl -n nchat-prod exec statefulset/postgres -- sh -c '
  PGPASSWORD="$POSTGRES_PASSWORD" psql --username="$POSTGRES_USER" --dbname=nchat -tA -c "
    SELECT tableowner, count(*) FROM pg_tables
    WHERE schemaname IN (''auth'',''chat'',''files'') GROUP BY 1"
'
```

Expect one row, `nchat_migrator`. Any row naming the admin role means the
restore was run by the wrong role: drop the database and repeat 3b.7.3 rather
than trying to repair ownership in place.

Then confirm a migration still applies (`make migrations-status` and, if
anything is pending, `make migrations-up`), and that the application can read
and write — the smoke in section 6 covers the second.

To rehearse all of this without a cluster, on any machine with Docker:

```bash
make db-restore-test
```

It bootstraps the roles with the production Job's own script, migrates, dumps,
restores, and then requires that `nchat_migrator` can still apply DDL, that
`nchat_app` still cannot, and that a restore run by the admin role leaves the
database unmigratable — so the failure mode stays proven rather than remembered.

---

## 4. First production — bootstrap

Blue is the baseline by definition; Green is the next candidate and is not
deployed.

```bash
NCHAT_PROD_RELEASE_SHA=<40-hex commit sha> \
NCHAT_PROD_TOPOLOGY_FILE=/secure/path/topology.env \
NCHAT_PROD_RELEASE_MANIFEST_DIR=/secure/path/release-manifest \
make prod-blue-green-bootstrap
```

The sealed manifest is the only description of the release bootstrap accepts.
It takes the image digests **and** the release identity from it, into a
temporary directory of its own, so the images Blue runs and the identity Blue is
annotated with cannot come from two places and disagree. There is deliberately
no `ARTIFACTS_DIR` here: a separately-supplied set of digests would be exactly
that second source.

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
#    The sealed manifest carries both the images and the release identity.
NCHAT_PROD_RELEASE_SHA=<sha> NCHAT_PROD_TOPOLOGY_FILE=... \
  NCHAT_PROD_RELEASE_MANIFEST_DIR=/secure/path/release-manifest \
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

Every workload of a slot is stamped with the same two annotations:

```yaml
metadata:
  annotations:
    nchat.io/release-sha: <commit sha>
    nchat.io/release-id: <sha-256 of the sealed release manifest>
```

**A commit is not a release.** Two builds of one commit do not produce the same
images: buildx stamps provenance and an SBOM into every layer set, so the
digests differ even though nothing in the source did. `nchat.io/release-sha`
therefore says which code a slot is running and cannot say which _bytes_.

`nchat.io/release-id` is what says that. It is the SHA-256 that seals the
release manifest — the value in `release-manifest.sha256`, the one `sha256sum
-c` verifies — and because the manifest carries all eleven image digests, the
identity changes the moment any one of them does. Rebuild the same commit and
you get a different release id, which is precisely the event a source SHA
cannot report.

Images are pinned by digest (`image@sha256:…`). Mutable tags — `latest`, `main`,
`develop`, `stable`, `prod` — are rejected by `prod-blue-green-check`.

```bash
make prod-blue-green-status
```

reports each slot as `CONSISTENT <sha>:<release id>`, `NOT DEPLOYED`, or
`MIXED` with a per-workload breakdown. A slot is only promotable when it is
CONSISTENT, and workloads that agree on the commit but not on the release id are
MIXED — that is a slot half-replaced by a rebuild.

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

### Capacity evidence (the deploy identity is namespaced)

Three of the four inputs — the nodes' allocatable capacity, and the Pods of
every namespace — are cluster-scoped reads. `nchat-prod-deployer` holds a Role
in `nchat-prod` and nothing cluster-scoped, so it is refused `kubectl get nodes`
and `kubectl get pods --all-namespaces`, and under that identity the preflight
reports `INCONCLUSIVE` on every cluster dimension.

Answering from `nchat-prod` alone is not the fix. The node these workloads share
sits at ~94% of its CPU requests and most of that belongs to other namespaces; a
namespace-only view would report room that is not there. Nor is widening the
Role: a capacity check is not a reason to give a deploy identity cluster-wide
read.

So collection is separated from evaluation. A context that **may** read those
resources — a cluster administrator, or a read-only identity kept for the
purpose — takes the snapshot; the deploy consumes it and consults the API only
for what it is already allowed to see.

```bash
# 1. as the trusted read-only context
make prod-capacity-evidence ARGS=/secure/path/capacity-evidence

# 2. as nchat-prod-deployer, within 15 minutes
NCHAT_PROD_CAPACITY_EVIDENCE_DIR=/secure/path/capacity-evidence \
NCHAT_PROD_RELEASE_SHA=<40-hex> \
NCHAT_PROD_TOPOLOGY_FILE=/secure/path/topology.env \
ARTIFACTS_DIR=./artifacts \
make prod-blue-green-deploy
```

The directory holds `node-allocatable.txt`, `cluster-requests.txt`,
`cluster-pods.txt` — the same lines the live collection produces —
`sha256sums.txt`, and a `metadata` record naming the schema, the collection
instant in UTC, the namespace it was collected for and the context that produced
it. It is a snapshot of other namespaces: keep it outside the repository and do
not commit it.

Leaving `NCHAT_PROD_CAPACITY_EVIDENCE_DIR` unset keeps the previous behaviour,
which is what a rehearsal cluster or an administrator running the deploy by hand
should use.

**What the evidence is trusted on.** The checksums detect a truncated or edited
file. They are **not** authenticity — anything that can write the directory can
write a matching `sha256sums.txt`. The evidence is believed because of where it
came from: a collection the operator ran, or later a controlled CI artifact
produced by a job with its own read-only credentials. Point the deploy only at a
directory you produced or received through such a channel. Schema, freshness and
the namespace binding narrow the window in which a stale or foreign snapshot is
accepted; they do not make an untrusted directory safe.

The gate stops the deploy — before the migration and before any `kubectl apply`
— when the evidence is absent, incomplete, empty, dated more than
`NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS` (900) ago or more than 60 seconds
in the future (the clock-skew allowance), collected for another namespace, of an
unknown schema, does not match its checksums, or reaches the deploy through a
symlink. `NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS` itself must be a
non-negative decimal integer of at most 18 digits; anything else is refused as a
misconfiguration rather than reaching the comparison.

**A failed refresh withdraws what was there.** Re-collecting into a directory
that already holds evidence invalidates it before the collection starts, and the
new snapshot is only accepted once every file, the metadata and the checksums
are in place. So a refresh that fails — the cluster unreachable, the read
refused, a resource that came back empty — leaves that destination **unusable**
rather than leaving the previous snapshot standing. This is deliberate: a
collector that reports an error while the deploy would still read yesterday's
picture of the cluster is a failure an operator can walk straight past. Collect
again into the same directory and it is usable once more.

**Malformed is not the same as missing.** A file the collector cannot have
produced — a Pod line without its `phase|nodeName` delimiter, a node line
outside its four positional fields, a request line short of its resources
column, or a quantity that is negative, infinite, `NaN` or not a number at all —
ends the preflight as **unusable input** (exit 3), not `INCONCLUSIVE`. Negative
and non-finite quantities are refused wherever they appear, the candidate
manifest and the namespace quota included: they subtract from demand or add to
free space, so absorbing one as a zero would be a pass the cluster cannot
honour. `NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY=1` reaches exit 2
and nothing else: a dimension the cluster did not report is something an
operator can check by hand and acknowledge, a broken input is not. Sparse
Kubernetes is still valid: a Pod the scheduler has not bound has no nodeName,
and a container may declare no requests at all.

The node line is **positional** — `<cpu> <memory> <ephemeral-storage> <pods>`,
one space between each — and an empty position stays empty. A node that reports
no ephemeral-storage arrives as `8 32Gi  110`, which means storage unknown and a
ceiling of 110 pods; the two dimensions are answered separately and neither
value moves into the other's place. Trailing positions may be omitted instead of
left blank. `cpu` and `memory` are required; the other two, when empty, are
dimensions the cluster did not report and come back `INCONCLUSIVE`.

The candidate slot's own workloads stay a live namespaced read in both modes:
they are what the redeploy is priced against and must describe the cluster now,
not when the snapshot was taken.

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
Release validated          : <sha>:<release id>
Authenticated release smoke: REQUIRED / NOT CONFIRMED BY THIS COMMAND
Cutover eligibility        : BLOCKED until the checklist above is recorded
```

The release it names is the commit **and** the sealed build, because a rebuild
of the same commit is a different release ("Identifying a release"). Copy the
whole value into the evidence the cutover asks for.

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
NCHAT_PROD_SMOKE_CONFIRMED=green:<sha>:<release id> \
  NCHAT_PROD_RELEASE_MANIFEST_DIR=/secure/path/release-manifest \
  make prod-blue-green-cutover ARGS="--target green"
```

`smoke.sh` prints both values at the end of a passing run; copy them from there
rather than assembling them by hand. `NCHAT_PROD_RELEASE_MANIFEST_DIR` is the
directory holding the `release-manifest.json` and `release-manifest.sha256` of
the release being promoted, and it is **required** — there is deliberately no
mode that falls back to the commit alone, because that is the mode this gate
exists to remove.

The target is **named, never derived**. Deriving it from the current state makes
a repeat run reverse the previous one instead of confirming it.

Gates, all before any mutation:

1. context and namespace;
2. the target is fully Ready;
3. the target carries exactly one release — one commit _and_ one release id.
   Ready is not enough: a deploy that failed part-way leaves untouched workloads
   Ready on the previous release;
4. the supplied manifest verifies against its own seal and satisfies the release
   contract, and the release id derived from it matches the one the slot is
   actually running;
5. the smoke evidence matches `<slot>:<sha>:<release id>` for that slot,
   recomputed at this moment.

Gate 4 is re-derived here rather than accepted from whoever invoked the command:
an identity that is typed, pasted or carried over from an earlier step can be
stale or edited, and only the manifest is evidence. Rebuild or redeploy the
candidate between the smoke and the promotion — even from the identical commit
— and gates 4 and 5 stop matching, so the smoke no longer covers what is on the
cluster and the promotion is refused.

If the target is already fully active it reports a no-op and changes nothing.

**The cutover is sequential, not atomic.** Kubernetes has no transaction across
nine Services. Each is patched and read back, and the run stops at the first
that does not take, leaving a described partial state rather than an assumed
complete one. The window is a few hundred milliseconds of two slots serving at
once — which the release contract already requires to be safe, since both run
against one database and one event bus.

The old slot keeps running. It is the rollback.

---

## 11b. The release from GitHub Actions

`.github/workflows/deploy-nchat-prod.yml` runs sections 6, 7 and 9 in its
`candidate` job, and section 11 in a separate `cutover` job that starts only
after a reviewer approves the run in the `production` GitHub Environment.
**The candidate job cannot promote**; the cutover job is the only automation in
this repository that changes a stable Service selector, and it does it by
calling the same `cutover.sh` section 11 does.

```text
workflow_dispatch (sha, run_id)
    |
    +-- candidate job          it cannot promote
          validate sha and run id, refuse a dispatch from outside main
          checkout the sha, prove it is reachable from main
          download the sealed release manifest of run_id
          pin the eleven digests the manifest seals
          derive the release id from the manifest seal
          snapshot every stable Service selector, resolve active,
            take the candidate as its opposite
          deploy.sh  -> the idle slot, no traffic, stamped sha + release id
          smoke.sh   -> automated checks only
                |
                v
          stable selector invariant
            re-read the stable Services, diff against the snapshot
                |
                v
          candidate release identity revalidation
            read the slot's release from the cluster, require it to equal
            the dispatched sha and the sealed release id
                |
                v
          release evidence / workflow success
    |
    +== the candidate is Ready and carries no traffic. The run stops here
    |   until a reviewer approves it.
    |
    +-- cutover job            environment: production, needs: candidate
          [required reviewer approves the run]
          checkout the sha, prove main can still reach it
          download the sealed release manifest of run_id again
          read every stable Service selector                 -> BEFORE
            classify against the target: every Service must select
            the target or its opposite, else FAIL before any patch
            rollback target = opposite_slot(target)
          revalidate: the candidate slot still carries exactly
            <sha>:<release id>, read from the cluster
          cutover.sh --target <candidate>   the one mutation
          record every stable Service                        -> AFTER
            read-only, and recorded whether the promotion passed or failed
          [promotion failed] -> job FAILED, AFTER kept, nothing judged
          require every Service to select the target
          re-read the release the promoted slot is running
                |
                v
          promotion evidence / workflow success
```

Promotion is **not** disabled anywhere: there is no `if: false` to flip and no
input that selects a promoting path, because a gate one edit away from being
open is not a boundary. What holds it shut is the approval on the `production`
environment, which is configured on the environment and not in this repository.

**Approving the run is the authenticated smoke.** The workflow cannot perform
section 10 — no shell against an in-cluster Service can sign in through
Keycloak — and does not claim to. What the approval records is that a human
performed and reviewed that checklist for the exact `candidate:release` the run
reports, and authorised that candidate for promotion. Approving without having
done it is the one failure nothing here can catch, which is why the run prints
the release identity before the gate and the cutover job prints it again after.

The cutover job re-reads the cluster before it patches anything, and that is
not redundancy: an approval can arrive hours after the smoke, and a candidate
redeployed, rebuilt or degraded in the meantime is exactly as Ready and as
consistent as the one that was validated. Only the release identity separates
them, so it is compared against `<sha>:<release id>` before the promotion and
again after it. The evidence handed to `cutover.sh` is `<slot>:<sha>:<release
id>` — which `cutover.sh` then recomputes from the cluster for itself, so a
token this job assembled is checked rather than believed.

The preflight **classifies the selectors against the target** rather than
resolving them into an active slot, and the difference matters. A namespace
split between blue and green is the ordinary shape of a cutover to this same
target that stopped part-way, and converging it is exactly what a retry with the
same `--target` is for; refusing every mixed reading would close the one path
that finishes it. So a blue/green split **continues**, to the same target. What
fails, before anything is patched, is a reading this cannot describe: a Service
selecting a value that is neither slot, one carrying no `nchat.io/release-slot`
key, or one that is not there at all.

**The workflow's preflight does not replace the gate inside `cutover.sh`, and
is not allowed to.** It runs before the approval, so what it proves is a fact
about the namespace at that moment; the approval can arrive hours later.
`cutover.sh` reads the cluster for itself and runs the same
`require_promotable_selectors` against **that** reading — the one its own
mutation is decided from — before it patches anything. Both checks are the same
primitive and neither is decorative: the preflight is what fails a run early and
before a reviewer is asked to approve it, and the check inside `cutover.sh` is
what holds when the namespace changes after the preflight passed. That second
one also holds for an operator running `cutover.sh` by hand per section 11,
where no preflight ran at all.

**The target is never recalculated.** It is the slot the candidate job built and
the reviewer approved, and a retry converges on it rather than inverting to the
opposite of whatever now looks active — the bug that would send production back
to the release it had just left.

**The rollback target is `opposite_slot(target)`**, derived from the authorised
target and never read back from the selectors. Reading it back would name the
target itself once the namespace has converged, which is the one slot a rollback
can never go to. The job prints it and does nothing with it: `rollback.sh`,
`drain-old.sh` and the observation window are outside this workflow, and no
rollback is ever automatic.

**The after-state is recorded even when the promotion fails.** A cutover that
stops part-way is the run whose after-state matters most and the run an
asserting step would never reach, so recording and judging are two steps: the
recording is read-only, repairs nothing, and runs when the promotion actually
ran; the judgement is an ordinary step that stays skipped when the promotion
failed, so no success is ever claimed for one. The job's status is the
promotion's. Finishing a half-converged namespace from there is `cutover.sh
--target <the same slot>` run by an operator who has looked at that recording
(section 12).

"When the promotion actually ran" is an allowlist of two conclusions, and it has
to be spelled as one:

| The `promote` step ended            | after-state recorded |
| ----------------------------------- | -------------------- |
| `success`                           | yes                  |
| `failure`                           | yes                  |
| `skipped` (a step before it failed) | no                   |
| the run was cancelled               | no                   |

Naming any status function in an `if:` stops Actions inserting the implicit
`success()`, so anything looser runs the step on a run that promoted nothing.
`steps.promote.conclusion != ''` is the specific trap: a skipped step reports
`skipped`, which is not the empty string, so that spelling sends a run that
never reached the promotion to query production anyway.

Dispatch it with the release SHA and the **run id of the "Build and push
images" run that built it**. The manifest of that run is sealed with a SHA-256
and names its own `source_sha`, so naming a run is not the same as trusting it:
`release-digests.sh` verifies the seal, checks the contract, and refuses unless
the manifest seals the commit being deployed. The digests the cluster then runs
are exactly the ones that release was sealed with — not a rebuild that would
produce different bytes under the same tag. It reads the manifest rather than
the `digest-*.txt` artifacts because the manifest is kept for 90 days and they
are kept for 7.

The candidate slot is `opposite_slot(active)`, read from the cluster. Neither
slot name appears in the workflow and no input names one, so a mixed or unknown
selector state fails the run instead of being guessed past.

The **candidate job** declares no environment, and the cutover job declares
`production`. An approval on the candidate would gate the phase that has nothing
to approve yet, and declaring an environment there would also pull that
environment's secrets into an unprotected deploy. The candidate's two
variables —
`vars.NCHAT_PROD_TOPOLOGY_FILE` and, where the deploy identity cannot read
Nodes, `vars.NCHAT_PROD_CAPACITY_EVIDENCE_DIR` — must therefore be **repository
or organisation variables**, never environment-scoped. Both name paths on the
runner; neither is a secret and neither is committed. An environment-only
variable would arrive as an empty string, and the failure would not look like a
configuration mistake: an empty `NCHAT_PROD_TOPOLOGY_FILE` means
`prepare_prod_deploy_tree` skips installing the topology, and the deploy is
refused later for carrying `REPLACE_ME_*` placeholders.

Both jobs run on the production runner and hold the same two read permissions,
`actions: read` (the sealed manifest of the named build run) and
`contents: read`. Neither holds a write of any kind, and the cutover job
downloads the manifest again into its own workspace rather than receiving an
identity through a job output: a string that travelled through an output is a
string a step could have edited, and the seal is what the promotion verifies.

The `refs/heads/main` check in the first step is defence in depth, not the
boundary. A dispatch carries the workflow file of the ref it was started from,
so a feature branch would run its own copy of that check; the runner's pre-job
guard below is what actually confines this to `main`.

### What the workflow proves, and how it is enforced

`scripts/ci/check_deploy_prod_workflow.py` enforces the shape structurally, and
as a closed allowlist rather than a search for dangerous commands: every job,
step, command, env binding and ordering the file may contain is written out
there, and anything else is refused for not being in the contract. So a
promotion added to the `candidate` job is refused for the same reason
`echo hello` is, however it is spelled — `bash cutover.sh`,
`env bash cutover.sh`, a wrapper, a hand-written `kubectl patch service`,
`switch_services_to_slot` — and a third job is refused whatever it is called.

The contract also holds the separation itself, in both directions: only the
`cutover` job may run `cutover.sh`, it may run it only at its one contracted
position, only that job may declare `environment:` and `needs:`, and only the
`production` environment satisfies it. `rollback.sh`, `drain-old.sh`, a
migration and a DNS call are refused in either job for not being contracted
steps. Neither job may declare `if:`, `continue-on-error:` on a gate, a write
permission, or a runner other than the production one.

The candidate job's last three steps are the evidence, produced on every run:

| Evidence                   | Where it comes from                                           |
| -------------------------- | ------------------------------------------------------------- |
| stable selectors, before   | `collect_service_slots`, in the same step that picks the slot |
| active slot                | `resolve_active_slot` of that same reading                    |
| candidate slot             | `opposite_slot(active)`                                       |
| migrations, rollout        | `deploy.sh`, each fail-closed                                 |
| automated smoke            | `smoke.sh` — isolation, one consistent release, readiness     |
| stable selectors, after    | `collect_service_slots` again                                 |
| selector invariant result  | `diff` of the two readings; any difference fails the run      |
| expected release SHA       | the dispatched `sha`, proved reachable from `main`            |
| expected sealed release id | the SHA-256 the release manifest was sealed with              |
| observed candidate release | `slot_release_state` of the candidate, read from the cluster  |
| identity comparison result | observed must equal `CONSISTENT <sha>:<release id>`           |

Both selector readings come from the same function, so that comparison is
between two canonical forms and not two renderings. The snapshot is taken in the
step that resolves the slot, from that one reading, so the evidence describes
the state the deploy decision was actually made from.

**A green smoke is not the end of the run.** Two gates follow it, and either can
still fail:

- the **selector invariant** fails if any stable Service selects something other
  than what the snapshot recorded — something moved production traffic;
- the **identity revalidation** fails if the candidate is not running the exact
  release this run built — the slot was redeployed or rebuilt underneath the
  run. A rebuild of the same commit seals a different manifest, so it is caught
  here even though every SHA in the picture is unchanged.

The two are independent: a concurrent redeploy of the candidate leaves the
selectors untouched and produces a slot that is equally Ready and equally
consistent, so only the identity separates the two releases.

**A difference is never repaired.** These steps exist to detect that something
changed underneath the run; putting a selector back, or accepting whatever the
slot happens to carry, would destroy the only record of it happening. Treat a
failure at either as an incident, not as a deploy that needs retrying.

The cutover job produces its own evidence, on both sides of the one mutation:

| Evidence                   | Where it comes from                                                                                              |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| approval                   | the required reviewer on the `production` environment                                                            |
| stable selectors, before   | `collect_service_slots`, before anything is patched                                                              |
| preflight result           | `require_promotable_selectors` against the target; unclassifiable fails here                                     |
| decisive classification    | `require_promotable_selectors` again inside `cutover.sh`, on the reading its own mutation is decided from        |
| rollback target            | `opposite_slot(target)`, from the authorised target                                                              |
| approved release           | `<sha>:<release id>`, from the dispatch and the candidate job                                                    |
| candidate still carries it | `require_slot_release_identity`, read from the cluster                                                           |
| the promotion              | `cutover.sh --target <candidate>`, its own gates all fail-closed                                                 |
| stable selectors, after    | `collect_service_slots` again, recorded on a failed promotion too, and not at all when the promotion was skipped |
| convergence result         | `all_services_on_slot` against the named target — total or FAIL                                                  |
| promoted release, after    | `require_slot_release_identity` again, once traffic has moved                                                    |

Convergence and release identity are independent there too. A Service that is
missing, carries no `nchat.io/release-slot` key, kept the previous slot or holds
some other value all fail the convergence proof; and a slot that degraded or was
redeployed during the patches would pass it and still fail the identity read
that follows.

### The runner refuses everything else — the pre-job guard

`runs-on: [self-hosted, linux, x64, nchat-prod-deploy]` is routing. The label is
public, this repository is public, and any workflow committed to any branch can
ask for it. A job that reaches its first step on that runner is already running
as `nchat-prod-runner`, the one identity on `srv-apps-01` that can read the
least-privilege production kubeconfig. The workflow's own `refs/heads/main`
check lives in a file that same author could edit, so it is defence in depth,
not the boundary.

The boundary is host-side and outside the repository's reach:
`scripts/deploy/nchat-prod/runner-job-guard.sh`, installed as a root-owned copy
and wired to `ACTIONS_RUNNER_HOOK_JOB_STARTED`. The runner executes it before
the first step of every job it accepts, and a non-zero exit ends the job there.

It authorises one context, by exact comparison, and refuses everything else —
including a variable the runner did not set:

| Variable              | Only accepted value                                                           |
| --------------------- | ----------------------------------------------------------------------------- |
| `GITHUB_REPOSITORY`   | `nicrepository/nchat`                                                         |
| `GITHUB_WORKFLOW_REF` | `nicrepository/nchat/.github/workflows/deploy-nchat-prod.yml@refs/heads/main` |
| `GITHUB_REF`          | `refs/heads/main`                                                             |
| `GITHUB_EVENT_NAME`   | `workflow_dispatch`                                                           |

So a pull request, a dispatch from `develop`, another workflow file, another
event, a fork, and an empty environment are all the same outcome: no step runs.
The refusal names the variable that disagreed and never its value, because the
value is a string an untrusted workflow chose and the line is read out of a
system log.

**The copy must not live where the runner can write.** The checkout, `_work`
and `/home/nchat-prod-runner` are all rewritable by the job the guard exists to
judge; pointing the hook at any of them would let a job disable its own gate.

#### Installing it

Run on `srv-apps-01`, from a checkout of the reviewed commit, after merge.

```bash
# 1. a root-owned directory outside anything the runner can write
sudo install -d -o root -g root -m 0755 /usr/local/libexec/nchat-prod

# 2. the guard itself, read-and-execute only
sudo install -o root -g root -m 0555 \
  scripts/deploy/nchat-prod/runner-job-guard.sh \
  /usr/local/libexec/nchat-prod/runner-job-guard.sh

# 3. the installation gates. Every one of them refuses by exiting non-zero,
#    and PASS is printed only after the condition has actually been proved.
sudo bash <<'VERIFY'
set -euo pipefail
SOURCE=scripts/deploy/nchat-prod/runner-job-guard.sh
GUARD_DIR=/usr/local/libexec/nchat-prod
GUARD="$GUARD_DIR/runner-job-guard.sh"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

# byte for byte the reviewed file, not merely two hashes printed side by side
cmp --silent "$SOURCE" "$GUARD" ||
  fail 'the installed guard differs from the reviewed guard'

# a real file, owned by root, in a directory owned by root
[ -f "$GUARD" ] && [ ! -L "$GUARD" ] || fail 'the installed guard is not a regular file'
[ "$(stat -c '%U:%G' "$GUARD")" = root:root ] || fail 'the installed guard is not root:root'
[ "$(stat -c '%U:%G' "$GUARD_DIR")" = root:root ] || fail "$GUARD_DIR is not root:root"

# Asked as the runner itself, because that is the identity that must be
# refused, and asked as the positive question `test ! -w`: not writable is the
# only answer that returns 0, so a writable path and a runuser that could not
# answer at all -- unknown user, no privilege, no runuser -- are both failures.
runuser -u nchat-prod-runner -- test '!' -w "$GUARD" ||
  fail 'could not prove that nchat-prod-runner cannot rewrite its own guard'
runuser -u nchat-prod-runner -- test '!' -w "$GUARD_DIR" ||
  fail 'could not prove that nchat-prod-runner cannot replace its own guard'

echo "PASS: $GUARD is the reviewed file, root-owned, and not writable by nchat-prod-runner"
VERIFY

# 4. its own drop-in. 10-kubernetes.conf is not touched.
sudo tee /etc/systemd/system/actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service.d/20-job-guard.conf >/dev/null <<'CONF'
[Service]
Environment=ACTIONS_RUNNER_HOOK_JOB_STARTED=/usr/local/libexec/nchat-prod/runner-job-guard.sh
CONF
sudo chmod 0644 /etc/systemd/system/actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service.d/20-job-guard.conf

# 5. reload, and restart only the production runner
sudo systemctl daemon-reload
sudo systemctl restart actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service

# 6. the service gates, on the exact unit and nothing else. Non-zero on any
#    failure, and no value is printed -- only the name it was matched against.
sudo bash <<'VERIFY'
set -euo pipefail
GUARD=/usr/local/libexec/nchat-prod/runner-job-guard.sh
UNIT=actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service
UNIT_DIR="/etc/systemd/system/$UNIT.d"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

grep -qxF "Environment=ACTIONS_RUNNER_HOOK_JOB_STARTED=$GUARD" "$UNIT_DIR/20-job-guard.conf" ||
  fail '20-job-guard.conf does not point the hook at the installed guard'
[ -f "$UNIT_DIR/10-kubernetes.conf" ] || fail '10-kubernetes.conf is no longer there'
systemctl is-active --quiet "$UNIT" || fail "$UNIT is not active"
systemctl show -p Environment --value "$UNIT" | tr ' ' '\n' |
  grep -qxF "ACTIONS_RUNNER_HOOK_JOB_STARTED=$GUARD" ||
  fail "$UNIT is not carrying the hook"

echo "PASS: $UNIT is active and runs the guard before every job"
VERIFY
```

Always the full unit name, never a wildcard: `srv-apps-01` also runs the
nchat-dev, GitLab and other runners, and none of them is in scope here. Nothing
above reads or prints the kubeconfig, and no other unit's drop-ins, permissions
or Kubernetes RBAC are modified.

#### Proving it refuses — negative evidence

`deploy-nchat-prod.yml` already exists on `develop` and is `workflow_dispatch`,
so the refusal can be observed without inventing a workflow for it.

Dispatch **Deploy nchat-prod** from the `develop` ref with syntactically valid
inputs — a real 40-hex SHA on `main` and a real "Build and push images" run id —
so that nothing but the guard can be what refused it.

PASS requires all three:

- the `candidate` job ends failed with **no step executed** — the run page
  shows the job's steps never started, so "Validate the release request" did not
  run and nothing was checked out;
- the host log carries the refusal and names only the variable:

  ```bash
  sudo journalctl -u actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service \
    --since '-15 min' | grep 'runner job guard'
  # runner job guard: DENY, GITHUB_WORKFLOW_REF is not the authorised production deploy context.
  ```

- `make prod-blue-green-status` is unchanged.

A run that reached its first step and was stopped by the workflow's own
`refs/heads/main` check is a **fail** of this procedure, not a pass: the hook is
the gate being evidenced, and that check is the layer behind it.

#### Proving it allows — positive evidence

Before the guard's commit reaches `main`, the offline suite is the evidence:

```bash
make prod-runner-guard-test
```

It drives the guard through the authorised context and thirty-odd refusals —
absent, empty, look-alike, and metacharacter-carrying values among them — and
runs in `pnpm run ci`.

Once this commit is on `main`, the first legitimate dispatch from `main` is the
live positive: the `candidate` job starts its steps normally. The guard has no
opinion beyond that point; the release SHA, the sealed manifest and the
candidate's own gates remain as described above, and the cutover job still
starts only once a reviewer approves the run.

#### Rollback

```bash
# Keep the evidence first, if a refusal is what is being investigated. An
# empty file is a legitimate answer -- there may have been no refusal -- so
# only grep's "no match" (exit 1) is accepted; a journalctl that could not
# read the log, a grep error, or a tee that could not write all fail the
# collection instead of leaving an empty file that looks like evidence.
sudo bash <<'EVIDENCE'
set -uo pipefail
EVIDENCE_FILE=/secure/path/job-guard-denials.txt
UNIT=actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service

journalctl -u "$UNIT" --since '-1 day' |
  { grep 'runner job guard' || [ "$?" -eq 1 ]; } |
  tee "$EVIDENCE_FILE" >/dev/null || {
  echo 'FAIL: the denial evidence could not be collected' >&2
  exit 1
}

echo "PASS: refusals up to now are in $EVIDENCE_FILE (empty if there were none)"
EVIDENCE

sudo rm -f /etc/systemd/system/actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service.d/20-job-guard.conf
sudo systemctl daemon-reload
sudo systemctl restart actions.runner.nicrepository-nchat.srv-apps-01-nchat-prod.service
```

That is the whole rollback: one file, one unit. `10-kubernetes.conf`, the
kubeconfig, the `nchat-prod-deployer` RBAC and every other runner are untouched
by both directions, and the guard reads four environment variables and writes
nothing — there is no state, no cluster object and no data it can leave behind.
Removing it restores exactly the exposure this section exists to close, so treat
a rollback as an open security finding, not a fix.

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
NCHAT_PROD_SMOKE_CONFIRMED=green:<sha>:<release id> \
  NCHAT_PROD_RELEASE_MANIFEST_DIR=/secure/path/release-manifest \
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

| Symptom                                | What it means                                                              | Action                                                                                                                        |
| -------------------------------------- | -------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `missing Secret: <name>`               | a prerequisite is absent; nothing was applied                              | provision it, re-run                                                                                                          |
| migration Job fails                    | schema not advanced; release blocked                                       | find the Job by label and read its logs (below); fix forward, do not cut over                                                 |
| `slot X is MIXED`                      | a deploy reached only some workloads                                       | re-run deploy for that slot; do not promote                                                                                   |
| `slot X is not deployed`               | no workloads exist                                                         | deploy the candidate first                                                                                                    |
| candidate Ready but cutover blocked    | release inconsistent, or evidence stale                                    | `status`, then re-smoke                                                                                                       |
| smoke evidence rejected                | candidate changed after smoke, or was rebuilt from the same commit         | re-run smoke, use the new `slot:sha:release-id`                                                                               |
| `-> MISSING` in status                 | a stable Service was deleted                                               | re-apply the shared half                                                                                                      |
| `-> UNSET` in status                   | a Service never got its slot selector                                      | re-apply shared, or converge with cutover                                                                                     |
| `preflight capacity inconclusive`      | cluster did not report a dimension                                         | check by hand, then `NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY=1`                                                                |
| `capacity evidence ... is unusable`    | snapshot absent, stale, incomplete or altered                              | collect it again with `make prod-capacity-evidence`; never point the deploy at a directory you did not produce or receive     |
| `capacity preflight refused its input` | a candidate manifest or evidence file that does not parse                  | read the `[ERROR] invalid ... input: line N` above it; collect the evidence again — the override does not apply to this case  |
| `cannot hold a second slot`            | quota or nodes genuinely too small                                         | raise quota or free capacity; do not force                                                                                    |
| `the shared stateful layer must exist` | `stateful.sh` was never run                                                | run `make prod-stateful-apply`, then bootstrap again                                                                          |
| PVC stuck `Pending`                    | host directory missing or wrongly owned                                    | create it as in 3b.2; do **not** delete the PV                                                                                |
| media-service never Ready              | the AWS LiveKit is unreachable from the pod, or `LIVEKIT_API_URL` is wrong | the `livekit-api` probe is failing; check 3b.3.3 and the egress policy `nchat-allow-livekit-api-egress`                       |
| `pv/X points at '...'`                 | an existing volume drifted from this overlay                               | reconcile by hand; the script will not delete a volume                                                                        |
| calls connect then drop                | no relay available for that client                                         | see 3b.3.4 — TURN strategy is an open item; check the AWS security group first. Nothing is forwarded on srv-apps-01 for media |
| token issued, browser never connects   | the CSP blocks the host, or the URL is not the AWS one                     | reconcile `NCHAT_WEB_LIVEKIT_CONNECT_SRC` with `LIVEKIT_API_URL`; `prod-blue-green-check` asserts they name one host          |
| cutover stopped part-way               | mixed state                                                                | re-run with the **same** `--target`                                                                                           |

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
It does not touch storage either: **no rollback, drain or cutover deletes a
PersistentVolume, a claim or a StatefulSet**, and none of the release scripts is
capable of it. If a rollback ever seems to call for removing a volume, it does
not — that is a data operation with its own review, taken from the backup in
3b.7.
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
[ ] Secrets provisioned: nchat-secrets, nchat-postgres-admin,
    nchat-postgres-runtime, nchat-postgres-migrator, nchat-file-encryption,
    ghcr-pull
[ ] LIVEKIT_API_KEY and LIVEKIT_API_SECRET sealed into nchat-prod/nchat-secrets
    (first release: dev's values, production's own Secret — see 3b.4.1)
[ ] no other Secret value copied from nchat-dev
[ ] host directories created and owned as in 3b.2
[ ] topology.env carries the confirmed LiveKit URL and CSP (3b.3)
[ ] AWS LiveKit reachable from both slots (3b.3.3)
[ ] dedicated production LiveKit key scheduled for a post-release window (3b.4.2)
[ ] make prod-stateful-apply run; postgres, valkey and seaweedfs Ready
[ ] four PVs Bound with reclaim policy Retain
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
[ ] audio call, video call, screen share and reconnect verified (3b.3.3)
[ ] TURN strategy determined and a relay-forced call tested (3b.3.4)
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
