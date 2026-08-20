#!/usr/bin/env bash
# Validates the /api/admin routing contract end to end (issue #578).
#
# Same class of bug the /api/auth contract check exists for, and this service
# had it: admin-service registers /bootstrap, /session and /audit/events, while
# the browser calls /api/admin/bootstrap. The local Traefik gateway stripped the
# prefix; no Kubernetes overlay did. Nothing noticed because the only routes the
# pod served were /healthz, /readyz and /version, which the probes reach
# directly rather than through the gateway.
#
# The rewrite is only correct once kustomize has resolved namespaces and name
# prefixes, so each overlay is rendered and the result inspected — reading the
# sources is what let the gap survive last time.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
RENDER="$ROOT_DIR/scripts/k8s/k8s-render.sh"
OVERLAYS=(k3s-dev k3s-staging nchat-dev-server)
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

# The nchat-dev-server overlay reads topology.env through a configMapGenerator,
# and that file is deliberately unversioned: it names one specific machine. So a
# clean checkout cannot render the overlay at all, and standing the REPLACE_ME
# example in for it renders `admin.REPLACE_ME_HOST` — which this check then
# rejects, correctly, as an unresolved administrative host.
#
# The fix is a synthetic topology, not a weaker assertion. The fixture below is
# the one the other manifest gates already use (documentation-range addresses,
# a reserved .invalid host), and it is materialized through the deploy tree
# helper so the overlay is rendered exactly the way a deployment renders it —
# kustomize replacements included. Pinning it rather than honouring an inherited
# NCHAT_DEV_TOPOLOGY_FILE is deliberate: this is a contract check, so it must
# assert the same host every time and never depend on a real domain.
#
# prepare_deploy_tree copies infra/k8s into WORK_DIR and writes topology.env
# there, so the working tree keeps no generated file and the trap above cleans
# up on every exit path.
export NCHAT_DEV_TOPOLOGY_FILE="$ROOT_DIR/scripts/ci/testdata/nchat-dev-topology.env"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"
if ! prepare_deploy_tree "$ROOT_DIR" "$WORK_DIR/tree"; then
  echo "error: could not materialize the synthetic nchat-dev topology fixture" >&2
  exit 1
fi
NCHAT_DEV_OVERLAY_PATH="$WORK_DIR/tree/infra-k8s/overlays/nchat-dev-server"

# The host the fixture declares, read back from the fixture rather than repeated
# here: the assertions below compare the rendered manifests against it, so the
# two cannot drift apart.
FIXTURE_HOST="$(awk -F= '/^NCHAT_DEV_HOST=/ { print $2; exit }' "$NCHAT_DEV_TOPOLOGY_FILE")"
if [ -z "$FIXTURE_HOST" ] || [ "${FIXTURE_HOST#*REPLACE_ME}" != "$FIXTURE_HOST" ]; then
  echo "error: the topology fixture must declare a resolved NCHAT_DEV_HOST" >&2
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1 || ! python3 -c "import yaml" >/dev/null 2>&1; then
  # Failing is deliberate: a contract gate that silently skips reports "passed"
  # for exactly the configuration it exists to reject.
  echo "error: admin route contract check requires python3 with PyYAML." >&2
  exit 1
fi

for overlay in "${OVERLAYS[@]}"; do
  # Absolute throughout: the fixture overlay lives in WORK_DIR, outside the
  # repository, so a path joined onto ROOT_DIR would not resolve.
  overlay_path="$ROOT_DIR/infra/k8s/overlays/$overlay"
  if [ "$overlay" = nchat-dev-server ]; then
    overlay_path="$NCHAT_DEV_OVERLAY_PATH"
  fi
  if ! "$RENDER" "$overlay_path" >"$WORK_DIR/$overlay.yaml" 2>"$WORK_DIR/$overlay.err"; then
    if command -v kustomize >/dev/null 2>&1 &&
      kustomize build "$overlay_path" >"$WORK_DIR/$overlay.yaml" 2>>"$WORK_DIR/$overlay.err"; then
      :
    else
      echo "error: failed to render overlay $overlay" >&2
      cat "$WORK_DIR/$overlay.err" >&2
      exit 1
    fi
  fi
done

python3 - "$ROOT_DIR" "$WORK_DIR" "$FIXTURE_HOST" "${OVERLAYS[@]}" <<'PY'
import re
import sys

import yaml

root, work, fixture_host, *overlays = sys.argv[1:]

# The overlay whose host is derived from the synthetic topology fixture, and the
# host that derivation must produce. k3s-dev and k3s-staging carry static hosts
# in their own manifests and are not driven by the fixture.
FIXTURE_OVERLAY = "nchat-dev-server"
FIXTURE_ADMIN_HOST = f"admin.{fixture_host}"

MW_NAME = "admin-api-prefix"
ANNOTATION = "traefik.ingress.kubernetes.io/router.middlewares"
PUBLIC_PREFIX = "/api/admin"

# Paths that must survive the middleware untouched: the annotation applies to
# every router the shared Ingress generates, so a regex broader than its own
# prefix would silently rewrite another service.
UNTOUCHED = ["/api/auth/login", "/api/chat/sidebar", "/api/files/upload", "/"]

errors = []
specs = {}


def rewrite(path, regex, replacement):
    """Traefik replacePathRegex: non-matching paths pass through unchanged."""
    compiled = re.compile(regex)
    if not compiled.search(path):
        return path
    return compiled.sub(replacement.replace("${1}", r"\1"), path)


# The internal contract is derived from the route constants admin-service
# actually registers, so a route added later is covered without touching this
# file — and a route renamed without a gateway change fails here.
routes_src = open(f"{root}/services/admin-service/internal/http/routes.go").read()
declared = sorted(set(re.findall(r'Route\w+\s*=\s*"(/[^"]*)"', routes_src)))
if not declared:
    errors.append("could not read any route constant from admin-service routes.go")
for internal in declared:
    if internal.startswith("/api/"):
        errors.append(f"admin-service must not alias the public prefix: {internal}")

for overlay in overlays:
    docs = [d for d in yaml.safe_load_all(open(f"{work}/{overlay}.yaml")) if d]

    middlewares = {
        (d["metadata"].get("namespace"), d["metadata"]["name"]): d
        for d in docs
        if d.get("kind") == "Middleware"
    }
    admin_ingresses = [
        d
        for d in docs
        if d.get("kind") == "Ingress"
        and any(
            p.get("path") == PUBLIC_PREFIX
            for r in d["spec"].get("rules", [])
            for p in r.get("http", {}).get("paths", [])
        )
    ]

    if not admin_ingresses:
        errors.append(f"{overlay}: no Ingress serves {PUBLIC_PREFIX}")
        continue

    for ing in admin_ingresses:
        meta = ing["metadata"]
        ns = meta.get("namespace")
        label = f"{overlay}: Ingress {meta['name']}"
        refs = (meta.get("annotations") or {}).get(ANNOTATION, "")
        expected_ref = f"{ns}-{MW_NAME}@kubernetescrd"

        # The prefix must reach admin-service: a rewrite that lands on the right
        # path at the wrong service is the same 404 with better logs.
        for rule in ing["spec"].get("rules", []):
            for path in rule.get("http", {}).get("paths", []):
                if path.get("path") != PUBLIC_PREFIX:
                    continue
                backend = (path.get("backend") or {}).get("service", {}).get("name")
                if backend != "admin-service":
                    errors.append(f"{label}: {PUBLIC_PREFIX} must route to admin-service, got {backend!r}")

        if expected_ref not in [r.strip() for r in refs.split(",") if r.strip()]:
            errors.append(
                f"{label} serves {PUBLIC_PREFIX} but does not reference {expected_ref} "
                f"(found: {refs or 'no middlewares annotation'})"
            )
            continue

        mw = middlewares.get((ns, MW_NAME))
        if mw is None:
            errors.append(f"{label} references {expected_ref} but no Middleware {MW_NAME} is rendered in {ns}")
            continue

        rpr = (mw.get("spec") or {}).get("replacePathRegex")
        if not rpr:
            errors.append(f"{overlay}: Middleware {MW_NAME} must use replacePathRegex")
            continue

        regex, replacement = rpr.get("regex", ""), rpr.get("replacement", "")
        specs[overlay] = (regex, replacement)

        if not regex.startswith("^/api/admin/"):
            errors.append(f"{overlay}: rewrite regex {regex!r} is broader than {PUBLIC_PREFIX}")
            continue

        for internal in declared:
            public = PUBLIC_PREFIX + internal
            got = rewrite(public, regex, replacement)
            if got != internal:
                errors.append(f"{overlay}: {public} reaches admin-service as {got!r}, expected {internal!r}")
        for path in UNTOUCHED:
            got = rewrite(path, regex, replacement)
            if got != path:
                errors.append(f"{overlay}: {path} must pass through unchanged, got {got!r}")

# ── Origin isolation ───────────────────────────────────────────────────────
#
# The Admin API must be reachable on the administrative host and nowhere else.
#
# This is the control that survives an XSS on the chat: an attacker holding a
# stolen chat access token cannot POST /api/admin/session from the chat origin,
# because no route there ends at admin-service. Hiding the console in the
# frontend would not do it — the request has to die in the routing table.
#
# The check is on the rendered manifests, so a path re-added to the chat Ingress
# fails here rather than in production.
for overlay in overlays:
    docs = [d for d in yaml.safe_load_all(open(f"{work}/{overlay}.yaml")) if d]

    admin_hosts = {
        rule.get("host")
        for d in docs
        if d.get("kind") == "Ingress"
        for rule in d["spec"].get("rules", [])
        if any(
            (p.get("backend") or {}).get("service", {}).get("name") == "nchat-admin-web"
            for p in rule.get("http", {}).get("paths", [])
        )
    }
    if not admin_hosts:
        errors.append(f"{overlay}: no host serves the administrative console")
        continue

    for d in docs:
        if d.get("kind") != "Ingress":
            continue
        for rule in d["spec"].get("rules", []):
            host = rule.get("host")
            for path in rule.get("http", {}).get("paths", []):
                backend = (path.get("backend") or {}).get("service", {}).get("name")
                if backend != "admin-service":
                    continue
                if host not in admin_hosts:
                    errors.append(
                        f"{overlay}: Ingress {d['metadata']['name']} routes {path.get('path')} "
                        f"to admin-service on {host!r}, which is not the administrative host"
                    )

# The same rule for the local gateway: no admin-service router may match the
# chat host.
local_routers = yaml.safe_load(open(f"{root}/infra/traefik/local/dynamic.yml"))
local_routers = local_routers.get("http", {}).get("routers", {})
local_console_hosts = {
    match.group(1)
    for router in local_routers.values()
    if router.get("service") == "admin-web"
    for match in [re.search(r"Host\(`([^`]+)`\)", router.get("rule", ""))]
    if match
}
if not local_console_hosts:
    errors.append("local gateway has no router serving the administrative console")
for name, router in local_routers.items():
    if router.get("service") != "admin-service":
        continue
    match = re.search(r"Host\(`([^`]+)`\)", router.get("rule", ""))
    host = match.group(1) if match else None
    if host not in local_console_hosts:
        errors.append(
            f"local gateway router {name} sends {host!r} to admin-service, "
            f"which is not the administrative host"
        )

# ── The administrative console host ────────────────────────────────────────
#
# The console is only reachable if some Ingress actually routes to the bundle.
# A Deployment and a Service are not enough — the first version of this overlay
# had both and no route, so the console rendered fine and answered nothing.
#
# The three backends must share one host, because that is what the design
# depends on: a __Host- cookie, SameSite, the CSRF double-submit and the OIDC
# callback are all statements about a single origin.
CONSOLE_BACKEND = "nchat-admin-web"
CONSOLE_ROUTES = {"/": CONSOLE_BACKEND, "/api/admin": "admin-service", "/api/auth": "auth-service"}

for overlay in overlays:
    docs = [d for d in yaml.safe_load_all(open(f"{work}/{overlay}.yaml")) if d]

    if not any(d.get("kind") == "Service" and d["metadata"]["name"] == CONSOLE_BACKEND for d in docs):
        errors.append(f"{overlay}: no Service {CONSOLE_BACKEND} is rendered")

    console_rules = [
        (d, rule)
        for d in docs
        if d.get("kind") == "Ingress"
        for rule in d["spec"].get("rules", [])
        if any(
            (p.get("backend") or {}).get("service", {}).get("name") == CONSOLE_BACKEND
            and p.get("path") == "/"
            for p in rule.get("http", {}).get("paths", [])
        )
    ]
    # The plain-HTTP redirect router also points at the bundle; the host that
    # serves the API is the one that must carry all three routes.
    serving = [(d, rule) for d, rule in console_rules if len(rule.get("http", {}).get("paths", [])) > 1]
    if not serving:
        errors.append(f"{overlay}: no Ingress serves the administrative console host")
        continue
    if len(serving) > 1:
        errors.append(f"{overlay}: the administrative console is served by {len(serving)} hosts, expected one")

    ingress, rule = serving[0]
    label = f"{overlay}: Ingress {ingress['metadata']['name']}"
    routed = {
        p.get("path"): (p.get("backend") or {}).get("service", {}).get("name")
        for p in rule.get("http", {}).get("paths", [])
    }
    for path, backend in CONSOLE_ROUTES.items():
        if routed.get(path) != backend:
            errors.append(f"{label}: {path} must route to {backend}, got {routed.get(path)!r}")
    for path in routed:
        if path not in CONSOLE_ROUTES:
            errors.append(f"{label}: unexpected route {path} on the administrative host")

    host = rule.get("host")
    if not host or "REPLACE_ME" in host:
        errors.append(f"{label}: administrative host is unresolved ({host!r})")
    elif overlay == FIXTURE_OVERLAY and host != FIXTURE_ADMIN_HOST:
        # The derivation itself, asserted on the rendered output: the overlay's
        # kustomize replacement splits "admin.REPLACE_ME_ADMIN_HOST" on the
        # first dot and substitutes NCHAT_DEV_HOST into the second segment. A
        # broken replacement leaves the placeholder (caught above) and a
        # mis-indexed one produces the wrong host, which is caught here.
        # Checking the rendered manifest is the point — patching REPLACE_ME out
        # of the YAML after the build would hide exactly this failure.
        errors.append(
            f"{label}: administrative host is {host!r}, expected {FIXTURE_ADMIN_HOST!r} "
            f"derived from NCHAT_DEV_HOST={fixture_host!r}"
        )
    elif any(
        other_rule.get("host") == host
        for d in docs
        if d.get("kind") == "Ingress" and d["metadata"]["name"] != ingress["metadata"]["name"]
        for other_rule in d["spec"].get("rules", [])
        if any(
            (p.get("backend") or {}).get("service", {}).get("name") not in (CONSOLE_BACKEND,)
            for p in other_rule.get("http", {}).get("paths", [])
        )
    ):
        errors.append(f"{label}: the administrative host {host} is shared with a non-console Ingress")

    # TLS is required wherever the overlay terminates TLS at all: the console's
    # session cookie carries the __Host- prefix, which a browser refuses over
    # plain HTTP.
    overlay_uses_tls = any(
        d.get("kind") == "Ingress" and d["spec"].get("tls") for d in docs
    )
    if overlay_uses_tls:
        tls_hosts = {h for entry in ingress["spec"].get("tls", []) for h in entry.get("hosts", [])}
        if host not in tls_hosts:
            errors.append(f"{label}: administrative host {host} is not covered by the Ingress TLS block")

# ── The derived pair, on the rendered fixture overlay ──────────────────────
#
# Both halves of the derivation are asserted together: the chat host is the
# fixture's NCHAT_DEV_HOST verbatim, and the administrative host is that same
# value under the fixed "admin" label. Asserting only the admin host would pass
# for an overlay that had quietly stopped rendering the chat host at all.
fixture_docs = [d for d in yaml.safe_load_all(open(f"{work}/{FIXTURE_OVERLAY}.yaml")) if d]
fixture_hosts = {
    rule.get("host")
    for d in fixture_docs
    if d.get("kind") == "Ingress"
    for rule in d["spec"].get("rules", [])
    if rule.get("host")
}
for expected in (fixture_host, FIXTURE_ADMIN_HOST):
    if expected not in fixture_hosts:
        errors.append(
            f"{FIXTURE_OVERLAY}: no Ingress renders the host {expected!r} "
            f"(rendered: {sorted(fixture_hosts)})"
        )
# Nothing may survive the render still holding a placeholder. The other gates
# check this for the whole manifest; here it is scoped to hosts, which is what
# this contract is about.
for host in fixture_hosts:
    if "REPLACE_ME" in host:
        errors.append(f"{FIXTURE_OVERLAY}: host {host!r} left a placeholder unresolved")

# Every overlay must implement the same rule; a divergence is how one
# environment starts answering 404 while the others work.
distinct = set(specs.values())
if len(distinct) > 1:
    errors.append(f"overlays disagree on the rewrite rule: {specs}")
missing = [o for o in overlays if o not in specs]
if missing:
    errors.append(f"no validated rewrite rule rendered for: {', '.join(missing)}")

# The local gateway strips the same prefix. It uses stripPrefix rather than
# replacePathRegex, which is the same transformation for this prefix; what
# matters is that the pod sees the same path in both worlds.
local = yaml.safe_load(open(f"{root}/infra/traefik/local/dynamic.yml"))
local_strip = (
    local.get("http", {}).get("middlewares", {}).get("strip-admin-prefix", {}).get("stripPrefix")
)
if not local_strip or local_strip.get("prefixes") != [PUBLIC_PREFIX]:
    errors.append(f"local gateway strip-admin-prefix must strip exactly ['{PUBLIC_PREFIX}'], got {local_strip!r}")
else:
    for internal in declared:
        got = (PUBLIC_PREFIX + internal)[len(PUBLIC_PREFIX):] or "/"
        if got != internal:
            errors.append(f"local gateway: {PUBLIC_PREFIX}{internal} reaches admin-service as {got!r}")

# Every local router that reaches admin-service must strip the prefix. The set
# is derived rather than listed, so a router added later is covered and a
# router removed does not leave a stale expectation behind.
routers = local.get("http", {}).get("routers", {})
admin_routers = [name for name, router in routers.items() if router.get("service") == "admin-service"]
if not admin_routers:
    errors.append("local gateway has no router reaching admin-service")
for router in admin_routers:
    mws = routers.get(router, {}).get("middlewares", [])
    if "strip-admin-prefix" not in mws:
        errors.append(f"local gateway router {router} must carry strip-admin-prefix, got {mws!r}")

# The administrative console must aim at the public contract. A bundle that
# called the internal path would work only through a gateway that had stopped
# rewriting, which is the failure this whole check exists to prevent.
client_src = open(f"{root}/apps/admin-web/src/api/client.ts").read()
base = re.search(r'ADMIN_BASE = "([^"]+)"', client_src)
if not base:
    errors.append("could not determine the admin console API base path")
elif base.group(1) != PUBLIC_PREFIX:
    errors.append(f"admin console base is {base.group(1)!r}, expected {PUBLIC_PREFIX!r}")

if errors:
    print("admin route contract check FAILED:", file=sys.stderr)
    for e in errors:
        print(f"  - {e}", file=sys.stderr)
    sys.exit(1)

for internal in declared:
    print(f"  [OK]   {PUBLIC_PREFIX}{internal} -> {internal}")
print("Admin route contract check passed.")
PY
