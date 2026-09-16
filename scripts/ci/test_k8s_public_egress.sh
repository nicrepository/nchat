#!/usr/bin/env bash
# Negative tests for the public-internet egress rules (issues #626, RF-21, #862).
#
# A gate is only worth its runtime if it fails on what it claims to catch. Each
# case below doctors a rendered manifest in one way and expects the rules in
# scripts/ci/lib/k8s-public-egress.sh to refuse it — a fourth 0.0.0.0/0 above
# all, which a count that was merely raised from two to three would not notice.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-public-egress-test.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# The two manifest readers live in the check itself, which renders every overlay
# as soon as it is sourced. Only their definitions are loaded here, so the rules
# are tested against the exact readers CI runs them with.
eval "$(awk '/^(yaml_document|port_pairs)\(\) \{$/ { f = 1 } f { print } f && /^\}$/ { f = 0 }' \
  "$ROOT_DIR/scripts/ci/k8s-manifests-check.sh")"
# shellcheck source=scripts/ci/lib/k8s-public-egress.sh
source "$ROOT_DIR/scripts/ci/lib/k8s-public-egress.sh"

touch "$WORK/failures"

public_policy() {
  local name="$1" selector="$2" except="$3"
  cat <<YAML
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: $name
  namespace: nchat-dev
spec:
  egress:
  - ports:
    - port: 443
      protocol: TCP
    to:
    - ipBlock:
        cidr: 0.0.0.0/0
$except
  podSelector:
$selector
  policyTypes:
  - Egress
YAML
}

exclusions() {
  printf '        except:\n'
  printf '        - %s\n' "${PUBLIC_INTERNET_EXCLUSIONS[@]}"
}

notification_selector=$'    matchLabels:\n      app.kubernetes.io/component: notification'

# The shape CI renders today: three named policies, one public ipBlock each.
valid_manifest() {
  public_policy nchat-allow-livekit-api-egress $'    matchLabels:\n      app.kubernetes.io/component: media' ""
  public_policy nchat-allow-link-safety-egress \
    $'    matchExpressions:\n    - key: app.kubernetes.io/component\n      operator: In\n      values:\n      - chat\n      - file' \
    "$(exclusions)"
  public_policy nchat-allow-notification-webpush-egress "$notification_selector" "$(exclusions)"
}

# Runs both rules over the manifest on stdin and compares the verdict with the
# expectation. A rejection must also name the rule it was meant to trip: a
# doctored fixture that failed for an unrelated reason would otherwise pass as a
# caught regression. Every case is fed through a pipe, so it runs in a subshell;
# failures are recorded in a file rather than in a variable that would not
# survive it.
expect() {
  local name="$1" wanted="$2" reason="${3:-}" manifest="$WORK/manifest.yaml" verdict=accept
  cat >"$manifest"
  : >"$WORK/stderr"
  # Both rules run on every case, so a rejection can be attributed to the rule
  # that owns the invariant even when the other one would also have refused.
  if ! validate_public_internet_egress_allowlist "$manifest" >/dev/null 2>>"$WORK/stderr"; then
    verdict=reject
  fi
  if ! validate_notification_webpush_egress "$manifest" >/dev/null 2>>"$WORK/stderr"; then
    verdict=reject
  fi
  if [[ "$verdict" != "$wanted" ]] || { [[ -n "$reason" ]] && ! grep -Fq -- "$reason" "$WORK/stderr"; }; then
    echo "  [FAIL] $name: wanted $wanted${reason:+ ($reason)}, got $verdict: $(cat "$WORK/stderr")" >&2
    echo "$name" >>"$WORK/failures"
    return
  fi
  echo "  [OK]   $name"
}

without_webpush_policy() {
  local manifest="$WORK/valid.yaml"
  valid_manifest >"$manifest"
  yaml_document "$manifest" NetworkPolicy nchat-allow-livekit-api-egress | sed '1i ---'
  yaml_document "$manifest" NetworkPolicy nchat-allow-link-safety-egress | sed '1i ---'
}

valid_manifest | expect "the three authorised policies are accepted" accept

# The Web Push policy rewritten by one sed program, beside the two others intact.
webpush_with() {
  without_webpush_policy
  public_policy nchat-allow-notification-webpush-egress "$notification_selector" "$(exclusions)" | sed "$1"
}

# The egress section replaced wholesale, for rules the valid shape cannot be
# edited into.
webpush_egress() {
  without_webpush_policy
  public_policy nchat-allow-notification-webpush-egress "$notification_selector" "$(exclusions)" |
    awk -v replacement="$1" '
      /^  egress:/ { print; print replacement; skip = 1; next }
      skip && /^  [^ -]/ { skip = 0 }
      !skip { print }
    '
}

valid_rule=$'  - ports:\n    - port: 443\n      protocol: TCP\n    to:\n    - ipBlock:\n        cidr: 0.0.0.0/0\n'"$(exclusions)"

webpush_egress '  - {}' |
  expect "1. egress: - {} as the only rule is rejected" reject "must not contain an empty egress rule"

webpush_egress "$valid_rule"$'\n  - {}' |
  expect "2. a second, empty egress rule is rejected" reject "must not contain an empty egress rule"

webpush_egress $'  - to:\n    - ipBlock:\n        cidr: 0.0.0.0/0\n'"$(exclusions)" |
  expect "3. a rule without ports is rejected" reject "must declare ports with exactly TCP/443"

webpush_egress $'  - ports: []\n    to:\n    - ipBlock:\n        cidr: 0.0.0.0/0\n'"$(exclusions)" |
  expect "4. a rule with ports: [] is rejected" reject "must declare ports with exactly TCP/443"

webpush_egress "$valid_rule"$'\n  - ports:\n    - port: 5432\n      protocol: TCP\n    to:\n    - podSelector:\n        matchLabels:\n          app.kubernetes.io/component: postgres' |
  expect "5. TCP/443 plus a second arbitrary rule is rejected" reject "must have exactly one egress rule (found 2)"

webpush_with 's/^    - ipBlock:$/    - namespaceSelector: {}\n    - ipBlock:/' |
  expect "6a. an extra in-cluster peer is rejected" reject "must have exactly one peer, the public ipBlock"

webpush_egress $'  - ports:\n    - port: 443\n      protocol: TCP' |
  expect "6b. a rule with no peer at all (every destination) is rejected" reject "must have exactly one peer, the public ipBlock"

webpush_with 's/^      protocol: TCP$/      protocol: TCP\n    - port: 80\n      protocol: TCP/' |
  expect "7. an extra port is rejected" reject "must declare ports with exactly TCP/443"

for dropped in 10.0.0.0/8 169.254.0.0/16 100.64.0.0/10 240.0.0.0/4; do
  webpush_with "/^        - ${dropped//\//\\/}\$/d" |
    expect "8. the $dropped exclusion removed is rejected" reject "must exclude exactly"
done

{ valid_manifest; public_policy nchat-allow-search-egress $'    matchLabels:\n      app.kubernetes.io/component: search' "$(exclusions)"; } |
  expect "9. a fourth public 0.0.0.0/0 policy is rejected" reject "0.0.0.0/0 is allowed exactly 3 times"

valid_manifest | sed '0,/cidr: 0.0.0.0\/0/s//cidr: 0.0.0.0\/0\n    - ipBlock:\n        cidr: 0.0.0.0\/0/' |
  expect "a second 0.0.0.0/0 inside an authorised policy is rejected" reject "must contain exactly one 0.0.0.0/0 ipBlock (found 2)"

without_webpush_policy |
  expect "a missing Web Push policy is rejected" reject "nchat-allow-notification-webpush-egress must contain exactly one 0.0.0.0/0 ipBlock (found 0)"

{ without_webpush_policy; public_policy nchat-allow-notification-webpush-egress $'    matchLabels:\n      app.kubernetes.io/component: chat' "$(exclusions)"; } |
  expect "the Web Push policy selecting another component is rejected" reject "must select only app.kubernetes.io/component: notification"

webpush_with 's/^  - Egress$/  - Egress\n  - Ingress/' |
  expect "the Web Push policy also governing ingress is rejected" reject "must be an Egress-only policy"

if [[ -s "$WORK/failures" ]]; then
  echo "public-internet egress tests failed: $(wc -l <"$WORK/failures")" >&2
  exit 1
fi
echo "public-internet egress tests passed."
