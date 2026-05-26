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
#    Rejects suspicious credential column names such as raw_token,
#    password_raw, api_key, secret, or token columns without _hash.
# ---------------------------------------------------------------------------
echo "--- no plaintext token/password columns ---"
is_plaintext_credential_column() {
  local column="$1"

  case "$column" in
    *_hash)
      return 1
      ;;
    token|*_token|token_*|*_token_*|raw_token|*_raw_token*)
      return 0
      ;;
    secret|*_secret|secret_*|*_secret_*|api_key|*_api_key|api_key_*|*_api_key_*)
      return 0
      ;;
    password|raw_password|password_raw|plain_password|password_plain|plaintext_password|password_plaintext|cleartext_password|password_cleartext|*_raw_password|*_password_raw|*_plain_password|*_password_plain|*_plaintext_password|*_password_plaintext|*_cleartext_password|*_password_cleartext)
      return 0
      ;;
  esac

  return 1
}

for f in "${UP_FILES[@]}"; do
  bad_columns=0

  while IFS= read -r line; do
    line="${line%%--*}"
    if [[ "$line" =~ ^[[:space:]]*([a-zA-Z_][a-zA-Z0-9_]*)[[:space:]]+ ]]; then
      column="${BASH_REMATCH[1],,}"
      if is_plaintext_credential_column "$column"; then
        fail "possible plaintext credential column in $(basename "$f"): $column"
        bad_columns=$((bad_columns + 1))
      fi
    fi
  done <"$f"

  if [ "$bad_columns" -eq 0 ]; then
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
# 7. Down migrations must not DROP EXTENSION
#    Extensions are database-wide; dropping them would break other schemas.
# ---------------------------------------------------------------------------
echo "--- down migrations do not DROP EXTENSION ---"
for up in "${UP_FILES[@]}"; do
  down="${up/.up.sql/.down.sql}"
  [ -f "$down" ] || continue
  if grep -qiE "DROP[[:space:]]+EXTENSION" "$down"; then
    fail "down migration must not DROP EXTENSION (database-wide side effect): $(basename "$down")"
  else
    ok "no DROP EXTENSION: $(basename "$down")"
  fi
done
echo

# ---------------------------------------------------------------------------
# 8. CREATE TABLE must be schema-qualified
#    Allowlisted: schema_migrations (lives in public by design)
# ---------------------------------------------------------------------------
echo "--- CREATE TABLE schema qualification ---"
schema_qual_ok=true
for f in "${UP_FILES[@]}"; do
  while IFS= read -r line; do
    clean="${line%%--*}"
    echo "$clean" | grep -qiE "CREATE[[:space:]]+TABLE[[:space:]]" || continue
    # Extract the table name token (last word before whitespace/paren)
    tname=$(echo "$clean" \
      | grep -oiE "CREATE[[:space:]]+TABLE[[:space:]]+(IF[[:space:]]+NOT[[:space:]]+EXISTS[[:space:]]+)?[a-zA-Z_][a-zA-Z0-9_.]*" \
      | grep -oiE "[a-zA-Z_][a-zA-Z0-9_.]*$" \
      | head -1)
    [ -z "$tname" ] && continue
    if echo "$tname" | grep -q '\.'; then
      continue  # schema-qualified
    fi
    case "${tname}" in
      schema_migrations) ;;  # allowlisted: public.schema_migrations
      *)
        fail "unqualified CREATE TABLE '$tname' in $(basename "$f") — use schema.tablename"
        schema_qual_ok=false
        ;;
    esac
  done < "$f"
done
$schema_qual_ok && ok "all CREATE TABLE statements are schema-qualified"
echo

# ---------------------------------------------------------------------------
# 9. Auth domain migrations must reference auth. schema
# ---------------------------------------------------------------------------
echo "--- auth domain schema consistency ---"
for f in "${UP_FILES[@]}"; do
  domain_dir="$(basename "$(dirname "$f")")"
  [ "$domain_dir" = "auth" ] || continue
  if grep -qi "auth\." "$f"; then
    ok "references auth. schema: $(basename "$f")"
  else
    fail "auth domain migration does not reference auth. schema: $(basename "$f")"
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
