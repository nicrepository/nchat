#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
RUNTIME_DIR="$ROOT_DIR/.nchat-local"
LOG_DIR="$RUNTIME_DIR/logs"
PID_DIR="$RUNTIME_DIR/pids"
LOCAL_ENV="$RUNTIME_DIR/services.env"
COMPOSE_ENV="$ROOT_DIR/infra/compose/.env.dev"
COMPOSE_ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
APP_URL="${NCHAT_LOCAL_URL:-http://nchat.local:8080}"
NO_BROWSER=false
OPEN_EDITOR=false

SERVICES=(
  auth-service
  chat-service
  file-service
  notification-service
  admin-service
  search-service
  media-service
)
PORTS=(8081 8082 8083 8084 8085 8086 8087)

usage() {
  cat <<'EOF'
Usage: scripts/dev/nchat-local.sh [command] [options]

Commands:
  up        Start infrastructure, migrations, APIs, web and gateway (default)
  down      Stop local app processes and Docker services
  restart   Stop and start the complete local project
  status    Show app processes, health endpoints and Docker services
  logs      Follow all local application logs
  check     Validate the Linux development prerequisites

Options:
  --no-browser  Do not open the application in the default browser
  --editor      Also open the repository in VS Code when available
  -h, --help    Show this help

Environment:
  NCHAT_LOCAL_URL       Browser URL (default: http://nchat.local:8080)
  NCHAT_SKIP_HOSTS=1    Do not offer to add nchat.local to /etc/hosts
EOF
}

log() { printf '[nchat] %s\n' "$*"; }
die() { printf '[nchat] ERROR: %s\n' "$*" >&2; exit 1; }

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
}

check_prerequisites() {
  local commands=(bash curl docker go node pnpm psql openssl)
  if [[ "${NCHAT_LOCAL_TEST_MODE:-0}" == "1" ]]; then
    commands=(bash)
  fi
  local command_name
  for command_name in "${commands[@]}"; do
    require_command "$command_name"
  done
  if [[ "${NCHAT_LOCAL_TEST_MODE:-0}" != "1" ]]; then
    docker compose version >/dev/null
    docker info >/dev/null 2>&1 || die "Docker daemon is not available. Start Docker and try again."
    [[ "$(node --version)" == v24.* ]] || die "Node 24 is required (found $(node --version))."
    [[ "$(pnpm --version)" == 11.* ]] || die "pnpm 11 is required (found $(pnpm --version))."
  fi
  log "Local prerequisites passed."
}

load_key() {
  local key="$1" file="$2" line
  line="$(grep -E "^${key}=" "$file" | tail -1 || true)"
  printf '%s' "${line#*=}"
}

ensure_local_configuration() {
  mkdir -p "$LOG_DIR" "$PID_DIR"
  if [[ ! -f "$COMPOSE_ENV" ]]; then
    cp "$COMPOSE_ENV_EXAMPLE" "$COMPOSE_ENV"
    log "Created infra/compose/.env.dev from the local example."
  fi

  if [[ ! -f "$LOCAL_ENV" ]]; then
    local jwt_secret email_key file_key
    jwt_secret="$(openssl rand -hex 32)"
    email_key="$(openssl rand -base64 32 | tr -d '\n')"
    file_key="$(openssl rand -base64 32 | tr -d '\n')"
    umask 077
    printf '%s\n' \
      "AUTH_JWT_HMAC_SECRET=$jwt_secret" \
      "AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY=$email_key" \
      "FILE_ENCRYPTION_MASTER_KEY=$file_key" \
      > "$LOCAL_ENV"
    log "Generated ignored local development secrets in .nchat-local/services.env."
  fi
}

ensure_local_hostname() {
  getent hosts nchat.local >/dev/null 2>&1 && return 0
  [[ "${NCHAT_SKIP_HOSTS:-0}" == "1" ]] && return 0

  if [[ -t 0 ]] && command -v sudo >/dev/null 2>&1; then
    log "nchat.local is missing from /etc/hosts; sudo may request your Linux password."
    printf '127.0.0.1 nchat.local\n' | sudo tee -a /etc/hosts >/dev/null || \
      die "Could not configure nchat.local. Add '127.0.0.1 nchat.local' to /etc/hosts."
  else
    die "Add '127.0.0.1 nchat.local' to /etc/hosts, then run this command again."
  fi
}

pid_is_running() {
  local pid_file="$1" pid
  [[ -f "$pid_file" ]] || return 1
  pid="$(<"$pid_file")"
  [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null
}

start_process() {
  local name="$1" workdir="$2"
  shift 2
  local pid_file="$PID_DIR/$name.pid" log_file="$LOG_DIR/$name.log"
  if pid_is_running "$pid_file"; then
    log "$name is already running (PID $(<"$pid_file"))."
    return 0
  fi
  rm -f "$pid_file"
  (
    cd "$workdir"
    nohup setsid "$@" >"$log_file" 2>&1 &
    printf '%s\n' "$!" > "$pid_file"
  )
  sleep 0.3
  pid_is_running "$pid_file" || die "$name failed to start. See $log_file"
  log "Started $name (PID $(<"$pid_file"))."
}

wait_for_url() {
  local name="$1" url="$2" attempts="${3:-60}" attempt
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    curl --fail --silent --show-error --max-time 2 "$url" >/dev/null 2>&1 && return 0
    sleep 1
  done
  die "$name did not become healthy at $url. Check $LOG_DIR/$name.log"
}

start_infrastructure() {
  log "Starting PostgreSQL, Valkey, ClamAV and SeaweedFS..."
  bash "$ROOT_DIR/scripts/dev/dev-env-up.sh"
  bash "$ROOT_DIR/scripts/dev/dev-env-validate.sh"
  log "Applying database migrations..."
  bash "$ROOT_DIR/scripts/db/migrate.sh" up
}

start_applications() {
  local db_user db_password db_name db_port valkey_password valkey_port
  db_user="$(load_key POSTGRES_USER "$COMPOSE_ENV")"
  db_password="$(load_key POSTGRES_PASSWORD "$COMPOSE_ENV")"
  db_name="$(load_key POSTGRES_DB "$COMPOSE_ENV")"
  db_port="$(load_key POSTGRES_HOST_PORT "$COMPOSE_ENV")"
  valkey_password="$(load_key VALKEY_PASSWORD "$COMPOSE_ENV")"
  valkey_port="$(load_key VALKEY_HOST_PORT "$COMPOSE_ENV")"

  local database_url="postgres://${db_user}:${db_password}@127.0.0.1:${db_port}/${db_name}?sslmode=disable"
  local valkey_url="valkey://:${valkey_password}@127.0.0.1:${valkey_port}"
  local jwt_secret email_key
  jwt_secret="$(load_key AUTH_JWT_HMAC_SECRET "$LOCAL_ENV")"
  email_key="$(load_key AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY "$LOCAL_ENV")"

  local index service port
  for index in "${!SERVICES[@]}"; do
    service="${SERVICES[$index]}"
    port="${PORTS[$index]}"
    start_process "$service" "$ROOT_DIR/services/$service" \
      env APP_ENV=development PORT="$port" DATABASE_URL="$database_url" \
      AUTH_JWT_HMAC_SECRET="$jwt_secret" AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY="$email_key" \
      AUTH_JWT_ISSUER=nchat-auth AUTH_JWT_AUDIENCE=nchat-api \
      VALKEY_URL="$valkey_url" FILE_UPLOADS_ENABLED=false SMTP_WORKER_ENABLED=false \
      LIVEKIT_ENABLED=false go run "./cmd/$service"
  done

  start_process web "$ROOT_DIR" pnpm --filter @nchat/web dev

  for index in "${!SERVICES[@]}"; do
    wait_for_url "${SERVICES[$index]}" "http://127.0.0.1:${PORTS[$index]}/healthz" 90
  done
  wait_for_url web http://127.0.0.1:5173 90
}

start_gateway() {
  if [[ ! -s "$ROOT_DIR/infra/traefik/local/certs/nchat.local.pem" || \
        ! -s "$ROOT_DIR/infra/traefik/local/certs/nchat.local-key.pem" ]]; then
    log "Generating the local TLS certificate required by Traefik..."
    bash "$ROOT_DIR/scripts/dev/dev-tls-generate.sh"
  fi
  bash "$ROOT_DIR/scripts/dev/dev-gateway-up.sh"
  for _ in {1..30}; do
    curl --fail --silent --show-error --max-time 2 -H 'Host: nchat.local' \
      http://127.0.0.1:8080/ >/dev/null 2>&1 && return 0
    sleep 1
  done
  die "Traefik gateway did not become ready. Run '$0 logs'."
}

open_local_tools() {
  if [[ "$OPEN_EDITOR" == "true" ]] && command -v code >/dev/null 2>&1; then
    nohup code "$ROOT_DIR" >"$LOG_DIR/editor.log" 2>&1 &
  fi
  if [[ "$NO_BROWSER" == "false" ]]; then
    if command -v xdg-open >/dev/null 2>&1; then
      nohup xdg-open "$APP_URL" >"$LOG_DIR/browser.log" 2>&1 &
    else
      log "xdg-open was not found; open $APP_URL manually."
    fi
  fi
}

up() {
  check_prerequisites
  ensure_local_configuration
  ensure_local_hostname
  [[ -d "$ROOT_DIR/node_modules" ]] || { log "Installing frontend dependencies..."; (cd "$ROOT_DIR" && pnpm install); }
  start_infrastructure
  start_applications
  start_gateway
  open_local_tools
  log "NChat is running locally at $APP_URL"
  log "Logs: $0 logs | Status: $0 status | Stop: $0 down"
}

stop_processes() {
  local pid_file pid name
  shopt -s nullglob
  for pid_file in "$PID_DIR"/*.pid; do
    name="$(basename "$pid_file" .pid)"
    if pid_is_running "$pid_file"; then
      pid="$(<"$pid_file")"
      kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
      for _ in {1..20}; do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.25
      done
      kill -9 -- "-$pid" 2>/dev/null || kill -9 "$pid" 2>/dev/null || true
      log "Stopped $name."
    fi
    rm -f "$pid_file"
  done
}

down() {
  stop_processes
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    bash "$ROOT_DIR/scripts/dev/dev-gateway-down.sh" || true
    if [[ -f "$COMPOSE_ENV" ]]; then
      docker compose --env-file "$COMPOSE_ENV" -f "$ROOT_DIR/infra/compose/compose.dev.yml" \
        --profile gateway stop upload-guard || true
      docker compose --env-file "$COMPOSE_ENV" -f "$ROOT_DIR/infra/compose/compose.dev.yml" \
        --profile gateway rm -f upload-guard || true
    fi
    bash "$ROOT_DIR/scripts/dev/dev-env-down.sh" || true
  fi
  log "NChat local environment stopped (data volumes preserved)."
}

status() {
  local index service port state
  printf '%-24s %-10s %s\n' COMPONENT STATE ENDPOINT
  for index in "${!SERVICES[@]}"; do
    service="${SERVICES[$index]}"; port="${PORTS[$index]}"; state=stopped
    pid_is_running "$PID_DIR/$service.pid" && state=running
    printf '%-24s %-10s http://127.0.0.1:%s/healthz\n' "$service" "$state" "$port"
  done
  state=stopped; pid_is_running "$PID_DIR/web.pid" && state=running
  printf '%-24s %-10s %s\n' web "$state" http://127.0.0.1:5173
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 && [[ -f "$COMPOSE_ENV" ]]; then
    docker compose --env-file "$COMPOSE_ENV" -f "$ROOT_DIR/infra/compose/compose.dev.yml" ps
  fi
}

logs() {
  mkdir -p "$LOG_DIR"
  local files=("$LOG_DIR"/*.log)
  [[ -e "${files[0]}" ]] || die "No local logs yet. Run '$0 up' first."
  tail -n 100 -F "${files[@]}"
}

command_name="up"
while [[ $# -gt 0 ]]; do
  case "$1" in
    up|down|restart|status|logs|check) command_name="$1" ;;
    --no-browser) NO_BROWSER=true ;;
    --editor) OPEN_EDITOR=true ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "Unknown argument: $1" ;;
  esac
  shift
done

case "$command_name" in
  up) up ;;
  down) down ;;
  restart) down; up ;;
  status) status ;;
  logs) logs ;;
  check) check_prerequisites ;;
esac
