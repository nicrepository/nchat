#!/usr/bin/env bash
# Validates the /api/auth routing contract end to end (issue #425).
#
# The bug this guards against was not a missing route: it was three layers
# disagreeing about the path. The browser calls /api/auth/admin/users, the
# auth-service router registers /auth/admin/users, and only a gateway rewrite
# joins them. That rewrite existed in the local Traefik config and in the live
# nchat-dev cluster, but in no Kubernetes manifest — so a fresh apply of any
# overlay served 404 for every auth route.
#
# Reading the overlay sources is what let that gap go unnoticed: the middleware
# is a separate resource from the Ingress that must reference it, and a
# reference is only correct once kustomize has resolved namespaces and name
# prefixes. This check therefore renders each overlay and inspects the result.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
RENDER="$ROOT_DIR/scripts/k8s/k8s-render.sh"
OVERLAYS=(k3s-dev k3s-staging nchat-dev-server)
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

if ! command -v python3 >/dev/null 2>&1 || ! python3 -c "import yaml" >/dev/null 2>&1; then
  # Failing is deliberate: a contract gate that silently skips reports "passed"
  # for exactly the configuration it exists to reject.
  echo "error: auth route contract check requires python3 with PyYAML." >&2
  exit 1
fi

for overlay in "${OVERLAYS[@]}"; do
  if ! "$RENDER" "infra/k8s/overlays/$overlay" >"$WORK_DIR/$overlay.yaml" 2>"$WORK_DIR/$overlay.err"; then
    # kubectl's embedded kustomize cannot apply the multi-document $patch:
    # delete used by nchat-dev-server; the standalone binary can.
    if command -v kustomize >/dev/null 2>&1 &&
      kustomize build "$ROOT_DIR/infra/k8s/overlays/$overlay" >"$WORK_DIR/$overlay.yaml" 2>>"$WORK_DIR/$overlay.err"; then
      :
    else
      echo "error: failed to render overlay $overlay" >&2
      cat "$WORK_DIR/$overlay.err" >&2
      exit 1
    fi
  fi
done

python3 - "$ROOT_DIR" "$WORK_DIR" "${OVERLAYS[@]}" <<'PY'
import re
import sys

import yaml

root, work, *overlays = sys.argv[1:]

MW_NAME = "auth-api-prefix"
ANNOTATION = "traefik.ingress.kubernetes.io/router.middlewares"
PUBLIC_PREFIX = "/api/auth"
EXPECTED_REGEX = "^/api/auth/(.*)"
EXPECTED_REPLACEMENT = "/auth/${1}"

# The contract, stated once. Every layer below is checked against this.
# The routes this check names explicitly. Keep it to a couple of representative
# paths — the exhaustive check is derived from routes.go below, so this list
# does not have to be updated every time a route is added, and does not
# accidentally assert a route that belongs to a different change.
CONTRACT = {
    "/api/auth/admin/users": "/auth/admin/users",
    "/api/auth/admin/invites": "/auth/admin/invites",
    "/api/auth/login": "/auth/login",
}
# Paths that must survive the middleware untouched: the annotation applies to
# every router the shared Ingress generates, so a regex that is too broad would
# silently rewrite other services.
UNTOUCHED = ["/api/chat/sidebar", "/api/files/upload", "/api/admin/healthz", "/"]

errors = []
specs = {}


def rewrite(path, regex, replacement):
    """Traefik replacePathRegex: non-matching paths pass through unchanged."""
    compiled = re.compile(regex)
    if not compiled.search(path):
        return path
    return compiled.sub(replacement.replace("${1}", r"\1"), path)


for overlay in overlays:
    docs = [d for d in yaml.safe_load_all(open(f"{work}/{overlay}.yaml")) if d]

    middlewares = {
        (d["metadata"].get("namespace"), d["metadata"]["name"]): d
        for d in docs
        if d.get("kind") == "Middleware"
    }
    auth_ingresses = [
        d
        for d in docs
        if d.get("kind") == "Ingress"
        and any(
            p.get("path") == PUBLIC_PREFIX
            for r in d["spec"].get("rules", [])
            for p in r.get("http", {}).get("paths", [])
        )
    ]

    if not auth_ingresses:
        errors.append(f"{overlay}: no Ingress serves {PUBLIC_PREFIX}")
        continue

    for ing in auth_ingresses:
        meta = ing["metadata"]
        ns = meta.get("namespace")
        refs = (meta.get("annotations") or {}).get(ANNOTATION, "")
        expected_ref = f"{ns}-{MW_NAME}@kubernetescrd"

        if expected_ref not in [r.strip() for r in refs.split(",") if r.strip()]:
            errors.append(
                f"{overlay}: Ingress {meta['name']} serves {PUBLIC_PREFIX} but does not "
                f"reference {expected_ref} (found: {refs or 'no middlewares annotation'})"
            )
            continue

        mw = middlewares.get((ns, MW_NAME))
        if mw is None:
            errors.append(
                f"{overlay}: Ingress {meta['name']} references {expected_ref} but no "
                f"Middleware {MW_NAME} is rendered in namespace {ns}"
            )
            continue

        rpr = (mw.get("spec") or {}).get("replacePathRegex")
        if not rpr:
            errors.append(f"{overlay}: Middleware {MW_NAME} must use replacePathRegex")
            continue

        regex, replacement = rpr.get("regex", ""), rpr.get("replacement", "")
        specs[overlay] = (regex, replacement)

        if not regex.startswith("^/api/auth/"):
            errors.append(
                f"{overlay}: rewrite regex {regex!r} is broader than /api/auth"
            )
            continue

        for public, internal in CONTRACT.items():
            got = rewrite(public, regex, replacement)
            if got != internal:
                errors.append(
                    f"{overlay}: {public} rewrites to {got!r}, expected {internal!r}"
                )
        for path in UNTOUCHED:
            got = rewrite(path, regex, replacement)
            if got != path:
                errors.append(
                    f"{overlay}: {path} must pass through unchanged, got {got!r}"
                )

# Every overlay must implement the same rule; a divergence is how one
# environment starts answering 404 while the others work.
distinct = set(specs.values())
if len(distinct) > 1:
    errors.append(f"overlays disagree on the rewrite rule: {specs}")
missing = [o for o in overlays if o not in specs]
if missing:
    errors.append(f"no validated rewrite rule rendered for: {', '.join(missing)}")

# The local Traefik gateway must implement the identical rule, or local dev and
# the clusters disagree about the contract.
local = yaml.safe_load(open(f"{root}/infra/traefik/local/dynamic.yml"))
local_rpr = (
    local.get("http", {})
    .get("middlewares", {})
    .get("rewrite-auth-api-prefix", {})
    .get("replacePathRegex")
)
if not local_rpr:
    errors.append("local gateway is missing the rewrite-auth-api-prefix middleware")
elif distinct and (local_rpr.get("regex"), local_rpr.get("replacement")) not in distinct:
    errors.append(
        f"local gateway rewrite {local_rpr} differs from the Kubernetes overlays {distinct}"
    )

# The Go router must register the internal paths and must not carry a public
# alias: an alias would make the gateway rewrite untestable and give the
# admin API a second, unguarded-looking entry point.
routes_src = open(f"{root}/services/auth-service/internal/http/routes.go").read()
declared = set(re.findall(r'"(/[^"]*)"', routes_src))
for internal in CONTRACT.values():
    if internal not in declared:
        errors.append(f"auth-service router does not register {internal}")
for alias in declared:
    if alias.startswith("/api/"):
        errors.append(f"auth-service router must not alias the public prefix: {alias}")

# Every /auth/* route the router actually registers must be reachable through
# the public prefix. Deriving this from routes.go rather than restating it
# keeps the check exhaustive as routes come and go, and keeps it honest about
# which branch it is running on.
if distinct:
    regex, replacement = next(iter(distinct))
    for internal in sorted(r for r in declared if r.startswith("/auth/")):
        public = "/api" + internal
        got = rewrite(public, regex, replacement)
        if got != internal:
            errors.append(f"registered route {internal} is unreachable: {public} -> {got!r}")

# The frontend must aim at the public contract, not at the internal path and
# not at /api/admin (which is routed to admin-service and has no user routes).
api_src = open(f"{root}/apps/web/src/admin/adminUsersApi.ts").read()
base = re.search(r'VITE_ADMIN_API_BASE_URL \?\? "([^"]+)"', api_src)
if not base:
    errors.append("could not determine the frontend admin API base URL")
elif base.group(1) != "/api/auth/admin":
    errors.append(
        f"frontend admin base is {base.group(1)!r}, expected '/api/auth/admin'"
    )

if errors:
    print("auth route contract check FAILED:", file=sys.stderr)
    for e in errors:
        print(f"  - {e}", file=sys.stderr)
    sys.exit(1)

for overlay in overlays:
    regex, replacement = specs[overlay]
    print(f"  [OK]   {overlay}: {regex} -> {replacement}")
for public, internal in CONTRACT.items():
    print(f"  [OK]   {public} -> {internal}")
print("Auth route contract check passed.")
PY
