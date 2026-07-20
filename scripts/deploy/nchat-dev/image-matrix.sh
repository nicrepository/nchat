#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$SCRIPT_DIR/lib.sh"

printf 'images=['
separator=''
for image in "${NCHAT_DEV_IMAGES[@]}"; do
  printf '%s"%s"' "$separator" "$image"
  separator=,
done
printf ']\n'
