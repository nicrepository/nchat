# shellcheck shell=bash
# Source-only helper. Each attempt replaces the report: partial findings from a
# failed download must never be combined with a completed scan.
govulncheck_retry() {
  local report="$1" diagnostics="$2" attempt status
  shift 2
  for attempt in 1 2 3; do
    if "$@" >"$report" 2>"$diagnostics"; then
      cat "$diagnostics" >&2
      return 0
    else
      status=$?
    fi
    cat "$diagnostics" >&2
    # Exit 3 is a completed scan with findings, judged by the existing gate.
    if [[ "$status" -eq 3 ]]; then
      return 3
    fi
    if [[ "$attempt" -eq 3 ]] || ! grep -Eq 'fetching vulnerabilities:.*(connection reset by peer|connection refused|i/o timeout|TLS handshake timeout|context deadline exceeded|unexpected EOF|temporary failure|Temporary failure|502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout)' "$diagnostics"; then
      return "$status"
    fi
    echo "govulncheck vulnerability database fetch failed (attempt $attempt/3); retrying in $((attempt * 10))s." >&2
    sleep "$((attempt * 10))"
  done
}
