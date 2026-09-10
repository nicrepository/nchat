#!/usr/bin/env bash
# The production migration entrypoint (CICD-08).
#
#   make migrations-prod-up
#
# This exists because the generic path cannot be trusted to carry a flag. A
# production migration must take the production mutation lock -- the barrier it
# shares with rollback -- and asking an operator to remember
#
#   NCHAT_PROD_MUTATION_LOCK=1 make migrations-up
#
# is not a control: the one time it is forgotten is the time a migration lands
# in the middle of a rollback's schema proof. So production has its own command,
# and that command sets the flag by construction. There is no documented way to
# migrate production without it.
#
# It defers to run-migrations.sh rather than calling migrate.sh directly, so the
# manual path is the same path the production migration Job takes, grants
# included, and the two cannot drift.
#
# `make migrations-up` stays what it was: the generic local and CI path, against
# databases with nothing to exclude.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

# The whole point of this file. Exported before anything else runs, so every
# path below inherits it.
export NCHAT_PROD_MUTATION_LOCK=1

echo "Production migration: taking the production mutation lock first." >&2
echo "If a rollback or another production mutation holds it this will refuse," >&2
echo "apply nothing, and NOT queue -- look at that operation, then run this again." >&2

# Strictly `up`, and the verb is never the operator's to give.
#
# `"${@:-up}"` let this entrypoint run `down`, `reset` and `status` -- so the one
# command documented for production, the one that takes the mutation lock, could
# also be used to tear the schema down. That is not what it exists for, and a
# down migration in particular has its own review and its own restore point (see
# section 17 of the runbook). Anything that looks like a command verb is
# refused -- `up` included, so there is one rule and no exception to get wrong --
# and the remaining arguments are passed through as options to `up`.
for argument in "$@"; do
  case "$argument" in
    -*) continue ;;
    *)
      echo "[ERROR] $(basename "$0") runs 'up' and nothing else; refusing the command '$argument'." >&2
      echo "[ERROR] A down migration or a reset is a separate, deliberate procedure with its own" >&2
      echo "[ERROR] review and restore point. Use scripts/db/migrate.sh directly for those." >&2
      exit 2
      ;;
  esac
done

exec "$SCRIPT_DIR/run-migrations.sh" up "$@"
