#!/usr/bin/env bash
# Shared helper — source this file (do not execute directly).
# Loads LiveKit + coturn dev env vars, renders the committed *.template config
# files into gitignored runtime files, and exposes a media_compose() wrapper.
#
# Never prints secret values (LIVEKIT_API_SECRET, COTURN_STATIC_AUTH_SECRET).
#
# Usage:
#   ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
#   source "$(dirname "${BASH_SOURCE[0]}")/_media_env.sh"

_MEDIA_COMPOSE_FILE="${ROOT_DIR}/infra/compose/compose.dev.yml"
_MEDIA_ENV_EXAMPLE="${ROOT_DIR}/infra/compose/.env.dev.example"
_MEDIA_ENV_DEV="${ROOT_DIR}/infra/compose/.env.dev"

_MEDIA_REQUIRED_KEYS=(
  LIVEKIT_IMAGE
  LIVEKIT_HOST_PORT
  LIVEKIT_RTC_TCP_HOST_PORT
  LIVEKIT_RTC_UDP_PORT_START
  LIVEKIT_RTC_UDP_PORT_END
  LIVEKIT_NODE_IP
  LIVEKIT_API_KEY
  LIVEKIT_API_SECRET
  COTURN_IMAGE
  COTURN_LISTENING_PORT
  COTURN_RELAY_MIN_PORT
  COTURN_RELAY_MAX_PORT
  COTURN_REALM
  COTURN_STATIC_AUTH_SECRET
  COTURN_HOST
)

if [ ! -f "$_MEDIA_ENV_DEV" ]; then
  cp "$_MEDIA_ENV_EXAMPLE" "$_MEDIA_ENV_DEV"
  echo "[INFO] Created infra/compose/.env.dev from .env.dev.example."
fi

# Load example first (defaults), then .env.dev (overrides) so a partially
# up-to-date .env.dev still works without printing any values.
set -a
# shellcheck disable=SC1090
source "$_MEDIA_ENV_EXAMPLE"
# shellcheck disable=SC1090
source "$_MEDIA_ENV_DEV"
set +a

_media_missing=()
for _key in "${_MEDIA_REQUIRED_KEYS[@]}"; do
  if ! grep -qE "^${_key}=" "$_MEDIA_ENV_DEV"; then
    _media_missing+=("$_key")
  fi
done
if [ "${#_media_missing[@]}" -gt 0 ]; then
  echo "[WARN] .env.dev is missing media keys (LiveKit/coturn); using .env.dev.example defaults for:" >&2
  printf '  %s\n' "${_media_missing[@]}" >&2
  echo "[WARN] Add these keys to infra/compose/.env.dev to silence this warning." >&2
fi

LIVEKIT_TEMPLATE="${ROOT_DIR}/infra/compose/livekit/livekit.yaml.template"
LIVEKIT_RUNTIME="${ROOT_DIR}/infra/compose/livekit/livekit.runtime.yaml"
COTURN_TEMPLATE="${ROOT_DIR}/infra/compose/coturn/turnserver.conf.template"
COTURN_RUNTIME="${ROOT_DIR}/infra/compose/coturn/turnserver.runtime.conf"

# render_media_config: renders the committed templates into gitignored runtime
# files using envsubst, restricted to the known placeholder variables so
# unrelated "$..." occurrences in the templates are left untouched.
render_media_config() {
  if ! command -v envsubst >/dev/null 2>&1; then
    echo "Required command not found: envsubst (part of the gettext package)" >&2
    exit 1
  fi

  envsubst '${LIVEKIT_RTC_UDP_PORT_START} ${LIVEKIT_RTC_UDP_PORT_END} ${LIVEKIT_NODE_IP} ${COTURN_HOST} ${COTURN_LISTENING_PORT} ${COTURN_STATIC_AUTH_SECRET}' \
    < "$LIVEKIT_TEMPLATE" > "$LIVEKIT_RUNTIME"

  envsubst '${COTURN_LISTENING_PORT} ${COTURN_STATIC_AUTH_SECRET} ${COTURN_REALM} ${COTURN_RELAY_MIN_PORT} ${COTURN_RELAY_MAX_PORT}' \
    < "$COTURN_TEMPLATE" > "$COTURN_RUNTIME"

  chmod 600 "$LIVEKIT_RUNTIME" "$COTURN_RUNTIME" 2>/dev/null || true
}

media_compose() {
  docker compose --env-file "$_MEDIA_ENV_DEV" -f "$_MEDIA_COMPOSE_FILE" --profile media "$@"
}
