# TASK-11 - Traefik local gateway

## Status

Validado localmente na branch `chore/task-011-local-gateway-traefik`; PR #31 aberto contra `develop`.

## Decisao

Traefik foi escolhido como gateway local padrao. Nginx permanece alternativa futura porque os requisitos aceitam Traefik ou Nginx.

## Objetivo

Criar gateway local para desenvolvimento do NChat.

## Rotas

| Rota                 | Destino                     |
| -------------------- | --------------------------- |
| `/`                  | web `5173`                  |
| `/api/auth`          | auth-service `8081`         |
| `/api/chat`          | chat-service `8082`         |
| `/api/files`         | file-service `8083`         |
| `/api/notifications` | notification-service `8084` |
| `/api/admin`         | admin-service `8085`        |
| `/api/search`        | search-service `8086`       |
| `/api/media`         | media-service `8087`        |

## Arquivos criados

- `infra/traefik/local/traefik.yml`
- `infra/traefik/local/dynamic.yml`
- `infra/traefik/local/README.md`
- `scripts/dev/dev-gateway-up.sh`
- `scripts/dev/dev-gateway-down.sh`
- `scripts/dev/dev-gateway-status.sh`
- `scripts/dev/dev-gateway-logs.sh`
- `scripts/dev/dev-gateway-validate.sh`
- `scripts/ci/gateway-config-check.sh`

## Seguranca

- Docker socket nao montado.
- Docker provider nao habilitado.
- Dashboard bindado em `127.0.0.1` pelo Docker Compose.
- Sem secrets.
- Sem TLS real nesta tarefa.
- Headers basicos de seguranca.
- Configuracao apenas local.

## Como usar

```bash
cp infra/compose/.env.dev.example infra/compose/.env.dev
```

Adicionar no `/etc/hosts`:

```text
127.0.0.1 nchat.local
```

Comandos:

```bash
make dev-gateway-up
make dev-gateway-validate
make dev-gateway-down
```

## Limitacoes

- Nao e producao.
- Nao tem TLS real.
- Nao tem rate limit avancado.
- Nao tem WAF.
- Servicos precisam rodar no host.
- Dockerfiles ainda nao existem.
- WebSocket real ainda nao foi implementado.

## Validacao local

Executado nesta maquina:

- `docker --version`: `Docker version 29.4.3, build 055a478`.
- `docker compose version`: `Docker Compose version v5.1.3`.
- `pnpm install`.
- `pnpm gateway:config-check`.
- `make gateway-config-check`.
- `make dev-gateway-up`.
- `make dev-gateway-status`.
- `make dev-gateway-validate`.
- `make dev-gateway-down`.
- `pnpm format:check`.
- `pnpm lint`.
- `pnpm typecheck:web`.
- `pnpm test`.
- `pnpm test:coverage`.
- `pnpm build`.
- `pnpm run ci`.
- `make ci`.

Observacoes locais:

- Node local esta em `v22.22.2`, abaixo do alvo `24.x`; pnpm emitiu warning de engine.
- `infra/compose/.env.dev` ja existia localmente e nao continha as novas chaves do gateway; os scripts usaram defaults de `.env.dev.example` e avisaram sem sobrescrever o arquivo.
- Traefik v3.6 nao possui comando `check-config`; `gateway-config-check` validou o render do Docker Compose e a imagem Traefik via `version` como fallback documentado.
- `make dev-gateway-validate` confirmou `/ping` e dashboard local. As rotas de API foram marcadas como skip porque os servicos Go nao estavam rodando no host.
- `pnpm k8s:ci` renderizou manifests e pulou apenas `kubectl apply --dry-run=client` porque nao havia API server Kubernetes local acessivel.

## Definition of Done

- [x] Traefik configurado
- [x] Compose profile gateway criado
- [x] Rotas locais criadas
- [x] StripPrefix configurado
- [x] Dashboard local configurado
- [x] Scripts dev criados
- [x] Config check no CI
- [x] README atualizado
- [x] Runbook criado
- [x] `make gateway-config-check` passa
- [x] `make ci` passa
- [x] PR aberto
