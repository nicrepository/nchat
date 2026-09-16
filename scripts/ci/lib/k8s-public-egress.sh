#!/usr/bin/env bash
# Public-internet egress rules for the rendered Kubernetes manifests.
#
# Sourced by scripts/ci/k8s-manifests-check.sh, which also provides the
# yaml_document and port_pairs readers these functions use, and by
# scripts/ci/test_k8s_public_egress.sh, which proves every rule rejects what it
# claims to.

# The only NetworkPolicies allowed to name 0.0.0.0/0, one ipBlock each: the
# LiveKit API (#626), the RF-21 URL scanner and Web Push delivery (#862). Adding a
# name here is the review; a public-internet rule anywhere else fails the count.
PUBLIC_INTERNET_EGRESS_POLICIES=(
  nchat-allow-livekit-api-egress
  nchat-allow-link-safety-egress
  nchat-allow-notification-webpush-egress
)

# What a scoped public-internet rule must keep out, in full: "unspecified",
# RFC 1918 (which holds the k3s pod and service CIDRs), CGNAT, loopback,
# link-local (169.254.169.254 is the cloud metadata endpoint), benchmarking,
# multicast and the reserved block — the list nchat-allow-link-safety-egress set.
PUBLIC_INTERNET_EXCLUSIONS=(
  0.0.0.0/8
  10.0.0.0/8
  100.64.0.0/10
  127.0.0.0/8
  169.254.0.0/16
  172.16.0.0/12
  192.168.0.0/16
  198.18.0.0/15
  224.0.0.0/4
  240.0.0.0/4
)

public_internet_cidr_count() {
  grep -Ec '^[[:space:]]+cidr: 0\.0\.0\.0/0$' <<<"$1" || true
}

# validate_public_internet_egress_allowlist fails unless every 0.0.0.0/0 in the
# rendered application sits in exactly one of the named policies, once each.
# Counting per policy and comparing with the total is what makes a fourth rule
# fail even when it is the only one in a policy of its own.
validate_public_internet_egress_allowlist() {
  local application="$1" policy count total authorised=0
  for policy in "${PUBLIC_INTERNET_EGRESS_POLICIES[@]}"; do
    count="$(public_internet_cidr_count "$(yaml_document "$application" NetworkPolicy "$policy")")"
    if [[ "$count" -ne 1 ]]; then
      echo "error: $policy must contain exactly one 0.0.0.0/0 ipBlock (found $count)" >&2
      return 1
    fi
    authorised=$((authorised + count))
  done
  total="$(public_internet_cidr_count "$(cat "$application")")"
  if [[ "$total" -ne "$authorised" ]]; then
    echo "error: 0.0.0.0/0 is allowed exactly ${#PUBLIC_INTERNET_EGRESS_POLICIES[@]} times, for ${PUBLIC_INTERNET_EGRESS_POLICIES[*]} (found $total)" >&2
    return 1
  fi
}

# spec_section prints the lines under one two-space key of a rendered
# NetworkPolicy document (kustomize output: list items sit at the key's own
# indent), up to the next key at that level.
spec_section() {
  awk -v key="$2" '$0 ~ "^  " key ":" { f = 1; next } f && /^  [^ -]/ { f = 0 } f' <<<"$1"
}

# egress_rule_peers prints the first key of every peer in the "to" list of the
# egress section it is given, one per line. A rule with no "to", or "to: []",
# prints nothing — and in a NetworkPolicy that means every destination.
egress_rule_peers() {
  awk '
    /^(  - |    )to:/ { f = 1; next }
    f && /^    - / { sub(/^    - /, ""); print; next }
    f && (/^    [^ -]/ || /^  [^ ]/) { f = 0 }
  ' <<<"$1"
}

# validate_notification_webpush_egress pins the Web Push rule (#862) to what it
# is for, one invariant at a time, each with its own message: notification-service
# alone, egress alone, exactly one rule, that rule's only peer the public ipBlock
# with every exclusion above, and its only port TCP/443. A rule the port reader
# would not see — "{}", no ports, no "to" — is refused by name rather than
# skipped: in a NetworkPolicy an empty egress rule allows everything.
validate_notification_webpush_egress() {
  local application="$1" name=nchat-allow-notification-webpush-egress policy_block egress rules
  policy_block="$(yaml_document "$application" NetworkPolicy "$name")"
  if [[ -z "$policy_block" ]]; then
    echo "error: $name is missing; notification-service cannot reach a push service" >&2
    return 1
  fi
  if [[ "$(spec_section "$policy_block" podSelector)" != "$(printf '%s\n%s' '    matchLabels:' '      app.kubernetes.io/component: notification')" ]]; then
    echo "error: $name must select only app.kubernetes.io/component: notification" >&2
    return 1
  fi
  if [[ "$(spec_section "$policy_block" policyTypes)" != "  - Egress" ]]; then
    echo "error: $name must be an Egress-only policy" >&2
    return 1
  fi
  egress="$(spec_section "$policy_block" egress)"
  if grep -Eq '^  - \{\}[[:space:]]*$' <<<"$egress"; then
    echo "error: $name must not contain an empty egress rule" >&2
    return 1
  fi
  rules="$(grep -Ec '^  - ' <<<"$egress" || true)"
  if [[ "$rules" -ne 1 ]]; then
    echo "error: $name must have exactly one egress rule (found $rules)" >&2
    return 1
  fi
  if [[ "$(port_pairs "$egress")" != "TCP/443" ]]; then
    echo "error: $name must declare ports with exactly TCP/443" >&2
    return 1
  fi
  if [[ "$(egress_rule_peers "$egress")" != "ipBlock:" ]]; then
    echo "error: $name must have exactly one peer, the public ipBlock" >&2
    return 1
  fi
  if [[ "$(grep -E '^[[:space:]]+cidr:' <<<"$egress" | tr -s ' ')" != " cidr: 0.0.0.0/0" ]]; then
    echo "error: $name must target exactly cidr 0.0.0.0/0" >&2
    return 1
  fi
  if [[ "$(awk '/^[[:space:]]+except:$/ { f = 1; next } f && /^[[:space:]]+- / { print $2; next } { f = 0 }' <<<"$egress" | LC_ALL=C sort)" != \
    "$(printf '%s\n' "${PUBLIC_INTERNET_EXCLUSIONS[@]}" | LC_ALL=C sort)" ]]; then
    echo "error: $name must exclude exactly ${PUBLIC_INTERNET_EXCLUSIONS[*]}" >&2
    return 1
  fi
}
