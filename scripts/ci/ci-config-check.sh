#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

required_files=(
  "$ROOT/.github/workflows/ci.yml"
  "$ROOT/.github/workflows/security.yml"
  "$ROOT/.gitlab-ci.yml"
)

for file in "${required_files[@]}"; do
  if [[ ! -f "$file" ]]; then
    echo "Required CI config is missing: ${file#$ROOT/}" >&2
    exit 1
  fi
done

mapfile -t yaml_files < <(
  find "$ROOT/.github/workflows" "$ROOT/infra/k8s" -type f \
    \( -name '*.yml' -o -name '*.yaml' \) \
    ! -path "$ROOT/infra/k8s/security/sealed-secrets/controller/controller.yaml" -print | sort
)
yaml_files+=("$ROOT/.gitlab-ci.yml")

if command -v ruby >/dev/null 2>&1; then
  ruby -e 'require "yaml"; ARGV.each { |file| YAML.load_file(file) }' "${yaml_files[@]}"
else
  echo "ruby not found; skipping generic YAML syntax parse."
fi

if command -v yamllint >/dev/null 2>&1; then
  yamllint -d '{extends: default, rules: {document-start: disable, truthy: disable, line-length: disable, comments: {min-spaces-from-content: 1}}}' "${yaml_files[@]}"
else
  echo "yamllint not found; skipping yamllint."
fi

if command -v actionlint >/dev/null 2>&1; then
  (cd "$ROOT" && actionlint)
else
  echo "actionlint not found; skipping GitHub Actions lint."
  echo "Install the repository-approved actionlint version before running this check."
fi

for workflow in governance.yml security.yml images.yml build-nchat-images.yml deploy-nchat-dev.yml deploy-nchat-prod.yml; do
  while IFS= read -r line; do
    [[ "$line" =~ uses:[[:space:]]*([^[:space:]#]+) ]] || continue
    reference="${BASH_REMATCH[1]}"
    [[ "$reference" == ./* ]] && continue
    [[ "$reference" =~ ^[^@]+@[a-f0-9]{40}$ ]] || {
      echo "Remote action is not pinned by a full SHA in $workflow: $reference" >&2
      exit 1
    }
  done <"$ROOT/.github/workflows/$workflow"
done

if grep -En '@(latest|main|master)([^A-Za-z0-9_.-]|$)' \
  "$ROOT/.github/workflows/security.yml" "$ROOT/.github/workflows/images.yml" \
  "$ROOT/.github/workflows/build-nchat-images.yml" \
  "$ROOT/.github/workflows/deploy-nchat-dev.yml" \
  "$ROOT/.github/workflows/deploy-nchat-prod.yml"; then
  echo "Mutable tool/action reference found in nchat deployment workflows." >&2
  exit 1
fi

if grep -q 'pull_request_target:' "$ROOT/.github/workflows/security.yml" \
  "$ROOT/.github/workflows/images.yml" "$ROOT/.github/workflows/build-nchat-images.yml" \
  "$ROOT/.github/workflows/deploy-nchat-dev.yml" \
  "$ROOT/.github/workflows/deploy-nchat-prod.yml"; then
  echo "pull_request_target is prohibited in nchat deployment workflows." >&2
  exit 1
fi

if command -v gitlab-ci-lint >/dev/null 2>&1; then
  gitlab-ci-lint "$ROOT/.gitlab-ci.yml"
else
  echo "gitlab-ci-lint not found; skipping GitLab CI remote/schema lint."
fi

printf '%s\n' 'CI config check passed.'
