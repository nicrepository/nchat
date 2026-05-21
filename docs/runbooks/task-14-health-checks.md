# TASK-14 - Health checks /healthz e /readyz

## Status

Implementado e validado localmente em `chore/task-014-health-readiness-checks`. PR pendente de abertura.

## Objetivo

Padronizar liveness e readiness nos servicos Go do NChat.

## Contrato

### /healthz

- Liveness.
- Nao verifica banco/cache/storage.
- Deve ser barato e rapido.
- Usado por livenessProbe/startupProbe.

### /readyz

- Readiness.
- Verifica se o servico pode receber trafego.
- Hoje verifica bootstrap/config local.
- Futuramente verificara dependencias criticas.
- Usado por readinessProbe.
- Retorna `503` quando um check critico falha.
- Retorna `200` com `degraded` quando apenas checks nao criticos falham.

## JSON response

### /healthz

```json
{
  "data": {
    "service": "auth-service",
    "probe": "liveness",
    "status": "ok",
    "version": "0.0.0",
    "commit": "dev",
    "checkedAt": "2026-05-21T12:00:00Z"
  }
}
```

### /readyz

```json
{
  "data": {
    "service": "auth-service",
    "probe": "readiness",
    "status": "ready",
    "version": "0.0.0",
    "commit": "dev",
    "checkedAt": "2026-05-21T12:00:00Z",
    "checks": [
      {
        "name": "service-bootstrap",
        "status": "pass",
        "critical": true,
        "durationMs": 0
      },
      {
        "name": "config-loaded",
        "status": "pass",
        "critical": true,
        "durationMs": 0
      }
    ]
  }
}
```

## Servicos cobertos

- auth-service
- chat-service
- file-service
- notification-service
- admin-service
- search-service
- media-service

## Kubernetes

Os manifests base foram revisados e ja usam:

- livenessProbe -> `/healthz`
- readinessProbe -> `/readyz`
- startupProbe -> `/healthz`

## Seguranca

- Nao expor secrets.
- Nao expor DSNs.
- Nao expor stack trace.
- Nao transformar readiness em enumeracao de infraestrutura sensivel.

## Fora do escopo

- PostgreSQL check real
- Valkey check real
- SeaweedFS check real
- WebSocket readiness
- LiveKit readiness

## Validacao

- `pnpm health:contract-check`
- `bash scripts/ci/go-test.sh`
- `pnpm run ci`
- `make health-contract-check`
- `make ci`

## Definition of Done

- [x] Pacote health compartilhado criado/atualizado
- [x] /healthz padronizado
- [x] /readyz padronizado
- [x] Todos os servicos cobertos
- [x] Search-service coberto
- [x] Testes atualizados
- [x] K8s probes revisadas
- [x] Script health-contract-check criado
- [x] README atualizado
- [x] Runbook criado
- [x] make ci passa
- [ ] PR aberto
