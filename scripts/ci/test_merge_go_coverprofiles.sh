#!/usr/bin/env bash
# Tests for merge-go-coverprofiles.awk. Run: bash scripts/ci/test_merge_go_coverprofiles.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
MERGE="$ROOT/scripts/ci/merge-go-coverprofiles.awk"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0

write() {
  local name="$1"
  shift
  printf '%s\n' "$@" >"$WORK/$name"
}

# Asserts stdout and exit status of a merge over the given profiles.
expect() {
  local label="$1" want_status="$2" want_output="$3"
  shift 3
  local output status
  output="$(awk -f "$MERGE" "$@" 2>"$WORK/stderr")"
  status=$?

  if [ "$status" -ne "$want_status" ]; then
    echo "FAIL $label: exit $status, want $want_status" >&2
    failures=$((failures + 1))
    return
  fi
  if [ "$output" != "$want_output" ]; then
    echo "FAIL $label: output" >&2
    diff <(printf '%s\n' "$want_output") <(printf '%s\n' "$output") >&2
    failures=$((failures + 1))
    return
  fi
  echo "ok   $label"
}

write a.out \
  'mode: atomic' \
  'nchat/a.go:1.1,2.2 1 3' \
  'nchat/a.go:4.1,5.2 2 0' \
  'nchat/b.go:1.1,9.9 4 1'
write b.out \
  'mode: atomic' \
  'nchat/a.go:1.1,2.2 1 5' \
  'nchat/c.go:7.1,8.2 3 2'

# Shared block combined, one-sided blocks kept, first-seen order preserved.
expect "atomic profiles merge" 0 'mode: atomic
nchat/a.go:1.1,2.2 1 8
nchat/a.go:4.1,5.2 2 0
nchat/b.go:1.1,9.9 4 1
nchat/c.go:7.1,8.2 3 2' "$WORK/a.out" "$WORK/b.out"

# Idempotent input order, so CI reruns produce byte-identical profiles.
expect "a single profile is passed through" 0 'mode: atomic
nchat/a.go:1.1,2.2 1 3
nchat/a.go:4.1,5.2 2 0
nchat/b.go:1.1,9.9 4 1' "$WORK/a.out"

# set mode counts presence, not executions: 1 + 1 must stay 1.
write set-a.out 'mode: set' 'nchat/a.go:1.1,2.2 1 1'
write set-b.out 'mode: set' 'nchat/a.go:1.1,2.2 1 1'
expect "set mode maximises instead of summing" 0 'mode: set
nchat/a.go:1.1,2.2 1 1' "$WORK/set-a.out" "$WORK/set-b.out"

write count.out 'mode: count' 'nchat/a.go:1.1,2.2 1 1'
expect "divergent modes are refused" 1 '' "$WORK/a.out" "$WORK/count.out"

write headerless.out 'nchat/a.go:1.1,2.2 1 1'
expect "a missing mode header is refused" 1 '' "$WORK/headerless.out"

write malformed.out 'mode: atomic' 'nchat/a.go:1.1,2.2 1'
expect "a malformed block line is refused" 1 '' "$WORK/malformed.out"

write conflict.out 'mode: atomic' 'nchat/a.go:1.1,2.2 9 1'
expect "a conflicting statement count is refused" 1 '' "$WORK/a.out" "$WORK/conflict.out"

# The point of all of the above: go tool cover must read the result back.
if command -v go >/dev/null 2>&1; then
  cover_dir="$WORK/src/nchat"
  mkdir -p "$cover_dir"
  cat >"$cover_dir/a.go" <<'GO'
package nchat

func A(flag bool) int {
	if flag {
		return 1
	}
	return 2
}
GO
  cat >"$cover_dir/a_test.go" <<'GO'
package nchat

import "testing"

func TestA(t *testing.T) {
	if A(true) != 1 {
		t.Fatal("A(true)")
	}
}
GO
  (cd "$cover_dir" && go mod init nchat >/dev/null 2>&1 &&
    go test . -covermode=atomic -coverprofile="$WORK/real.out" >/dev/null)
  if [ -s "$WORK/real.out" ]; then
    awk -f "$MERGE" "$WORK/real.out" "$WORK/real.out" >"$WORK/real-merged.out"
    # From the fixture module: go tool cover resolves block paths against it.
    if (cd "$cover_dir" && go tool cover -func="$WORK/real-merged.out") >/dev/null 2>&1; then
      echo "ok   go tool cover accepts the merged profile"
    else
      echo "FAIL go tool cover rejected the merged profile" >&2
      failures=$((failures + 1))
    fi
  else
    echo "skip go tool cover: could not build the fixture profile" >&2
  fi
else
  echo "skip go tool cover: go is not installed" >&2
fi

if [ "$failures" -ne 0 ]; then
  echo "$failures test(s) failed." >&2
  exit 1
fi
echo "merge-go-coverprofiles tests passed."
