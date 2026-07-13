#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# shellcheck source=_media_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_media_env.sh"

media_compose ps livekit coturn

cat <<EOF

Endpoints (if running):
  LiveKit signaling/HTTP/WS : http://localhost:${LIVEKIT_HOST_PORT}
  LiveKit RTC (TCP fallback): localhost:${LIVEKIT_RTC_TCP_HOST_PORT}
  LiveKit RTC (UDP)         : localhost:${LIVEKIT_RTC_UDP_PORT_START}-${LIVEKIT_RTC_UDP_PORT_END}
  coturn STUN/TURN          : localhost:${COTURN_LISTENING_PORT} (UDP + TCP)
  coturn TURN relay (UDP)   : localhost:${COTURN_RELAY_MIN_PORT}-${COTURN_RELAY_MAX_PORT}
EOF
