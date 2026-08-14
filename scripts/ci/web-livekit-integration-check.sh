#!/usr/bin/env bash
# Exercises the real chain behind the LiveKit CSP allowlist (issue #528):
#
#   infra/k8s/overlays/nchat-dev-server (rendered)
#   -> Deployment/nchat-web container "web" env NCHAT_WEB_LIVEKIT_CONNECT_SRC
#   -> docker build -f Dockerfile.web
#   -> the image's own startup (no command/args override)
#   -> envsubst (restricted to that one variable)
#   -> /etc/nginx/conf.d/default.conf
#   -> nginx -t
#   -> a real HTTP request
#   -> the Content-Security-Policy header actually served
#
# scripts/ci/web-security-headers-check.sh proves the template and an isolated
# envsubst render are correct in isolation. It cannot catch a break in the
# wiring between those layers (a stale template path in Dockerfile.web, a
# Deployment that stops setting the env var, a startup command that silently
# fails to render). This script proves the wiring itself.
#
# A gate that reports "passed" because docker/kubectl/python3/curl is missing
# hides exactly the regression it exists to catch, so every dependency below
# is a hard requirement — no soft skip, in CI or locally. envsubst is
# deliberately NOT a host requirement: it never runs on the runner, only
# inside the container via the image's own CMD (Dockerfile.web); if it were
# missing from the image, the container would fail to come up and step 4
# below would catch that naturally.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
IMAGE_TAG="nchat-web:livekit-integration-check"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-web-livekit-check.XXXXXX")"
CONTAINER_ID=""

cleanup() {
  if [ -n "$CONTAINER_ID" ]; then
    docker rm -f "$CONTAINER_ID" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

for tool in python3 docker curl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "error: web LiveKit integration check requires '$tool'." >&2
    exit 1
  fi
done
if ! python3 -c "import yaml" >/dev/null 2>&1; then
  echo "error: web LiveKit integration check requires python3 with PyYAML." >&2
  exit 1
fi
if ! command -v kustomize >/dev/null 2>&1 && ! command -v kubectl >/dev/null 2>&1; then
  echo "error: web LiveKit integration check requires kustomize or kubectl." >&2
  exit 1
fi

# --- 1. Render infra/k8s/overlays/nchat-dev-server -----------------------
#
# Same rendering path as scripts/ci/k8s-manifests-check.sh: an isolated copy
# of infra/k8s so a generated topology.env never touches the real tree.
export NCHAT_DEV_TOPOLOGY_FILE="${NCHAT_DEV_TOPOLOGY_FILE:-$ROOT_DIR/scripts/ci/testdata/nchat-dev-topology.env}"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"
prepare_deploy_tree "$ROOT_DIR" "$WORK_DIR/tree"
OVERLAY="$WORK_DIR/tree/infra-k8s/overlays/nchat-dev-server"
RENDERED="$WORK_DIR/nchat-dev-server.yaml"

if command -v kustomize >/dev/null 2>&1; then
  kustomize build "$OVERLAY" >"$RENDERED"
else
  KUBECONFIG=/dev/null kubectl kustomize "$OVERLAY" >"$RENDERED"
fi
[ -s "$RENDERED" ] || { echo "error: rendered nchat-dev-server overlay is empty." >&2; exit 1; }

# --- 2. Extract the DEV value and the hardening contract from the render --
#
# PyYAML instead of line/grep parsing: the assertions below are about
# structure (which Deployment, which container, which env entry), not text
# position, so a reorder of unrelated fields must not break this check.
python3 - "$RENDERED" "$WORK_DIR" <<'PY'
import sys

import yaml

rendered, work = sys.argv[1:]
docs = [d for d in yaml.safe_load_all(open(rendered)) if d]

deployments = [
    d
    for d in docs
    if d.get("kind") == "Deployment" and d.get("metadata", {}).get("name") == "nchat-web"
]
if len(deployments) != 1:
    print(f"error: expected exactly one Deployment/nchat-web, found {len(deployments)}.", file=sys.stderr)
    sys.exit(1)
dep = deployments[0]
pod_spec = dep["spec"]["template"]["spec"]

containers = [c for c in pod_spec.get("containers", []) if c.get("name") == "web"]
if len(containers) != 1:
    print(f"error: expected exactly one container named 'web', found {len(containers)}.", file=sys.stderr)
    sys.exit(1)
web = containers[0]

env_entries = [e for e in web.get("env", []) if e.get("name") == "NCHAT_WEB_LIVEKIT_CONNECT_SRC"]
if len(env_entries) != 1:
    print(
        f"error: expected exactly one NCHAT_WEB_LIVEKIT_CONNECT_SRC env entry on container "
        f"'web', found {len(env_entries)}.",
        file=sys.stderr,
    )
    sys.exit(1)

value = (env_entries[0].get("value") or "").strip()
if not value:
    print("error: NCHAT_WEB_LIVEKIT_CONNECT_SRC is empty in the rendered nchat-dev-server overlay.", file=sys.stderr)
    sys.exit(1)

# The startup (envsubst + exec nginx) must come only from Dockerfile.web.
if web.get("command") or web.get("args"):
    print(
        "error: container 'web' must not set command/args in the Deployment; the image CMD "
        "(Dockerfile.web) is the single source of the startup sequence.",
        file=sys.stderr,
    )
    sys.exit(1)

pod_sc = pod_spec.get("securityContext") or {}
if pod_sc.get("runAsNonRoot") is not True:
    print(f"error: pod securityContext.runAsNonRoot must be true, got {pod_sc.get('runAsNonRoot')!r}.", file=sys.stderr)
    sys.exit(1)
if (pod_sc.get("seccompProfile") or {}).get("type") != "RuntimeDefault":
    print(
        f"error: pod securityContext.seccompProfile.type must be RuntimeDefault, got "
        f"{(pod_sc.get('seccompProfile') or {}).get('type')!r}.",
        file=sys.stderr,
    )
    sys.exit(1)
if pod_spec.get("automountServiceAccountToken") is not False:
    print(
        f"error: automountServiceAccountToken must be false, got "
        f"{pod_spec.get('automountServiceAccountToken')!r}.",
        file=sys.stderr,
    )
    sys.exit(1)

c_sc = web.get("securityContext") or {}
if c_sc.get("allowPrivilegeEscalation") is not False:
    print(
        f"error: container securityContext.allowPrivilegeEscalation must be false, got "
        f"{c_sc.get('allowPrivilegeEscalation')!r}.",
        file=sys.stderr,
    )
    sys.exit(1)
if c_sc.get("readOnlyRootFilesystem") is not True:
    print(
        f"error: container securityContext.readOnlyRootFilesystem must be true, got "
        f"{c_sc.get('readOnlyRootFilesystem')!r}.",
        file=sys.stderr,
    )
    sys.exit(1)
if (c_sc.get("capabilities") or {}).get("drop") != ["ALL"]:
    print(
        f"error: container securityContext.capabilities.drop must be ['ALL'], got "
        f"{(c_sc.get('capabilities') or {}).get('drop')!r}.",
        file=sys.stderr,
    )
    sys.exit(1)

# readOnlyRootFilesystem means /etc/nginx/conf.d needs exactly one writable
# emptyDir mount so the envsubst-rendered fragment can land there.
conf_d_mounts = [vm["name"] for vm in web.get("volumeMounts", []) if vm.get("mountPath") == "/etc/nginx/conf.d"]
if len(conf_d_mounts) != 1:
    print(
        f"error: expected exactly one volumeMount at /etc/nginx/conf.d, found {len(conf_d_mounts)}.",
        file=sys.stderr,
    )
    sys.exit(1)
volumes = {v["name"]: v for v in pod_spec.get("volumes", [])}
conf_d_volume = volumes.get(conf_d_mounts[0])
if conf_d_volume is None or "emptyDir" not in conf_d_volume:
    print(f"error: volume backing /etc/nginx/conf.d must be an emptyDir, got {conf_d_volume!r}.", file=sys.stderr)
    sys.exit(1)

with open(f"{work}/livekit_connect_src.txt", "w") as f:
    f.write(value)

print(f"  [OK] Deployment/nchat-web container 'web': NCHAT_WEB_LIVEKIT_CONNECT_SRC={value!r}")
print("  [OK] no command/args override on container 'web' (startup owned by Dockerfile.web)")
print("  [OK] hardening contract preserved (runAsNonRoot, seccompProfile, automountServiceAccountToken,")
print("       allowPrivilegeEscalation, readOnlyRootFilesystem, capabilities.drop)")
print("  [OK] /etc/nginx/conf.d backed by exactly one writable emptyDir")
PY

DEV_VALUE="$(cat "$WORK_DIR/livekit_connect_src.txt")"

# --- 3. Build the real image -----------------------------------------------

docker build -f "$ROOT_DIR/Dockerfile.web" -t "$IMAGE_TAG" "$ROOT_DIR" >&2

# --- 4. Run it with the image's own startup, no command override -----------
#
# Binds to a random loopback port so parallel runs never collide.
CONTAINER_ID="$(
  docker run -d --rm \
    -p 127.0.0.1::8080 \
    -e NCHAT_WEB_LIVEKIT_CONNECT_SRC="$DEV_VALUE" \
    "$IMAGE_TAG"
)"

HOST_PORT=""
for _ in $(seq 1 30); do
  HOST_PORT="$(docker port "$CONTAINER_ID" 8080/tcp 2>/dev/null | head -1 | sed -E 's/.*:([0-9]+)$/\1/')"
  if [ -n "$HOST_PORT" ] && curl -fsS --max-time 2 -o /dev/null "http://127.0.0.1:$HOST_PORT/healthz" 2>/dev/null; then
    break
  fi
  HOST_PORT=""
  sleep 0.5
done
if [ -z "$HOST_PORT" ]; then
  echo "error: container never became ready on /healthz." >&2
  docker logs "$CONTAINER_ID" >&2 || true
  exit 1
fi

# --- 5. nginx -t against the config the running container actually loaded --

if ! docker exec "$CONTAINER_ID" nginx -t; then
  echo "error: nginx -t failed inside the running container." >&2
  docker exec "$CONTAINER_ID" cat /etc/nginx/conf.d/default.conf >&2 || true
  exit 1
fi

# --- 6. Real HTTP request; validate the served CSP -------------------------

HEADERS="$(curl -fsS --max-time 10 -o /dev/null -D - "http://127.0.0.1:$HOST_PORT/healthz")"

printf '%s' "$HEADERS" >"$WORK_DIR/headers.txt"
printf '%s' "$DEV_VALUE" >"$WORK_DIR/dev_value.txt"

python3 - "$WORK_DIR" <<'PY'
import re
import sys

work = sys.argv[1]
headers = open(f"{work}/headers.txt", encoding="utf-8", errors="replace").read()
dev_value = open(f"{work}/dev_value.txt").read().strip()
dev_tokens = set(dev_value.split())

csp_lines = [
    line for line in headers.splitlines() if re.match(r"(?i)^content-security-policy:", line)
]
if len(csp_lines) != 1:
    print(f"error: expected exactly one Content-Security-Policy header, found {len(csp_lines)}.", file=sys.stderr)
    sys.exit(1)

if re.search(r"(?im)^content-security-policy-report-only:", headers):
    print("error: Content-Security-Policy-Report-Only must not be served alongside the enforced policy.", file=sys.stderr)
    sys.exit(1)

policy = csp_lines[0].split(":", 1)[1].strip()
directives = {}
for part in policy.split(";"):
    part = part.strip()
    if not part:
        continue
    name, _, rest = part.partition(" ")
    directives[name] = rest.strip()

connect_src = directives.get("connect-src")
if connect_src is None:
    print("error: served CSP has no connect-src directive.", file=sys.stderr)
    sys.exit(1)
tokens = set(connect_src.split())

# $host must have resolved to a concrete authority, never survived literally
# and never been swallowed by envsubst into an empty/missing token.
host_wss = {t for t in tokens if t.startswith("wss://") and t not in dev_tokens}
host_https = {t for t in tokens if t.startswith("https://") and t not in dev_tokens}
if len(host_wss) != 1 or len(host_https) != 1:
    print(
        f"error: connect-src must contain exactly one wss://<host> and one https://<host> "
        f"besides the LiveKit DEV sources; got wss={host_wss!r} https={host_https!r}.",
        file=sys.stderr,
    )
    sys.exit(1)
observed_host_wss = next(iter(host_wss))
observed_host_https = next(iter(host_https))
if "$host" in observed_host_wss or "$host" in observed_host_https:
    print("error: nginx did not resolve $host at request time.", file=sys.stderr)
    sys.exit(1)
if observed_host_wss.removeprefix("wss://") != observed_host_https.removeprefix("https://"):
    print(f"error: wss and https host mismatch: {observed_host_wss!r} vs {observed_host_https!r}.", file=sys.stderr)
    sys.exit(1)

expected = {"'self'", observed_host_wss, observed_host_https} | dev_tokens
if tokens != expected:
    print(f"error: connect-src {tokens!r} does not match the expected set {expected!r}.", file=sys.stderr)
    sys.exit(1)

for name, value in directives.items():
    dtokens = set(value.split())
    if "*" in dtokens:
        print(f"error: {name} must not contain the wildcard '*'.", file=sys.stderr)
        sys.exit(1)
    if name == "connect-src" and ({"https:", "wss:"} & dtokens):
        print(f"error: connect-src must not liberate a bare scheme: {dtokens & {'https:', 'wss:'}!r}.", file=sys.stderr)
        sys.exit(1)

print(f"  [OK] exactly one Content-Security-Policy header, no Report-Only")
print(f"  [OK] connect-src == {sorted(tokens)}")
print(f"  [OK] $host resolved to {observed_host_wss.removeprefix('wss://')!r} at request time")
PY

echo "Web LiveKit integration check passed."
