# Tarefa 8 — Ambiente dev com PostgreSQL, Valkey e SeaweedFS

## Status

Validacao local concluida na branch `chore/task-008-dev-env-postgres-valkey-seaweedfs`.

Comandos executados:

- `docker --version`
- `docker compose version`
- `pnpm dev:env:config-check`
- `make dev-env-up`
- `make dev-env-status`
- `make dev-env-validate`

Observacoes da execucao local:

- A porta local `5432` ja estava ocupada em `127.0.0.1`; apenas o arquivo ignorado
  `infra/compose/.env.dev` foi ajustado para `POSTGRES_HOST_PORT=15432`.
- O arquivo versionado `.env.dev.example` permanece com `POSTGRES_HOST_PORT=5432`.
- O Docker daemon emitiu erro transitorio de unmount ao criar containers na primeira
  tentativa; repetir `make dev-env-up` concluiu a criacao.

## Objetivo

Subir ambiente local reprodutivel para desenvolvimento do NChat.

## Serviços

| Serviço              | Versão / imagem              | Função                         |
| -------------------- | ---------------------------- | ------------------------------ |
| PostgreSQL 16        | `postgres:16-alpine`         | Banco relacional local         |
| Valkey 8             | `valkey/valkey:8-alpine`     | Cache, locks, streams e pubsub |
| SeaweedFS master     | `chrislusf/seaweedfs:latest` | Coordenacao do cluster local   |
| SeaweedFS volume     | `chrislusf/seaweedfs:latest` | Armazenamento local de objetos |
| SeaweedFS filer      | `chrislusf/seaweedfs:latest` | API HTTP de arquivos           |
| SeaweedFS S3 gateway | `chrislusf/seaweedfs:latest` | Gateway S3 local provisório    |

## Portas

| Serviço          | Porta local padrão |
| ---------------- | -----------------: |
| PostgreSQL       |               5432 |
| Valkey           |               6379 |
| SeaweedFS master |               9333 |
| SeaweedFS volume |               8088 |
| SeaweedFS filer  |               8888 |
| SeaweedFS S3     |               8333 |

## Arquivos criados

- `infra/compose/compose.dev.yml`
- `infra/compose/.env.dev.example`
- `infra/compose/postgres/init/001_init.sql`
- `infra/compose/valkey/valkey.conf`
- `infra/compose/seaweedfs/README.md`
- `scripts/dev/dev-env-up.sh`
- `scripts/dev/dev-env-down.sh`
- `scripts/dev/dev-env-reset.sh`
- `scripts/dev/dev-env-status.sh`
- `scripts/dev/dev-env-logs.sh`
- `scripts/dev/dev-env-validate.sh`
- `scripts/ci/dev-env-config-check.sh`

## Decisões

- PostgreSQL roda single-node no ambiente dev.
- Patroni fica fora do dev local.
- Valkey usa senha mesmo em dev.
- SeaweedFS e provisório no Sprint 0.
- S3 auth/policies serão endurecidas depois.
- Backup/restore completo fica para tarefa futura.

## Validações implementadas

- PostgreSQL `pg_isready`.
- PostgreSQL query em `public.dev_environment_info`.
- Valkey `PING`.
- Valkey `SET`/`GET`.
- Valkey `SETNX`.
- Valkey `TTL`.
- Valkey Stream `XADD`/`XRANGE`.
- Valkey Pub/Sub smoke test.
- Sliding window com `INCR`/`EXPIRE`.
- SeaweedFS master/filer HTTP.
- SeaweedFS upload/download smoke test.

## Comandos

- `make dev-env-up`
- `make dev-env-status`
- `make dev-env-validate`
- `make dev-env-logs`
- `make dev-env-down`
- `make dev-env-reset`

## Segurança

- `.env.dev` não versionado.
- `.env.dev.example` contém apenas valores de desenvolvimento.
- Senhas reais são proibidas no repositório.
- Valkey é protegido por senha.
- Ambiente não é produção.

## Limitações

- Sem HA.
- Sem Patroni.
- Sem backup agendado.
- Sem TLS.
- Sem Traefik/Nginx.
- Sem integração dos serviços Go.
- Sem ClamAV.
- Sem teste de upload grande ou recuperação de nó.

## Definition of Done

- [x] Compose criado
- [x] `.env.dev.example` criado
- [x] PostgreSQL healthy
- [x] Valkey healthy
- [x] SeaweedFS acessível
- [x] Init SQL criado
- [x] Scripts dev criados
- [x] Validação local passa
- [x] Config check no CI passa
- [x] README atualizado
- [x] Runbook criado
- [ ] PR aberto
