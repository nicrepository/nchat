#!/usr/bin/env bash
# CI validation for SQL migration files: no database connection required.
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

validate_domain_name() {
  local domain="$1"
  [[ "$domain" =~ ^[a-z][a-z0-9_]*$ ]]
}

trim_sql_token() {
  local token="$1"
  token="${token#${token%%[![:space:]]*}}"
  token="${token%${token##*[![:space:]]}}"
  token="${token%;}"
  token="${token%,}"
  printf '%s' "$token"
}

statement_stream_check() {
  local file="$1" callback="$2" statement="" line clean current
  while IFS= read -r line || [[ -n "$line" ]]; do
    clean="${line%%--*}"
    statement+=" $clean"
    while [[ "$statement" == *";"* ]]; do
      current="${statement%%;*};"
      "$callback" "$file" "$current"
      statement="${statement#*;}"
    done
  done < "$file"
  if [[ -n "${statement//[[:space:]]/}" ]]; then
    "$callback" "$file" "$statement"
  fi
}

echo "=== migrations check ==="
echo

# ---------------------------------------------------------------------------
# 1. Collect migration files
# ---------------------------------------------------------------------------
mapfile -t UP_FILES < <(if [[ -d "$MIGRATIONS_DIR" ]]; then find "$MIGRATIONS_DIR" -name "*.up.sql" | sort; fi)
mapfile -t DOWN_FILES < <(if [[ -d "$MIGRATIONS_DIR" ]]; then find "$MIGRATIONS_DIR" -name "*.down.sql" | sort; fi)

if [ ${#UP_FILES[@]} -eq 0 ]; then
  fail "No *.up.sql files found under $MIGRATIONS_DIR"
  echo
  echo "migrations check failed with $ERRORS error(s)."
  exit 1
fi

echo "Found ${#UP_FILES[@]} up migration(s)."
echo "Found ${#DOWN_FILES[@]} down migration(s)."
echo

# ---------------------------------------------------------------------------
# 2. Domains and filenames must be safe
# ---------------------------------------------------------------------------
echo "--- domain and filename convention ---"
for f in "${UP_FILES[@]}" "${DOWN_FILES[@]}"; do
  domain_dir="$(basename "$(dirname "$f")")"
  base="$(basename "$f")"

  if validate_domain_name "$domain_dir"; then
    ok "domain ok: $domain_dir"
  else
    fail "domain convention violation (expected ^[a-z][a-z0-9_]*$): $domain_dir"
  fi

  case "$base" in
    *.up.sql)
      if [[ "$base" =~ ^[0-9]{6}_[a-z0-9_]+\.up\.sql$ ]]; then
        ok "up filename ok: $base"
      else
        fail "up filename convention violation: $base"
      fi
      ;;
    *.down.sql)
      if [[ "$base" =~ ^[0-9]{6}_[a-z0-9_]+\.down\.sql$ ]]; then
        ok "down filename ok: $base"
      else
        fail "down filename convention violation: $base"
      fi
      ;;
  esac
done
echo

# ---------------------------------------------------------------------------
# 3. Each up migration must have a corresponding down migration
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
for down in "${DOWN_FILES[@]}"; do
  up="${down/.down.sql/.up.sql}"
  if [ ! -f "$up" ]; then
    fail "orphan down migration without matching up: $(basename "$down")"
  fi
done
echo

# ---------------------------------------------------------------------------
# 4. Migration files must be non-empty and transactional
# ---------------------------------------------------------------------------
echo "--- files are non-empty and transactional ---"
for check in "${UP_FILES[@]}" "${DOWN_FILES[@]}"; do
  [ -f "$check" ] || continue
  base="$(basename "$check")"
  if [ ! -s "$check" ]; then
    fail "empty file: $base"
    continue
  fi
  ok "non-empty: $base"
  if grep -Eq '^[[:space:]]*BEGIN[[:space:]]*;' "$check" && grep -Eq '^[[:space:]]*COMMIT[[:space:]]*;' "$check"; then
    ok "transaction wrapper: $base"
  else
    fail "missing BEGIN; / COMMIT; transaction wrapper: $base"
  fi
done
echo

# ---------------------------------------------------------------------------
# 5. No plaintext token or password columns
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

  while IFS= read -r line || [[ -n "$line" ]]; do
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
# 6. Down migrations must contain at least one DROP statement
# ---------------------------------------------------------------------------
echo "--- down migrations contain DROP statements ---"
for down in "${DOWN_FILES[@]}"; do
  [ -f "$down" ] || continue
  if ! grep -qi "DROP" "$down"; then
    fail "down migration has no DROP statement: $(basename "$down")"
  else
    ok "has DROP: $(basename "$down")"
  fi
done
echo

# ---------------------------------------------------------------------------
# 7. Down migrations must avoid broad/destructive SQL and stay domain scoped.
# ---------------------------------------------------------------------------
echo "--- down migrations are domain-scoped and safe ---"
check_down_statement() {
  local file="$1" statement="$2" domain_dir targets target table schema
  domain_dir="$(basename "$(dirname "$file")")"
  shopt -s nocasematch

  if [[ "$statement" =~ DROP[[:space:]]+DATABASE ]]; then
    fail "down migration must not DROP DATABASE: $(basename "$file")"
  fi
  if [[ "$statement" =~ DROP[[:space:]]+SCHEMA ]]; then
    fail "down migration must not DROP SCHEMA: $(basename "$file")"
  fi
  if [[ "$statement" =~ DROP[[:space:]]+OWNED ]]; then
    fail "down migration must not DROP OWNED: $(basename "$file")"
  fi
  if [[ "$statement" =~ TRUNCATE([[:space:]]|$) ]]; then
    fail "down migration must not TRUNCATE: $(basename "$file")"
  fi
  if [[ "$statement" =~ DROP[[:space:]]+EXTENSION ]]; then
    fail "down migration must not DROP EXTENSION (database-wide side effect): $(basename "$file")"
  fi

  if [[ "$statement" =~ DROP[[:space:]]+TABLE[[:space:]]+(IF[[:space:]]+EXISTS[[:space:]]+)?(.+) ]]; then
    targets="${BASH_REMATCH[2]}"
    targets="${targets//;/}"
    IFS=',' read -r -a drop_targets <<< "$targets"
    for target in "${drop_targets[@]}"; do
      target="$(trim_sql_token "$target")"
      [ -z "$target" ] && continue
      set -- $target
      table="${1:-}"
      table="${table%;}"
      table="${table%,}"
      if [[ "$table" != *.* ]]; then
        fail "unqualified DROP TABLE '$table' in $(basename "$file") - use $domain_dir.<table>"
        continue
      fi
      schema="${table%%.*}"
      if [[ "$schema" == "public" ]]; then
        fail "down migration must not DROP TABLE public.* in $(basename "$file")"
      elif [[ "$schema" != "$domain_dir" ]]; then
        fail "DROP TABLE '$table' is outside domain '$domain_dir' in $(basename "$file")"
      fi
    done
  fi

  shopt -u nocasematch
}

for down in "${DOWN_FILES[@]}"; do
  [ -f "$down" ] || continue
  statement_stream_check "$down" check_down_statement
done
ok "down migration destructive SQL guard executed"
echo

# ---------------------------------------------------------------------------
# 8. CREATE TABLE must be schema-qualified, including multiline statements.
#    Allowlisted: schema_migrations (lives in public by design).
# ---------------------------------------------------------------------------
echo "--- CREATE TABLE schema qualification ---"
schema_qual_ok=true
check_create_table_statement() {
  local file="$1" statement="$2" tname
  shopt -s nocasematch
  if [[ "$statement" =~ CREATE[[:space:]]+TABLE[[:space:]]+(IF[[:space:]]+NOT[[:space:]]+EXISTS[[:space:]]+)?([a-zA-Z_][a-zA-Z0-9_.]*) ]]; then
    tname="${BASH_REMATCH[2]}"
    if [[ "$tname" == "schema_migrations" || "$tname" == "public.schema_migrations" ]]; then
      shopt -u nocasematch
      return
    fi
    if [[ "$tname" != *.* ]]; then
      fail "unqualified CREATE TABLE '$tname' in $(basename "$file") - use schema.tablename"
      schema_qual_ok=false
    fi
  fi
  shopt -u nocasematch
}

for f in "${UP_FILES[@]}"; do
  statement_stream_check "$f" check_create_table_statement
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
