#!/usr/bin/env bash
# Open or close manual presence (issue #798): members choosing Busy, Do Not
# Disturb, Volto já, Away, Available or Appear offline.
#
#   manual-presence.sh --status
#   manual-presence.sh --set true
#   manual-presence.sh --set false
#
# A chat-service from before #798 neither reads chat.user_presence nor
# understands the presence.settings_changed hint, and a notification-service
# from before it ignores Do Not Disturb: while either still runs, a status
# chosen is not honoured and appearing offline would not hide anybody. So the
# gate opens only when the cluster proves none is left — every pod and every
# scaled-up Deployment of both in both slots carries the release's
# nchat.io/capability-manual-presence annotation (lib.sh,
# require_presence_capable_cluster) — and the first opening is recorded on
# nchat-config, which from then on makes cutover and rollback refuse a slot
# without it (require_presence_capable_target). Runbook, section 16c.
#
# Every other step — patch, restart both slots, wait, prove what the pods
# loaded — is chat-capability.sh.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

NCHAT_PROD_CAPABILITY_KEY=CHAT_MANUAL_PRESENCE_ENABLED
NCHAT_PROD_CAPABILITY_PRECONDITION="Precondition, checked below: no chat-service or notification-service from before
issue #798 is left in either slot (make prod-blue-green-drain-old)."

# shellcheck source=scripts/deploy/nchat-prod/chat-capability.sh
source "$SCRIPT_DIR/chat-capability.sh"

capability_precheck() { require_presence_capable_cluster; }
capability_before_enable() { mark_manual_presence_activated; }
capability_after_enable() { require_presence_capable_cluster; }

capability_main "$@"
