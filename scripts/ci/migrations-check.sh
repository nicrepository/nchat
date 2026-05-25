#!/usr/bin/env bash
# CI validation for SQL migration files — no database connection required.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
MIGRATIONS_DIR="$ROOT_DIR/migrations"

ERRORS=0

fail() {
  echo "  [FAIL] $*" >&2
  ERRORS=$((ERRORS + 1))
}

ok() {
  echo "  [OK]   $*"
}

echo "=== migrations check ==="
echo

# ---------------------------------------------------------------------------
# 1. Collect all up migration files
# ---------------------------------------------------------------------------
mapfile -t UP_FILES < <(find "$MIGRATIONS_DIR" -name "*.up.sql" | sort)

if [ ${#UP_FILES[@]} -eq 0 ]; then
  fail "No *.up.sql files found under $MIGRATIONS_DIR"
  echo
  echo "migrations check failed with $ERRORS error(s)."
  exit 1
fi

echo "Found ${#UP_FILES[@]} up migration(s)."
echo

# ---------------------------------------------------------------------------
# 2. Each up migration must have a corresponding down migration
# ---------------------------------------------------------------------------
echo "--- down migration exists ---"
for up in "${UP_FILES[@]}"; do
  down="${up/.up.sql/.down.sql}"
  if [ ! -f "$down" ]; then
    fail "missing down migration for: $(basename "$up")"
  else
    ok "$(basename "$down") exists"
  fi
done
echo

# ---------------------------------------------------------------------------
# 3. Migration files must be non-empty
# ---------------------------------------------------------------------------
echo "--- files are non-empty ---"
for f in "${UP_FILES[@]}"; do
  down="${f/.up.sql/.down.sql}"
  for check in "$f" "$down"; do
    [ -f "$check" ] || continue
    if [ ! -s "$check" ]; then
      fail "empty file: $(basename "$check")"
    else
      ok "non-empty: $(basename "$check")"
    fi
  done
done
echo

# ---------------------------------------------------------------------------
# 4. No plaintext token or password columns
#    Rejects column definitions named: password, token, secret, api_key
#    (token_hash, password_hash are fine)
# ---------------------------------------------------------------------------
echo "--- no plaintext token/password columns ---"
for f in "${UP_FILES[@]}"; do
  # Match lines that look like column definitions with forbidden names
  # Allow: token_hash, password_hash, password_changed_at, password_expires_at, must_change_password
  if grep -inE '^\s+(password|token|secret|api_key)\s' "$f" | grep -ivE '_hash|_at|must_change'; then
    fail "possible plaintext credential column in: $(basename "$f")"
  else
    ok "no plaintext credentials: $(basename "$f")"
  fi
done
echo

# ---------------------------------------------------------------------------
# 5. Down migrations must contain at least one DROP statement
# ---------------------------------------------------------------------------
echo "--- down migrations contain DROP statements ---"
for up in "${UP_FILES[@]}"; do
  down="${up/.up.sql/.down.sql}"
  [ -f "$down" ] || continue
  if ! grep -qi "DROP" "$down"; then
    fail "down migration has no DROP statement: $(basename "$down")"
  else
    ok "has DROP: $(basename "$down")"
  fi
done
echo

# ---------------------------------------------------------------------------
# 6. Naming convention: NNN_<name>.(up|down).sql
# ---------------------------------------------------------------------------
echo "--- naming convention ---"
for f in "${UP_FILES[@]}"; do
  base="$(basename "$f")"
  if ! echo "$base" | grep -qE '^[0-9]{6}_[a-z0-9_]+\.up\.sql$'; then
    fail "naming convention violation (expected 000NNN_name.up.sql): $base"
  else
    ok "naming ok: $base"
  fi
done
echo

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
if [ "$ERRORS" -gt 0 ]; then
  echo "migrations check failed with $ERRORS error(s)." >&2
  exit 1
fi

echo "migrations check passed."
