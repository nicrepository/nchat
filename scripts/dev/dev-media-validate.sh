#!/usr/bin/env bash
# TASK: livekit-coturn-dev — smoke validation for LiveKit + coturn dev stack.
#
# Fails (non-zero exit) if:
#   - LiveKit does not respond
#   - the smoke room cannot be created
#   - coturn is not configured (open relay, or auth rejected for valid creds)
#   - coturn does not expose the documented STUN/TURN ports
#   - a real participant cannot connect to the room
#
# No mocks: the participant connection is a real LiveKit CLI (lk) client
# performing an actual WebRTC join (ICE/DTLS/SRTP) and publishing a demo track.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# shellcheck source=_media_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_media_env.sh"

LK_CLI_IMAGE="livekit/livekit-cli:v2.17"
DOCKER_NETWORK="nchat-dev"
ROOM_NAME="nchat-smoke-$(date +%s)"
PARTICIPANT_IDENTITY="smoke-participant"
PARTICIPANT_CONTAINER="nchat-media-smoke-participant"

ERRORS=0
CLEANUP_ROOM=0

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

cleanup() {
  docker rm -f "$PARTICIPANT_CONTAINER" >/dev/null 2>&1 || true
  if [ "$CLEANUP_ROOM" -eq 1 ]; then
    lk_cli room delete "$ROOM_NAME" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

require_command docker
docker compose version >/dev/null
require_command curl

# lk_cli: runs the pinned LiveKit CLI image against the dev LiveKit server.
# Credentials are passed as environment variables (never as CLI args), so
# they never appear in this script's own stdout/stderr or in `docker ps`.
lk_cli() {
  docker run --rm --network "$DOCKER_NETWORK" \
    -e LIVEKIT_URL="ws://livekit:7880" \
    -e LIVEKIT_API_KEY="$LIVEKIT_API_KEY" \
    -e LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET" \
    "$LK_CLI_IMAGE" "$@"
}

echo "==> Checking LiveKit + coturn containers are running (profile: media)"
running_services="$(media_compose ps --status running --services 2>/dev/null || true)"
for svc in livekit coturn; do
  if ! grep -qx "$svc" <<<"$running_services"; then
    echo "FAIL: $svc is not running. Run 'make dev-media-up' first." >&2
    exit 1
  fi
done
echo "  [OK] livekit and coturn containers are running"

echo
echo "==> Waiting for LiveKit to respond on http://localhost:${LIVEKIT_HOST_PORT}"
livekit_ready=0
for _ in $(seq 1 20); do
  http_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://localhost:${LIVEKIT_HOST_PORT}/" || echo "000")"
  if [ "$http_code" != "000" ]; then
    echo "  [OK] LiveKit responded (HTTP ${http_code})"
    livekit_ready=1
    break
  fi
  sleep 3
done
if [ "$livekit_ready" -ne 1 ]; then
  echo "FAIL: LiveKit did not respond on http://localhost:${LIVEKIT_HOST_PORT} after 60s." >&2
  exit 1
fi

echo
echo "==> coturn: checking documented ports are open"
for proto_port in "tcp:${COTURN_LISTENING_PORT}"; do
  port="${proto_port#*:}"
  if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
    exec 3>&- 3<&- || true
    echo "  [OK] coturn TCP ${port} is open"
  else
    echo "FAIL: coturn TCP ${port} is not reachable on localhost. Check port publishing in compose.dev.yml." >&2
    ERRORS=$((ERRORS + 1))
  fi
done

echo
echo "==> coturn: verifying STUN binding (turnutils_stunclient)"
if timeout 20 docker run --rm --network "$DOCKER_NETWORK" "$COTURN_IMAGE" \
  turnutils_stunclient coturn >/tmp/nchat-stunclient.log 2>&1; then
  echo "  [OK] STUN binding request succeeded"
else
  echo "FAIL: STUN binding request failed — coturn is not answering STUN requests." >&2
  cat /tmp/nchat-stunclient.log >&2 || true
  ERRORS=$((ERRORS + 1))
fi
rm -f /tmp/nchat-stunclient.log

echo
echo "==> coturn: verifying anonymous TURN allocation is REJECTED (no open relay)"
if timeout 20 docker run --rm --network "$DOCKER_NETWORK" "$COTURN_IMAGE" \
  turnutils_uclient -y coturn >/tmp/nchat-uclient-anon.log 2>&1; then
  echo "FAIL: anonymous TURN allocation SUCCEEDED — coturn is an open relay!" >&2
  cat /tmp/nchat-uclient-anon.log >&2 || true
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK] anonymous TURN allocation was rejected (authentication required)"
fi
rm -f /tmp/nchat-uclient-anon.log

echo
echo "==> coturn: verifying TURN allocation with valid time-limited credentials"
# -W passes the plain-text TURN REST API secret; turnutils_uclient derives the
# ephemeral "<timestamp>:<label>" username and HMAC-SHA1 password itself, so
# COTURN_STATIC_AUTH_SECRET never has to be echoed by this script.
if timeout 20 docker run --rm --network "$DOCKER_NETWORK" "$COTURN_IMAGE" \
  turnutils_uclient -y -W "$COTURN_STATIC_AUTH_SECRET" coturn >/tmp/nchat-uclient-auth.log 2>&1; then
  echo "  [OK] authenticated TURN allocation succeeded"
else
  echo "FAIL: authenticated TURN allocation failed — check COTURN_STATIC_AUTH_SECRET matches between coturn and LiveKit." >&2
  cat /tmp/nchat-uclient-auth.log >&2 || true
  ERRORS=$((ERRORS + 1))
fi
rm -f /tmp/nchat-uclient-auth.log

if [ "$ERRORS" -gt 0 ]; then
  echo
  echo "coturn validation FAILED with $ERRORS error(s). Skipping LiveKit room/participant checks." >&2
  exit 1
fi

echo
echo "==> LiveKit: creating room '${ROOM_NAME}'"
if lk_cli room create "$ROOM_NAME" >/tmp/nchat-room-create.log 2>&1; then
  CLEANUP_ROOM=1
  echo "  [OK] room created"
else
  echo "FAIL: could not create LiveKit room." >&2
  cat /tmp/nchat-room-create.log >&2 || true
  rm -f /tmp/nchat-room-create.log
  exit 1
fi
rm -f /tmp/nchat-room-create.log

echo
echo "==> LiveKit: connecting a real participant (lk room join --publish-demo)"
docker rm -f "$PARTICIPANT_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$PARTICIPANT_CONTAINER" --network "$DOCKER_NETWORK" \
  -e LIVEKIT_URL="ws://livekit:7880" \
  -e LIVEKIT_API_KEY="$LIVEKIT_API_KEY" \
  -e LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET" \
  "$LK_CLI_IMAGE" room join "$ROOM_NAME" --identity "$PARTICIPANT_IDENTITY" --publish-demo >/dev/null

participant_connected=0
for _ in $(seq 1 10); do
  sleep 2
  if lk_cli room participants list "$ROOM_NAME" 2>/dev/null | grep -q "$PARTICIPANT_IDENTITY"; then
    participant_connected=1
    break
  fi
done

if [ "$participant_connected" -eq 1 ]; then
  echo "  [OK] participant '${PARTICIPANT_IDENTITY}' connected to '${ROOM_NAME}'"
else
  echo "FAIL: participant did not connect to the room within the expected time." >&2
  echo "--- participant container logs ---" >&2
  docker logs "$PARTICIPANT_CONTAINER" 2>&1 | tail -n 40 >&2 || true
  exit 1
fi

echo
echo "LiveKit + coturn dev validation passed."
