#!/usr/bin/env bash
# This file is sourced by deploy.sh.
# The caller must enable: set -Eeuo pipefail.

NCHAT_DEV_TOPOLOGY_KEYS_RE='^(NCHAT_DEV_NODE_IP|NCHAT_DEV_NODE_CIDR|NCHAT_DEV_HOST|NCHAT_DEV_PUBLIC_URL|NCHAT_DEV_TURN_EXTERNAL_IP|LIVEKIT_API_PORT|LIVEKIT_RTC_TCP_PORT|LIVEKIT_RTC_UDP_PORT|TURN_LISTEN_PORT|TURN_RELAY_MIN_PORT|TURN_RELAY_MAX_PORT)$'
# Full RFC 1123 hostname: 1-63 char labels, alphanumeric with interior
# hyphens only (no leading/trailing hyphen or dot, no empty label), joined by
# single dots, 253 chars overall. `[a-z0-9.-]+` alone accepts underscores'
# absence but still lets through leading/trailing dots/hyphens and empty
# labels, which is not a valid Ingress/Certificate host.
NCHAT_DEV_HOSTNAME_RE='^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$'

is_rfc1123_hostname() {
  # Bash's [[ =~ ]] uses the libc regex engine, which is sensitive to
  # LC_CTYPE/LC_COLLATE: under some UTF-8 locales (e.g. pt_BR.utf8),
  # [a-z0-9] ranges also match accented/multibyte characters like "é",
  # letting non-ASCII hosts through. `local LC_ALL=C` scopes the C locale to
  # this function only (and anything it calls), forcing byte-wise ASCII
  # matching without affecting any other script's locale.
  local host="$1"
  local LC_ALL=C
  [[ "$host" =~ $NCHAT_DEV_HOSTNAME_RE && "${#host}" -le 253 ]]
}

is_ipv4() {
  local value="$1" octet
  local LC_ALL=C
  local -a octets
  [[ "$value" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
  IFS='.' read -r -a octets <<<"$value"
  [[ "${#octets[@]}" -eq 4 ]] || return 1
  for octet in "${octets[@]}"; do
    [[ "$octet" == "0" || "$octet" != 0* ]] || return 1
    ((10#$octet <= 255)) || return 1
  done
}

load_nchat_dev_topology() {
  local file="$1" key value legacy_livekit_api_url_seen=''
  local -A seen=()
  [[ -f "$file" && ! -L "$file" ]] || return 1
  while IFS='=' read -r key value || [[ -n "$key$value" ]]; do
    if [[ "$key" == LIVEKIT_API_URL ]]; then
      [[ -z "$legacy_livekit_api_url_seen" && -n "$value" && "$value" != *[[:space:]]* ]] || return 1
      legacy_livekit_api_url_seen=1
      continue
    fi
    [[ "$key" =~ $NCHAT_DEV_TOPOLOGY_KEYS_RE ]] || return 1
    [[ -z "${seen[$key]:-}" ]] || return 1
    [[ -n "$value" && "$value" != *[[:space:]]* ]] || return 1
    seen["$key"]=1
    printf -v "$key" '%s' "$value"
  done <"$file"
  [[ "${#seen[@]}" -eq 11 ]] || return 1

  is_ipv4 "$NCHAT_DEV_NODE_IP" || return 1
  [[ "$NCHAT_DEV_NODE_CIDR" == "$NCHAT_DEV_NODE_IP/32" ]] || return 1
  is_ipv4 "$NCHAT_DEV_TURN_EXTERNAL_IP" || return 1
  is_rfc1123_hostname "$NCHAT_DEV_HOST" || return 1
  is_rfc1123_hostname "turn.$NCHAT_DEV_HOST" || return 1
  [[ "$NCHAT_DEV_PUBLIC_URL" == "https://$NCHAT_DEV_HOST" ]] || return 1
  local port
  for port in "$LIVEKIT_API_PORT" "$LIVEKIT_RTC_TCP_PORT" "$LIVEKIT_RTC_UDP_PORT" \
    "$TURN_LISTEN_PORT" "$TURN_RELAY_MIN_PORT" "$TURN_RELAY_MAX_PORT"; do
    [[ "$port" =~ ^[0-9]{1,5}$ && "$port" -ge 1 && "$port" -le 65535 && "$port" -ne 3478 ]] || return 1
  done
  [[ "$TURN_RELAY_MIN_PORT" -le "$TURN_RELAY_MAX_PORT" ]] || return 1
}

materialize_nchat_dev_topology() {
  local example="$1" destination="$2" key value
  local api_port='' rtc_tcp_port='' rtc_udp_port='' turn_port='' relay_min='' relay_max=''
  local node_ip="${NCHAT_DEV_NODE_IP:-}" node_cidr="${NCHAT_DEV_NODE_CIDR:-}" host="${NCHAT_DEV_HOST:-}" turn_external_ip="${NCHAT_DEV_TURN_EXTERNAL_IP:-}"
  local -A seen=()

  [[ -f "$example" && ! -L "$example" && ! -e "$destination" ]] || return 1
  while IFS='=' read -r key value || [[ -n "$key$value" ]]; do
    [[ "$key" =~ $NCHAT_DEV_TOPOLOGY_KEYS_RE && -z "${seen[$key]:-}" ]] || return 1
    seen["$key"]=1
    case "$key" in
      NCHAT_DEV_NODE_IP) [[ "$value" == REPLACE_ME_NODE_IP ]] || return 1 ;;
      NCHAT_DEV_NODE_CIDR) [[ "$value" == REPLACE_ME_NODE_CIDR ]] || return 1 ;;
      NCHAT_DEV_HOST) [[ "$value" == REPLACE_ME_HOST ]] || return 1 ;;
      NCHAT_DEV_PUBLIC_URL) [[ "$value" == REPLACE_ME_PUBLIC_URL ]] || return 1 ;;
      NCHAT_DEV_TURN_EXTERNAL_IP) [[ "$value" == REPLACE_ME_TURN_EXTERNAL_IP ]] || return 1 ;;
      LIVEKIT_API_PORT) api_port="$value" ;;
      LIVEKIT_RTC_TCP_PORT) rtc_tcp_port="$value" ;;
      LIVEKIT_RTC_UDP_PORT) rtc_udp_port="$value" ;;
      TURN_LISTEN_PORT) turn_port="$value" ;;
      TURN_RELAY_MIN_PORT) relay_min="$value" ;;
      TURN_RELAY_MAX_PORT) relay_max="$value" ;;
    esac
  done <"$example"
  [[ "${#seen[@]}" -eq 11 ]] || return 1
  [[ -n "$node_ip" && -n "$node_cidr" && -n "$host" && -n "$turn_external_ip" ]] || return 1
  [[ "$node_ip$node_cidr$host$turn_external_ip" != *[[:space:]]* ]] || return 1

  (
    umask 077
    printf '%s\n' \
      "NCHAT_DEV_NODE_IP=$node_ip" \
      "NCHAT_DEV_NODE_CIDR=$node_cidr" \
      "NCHAT_DEV_HOST=$host" \
      "NCHAT_DEV_PUBLIC_URL=https://$host" \
      "NCHAT_DEV_TURN_EXTERNAL_IP=$turn_external_ip" \
      "LIVEKIT_API_PORT=$api_port" \
      "LIVEKIT_RTC_TCP_PORT=$rtc_tcp_port" \
      "LIVEKIT_RTC_UDP_PORT=$rtc_udp_port" \
      "TURN_LISTEN_PORT=$turn_port" \
      "TURN_RELAY_MIN_PORT=$relay_min" \
      "TURN_RELAY_MAX_PORT=$relay_max" >"$destination"
  )
  load_nchat_dev_topology "$destination"
}

prepare_nchat_dev_topology() {
  local root_dir="$1" destination="$2"
  local local_file="$root_dir/infra/k8s/overlays/nchat-dev-server/topology.env"
  local example="$root_dir/infra/k8s/overlays/nchat-dev-server/topology.env.example"
  local source_file="${NCHAT_DEV_TOPOLOGY_FILE:-}"

  if [[ -n "$source_file" ]]; then
    load_nchat_dev_topology "$source_file" || return 1
    install -m 0600 "$source_file" "$destination" || return 1
    sed -i '/^LIVEKIT_API_URL=/d' "$destination" || return 1
  elif [[ -n "${NCHAT_DEV_NODE_IP:-}" || -n "${NCHAT_DEV_NODE_CIDR:-}" || -n "${NCHAT_DEV_HOST:-}" || -n "${NCHAT_DEV_TURN_EXTERNAL_IP:-}" ]]; then
    materialize_nchat_dev_topology "$example" "$destination" || return 1
  elif [[ -e "$local_file" ]]; then
    load_nchat_dev_topology "$local_file" || return 1
    install -m 0600 "$local_file" "$destination" || return 1
    sed -i '/^LIVEKIT_API_URL=/d' "$destination" || return 1
  else
    echo "error: provide NCHAT_DEV_TOPOLOGY_FILE or nchat-dev environment variables" >&2
    return 1
  fi
}

render_nchat_dev_topology_template() {
  local source="$1" destination="$2" content
  content="$(<"$source")"
  content="${content//REPLACE_ME_NODE_IP/$NCHAT_DEV_NODE_IP}"
  content="${content//REPLACE_ME_HOST/$NCHAT_DEV_HOST}"
  content="${content//REPLACE_ME_TURN_EXTERNAL_IP/$NCHAT_DEV_TURN_EXTERNAL_IP}"
  content="${content//REPLACE_ME_LIVEKIT_API_PORT/$LIVEKIT_API_PORT}"
  content="${content//REPLACE_ME_LIVEKIT_RTC_TCP_PORT/$LIVEKIT_RTC_TCP_PORT}"
  content="${content//REPLACE_ME_LIVEKIT_RTC_UDP_PORT/$LIVEKIT_RTC_UDP_PORT}"
  content="${content//REPLACE_ME_TURN_LISTEN_PORT/$TURN_LISTEN_PORT}"
  content="${content//REPLACE_ME_TURN_RELAY_MIN_PORT/$TURN_RELAY_MIN_PORT}"
  content="${content//REPLACE_ME_TURN_RELAY_MAX_PORT/$TURN_RELAY_MAX_PORT}"
  printf '%s\n' "$content" >"$destination"
}
