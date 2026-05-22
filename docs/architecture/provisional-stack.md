# NChat — Stack tecnológica provisória

## Resumo executivo

Este documento consolida a stack tecnológica do NChat após as validações do Sprint 0.

**O que está confirmado:** Go + React monorepo, PostgreSQL 16, Valkey 8, Traefik (dev/k3s),
Sealed Secrets, TLS 1.3 (dev/staging), GitHub Actions + GitLab CI, Kubernetes/k3s manifests
com Kustomize, health/readiness em todos os serviços Go, observabilidade base local (Prometheus,
Grafana, Jaeger, OpenTelemetry SDK — TASK-18).

**O que é provisório:** SeaweedFS como solução de armazenamento de arquivos. Aceito após PoC
do Sprint 0, mas a decisão definitiva depende de validações mais completas no Sprint 3.

**O que está planejado:** Dashboards Grafana detalhados (TASK-19), Loki ou OpenSearch para logs,
ClamAV para antimalware, ArgoCD para GitOps, Dockerfiles e imagens reais.

**O que está fora do MVP:** E2E com MLS RFC 9420, chamadas de áudio/vídeo (LiveKit + coturn),
cliente desktop (Tauri), cliente mobile (React Native), multi-workspace, modos DND/URGENT,
Istio mTLS.

---

## Stack por camada

### Aplicação

| Componente | Tecnologia                | Status     | Observações                        |
| ---------- | ------------------------- | ---------- | ---------------------------------- |
| Backend    | Go 1.25                   | Confirmado | Microsserviços; 7 serviços base    |
| Frontend   | React + TypeScript + Vite | Confirmado | SPA/PWA                            |
| PWA        | Suporte offline           | Planejado  | Offline básico no MVP; completo V1 |

**Serviços Go no MVP:**

- `auth-service` — autenticação e gestão de sessões
- `chat-service` — mensagens, canais e Pub/Sub
- `file-service` — upload, download e armazenamento
- `notification-service` — notificações push e in-app
- `admin-service` — painel administrativo
- `search-service` — busca de mensagens e usuários
- `media-service` — processamento de mídia

### Dados

| Componente | Tecnologia    | Status              | Observações                                |
| ---------- | ------------- | ------------------- | ------------------------------------------ |
| Banco      | PostgreSQL 16 | Confirmado          | Dev local; migrations e schema são futuros |
| Cache      | Valkey 8      | Confirmado Sprint 0 | Pub/Sub, Streams, locks, TTL, rate limit   |
| Storage    | SeaweedFS     | Provisório Sprint 0 | Decisão definitiva no Sprint 3             |

**Valkey — padrões validados no Sprint 0:**

- Pub/Sub para mensagens em tempo real
- Streams (XADD/XREAD/XRANGE) para outbox pattern
- SETNX para locks distribuídos
- TTL/EXPIRE para sessões e tokens
- Sliding window (sorted set) para rate limiting

### Segurança

| Componente     | Tecnologia          | Status     | Observações                          |
| -------------- | ------------------- | ---------- | ------------------------------------ |
| TLS            | TLS 1.3 via Traefik | Confirmado | Dev/staging; produção é futuro       |
| Secrets        | Sealed Secrets      | Confirmado | Escopo strict; runbook documentado   |
| Encryption     | AES-256 envelope    | Planejado  | Para file-service no Sprint 3        |
| Antimalware    | ClamAV              | Planejado  | Sprint 3; async scan no file-service |
| E2E Encryption | MLS RFC 9420        | V1.0       | Fora do MVP                          |

### Infraestrutura

| Componente   | Tecnologia                 | Status     | Observações                         |
| ------------ | -------------------------- | ---------- | ----------------------------------- |
| Orquestração | Kubernetes/k3s + Kustomize | Confirmado | Base dev/staging; produção é futuro |
| Gateway      | Traefik                    | Confirmado | Dev/k3s; Nginx como alternativa     |
| CI/CD        | GitHub Actions + GitLab CI | Confirmado | Deploy automático fora do escopo    |
| GitOps       | ArgoCD                     | Planejado  | Futuro                              |

### Observabilidade

| Componente     | Tecnologia             | Status                        | Observações                                       |
| -------------- | ---------------------- | ----------------------------- | ------------------------------------------------- |
| Métricas       | Prometheus + Grafana   | Base local / TASK-18 completo | Stack Docker Compose local; dashboards em TASK-19 |
| Tracing        | OpenTelemetry + Jaeger | Base local / TASK-18 completo | OTLP HTTP; spans básicos por request HTTP         |
| Instrumentação | OpenTelemetry SDK      | Implementado                  | Pacote `libs/go/platform/observability`; opt-in   |
| /metrics       | Prometheus             | Implementado                  | Todos os serviços Go expõem `/metrics`            |
| Logs           | Loki ou OpenSearch     | Planejado                     | Decisão adiada; tarefa futura                     |

### Fora do MVP

Os itens abaixo estão documentados nos requisitos v5.0, mas estão fora do escopo do MVP
Interno. Serão abordados em versões futuras:

| Componente      | Versão alvo | Tecnologia       |
| --------------- | ----------- | ---------------- |
| E2E encryption  | V1.0        | MLS RFC 9420     |
| Chamadas AV     | V1.0        | LiveKit + coturn |
| Cliente desktop | V1.0        | Tauri            |
| Cliente mobile  | V1.1        | React Native     |
| Multi-workspace | V1.0        | —                |
| DND/URGENT      | V1.0        | —                |
| Istio mTLS      | Futuro      | Istio            |

---

## Matriz de maturidade

| Componente       | Status     | Maturidade                   | Próxima validação                        |
| ---------------- | ---------- | ---------------------------- | ---------------------------------------- |
| Go services      | Confirmado | Base criada                  | Integração com dados (PG, Valkey, SeaWF) |
| React web        | Confirmado | Bootstrap completo           | Design system / frontend shell           |
| PostgreSQL       | Confirmado | Dev env provisionado         | Migrations e schema                      |
| Valkey           | Confirmado | PoC completa Sprint 0        | Integração chat/notification             |
| SeaweedFS        | Provisório | PoC inicial Sprint 0         | Sprint 3 — validação completa            |
| Traefik          | Confirmado | Local/dev operacional        | Staging/prod hardening                   |
| Sealed Secrets   | Confirmado | Estrutura operacional + CI   | Cluster real                             |
| TLS              | Confirmado | Dev/staging configurado      | Produção + cert-manager                  |
| CI/CD            | Confirmado | Workflows completos + checks | Deploy automático futuro                 |
| Health/readiness | Confirmado | Todos os serviços            | Checks reais (PG, Valkey, SeaWF)         |
| Observabilidade  | Planejado  | Não iniciado                 | Próxima tarefa                           |
| ClamAV           | Planejado  | Não iniciado                 | Sprint 3                                 |

---

## Decisões que NÃO foram tomadas

As decisões a seguir estão intencionalmente adiadas. Não devem ser assumidas como
definidas sem um ADR explícito:

- **Cloud provider de failover:** nenhum provider definido para produção real.
- **Estratégia final de backup:** backup automático de PostgreSQL não definido.
- **Decisão definitiva SeaweedFS:** aguarda Sprint 3 (ver ADR-0002).
- **Provedor final de e-mail:** para notificações transacionais (SMTP, SendGrid, SES etc.).
- **Integração FCM/APNs real:** notificações push para mobile ainda não decididas.
- **Estratégia de HA PostgreSQL:** Patroni, pgBouncer e Barman estão em avaliação futura.
- **Observabilidade final — Loki vs OpenSearch:** decisão adiada para quando a stack de
  observabilidade for implantada.
- **LiveKit deployment topology:** self-hosted vs. managed para chamadas V1.0.
- **Certificados reais de produção:** cert-manager e CA ainda não configurados.
- **Estratégia de DR:** disaster recovery e RTO/RPO não definidos.

---

## Próximas tarefas recomendadas

Com base no Sprint 0 concluído e no cronograma do projeto:

1. **Observabilidade (próxima tarefa):** configurar Prometheus, Grafana, Jaeger e
   instrumentação OpenTelemetry nos serviços Go.
2. **Dashboard inicial:** criar dashboard de métricas de saúde dos serviços.
3. **Revisão semanal:** retrospectiva do Sprint 0 e planejamento do Sprint 1.
4. **Dockerfiles e GHCR:** quando o cronograma demandar ou for necessário para deploy.
5. **Integração PostgreSQL:** migrations, schema e queries nos serviços Go.
6. **Integração Valkey:** Pub/Sub e Streams no chat-service; rate limit no middleware.
7. **Sprint 3 — SeaweedFS:** validação definitiva conforme critérios do ADR-0002.

---

## Referências

- [ADR-0001 — Monorepo](../adr/0001-repository-strategy.md)
- [ADR-0002 — Decisão provisória da stack](../adr/0002-provisional-stack-decision.md)
- [Runbook TASK-15 — SeaweedFS PoC](../runbooks/task-15-seaweedfs-poc.md)
- [Runbook TASK-16 — Valkey PoC](../runbooks/task-16-valkey-poc.md)
- [Runbook TASK-12 — TLS dev/staging](../runbooks/task-12-tls-dev-staging.md)
- [Runbook TASK-13 — Sealed Secrets](../runbooks/task-13-sealed-secrets.md)
- [Runbook TASK-14 — Health checks](../runbooks/task-14-health-checks.md)
