# #1129 E — dev-only capacity/isolation candidate (NOT approved for rollout)

This directory is deliberately outside the default dev overlay and has no
`kustomization.yaml`. It composes **only** `overlays/nchat-dev-server`; neither
base nor production nor the default dev release changes. Kustomize rejects an
ancestor overlay referenced from its own child, hence this sibling of B under
`candidates/`. Do not add it to the default overlay or GitOps reconciliation.

## Design and trust boundaries

Previously the file API also hosted scan and preview consumers. The candidate
uses the same file-service image for API (`api`), scan (`scan-worker`) and
preview (`preview-worker`). API retains cleanup, draft expiry, URL processing
and synchronous audio conversion. Workers expose only internal health/metrics;
no ingress route or API Service selects them. Migrations remain the separate
migration Job, never a worker responsibility. Application mode selection,
cancellation and fenced claims are implemented/tested separately from this IaC.

Shared-process resource flags cannot isolate CPU/memory failures. Separate
processes are the smallest boundary using the existing image. Cost pools need
Entrega F classification and are deferred. Fixed capacity is intentional:
there is no verified Kubernetes metrics adapter/KEDA discovery here. No HPA,
controller permissions, autoscaler installation or scale-to-zero is introduced.

| Role    | Replicas | Scan concurrency/process | CPU request/limit  | Memory request/limit |
| ------- | -------- | ------------------------ | ------------------ | -------------------- |
| API     | 1        | no scan consumer         | inherited 25m/250m | inherited 64Mi/512Mi |
| Scan    | 1        | 1                        | 50m/500m           | 128Mi/512Mi          |
| Preview | 1        | no scan consumer         | 50m/500m           | 128Mi/512Mi          |

Worker budgets are conservative initial allocations, **not measured capacity**.
Both request 64Mi ephemeral storage, limit 1Gi and have private 64Mi `/tmp`.
Recreate prevents worker surge during updates; 60s grace allows cancellation and
bounded cleanup, not completion of a 300s scan. Grace needs operational fault
validation; interrupted jobs recover through application leases/fencing. An
external manual replica change is not a concurrency limiter: the parent global
claim-capacity implementation must be validated before rollout. No claim of
measured throughput or global concurrency enforcement is made by these manifests.
ClamAV replica count, MaxThreads and timeout budgets are not increased.

Workers have distinct SAs, no token automount/RBAC, non-root UID65532, seccomp,
read-only rootfs and no capabilities. Both receive DB and attachment encryption Secret keys; only scan receives
optional Valkey for verdict publication; no JWT or Cloudflare credential. The
existing runtime DB role and encryption key ring remain shared: SA separation
does not create independent DB or object-level authorization. Shared ConfigMap
contains configuration, not secret credentials.

Default-deny remains. Worker egress is limited to PostgreSQL5432, SeaweedFS filer8888 and kube-dns53;
only scan also reaches Valkey6379. Preview has no publisher, Valkey credential
or Valkey ingress/egress permission. Only scan reaches Guardian3310; only preview
and API reach converter8089. Both halves of each network flow are supplied.
The two B/legacy consumer ingress policies are narrowed to scan-worker together,
because NetworkPolicies are additive. API loses scanner access. Guardian has no
outbound access; B updater remains pinned to its separately approved mirror.
Metrics ingress permits only component=prometheus in nchat-dev, port8083.
A compromised worker cannot reach the API, converter from scan, or public HTTPS.
API retains its existing link-safety egress; workers do not inherit it.

## Mandatory B prerequisites — SEC-B-002 remains blocking

The operational dev config has no approved signature max age and points at raw
ClamAV TCP3310. `NewGuarded` needs a positive max age and Guardian HTTP; setting
86400 alone does not repair that mismatch. B's Guardian candidate is dormant and
its trust/receipt-authenticity decision SEC-B-002 remains unresolved. **Do not
activate B, fabricate an approval, or use this candidate with raw clamd.** See
[the B runbook](../../../../docs/runbooks/clamav-signatures-1129.md) and
[the D limitations](../../../../docs/observability/clamav-1129-delivery-d.md).

In an isolated temporary copy of the full infra/k8s tree, prepare reviewed B
artifacts using its existing templates/renderer and approved inputs. Place
`consumer-patch.yaml`, `storage.yaml`, `updater.yaml`, `network-policy.yaml`
under this candidate's `guardian/` directory. This explicit path is mandatory;
do not include B's full kustomization, which would duplicate Deployment/clamav.
Copy `kustomization.yaml.in` to `kustomization.yaml` only in that temporary tree;
replace `__APPROVED_MAX_AGE_SECONDS__` with the same approved age as B and DB
policy, and `__APPROVED_FILE_SERVICE_DIGEST__` with the reviewed mode-capable
image digest. Reject any remaining `__...__` placeholder. Keep B's `replicas: 0`
and suspended updater until its separate approval/promotion procedure completes.
This produces a dormant **integration candidate**, not an executable rollout plan.

The fixed image name allows the existing dev digest substitution to cover all
three Deployments, proven by the config test. No image inventory/global CD change
is needed for substitution. Existing CD wait/smoke inventory does **not** include
candidate workers; candidate promotion must explicitly verify both rollouts.

### Render in a temporary tree, never apply

Run from the repository root in Bash **only after obtaining real reviewed inputs**.
Export `NCHAT_DEV_TOPOLOGY_FILE`, `DIGEST_ARTIFACTS_DIRECTORY` (the existing dev
release's complete digest artifact directory), `HELPER_IMAGE`, `STORAGE_CLASS`,
`APPROVAL`, `MIRROR`, `MIRROR_CIDR`, `MAX_AGE`. These are inputs to B's existing
validator, not defaults or a new approval path. The age must match the approved
DB policy; SEC-B-002 still blocks deployment even if rendering succeeds.

```bash
set -euo pipefail
: "${NCHAT_DEV_TOPOLOGY_FILE:?}" "${DIGEST_ARTIFACTS_DIRECTORY:?}"
: "${HELPER_IMAGE:?}" "${STORAGE_CLASS:?}" "${APPROVAL:?}"
: "${MIRROR:?}" "${MIRROR_CIDR:?}" "${MAX_AGE:?}"
export HELPER_IMAGE STORAGE_CLASS APPROVAL MIRROR MIRROR_CIDR MAX_AGE
source scripts/deploy/nchat-dev/lib.sh
validate_digest_artifacts "$DIGEST_ARTIFACTS_DIRECTORY"
export CAPACITY_RENDER_DIR
CAPACITY_RENDER_DIR="$(mktemp -d /tmp/nchat-capacity-render.XXXXXX)"
prepare_deploy_tree "$PWD" "$CAPACITY_RENDER_DIR"
# First validate B inputs using B's unchanged validator/renderer.
python3 scripts/testing/render-clamav-signatures.py \
  --helper-image "$HELPER_IMAGE" --storage-class "$STORAGE_CLASS" \
  --approval "$APPROVAL" --mirror "$MIRROR" --mirror-cidr "$MIRROR_CIDR" \
  --max-age "$MAX_AGE" > "$CAPACITY_RENDER_DIR/guardian-standalone.yaml"
for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
  set_digest_image "$CAPACITY_RENDER_DIR/infra-k8s/overlays/nchat-dev-server" \
    "ghcr.io/nicrepository/nchat/$image" \
    "$DIGEST_ARTIFACTS_DIRECTORY/digest-$image.txt"
done
export FILE_SERVICE_DIGEST
FILE_SERVICE_DIGEST="$(cat "$DIGEST_ARTIFACTS_DIRECTORY/digest-file-service.txt")"
python3 - <<'PYRENDER'
import os
from pathlib import Path
root = Path(os.environ['CAPACITY_RENDER_DIR']) / 'infra-k8s'
candidate = root / 'candidates/clamav-capacity-isolation'
guardian = candidate / 'guardian'
guardian.mkdir()  # refuse to overwrite a previous candidate
keys = ('HELPER_IMAGE', 'STORAGE_CLASS', 'APPROVAL', 'MIRROR', 'MIRROR_CIDR', 'MAX_AGE')
for source in (root / 'candidates/clamav-signatures').glob('*.yaml.in'):
    text = source.read_text()
    for key in keys:
        text = text.replace('__' + key + '__', os.environ[key])
    assert '__' not in text, 'unresolved B placeholder'
    (guardian / source.name.removesuffix('.in')).write_text(text)
text = (candidate / 'kustomization.yaml.in').read_text()
text = text.replace('__APPROVED_MAX_AGE_SECONDS__', os.environ['MAX_AGE'])
text = text.replace('__APPROVED_FILE_SERVICE_DIGEST__', os.environ['FILE_SERVICE_DIGEST'].removeprefix('sha256:'))
assert '__' not in text, 'unresolved E placeholder'
(candidate / 'kustomization.yaml').write_text(text)
PYRENDER
validate_rendered_overlay \
  "$CAPACITY_RENDER_DIR/infra-k8s/candidates/clamav-capacity-isolation" \
  "$CAPACITY_RENDER_DIR/candidate.yaml"
trivy config --severity HIGH,CRITICAL --exit-code 1 "$CAPACITY_RENDER_DIR/candidate.yaml"
printf 'Dormant candidate only, NOT approval to deploy: %s/candidate.yaml\n' "$CAPACITY_RENDER_DIR"
```

No `kubectl`, `apply`, Git writes or cluster installation occurs. Temporary output
may be removed after inspection. Guardian still has zero replicas and its updater
is suspended. Do not use the synthetic config-test fixture as a release artifact.

## Validation and pending rollout/rollback

Safe local check (synthetic fixtures in /tmp, no cluster calls):

```sh
python3 scripts/ci/test_file_worker_isolation.py
bash scripts/ci/k8s-manifests-check.sh
bash scripts/ci/nchat-dev-deployment-check.sh
trivy config --severity HIGH,CRITICAL --exit-code 1 <reviewed-rendered-candidate.yaml>
```

Scan rendered resources, not partial strategic-merge patches: those omit inherited
securityContext fields and produce misleading standalone findings.

The test uses **TEST-ONLY-NOT-APPROVED** values including max-age86400 and dummy
registry digests. They are never promoted or written to the default overlay.
It composes B without starting it, checks roles, least privilege, both network
halves, digest equality and rejects unsafe configuration mutations. Rendering
is not evidence of running scanner efficacy. Probes require the parent worker
mode contract: `/healthz` process liveness, `/readyz` worker readiness, `/metrics`;
a dependency outage must not make liveness restart the worker.

After B approval, mode/fencing tests and independent reviews, a separately
approved versioned promotion must: render with actual digests, validate CNI,
RWX/mirror/DB signature policy/clocks and quota; remove old API consumers before
starting the new scan consumer unless global capacity is proven; activate the
Guardian via B's approved procedure; verify worker rollout, queue recovery and
benign/EICAR denial/download integrity; run termination/backlog fault tests.
Do not apply this template automatically. No cluster mutation was performed.

Read-only inspection after an authorized promotion can use:

```sh
kubectl -n nchat-dev rollout status deployment/file-scan-worker
kubectl -n nchat-dev rollout status deployment/file-preview-worker
kubectl -n nchat-dev get pods -l app.kubernetes.io/component=file-scan-worker
```

Worker Services carry metrics annotations, but no Kubernetes Prometheus discovery
is provisioned in this repository (the existing scrape config targets Compose).
Annotations alone do not collect data. Before operational validation, wire the
approved collector to **all** worker endpoints using job=nchat-go-services and
service=file-service; preserve per-instance histograms/counters and existing
max aggregation for authoritative queue gauges. No claim that dashboards now
observe Kubernetes workers. No automatic scaling until that pipeline is verified.

Rollback must be versioned and rehearsed after B trust approval. Stop candidate
consumers and prove cancellation/lease recovery before restoring an older API
that also consumes jobs; never run an old combined consumer alongside an
unverified capacity configuration. Preserve fresh approved Guardian/signature
policy; do not restore a raw TCP scanner as a working fallback. If no compatible
approved version exists, leave workers stopped and files pending. Reverting E
manifests alone is not a demonstrated functional rollback of B. SEC-B-002,
mode-capable image, DB grants/policy, RWX/CNI, metrics discovery, quota and real
burst/fault benchmarks remain operational blockers, not passed acceptance items.
