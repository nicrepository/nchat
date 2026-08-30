#!/usr/bin/env bash
# Collect the cluster-wide half of the capacity preflight (issue #626).
#
#   scripts/deploy/nchat-prod/capacity-evidence.sh <output-directory>
#
# Run this from a context that may read Nodes and Pods across namespaces -- a
# cluster administrator, or a read-only identity kept for the purpose. The
# production deploy identity is namespaced and cannot; that is the point. It
# then reads what this writes:
#
#   NCHAT_PROD_CAPACITY_EVIDENCE_DIR=<output-directory> \
#     scripts/deploy/nchat-prod/deploy.sh
#
# The evidence is a snapshot of somebody else's namespaces. It is not committed,
# and the directory belongs outside the repository.
#
# Refuses to publish anything it could not collect in full: a kubectl that
# failed, or a resource that came back empty, ends the run with no metadata
# written, and the deployer treats a directory without valid metadata as absent.
#
# Re-collecting into a directory that already holds evidence withdraws it first.
# A refresh that fails therefore leaves that destination unusable rather than
# leaving the previous snapshot standing: "the collection failed" and "the
# deploy may still read what was there" cannot both be true, or the failure is
# one an operator can walk straight past. Collect again to restore it.
# An evidence file that reads as "no allocatable capacity" or "nothing committed
# yet" is the one shape that could turn a shortfall into a pass.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

OUTPUT_DIR="${1:-}"
STAGING="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nchat-prod-capacity.XXXXXX")"
trap 'rm -rf "$STAGING"' EXIT

# Written last and moved last, so a directory carrying valid metadata carries a
# complete collection. Deliberately no cluster identity beyond the namespace the
# snapshot is for and the context name that produced it: this record is read by
# an operator and by lib.sh, and neither needs anything else about the cluster.
#
# Both values are captured and checked before anything is written. Inside
# `printf '%s' "$(cmd)"` the exit status belongs to printf, so a kubectl that
# could not reach the API and a date that failed both produced an empty field
# and a successful write -- and the run carried on to publish a snapshot whose
# collected_at was blank, which the deployer would then refuse for a reason that
# named the timestamp rather than the command that never ran.
write_metadata() {
  local file="$1" collected_at collector_context
  collected_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)" ||
    prod_fail "could not read the current time; refusing to date a snapshot with nothing"
  collector_context="$(kubectl config current-context)" ||
    prod_fail "could not read the current kube context; refusing to publish a snapshot of an unidentified cluster"
  [[ -n "$collected_at" && -n "$collector_context" ]] ||
    prod_fail "the time or the kube context came back empty; refusing to publish incomplete metadata"
  {
    printf 'schema=%s\n' "$NCHAT_PROD_CAPACITY_EVIDENCE_SCHEMA"
    printf 'collected_at=%s\n' "$collected_at"
    printf 'namespace=%s\n' "$NCHAT_PROD_NAMESPACE"
    printf 'collector_context=%s\n' "$collector_context"
  } >"$file"
}

# Withdraws whatever was published here before this collection starts.
#
# The metadata record is what load_capacity_evidence needs before it will look
# at anything else, so removing it is enough to make the directory unacceptable
# without touching the payload. It is also the file publish() copies last, which
# makes it the commit marker for the new collection: between these two points
# the destination is objectively not loadable.
#
# Without this, a refresh that failed left the previous snapshot in place and
# still loadable -- so an operator who re-collected, saw the collector fail, and
# deployed anyway got yesterday's picture of the cluster with nothing to say so.
# One named file, never `rm -rf` on an operator-supplied path.
invalidate_previous_publication() {
  rm -f -- "$OUTPUT_DIR/metadata"
}

# Copies the staged collection over the destination, metadata last.
#
# Payload first and marker last, so a copy that fails part-way leaves a
# directory with no metadata -- refused -- rather than a marker vouching for a
# half-replaced payload.
publish() {
  local name
  mkdir -p "$OUTPUT_DIR"
  for name in "${NCHAT_PROD_CAPACITY_EVIDENCE_FILES[@]}" sha256sums.txt metadata; do
    cp -- "$STAGING/$name" "$OUTPUT_DIR/$name"
  done
}

main() {
  [[ -n "$OUTPUT_DIR" ]] ||
    prod_fail "usage: capacity-evidence.sh <output-directory>"
  invalidate_previous_publication
  collect_cluster_capacity_files "$STAGING" ||
    prod_fail "could not read the cluster's Nodes and Pods; run this from a context that may, and check the result before deploying"
  capacity_evidence_files_present "$STAGING" ||
    prod_fail "the cluster returned nothing for one of the capacity inputs; refusing to publish a snapshot that would read as free capacity"
  # Integrity only. It catches a truncated or edited file; it is not a signature
  # and proves nothing about who produced the directory.
  (cd "$STAGING" && sha256sum "${NCHAT_PROD_CAPACITY_EVIDENCE_FILES[@]}" >sha256sums.txt)
  write_metadata "$STAGING/metadata"
  publish
  echo "capacity evidence written to $OUTPUT_DIR"
  echo "it is valid for ${NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS}s; deploy with"
  echo "  NCHAT_PROD_CAPACITY_EVIDENCE_DIR=$OUTPUT_DIR"
}

main "$@"
