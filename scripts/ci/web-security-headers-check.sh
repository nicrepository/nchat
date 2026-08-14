#!/usr/bin/env bash
set -euo pipefail

# Valida os headers de segurança do web (issue #388).
#
# Modo estático (padrão, roda no CI): valida a CSP versionada em
# infra/docker/web/nginx.conf e garante que nenhuma outra camada do repositório
# emita uma segunda política.
#
# Modo live (opcional): recebe a URL do ambiente renderizado e valida a resposta
# real, incluindo ausência de CSP duplicada/Report-Only injetada fora do repo.
#
#   bash scripts/ci/web-security-headers-check.sh https://nchat-dev.nic-labs.com

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
NGINX_CONF="$ROOT_DIR/infra/docker/web/nginx.conf"
INDEX_HTML="$ROOT_DIR/apps/web/index.html"

fail() {
  echo "$1" >&2
  exit 1
}

# Diretivas obrigatórias, na forma exata exigida pela política de segurança.
REQUIRED_DIRECTIVES=(
  "default-src 'self'"
  "base-uri 'self'"
  "object-src 'none'"
  "frame-ancestors 'none'"
  "form-action 'self'"
  "script-src 'self'"
  "connect-src 'self'"
)

directive_value() {
  # directive_value <policy> <nome-da-diretiva>
  printf '%s' "$1" | tr ';' '\n' | sed -n "s/^[[:space:]]*$2[[:space:]]\+//p"
}

assert_policy() {
  local label="$1" policy="$2" directive value

  [ -n "$policy" ] || fail "$label: Content-Security-Policy vazia."

  for directive in "${REQUIRED_DIRECTIVES[@]}"; do
    case "$policy" in
      *"$directive"*) ;;
      *) fail "$label: CSP não contém a diretiva obrigatória \"$directive\"." ;;
    esac
  done

  case "$policy" in
    *"unsafe-eval"*) fail "$label: CSP não pode conter 'unsafe-eval'." ;;
  esac

  value="$(directive_value "$policy" 'script-src')"
  case " $value " in
    *" 'unsafe-inline' "*) fail "$label: script-src não pode conter 'unsafe-inline'." ;;
    *" * "*) fail "$label: script-src não pode conter o curinga '*'." ;;
  esac

  value="$(directive_value "$policy" 'connect-src')"
  case " $value " in
    *" * "*) fail "$label: connect-src não pode conter o curinga '*'." ;;
    *" https: "*) fail "$label: connect-src não pode liberar o esquema https: inteiro." ;;
    *" wss: "*) fail "$label: connect-src não pode liberar o esquema wss: inteiro." ;;
  esac

  value="$(directive_value "$policy" 'default-src')"
  case " $value " in
    *" * "*) fail "$label: default-src não pode conter o curinga '*'." ;;
  esac
}

# --- Modo estático -----------------------------------------------------------

[ -f "$NGINX_CONF" ] || fail "Arquivo obrigatório ausente: infra/docker/web/nginx.conf"
[ -f "$INDEX_HTML" ] || fail "Arquivo obrigatório ausente: apps/web/index.html"

csp_lines="$(grep -c 'add_header Content-Security-Policy ' "$NGINX_CONF" || true)"
if [ "$csp_lines" -ne 1 ]; then
  fail "nginx.conf deve declarar exatamente um add_header Content-Security-Policy (encontrado: $csp_lines)."
fi

if grep -q 'Content-Security-Policy-Report-Only' "$NGINX_CONF"; then
  fail "nginx.conf não pode emitir Content-Security-Policy-Report-Only junto com a política enforced."
fi

nginx_policy="$(sed -n 's/.*add_header Content-Security-Policy "\(.*\)".*/\1/p' "$NGINX_CONF")"
assert_policy "nginx.conf" "$nginx_policy"

# connect-src precisa preservar same-origin via $host e o slot configurável do
# LiveKit por ambiente (issue #528): nenhum dos dois pode ser removido, e o
# slot não pode virar um hardcode de domínio no fragmento genérico.
value="$(directive_value "$nginx_policy" 'connect-src')"
case " $value " in
  *' wss://$host '*) ;;
  *) fail "nginx.conf: connect-src perdeu wss://\$host." ;;
esac
case " $value " in
  *' https://$host '*) ;;
  *) fail "nginx.conf: connect-src não contém https://\$host." ;;
esac
case " $value " in
  *' ${NCHAT_WEB_LIVEKIT_CONNECT_SRC} '*) ;;
  *) fail "nginx.conf: connect-src não contém o slot \${NCHAT_WEB_LIVEKIT_CONNECT_SRC}." ;;
esac
case " $value " in
  *'livekit-dev.nic-labs.com'*) fail "nginx.conf: connect-src não pode conter domínio de LiveKit hardcoded (use \${NCHAT_WEB_LIVEKIT_CONNECT_SRC})." ;;
esac

# O bundle é injetado pelo Vite como <script type="module" src=...>. Qualquer
# script inline versionado voltaria a violar script-src 'self'.
if grep -o '<script[^>]*>' "$INDEX_HTML" | grep -qv 'src='; then
  fail "apps/web/index.html não pode conter script inline (remova o inline ou use nonce/hash específico)."
fi

# Nenhuma outra camada versionada (Traefik, Ingress, serviços Go) pode emitir CSP.
extra_csp="$(
  git -C "$ROOT_DIR" grep -lI -i 'Content-Security-Policy' -- . \
    ':!infra/docker/web/nginx.conf' \
    ':!scripts/ci/web-security-headers-check.sh' \
    ':!scripts/ci/web-livekit-integration-check.sh' \
    ':!*.md' || true
)"
if [ -n "$extra_csp" ]; then
  fail "Content-Security-Policy emitida por mais de uma camada do repositório:"$'\n'"$extra_csp"
fi

echo "Web security headers check (estático) passou."

# --- Render check (envsubst) --------------------------------------------------
#
# Confirma que o render restrito a NCHAT_WEB_LIVEKIT_CONNECT_SRC (Dockerfile.web
# / infra/k8s/base/web/deployment.yaml) expande só esse slot e não toca $host,
# que é variável do próprio nginx, não do envsubst.

if command -v envsubst >/dev/null 2>&1; then
  dev_value="wss://livekit-dev.nic-labs.com https://livekit-dev.nic-labs.com"
  rendered="$(NCHAT_WEB_LIVEKIT_CONNECT_SRC="$dev_value" envsubst '$NCHAT_WEB_LIVEKIT_CONNECT_SRC' <"$NGINX_CONF")"
  rendered_policy="$(sed -n 's/.*add_header Content-Security-Policy "\(.*\)".*/\1/p' <<<"$rendered")"

  case "$rendered_policy" in
    *'wss://$host'*) ;;
    *) fail "render: \$host não pode ser consumido pelo envsubst (deve permanecer literal para o nginx)." ;;
  esac
  case "$rendered_policy" in
    *'https://$host'*) ;;
    *) fail "render: \$host não pode ser consumido pelo envsubst (deve permanecer literal para o nginx)." ;;
  esac
  case "$rendered_policy" in
    *"$dev_value"*) ;;
    *) fail "render: NCHAT_WEB_LIVEKIT_CONNECT_SRC não foi expandido em connect-src." ;;
  esac

  assert_policy "nginx.conf (renderizado DEV)" "$rendered_policy"
  echo "Render check (envsubst DEV) passou."
else
  echo "warning: envsubst não encontrado; render check ignorado." >&2
fi

# --- Modo live ---------------------------------------------------------------

TARGET_URL="${1:-${NCHAT_WEB_URL:-}}"
if [ -z "$TARGET_URL" ]; then
  echo "Modo live ignorado (nenhuma URL informada)."
  exit 0
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "warning: curl não encontrado; modo live ignorado." >&2
  exit 0
fi

# Sem -L: a resposta inicial do host final é exatamente o que precisa ser validado.
headers="$(curl -fsS --max-time 15 -o /dev/null -D - "$TARGET_URL")" ||
  fail "Falha ao obter os headers de $TARGET_URL"

enforced_count="$(printf '%s\n' "$headers" | grep -c -i '^content-security-policy:' || true)"
report_count="$(printf '%s\n' "$headers" | grep -c -i '^content-security-policy-report-only:' || true)"

if [ "$enforced_count" -ne 1 ]; then
  fail "$TARGET_URL: esperado exatamente 1 header Content-Security-Policy (encontrado: $enforced_count)."
fi

if [ "$report_count" -ne 0 ]; then
  fail "$TARGET_URL: header Content-Security-Policy-Report-Only presente ($report_count); remova-o na camada operacional (gateway/Cloudflare)."
fi

live_policy="$(
  printf '%s\n' "$headers" |
    sed -n 's/^[Cc]ontent-[Ss]ecurity-[Pp]olicy:[[:space:]]*//p' |
    tr -d '\r'
)"
assert_policy "$TARGET_URL" "$live_policy"

echo "Web security headers check (live: $TARGET_URL) passou."
