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
  find "$ROOT/.github/workflows" "$ROOT/infra/k8s" -type f \( -name '*.yml' -o -name '*.yaml' \) -print | sort
)
yaml_files+=("$ROOT/.gitlab-ci.yml")

if command -v ruby >/dev/null 2>&1; then
  ruby -e 'require "yaml"; ARGV.each { |file| YAML.load_file(file) }' "${yaml_files[@]}"
else
  echo "ruby not found; skipping generic YAML syntax parse."
fi

if command -v yamllint >/dev/null 2>&1; then
  yamllint -d '{extends: default, rules: {document-start: disable, truthy: disable, line-length: disable}}' "${yaml_files[@]}"
else
  echo "yamllint not found; skipping yamllint."
fi

if command -v actionlint >/dev/null 2>&1; then
  (cd "$ROOT" && actionlint)
else
  echo "actionlint not found; skipping GitHub Actions lint."
  echo "Install it with: go install github.com/rhysd/actionlint/cmd/actionlint@latest"
fi

if command -v gitlab-ci-lint >/dev/null 2>&1; then
  gitlab-ci-lint "$ROOT/.gitlab-ci.yml"
else
  echo "gitlab-ci-lint not found; skipping GitLab CI remote/schema lint."
fi

printf '%s\n' 'CI config check passed.'
