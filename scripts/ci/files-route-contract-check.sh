#!/usr/bin/env bash
# Validates the /api/files routing contract end to end.
#
# Same class of bug as issue #425, third instance. file-service registers
# /attachments/{id}/content — see services/file-service/internal/http/routes.go,
# which says outright that the gateway owns the /api/files prefix — so the
# gateway must strip it. The dedicated upload IngressRoute did (strip-files-prefix
# was written for it), the shared Ingress did not, and the asymmetry is exactly
# what made the bug look impossible: uploads succeeded, scanning succeeded,
# previews rendered, and then every GET under /api/files answered 404 without a
# single line in file-service's download or preview logs, because the request
# never reached a registered route.
#
# The reference is only correct after kustomize has resolved namespaces and name
# prefixes, so this reads the rendered overlay, not the sources — and it walks
# the whole middleware chain the Ingress declares, so a rewrite added later for
# some other service cannot quietly change what file-service receives.
#
# Not re-asserted here, because scripts/ci/gateway-config-check.sh already owns
# them with self-tests: that the dedicated upload route out-prioritises every
# competing route, and that no other rendered route can carry an attachment POST
# to file-service around the streaming cap.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
RENDER="$ROOT_DIR/scripts/k8s/k8s-render.sh"
# Only the overlay that carries the fix. k3s-dev and k3s-staging still forward
# /api/files verbatim (they also lack media-api-prefix), so listing them here
# would assert a contract they do not implement yet.
OVERLAYS=(nchat-dev-server)
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

if ! command -v python3 >/dev/null 2>&1 || ! python3 -c "import yaml" >/dev/null 2>&1; then
  # Failing is deliberate: a contract gate that silently skips reports "passed"
  # for exactly the configuration it exists to reject.
  echo "error: files route contract check requires python3 with PyYAML." >&2
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

ANNOTATION = "traefik.ingress.kubernetes.io/router.middlewares"
PUBLIC_PREFIX = "/api/files"
FILES_MW = "strip-files-prefix"
# The middlewares the shared Ingress must keep referencing. Adding the files
# rewrite must not cost the two that were already there.
REQUIRED_MW = ["auth-api-prefix", "media-api-prefix", FILES_MW]
UPLOAD_RULE = "PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)"

# Paths that must survive the Ingress's whole middleware chain untouched. The
# annotation applies to every router the shared Ingress generates, so a rewrite
# that is broader than its own prefix would silently maul another service.
# /api/admin is deliberately absent: since issue #578 the Admin API is published
# only on the administrative host, so it never reaches this Ingress at all. That
# separation has its own gate in scripts/ci/admin-route-contract-check.sh. The
# prefixes below are the ones no middleware in this chain may touch.
UNTOUCHED = [
    "/api/chat/channels/c1/messages",
    "/api/notifications/subscriptions",
    "/api/search/messages?q=x",
    "/",
    "/index.html",
]

errors = []


def strip_prefix(path, prefixes):
    """Traefik stripPrefix, as observed on traefik:v3.6."""
    for prefix in prefixes:
        if path.startswith(prefix):
            return path[len(prefix):] or "/"
    return path


def replace_path_regex(path, regex, replacement):
    """Traefik replacePathRegex: non-matching paths pass through unchanged."""
    compiled = re.compile(regex)
    if not compiled.search(path):
        return path
    return compiled.sub(replacement.replace("${1}", r"\1"), path)


# Pins the two behaviours the simulation above depends on. A helper that
# stripped unconditionally, or that returned "" for the bare prefix, would make
# every assertion below meaningless while still passing.
assert strip_prefix("/api/chat/x", ["/api/files"]) == "/api/chat/x"
assert strip_prefix("/api/files", ["/api/files"]) == "/"
assert strip_prefix("/api/files/attachments/a/content", ["/api/files"]) == "/attachments/a/content"


def apply_chain(path, chain):
    """Run a path through an ordered list of Middleware specs."""
    for mw in chain:
        spec = mw.get("spec") or {}
        if "stripPrefix" in spec:
            path = strip_prefix(path, spec["stripPrefix"].get("prefixes", []))
        elif "replacePathRegex" in spec:
            rpr = spec["replacePathRegex"]
            path = replace_path_regex(path, rpr.get("regex", ""), rpr.get("replacement", ""))
    return path


# The public contract, derived from the route constants file-service actually
# registers rather than restated here, so a route added later is covered without
# touching this file — and a route renamed without a gateway change fails.
routes_src = open(f"{root}/services/file-service/internal/http/routes.go").read()
internal_routes = sorted(set(re.findall(r'Route\w+\s*=\s*"(/[^"]*)"', routes_src)))
if not internal_routes:
    errors.append("could not read any route constant from file-service routes.go")
for internal in internal_routes:
    if internal.startswith("/api/"):
        errors.append(f"file-service must not alias the public prefix: {internal}")

for overlay in overlays:
    docs = [d for d in yaml.safe_load_all(open(f"{work}/{overlay}.yaml")) if d]

    middlewares = {
        (d["metadata"].get("namespace"), d["metadata"]["name"]): d
        for d in docs
        if d.get("kind") == "Middleware"
    }

    def files_paths(ing):
        return [
            p
            for r in ing["spec"].get("rules", [])
            for p in r.get("http", {}).get("paths", [])
            if p.get("path") == PUBLIC_PREFIX
        ]

    files_ingresses = [d for d in docs if d.get("kind") == "Ingress" and files_paths(d)]
    if not files_ingresses:
        errors.append(f"{overlay}: no Ingress serves {PUBLIC_PREFIX}")
        continue

    for ing in files_ingresses:
        meta = ing["metadata"]
        ns = meta.get("namespace")
        label = f"{overlay}: Ingress {meta['name']}"

        # The prefix must still reach file-service: a rewrite that lands on the
        # right path at the wrong service is the same 404 with better logs.
        for path in files_paths(ing):
            backend = (path.get("backend") or {}).get("service", {}).get("name")
            if backend != "file-service":
                errors.append(f"{label}: {PUBLIC_PREFIX} must route to file-service, got {backend!r}")

        # The chain is built from every reference the annotation declares, in the
        # declared order, so the simulation below is of what Traefik will
        # actually run — not of the three middlewares this check expects.
        refs = [r.strip() for r in (meta.get("annotations") or {}).get(ANNOTATION, "").split(",") if r.strip()]
        chain, names = [], []
        for ref in refs:
            name = ref.split("@")[0]
            if ns and name.startswith(f"{ns}-"):
                name = name[len(ns) + 1:]
            mw = middlewares.get((ns, name))
            if mw is None:
                errors.append(f"{label} references {ref} but no such Middleware is rendered in {ns}")
                continue
            chain.append(mw)
            names.append(name)
        for required in REQUIRED_MW:
            if required not in names:
                errors.append(
                    f"{label} serves {PUBLIC_PREFIX} but does not reference "
                    f"{ns}-{required}@kubernetescrd (found: {refs or 'no middlewares annotation'})"
                )
        if not all(r in names for r in REQUIRED_MW):
            continue

        strip = (middlewares[(ns, FILES_MW)].get("spec") or {}).get("stripPrefix")
        if not strip or strip.get("prefixes") != [PUBLIC_PREFIX]:
            errors.append(f"{overlay}: Middleware {FILES_MW} must strip exactly ['{PUBLIC_PREFIX}'], got {strip!r}")
            continue

        for internal in internal_routes:
            public = PUBLIC_PREFIX + internal
            got = apply_chain(public, chain)
            if got != internal:
                errors.append(f"{overlay}: {public} reaches file-service as {got!r}, expected {internal!r}")
        for path in UNTOUCHED:
            got = apply_chain(path, chain)
            if got != path:
                errors.append(f"{overlay}: {path} must pass through unchanged, got {got!r}")

    # The shared Ingress must not have become a way around the upload guard:
    # the dedicated POST route still has to exist, outrank it and end at the
    # guard with the same prefix strip. (Priority arithmetic against every other
    # rendered route lives in gateway-config-check.sh.)
    upload_routes = [
        route
        for d in docs
        if d.get("kind") == "IngressRoute"
        for route in d["spec"].get("routes", [])
        if UPLOAD_RULE in route.get("match", "")
    ]
    if len(upload_routes) != 1:
        errors.append(f"{overlay}: expected exactly one dedicated upload route, found {len(upload_routes)}")
        continue
    route = upload_routes[0]
    if "Method(`POST`)" not in route.get("match", ""):
        errors.append(f"{overlay}: the dedicated upload route must be narrowed to POST")
    if route.get("priority") != 200:
        errors.append(f"{overlay}: the dedicated upload route must keep priority 200, got {route.get('priority')!r}")
    backends = [s.get("name") for s in route.get("services", [])]
    if backends != ["upload-guard"]:
        errors.append(f"{overlay}: uploads must go to upload-guard, got {backends!r}")
    if FILES_MW not in [m.get("name") for m in route.get("middlewares", [])]:
        errors.append(f"{overlay}: the dedicated upload route must keep the {FILES_MW} middleware")

# The local gateway must implement the identical strip, or local dev and the
# cluster disagree about the contract.
local = yaml.safe_load(open(f"{root}/infra/traefik/local/dynamic.yml"))
local_mw = local.get("http", {}).get("middlewares", {}).get(FILES_MW, {}).get("stripPrefix")
if not local_mw or local_mw.get("prefixes") != [PUBLIC_PREFIX]:
    errors.append(f"local gateway {FILES_MW} must strip exactly ['{PUBLIC_PREFIX}'], got {local_mw!r}")
for router in ("nchat-files", "nchat-files-https"):
    mws = local.get("http", {}).get("routers", {}).get(router, {}).get("middlewares", [])
    if FILES_MW not in mws:
        errors.append(f"local gateway router {router} must carry {FILES_MW}, got {mws!r}")

if errors:
    print("files route contract check FAILED:", file=sys.stderr)
    for e in errors:
        print(f"  - {e}", file=sys.stderr)
    sys.exit(1)

for internal in internal_routes:
    print(f"  [OK]   {PUBLIC_PREFIX}{internal} -> {internal}")
print("Files route contract check passed.")
PY
