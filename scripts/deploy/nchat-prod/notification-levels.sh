#!/usr/bin/env bash
# Open or close the granular conversation notification levels (issue #136).
#
#   notification-levels.sh --status
#   notification-levels.sh --set true
#   notification-levels.sh --set false
#
# The gate lives in chat-service's SidebarService, and the web app learns the
# capability from chat-service's own sidebar payload. Every step — patch,
# restart both slots, wait, prove what the pods loaded — is chat-capability.sh.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

NCHAT_PROD_CAPABILITY_KEY=CHAT_CONVERSATION_NOTIFICATION_LEVELS_ENABLED
NCHAT_PROD_CAPABILITY_PRECONDITION="Precondition: no reader from before issue #136 is left — neither an HTTP
slot, nor a realtime connection, nor a notification worker."

# shellcheck source=scripts/deploy/nchat-prod/chat-capability.sh
source "$SCRIPT_DIR/chat-capability.sh"
capability_main "$@"
