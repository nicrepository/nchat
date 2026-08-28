#!/usr/bin/env bash
# CI-only config check for PoC scripts — does NOT start containers.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

echo "=== PoC config check ==="

# ---------------------------------------------------------------------------
# 1. Scripts exist and are executable
# ---------------------------------------------------------------------------
SCRIPTS=(
  "scripts/poc/seaweedfs-poc.sh"
  "scripts/poc/valkey-poc.sh"
)

for s in "${SCRIPTS[@]}"; do
  FULL="$ROOT_DIR/$s"
  if [ ! -f "$FULL" ]; then
    echo "[ERROR] Script not found: $s" >&2
    exit 1
  fi
  if [ ! -x "$FULL" ]; then
    echo "[ERROR] Script not executable: $s" >&2
    exit 1
  fi
  echo "[OK]   exists and executable: $s"
done

# ---------------------------------------------------------------------------
# 2. Bash syntax check
# ---------------------------------------------------------------------------
for s in "${SCRIPTS[@]}"; do
  FULL="$ROOT_DIR/$s"
  if ! bash -n "$FULL"; then
    echo "[ERROR] Syntax error in: $s" >&2
    exit 1
  fi
  echo "[OK]   bash -n: $s"
done

# ---------------------------------------------------------------------------
# 3. poc-results/ is gitignored
# ---------------------------------------------------------------------------
cd "$ROOT_DIR"
if ! git check-ignore poc-results/test.txt > /dev/null 2>&1; then
  echo "[ERROR] poc-results/test.txt is NOT gitignored — update .gitignore" >&2
  exit 1
fi
echo "[OK]   poc-results/ is gitignored"

# ---------------------------------------------------------------------------
# 4. compose.dev.yml has seaweed-volume-2 and profile seaweed-replication
# ---------------------------------------------------------------------------
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
if ! grep -q "seaweed-volume-2" "$COMPOSE_FILE"; then
  echo "[ERROR] compose.dev.yml missing seaweed-volume-2 service" >&2
  exit 1
fi
echo "[OK]   compose.dev.yml has seaweed-volume-2"

if ! grep -q "seaweed-replication" "$COMPOSE_FILE"; then
  echo "[ERROR] compose.dev.yml missing seaweed-replication profile" >&2
  exit 1
fi
echo "[OK]   compose.dev.yml has seaweed-replication profile"

# ---------------------------------------------------------------------------
# 5. package.json has poc targets
# ---------------------------------------------------------------------------
PACKAGE_JSON="$ROOT_DIR/package.json"
for target in "poc:seaweedfs" "poc:valkey" "poc:config-check"; do
  if ! grep -q "\"$target\"" "$PACKAGE_JSON"; then
    echo "[ERROR] package.json missing script: $target" >&2
    exit 1
  fi
  echo "[OK]   package.json has: $target"
done

# ---------------------------------------------------------------------------
# 6. Makefile has poc targets
# ---------------------------------------------------------------------------
MAKEFILE="$ROOT_DIR/Makefile"
for target in "poc-seaweedfs" "poc-valkey" "poc-config-check"; do
  if ! grep -q "^${target}:" "$MAKEFILE"; then
    echo "[ERROR] Makefile missing target: $target" >&2
    exit 1
  fi
  echo "[OK]   Makefile has: $target"
done

echo ""
echo "PoC config check passed."
