#!/usr/bin/env bash
# Shared helper — source this file (do not execute directly).
# Sets EFFECTIVE_ENV_FILE and optionally TEMP_ENV_FILE.
# Exports PROMETHEUS_HOST_PORT, GRAFANA_HOST_PORT, JAEGER_UI_HOST_PORT into
# the calling shell so validate/status scripts can use the right ports.
#
# Usage:
#   source "$(dirname "${BASH_SOURCE[0]}")/_observability_env.sh"

_OBS_ENV_EXAMPLE="${ROOT_DIR}/infra/compose/.env.dev.example"
_OBS_ENV_DEV="${ROOT_DIR}/infra/compose/.env.dev"

_OBS_REQUIRED_KEYS=(
  PROMETHEUS_IMAGE
  GRAFANA_IMAGE
  JAEGER_IMAGE
  PROMETHEUS_HOST_PORT
  GRAFANA_HOST_PORT
  JAEGER_UI_HOST_PORT
  JAEGER_OTLP_GRPC_HOST_PORT
  JAEGER_OTLP_HTTP_HOST_PORT
  GRAFANA_ADMIN_USER
  GRAFANA_ADMIN_PASSWORD
)

TEMP_ENV_FILE=""

if [ ! -f "$_OBS_ENV_DEV" ]; then
  echo "[INFO] .env.dev not found — using .env.dev.example defaults."
  EFFECTIVE_ENV_FILE="$_OBS_ENV_EXAMPLE"
else
  # Collect missing required keys.
  _obs_missing=()
  for _key in "${_OBS_REQUIRED_KEYS[@]}"; do
    if ! grep -qE "^${_key}=" "$_OBS_ENV_DEV"; then
      _obs_missing+=("$_key")
    fi
  done

  if [ ${#_obs_missing[@]} -eq 0 ]; then
    EFFECTIVE_ENV_FILE="$_OBS_ENV_DEV"
  else
    echo "[WARN] .env.dev is missing observability keys introduced in TASK-18:"
    for _k in "${_obs_missing[@]}"; do
      # Show the default value so the user knows what will be used.
      _default=$(grep -E "^${_k}=" "$_OBS_ENV_EXAMPLE" | head -1 || true)
      printf "       - %-40s  (default: %s)\n" "$_k" "${_default#*=}"
    done
    echo "[WARN] Using .env.dev.example defaults for missing keys."
    echo "[WARN] Your existing .env.dev values are preserved for all other keys."
    echo "[WARN] To silence this warning, add the missing keys to .env.dev"
    echo "[WARN] (see infra/compose/.env.dev.example for reference values)."
    echo

    # Build merged env: .env.dev values first (user overrides),
    # then append only the missing keys from .env.dev.example.
    TEMP_ENV_FILE="$(mktemp /tmp/nchat_obs_env_XXXXXX)"
    cat "$_OBS_ENV_DEV" > "$TEMP_ENV_FILE"
    for _key in "${_obs_missing[@]}"; do
      _val=$(grep -E "^${_key}=" "$_OBS_ENV_EXAMPLE" | head -1 || true)
      [ -n "$_val" ] && echo "$_val" >> "$TEMP_ENV_FILE"
    done

    EFFECTIVE_ENV_FILE="$TEMP_ENV_FILE"
  fi
fi

# Export port vars into the calling shell so scripts that read them as
# shell variables (e.g. validate.sh) pick up the correct values.
_obs_export_ports() {
  local _file="$1"
  local _key _val
  for _key in PROMETHEUS_HOST_PORT GRAFANA_HOST_PORT JAEGER_UI_HOST_PORT; do
    _val=$(grep -E "^${_key}=" "$_file" | tail -1 | cut -d= -f2-)
    [ -n "$_val" ] && export "${_key}=${_val}"
  done
}
_obs_export_ports "$EFFECTIVE_ENV_FILE"

# Register cleanup of temp file on EXIT (safe even if already empty).
_obs_cleanup() { [ -n "$TEMP_ENV_FILE" ] && rm -f "$TEMP_ENV_FILE"; return 0; }
trap _obs_cleanup EXIT
