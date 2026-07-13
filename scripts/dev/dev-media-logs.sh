#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# shellcheck source=_media_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_media_env.sh"

media_compose logs -f livekit coturn
