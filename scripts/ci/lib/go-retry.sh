# Retries a Go module-resolving command with backoff (source, not executable).
#
# go test/go vet resolve every module dependency on first run, exactly like the
# `go install` in install-golangci-lint.sh already had to guard against — a
# single transient network error from the module proxy or sum.golang.org
# ("stream error ... INTERNAL_ERROR" over HTTP/2) otherwise fails the whole CI
# job, even for a module the failure has nothing to do with (go-test.sh/go-vet.sh/
# go-coverage-check.sh iterate every module in one run, so a flaky fetch for one
# module's dependency aborts every other module's step too). Retried with
# backoff rather than disabling GOSUMDB/GONOSUMCHECK, which would drop
# supply-chain verification instead of just tolerating a flaky fetch.
go_retry() {
  local max_attempts=3 attempt=1 delay
  while true; do
    if "$@"; then
      return 0
    fi
    if ((attempt >= max_attempts)); then
      echo "$* failed after $max_attempts attempts." >&2
      return 1
    fi
    delay=$((attempt * 10))
    echo "$* failed (attempt $attempt/$max_attempts); retrying in ${delay}s." >&2
    sleep "$delay"
    attempt=$((attempt + 1))
  done
}
