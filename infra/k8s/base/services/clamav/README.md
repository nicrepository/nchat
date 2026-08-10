# clamav

Antimalware daemon for attachment scanning (RF-22, issue #483).

## Why it is here and not in the base kustomization

The directory sits under `base/services/` for exactly one reason: `clamd.conf`
has two consumers — this Kustomization and the Docker Compose stack — and
kustomize refuses to read files outside its own root, while
`scripts/deploy/nchat-dev/lib.sh` copies only `infra/k8s` into the deploy tree.
One file under `infra/k8s` is the only arrangement that keeps local and cluster
from drifting. It is the same reason `upload-guard/nginx.conf.template` lives
beside its manifests.

**`base/kustomization.yaml` does not list this directory.** Only
`overlays/nchat-dev-server/kustomization.yaml` references it, so `base`,
`k3s-dev` and `k3s-staging` render no ClamAV at all. Those environments do not
enable uploads, and a 1 GiB daemon they never call is cost without a purpose.

## Exposure

`ClusterIP` on 3310, reachable only from `app.kubernetes.io/component: file`
via the `nchat-allow-clamav` NetworkPolicy. clamd authenticates nobody — reach
is the whole access control — so there is no Ingress, IngressRoute, NodePort,
LoadBalancer or hostPort for it, and it gets no DNS egress either.

## Signatures

No freshclam, no PVC, no egress. An init container copies the database out of
the pinned image into an `emptyDir` that the daemon then mounts at
`/var/lib/clamav`. A Kubernetes `emptyDir` does not inherit image content the
way a Docker named volume does, so mounting one directly would hide the
database and clamd would refuse to start.

The copy fails loudly rather than half-succeeding: missing source directory, no
`*.cvd`/`*.cld`/`*.cud` present, an unexpected symlink, an empty destination or
a file the daemon's uid cannot read all exit non-zero, and the daemon container
never starts. Nothing is approved while it is down, which is the correct
outcome.

Updating signatures means bumping the image digest, deliberately, in a commit.

## Probes

`clamdcheck.sh`, the image's own health check, is **not** used. It expects the
unix socket that this configuration does not create, and was observed reporting
`ERROR: Unable to contact server` against a daemon that was answering `PONG` on
3310 and completing real INSTREAM scans. Probes here speak the protocol
file-service speaks, on the port file-service uses:

| Probe     | Mechanism                   | Why                                                     |
| --------- | --------------------------- | ------------------------------------------------------- |
| startup   | `clamdscan --ping 1`        | semantic; ~10 min of headroom for the signature load     |
| readiness | `clamdscan --ping 1`        | an open socket does not prove the engine finished loading |
| liveness  | `tcpSocket: 3310`           | cheap and unhurried, so it never restarts a live scan     |

## Entrypoint

`/init-unprivileged` with an explicit `clamd --foreground`. Left to default,
that script starts clamd and then waits for `/tmp/clamd.sock`, giving up after
`CLAMD_STARTUP_TIMEOUT`; with a TCP-only config the socket never appears, so
the container would fail its own start-up while the daemon worked. Passing a
command takes the entrypoint's first branch, which `exec`s it under tini and
skips both the socket wait and freshclam.

## clamd.conf

The limits are not decoration — see the comments in the file itself. Two of
them are security properties rather than tuning:

- **`AlertExceedsMax yes`** — without it, exceeding `MaxFileSize`,
  `MaxScanSize`, `MaxFiles` or `MaxRecursion` makes clamd stop inspecting and
  answer `OK`, which file-service records as a clean verdict over a partly
  examined file. With it, the same event answers
  `Heuristics.Limits.Exceeded FOUND` and the attachment is rejected.
- **`MaxScanTime 420000`** — the engine's ceiling must sit *behind*
  file-service's 300 s deadline (and behind the worker's 330 s claim lease), so
  the external timeout is what decides an unfinished scan. The order
  300 s < 330 s < 420 s is asserted by `scripts/ci/k8s-manifests-check.sh`.

`User`, `LocalSocket`, `PidFile` and `ForceToDisk` are absent on purpose; each
absence is explained in the file.
