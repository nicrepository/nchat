#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# shellcheck source=_media_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_media_env.sh"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

require_command docker
docker compose version >/dev/null

render_media_config

echo "==> Starting LiveKit + coturn (profile: media)..."
media_compose up -d coturn livekit
media_compose ps coturn livekit

cat <<EOF

Media stack started (dev only, not for production):
  LiveKit signaling/HTTP/WS : http://localhost:${LIVEKIT_HOST_PORT}
  LiveKit RTC (TCP fallback): localhost:${LIVEKIT_RTC_TCP_HOST_PORT}
  LiveKit RTC (UDP)         : localhost:${LIVEKIT_RTC_UDP_PORT_START}-${LIVEKIT_RTC_UDP_PORT_END}
  coturn STUN/TURN          : localhost:${COTURN_LISTENING_PORT} (UDP + TCP)
  coturn TURN relay (UDP)   : localhost:${COTURN_RELAY_MIN_PORT}-${COTURN_RELAY_MAX_PORT}

Run 'make dev-media-validate' to create a room and connect a real participant.
See docs/runbooks/task-livekit-coturn-dev.md for details and Windows 11 /
Docker Desktop firewall troubleshooting.
EOF
