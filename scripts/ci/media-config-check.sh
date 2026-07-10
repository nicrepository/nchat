#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
LIVEKIT_TEMPLATE="$ROOT_DIR/infra/compose/livekit/livekit.yaml.template"
COTURN_TEMPLATE="$ROOT_DIR/infra/compose/coturn/turnserver.conf.template"

ERRORS=0

require_file() {
  local path="$1"
  if [ -f "$path" ]; then
    echo "  [OK]   $path"
  else
    echo "  [FAIL] missing: $path" >&2
    ERRORS=$((ERRORS + 1))
  fi
}

echo "==> LiveKit + coturn (media) config check"
echo

echo "--- Required files ---"
require_file "$LIVEKIT_TEMPLATE"
require_file "$ROOT_DIR/infra/compose/livekit/README.md"
require_file "$COTURN_TEMPLATE"
require_file "$ROOT_DIR/infra/compose/coturn/README.md"
require_file "$COMPOSE_FILE"
require_file "$ENV_EXAMPLE"
require_file "$ROOT_DIR/scripts/dev/_media_env.sh"
require_file "$ROOT_DIR/scripts/dev/dev-media-up.sh"
require_file "$ROOT_DIR/scripts/dev/dev-media-down.sh"
require_file "$ROOT_DIR/scripts/dev/dev-media-status.sh"
require_file "$ROOT_DIR/scripts/dev/dev-media-logs.sh"
require_file "$ROOT_DIR/scripts/dev/dev-media-validate.sh"
require_file "$ROOT_DIR/docs/runbooks/task-livekit-coturn-dev.md"

# Templates/config must never be baked into runtime files that get committed.
echo
echo "--- Runtime config must not be committed ---"
for f in \
  "$ROOT_DIR/infra/compose/livekit/livekit.runtime.yaml" \
  "$ROOT_DIR/infra/compose/coturn/turnserver.runtime.conf"; do
  if git -C "$ROOT_DIR" ls-files --error-unmatch "$f" > /dev/null 2>&1; then
    echo "  [FAIL] rendered runtime file is tracked by git: $f" >&2
    ERRORS=$((ERRORS + 1))
  else
    echo "  [OK]   not tracked: $(basename "$f")"
  fi
done

# Bash syntax check for all new scripts.
echo
echo "--- Script syntax check (bash -n) ---"
for script in \
  scripts/dev/_media_env.sh \
  scripts/dev/dev-media-up.sh \
  scripts/dev/dev-media-down.sh \
  scripts/dev/dev-media-status.sh \
  scripts/dev/dev-media-logs.sh \
  scripts/dev/dev-media-validate.sh; do
  script_path="$ROOT_DIR/$script"
  if [ -f "$script_path" ] && bash -n "$script_path" 2>/dev/null; then
    echo "  [OK]   $script"
  else
    echo "  [FAIL] syntax error (or missing): $script" >&2
    ERRORS=$((ERRORS + 1))
  fi
done

# Validate compose config (no containers started).
echo
echo "--- Docker Compose config validation ---"
if command -v docker > /dev/null 2>&1 && docker compose version > /dev/null 2>&1; then
  COMPOSE_DIR="$(dirname "$COMPOSE_FILE")"
  ENV_DEV="$COMPOSE_DIR/.env.dev"
  TEMP_ENV_DEV=""
  TEMP_LIVEKIT_RUNTIME=""
  TEMP_COTURN_RUNTIME=""
  LIVEKIT_RUNTIME="$COMPOSE_DIR/livekit/livekit.runtime.yaml"
  COTURN_RUNTIME="$COMPOSE_DIR/coturn/turnserver.runtime.conf"

  # If .env.dev doesn't exist (CI), create a temporary stub from .env.dev.example.
  if [ ! -f "$ENV_DEV" ]; then
    TEMP_ENV_DEV="$(mktemp)"
    cp "$ENV_EXAMPLE" "$TEMP_ENV_DEV"
    cp "$TEMP_ENV_DEV" "$ENV_DEV"
  fi
  # docker compose config does not require the bind-mounted runtime files to
  # exist with real content, but it does resolve relative volume paths, so
  # create empty placeholders if they are not already rendered.
  if [ ! -f "$LIVEKIT_RUNTIME" ]; then
    TEMP_LIVEKIT_RUNTIME="$LIVEKIT_RUNTIME"
    : > "$LIVEKIT_RUNTIME"
  fi
  if [ ! -f "$COTURN_RUNTIME" ]; then
    TEMP_COTURN_RUNTIME="$COTURN_RUNTIME"
    : > "$COTURN_RUNTIME"
  fi

  if docker compose \
    --env-file "$ENV_EXAMPLE" \
    -f "$COMPOSE_FILE" \
    --profile media \
    config > /dev/null 2>&1; then
    echo "  [OK]   compose config valid"
  else
    echo "  [FAIL] compose config invalid" >&2
    ERRORS=$((ERRORS + 1))
  fi

  if [ -n "$TEMP_ENV_DEV" ]; then
    rm -f "$ENV_DEV" "$TEMP_ENV_DEV"
  fi
  if [ -n "$TEMP_LIVEKIT_RUNTIME" ]; then
    rm -f "$TEMP_LIVEKIT_RUNTIME"
  fi
  if [ -n "$TEMP_COTURN_RUNTIME" ]; then
    rm -f "$TEMP_COTURN_RUNTIME"
  fi
else
  echo "  [SKIP] docker not available"
fi

# Security: ensure no real secrets are hardcoded outside .env.dev.example.
echo
echo "--- Security checks ---"
REAL_SECRET_FILES=$(grep -rnE "(LIVEKIT_API_SECRET|COTURN_STATIC_AUTH_SECRET)\s*=" \
  "$ROOT_DIR" \
  --include="*.yml" --include="*.yaml" --include="*.conf" --include="*.env" \
  --exclude-dir=".git" --exclude-dir="node_modules" \
  --exclude=".env.dev.example" \
  2>/dev/null | grep -v "^Binary" || true)
if [ -n "$REAL_SECRET_FILES" ]; then
  echo "  [FAIL] LiveKit/coturn secret assignment found outside .env.dev.example:" >&2
  echo "$REAL_SECRET_FILES" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   no LiveKit/coturn secret hardcoded outside .env.dev.example"
fi

# Templates must not contain a literal secret value (placeholders only).
if grep -qE '\$\{COTURN_STATIC_AUTH_SECRET\}' "$COTURN_TEMPLATE" \
  && grep -qE '\$\{COTURN_STATIC_AUTH_SECRET\}' "$LIVEKIT_TEMPLATE"; then
  echo "  [OK]   coturn/LiveKit templates reference COTURN_STATIC_AUTH_SECRET via placeholder only"
else
  echo "  [FAIL] coturn/LiveKit templates must reference \${COTURN_STATIC_AUTH_SECRET} as a placeholder" >&2
  ERRORS=$((ERRORS + 1))
fi
if grep -qE '^\s*keys\s*:' "$LIVEKIT_TEMPLATE"; then
  echo "  [FAIL] livekit.yaml.template must not define a 'keys:' section (use LIVEKIT_KEYS env var instead)" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   livekit.yaml.template has no embedded 'keys:' section"
fi

# coturn must not be configured as an open/no-auth relay.
if grep -qE '^\s*no-auth\s*$' "$COTURN_TEMPLATE" 2>/dev/null; then
  echo "  [FAIL] turnserver.conf.template has 'no-auth' — this would be an open relay" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   turnserver.conf.template does not set no-auth"
fi
if grep -qE '^\s*use-auth-secret\s*$' "$COTURN_TEMPLATE" 2>/dev/null; then
  echo "  [OK]   turnserver.conf.template uses use-auth-secret (TURN REST API credentials)"
else
  echo "  [FAIL] turnserver.conf.template is missing use-auth-secret" >&2
  ERRORS=$((ERRORS + 1))
fi

# Ensure media ports bind to 127.0.0.1 not 0.0.0.0.
echo
echo "--- Port binding checks ---"
if grep -E '"0\.0\.0\.0:[0-9]+' "$COMPOSE_FILE" | grep -qE '7880|7881|3478|4916[0-9]|492(0[0-9]|00)' 2>/dev/null; then
  echo "  [FAIL] LiveKit/coturn ports bound to 0.0.0.0 — must use 127.0.0.1" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   LiveKit/coturn ports bound to 127.0.0.1"
fi

# The media profile must be present on both new services. Extract each
# service's block (from its header to the next top-level "  key:" line) so the
# check is not sensitive to how many lines precede "profiles:".
echo
echo "--- Compose profile checks ---"
service_block() {
  awk -v svc="  $1:" 'index($0, svc) == 1 {found=1; print; next} found && /^  [A-Za-z]/ {exit} found {print}' "$COMPOSE_FILE"
}
if service_block livekit | grep -q '^\s*profiles:' \
  && service_block coturn | grep -q '^\s*profiles:'; then
  echo "  [OK]   livekit and coturn are scoped to a compose profile (opt-in, not part of default dev stack)"
else
  echo "  [FAIL] livekit/coturn must be scoped to a profile (e.g. 'media')" >&2
  ERRORS=$((ERRORS + 1))
fi

# Verify dev scripts use the shared env fallback helper.
echo
echo "--- Dev script env fallback ---"
HELPER="$ROOT_DIR/scripts/dev/_media_env.sh"
if [ -f "$HELPER" ]; then
  echo "  [OK]   _media_env.sh helper exists"
  for script in dev-media-up dev-media-down dev-media-status dev-media-logs dev-media-validate; do
    script_path="$ROOT_DIR/scripts/dev/${script}.sh"
    if grep -q "_media_env.sh" "$script_path" 2>/dev/null; then
      echo "  [OK]   ${script}.sh sources helper"
    else
      echo "  [FAIL] ${script}.sh does not source _media_env.sh" >&2
      ERRORS=$((ERRORS + 1))
    fi
  done
else
  echo "  [FAIL] missing: $HELPER" >&2
  ERRORS=$((ERRORS + 1))
fi

# Verify dev-media-validate.sh covers the mandatory smoke-test assertions.
echo
echo "--- Validate script coverage check ---"
VALIDATE_SCRIPT="$ROOT_DIR/scripts/dev/dev-media-validate.sh"
for marker in "room create" "room join" "room participants list" "turnutils_uclient" "turnutils_stunclient"; do
  if grep -q "$marker" "$VALIDATE_SCRIPT" 2>/dev/null; then
    echo "  [OK]   dev-media-validate.sh covers: $marker"
  else
    echo "  [FAIL] dev-media-validate.sh is missing: $marker" >&2
    ERRORS=$((ERRORS + 1))
  fi
done

echo

if [ "$ERRORS" -gt 0 ]; then
  echo "Media (LiveKit + coturn) config check FAILED with $ERRORS error(s)." >&2
  exit 1
fi

echo "Media (LiveKit + coturn) config check passed."
