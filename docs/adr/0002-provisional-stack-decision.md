# ADR-0002 — Decisão provisória da stack tecnológica

## Status

Accepted — Provisional

## Data

2026-05-21

## Contexto

O NChat está no Sprint 0 / fase de fundação. O objetivo deste ADR é registrar a stack provisória
para orientar o desenvolvimento nas próximas sprints.

Durante o Sprint 0 foram realizadas validações de PoC para os componentes principais da
infraestrutura. Algumas decisões foram confirmadas com base nessas validações. Outras permanecem
provisórias até que validações mais completas sejam realizadas em sprints futuras.

Destaques do Sprint 0:

- Monorepo Go + React bootstrapado com CI completo.
- Serviços Go base (auth, chat, file, notification, admin, search, media) com health/readiness.
- PostgreSQL 16, Valkey 8 e SeaweedFS provisionados no ambiente de desenvolvimento local.
- Kubernetes/k3s manifests com Kustomize base e overlays dev/staging.
- GitHub Actions e GitLab CI configurados com required checks.
- Traefik local como gateway com TLS dev/staging.
- Sealed Secrets com política strict e runbook de rotação.
- Valkey 8 validado para todos os padrões de uso do NChat (Pub/Sub, Streams, SETNX, TTL,
  sliding window).
- SeaweedFS validado provisoriamente para upload/download, integridade SHA-256, latência
  básica e replicação básica. A decisão definitiva depende de validações adicionais no Sprint 3.

## Drivers de decisão

- Segurança: TLS obrigatório, sem secrets em texto plano, modelo de licença aceitável.
- Simplicidade operacional inicial: stack coerente com time pequeno e fase de fundação.
- Compatibilidade com Kubernetes/k3s: todos os componentes devem operar bem em cluster.
- Licenças OSS aceitáveis: preferência por licenças permissivas ou copyleft sem restrições comerciais.
- Baixo footprint: ambientes dev/staging com recursos controlados.
- Capacidade de evoluir para produção: decisões que não criem dívida técnica irreversível.
- Observabilidade: stack compatível com Prometheus, OpenTelemetry, Loki/OpenSearch.
- CI/CD automatizado: todos os componentes devem ser verificáveis em pipeline.
- Compatibilidade com requisitos MVP: stack alinhada ao documento de requisitos v5.0.

## Decisão

| Área            | Decisão                        | Status              | Evidência                                        | Observações                    |
| --------------- | ------------------------------ | ------------------- | ------------------------------------------------ | ------------------------------ |
| Backend         | Go                             | Confirmado          | Monorepo e serviços base criados                 | Microsserviços                 |
| Frontend        | React + TypeScript + Vite      | Confirmado          | App web bootstrap + CI                           | SPA/PWA                        |
| Banco           | PostgreSQL 16                  | Confirmado          | Ambiente dev criado                              | Integração real futura         |
| Cache/PubSub    | Valkey 8                       | Confirmado Sprint 0 | PoC Pub/Sub, Streams, SETNX, TTL, sliding window | Usar comandos compatíveis      |
| Storage         | SeaweedFS                      | Provisório Sprint 0 | PoC upload/download/checksum/latência/replicação | Decisão definitiva no Sprint 3 |
| Gateway         | Traefik                        | Confirmado para dev | Gateway local + K8s ingress                      | Nginx permanece alternativa    |
| Secrets         | Sealed Secrets                 | Confirmado          | Estrutura + runbook + CI policy                  | Sem plaintext secrets          |
| TLS             | TLS 1.3 via Traefik            | Confirmado dev/stg  | Configuração local/staging                       | Produção futura                |
| CI/CD           | GitHub Actions + GitLab CI     | Confirmado          | Workflows e required checks                      | Deploy fora do escopo          |
| Orquestração    | Kubernetes/k3s manifests       | Confirmado base     | Kustomize base/overlays                          | Produção futura                |
| Observabilidade | Prometheus/Grafana/Jaeger/Loki | Planejado           | Requisitos v5.0                                  | Próxima etapa                  |
| Arquivos AV     | ClamAV                         | Planejado MVP Spr3  | Requisitos v5.0                                  | Ainda não integrado            |
| E2E             | MLS RFC 9420                   | V1.0 futuro         | Requisitos v5.0                                  | Fora do MVP                    |
| Chamadas        | LiveKit + coturn               | V1.0 futuro         | Requisitos v5.0                                  | Fora do MVP atual              |
| Desktop         | Tauri                          | V1.0 futuro         | Requisitos v5.0                                  | Fora do MVP                    |
| Mobile          | React Native                   | v1.1 futuro         | Requisitos v5.0                                  | Fora do MVP                    |

## Decisões confirmadas no Sprint 0

### Go + React monorepo

O monorepo foi bootstrapado com Go 1.25 e React + TypeScript + Vite. Os sete serviços Go
(auth, chat, file, notification, admin, search, media) têm estrutura padronizada com
`cmd/`, `internal/` e integração ao CI. O frontend tem build, lint, typecheck, testes e
coverage automatizados.

### PostgreSQL 16

Provisionado no ambiente de desenvolvimento local via Docker Compose. Integração real com
os serviços Go (migrations, schema, queries) é trabalho futuro planejado.

### Valkey 8

Validado no Sprint 0 para todos os padrões de uso identificados no documento de requisitos:

- Pub/Sub para mensagens em tempo real.
- Streams (XADD/XREAD/XRANGE) para outbox pattern e filas.
- SETNX para locks distribuídos.
- TTL/EXPIRE para expiração de sessões e tokens.
- Sliding window via sorted set para rate limiting.

Valkey substitui Redis por compatibilidade de protocolo e licença OSS mais permissiva.

### Traefik (dev/k3s)

Gateway local configurado via Docker Compose com profile `gateway`. Roteia para web e
serviços Go no host. Integrado ao K8s/k3s como Ingress. Nginx permanece como alternativa
documentada caso Traefik apresente limitações em produção.

### Sealed Secrets

Estrutura operacional criada com política strict (escopo por cluster + namespace + nome).
Runbook de rotação documentado. CI policy check automatizado em `make ci`. Nenhum secret
em texto plano é versionado.

### TLS 1.3 (dev/staging)

Configurado via Traefik local com `mkcert` ou `openssl`. Overlay K8s staging com Ingress
TLS placeholder para `staging.nchat.local`. TLS público real com cert-manager é trabalho
futuro.

### CI/CD

GitHub Actions configurado com workflows para governance, backend, frontend, quality,
CI agregado e security (secret scan, govulncheck, Trivy). GitLab CI espelhado para
mirror futuro. Deploy automático está fora do escopo desta fase.

### Health/readiness

Todos os serviços Go expõem `/healthz` (liveness) e `/readyz` (readiness) com envelope
JSON compartilhado. Checks reais de PostgreSQL, Valkey e SeaweedFS entram quando as
integrações forem implementadas.

## Decisões provisórias

### SeaweedFS

SeaweedFS foi aceito **provisoriamente** como solução de armazenamento de arquivos após
a PoC do Sprint 0.

A PoC comprovou:

- Upload e download de arquivos pequenos e grandes com integridade SHA-256.
- Latência básica aceitável para o caso de uso do NChat.
- Replicação básica entre dois volume servers locais.

Esta não é uma decisão definitiva. A decisão definitiva depende das validações do Sprint 3.

**Critérios pendentes para Sprint 3:**

- Upload grande realista (arquivos de vídeo/imagem em tamanho de produção).
- Preview inline de imagens e documentos.
- ClamAV async scan integrado ao file-service.
- Backup e restore operacional.
- Simulação de falha de nó e recuperação.
- Teste de concorrência com múltiplos uploads simultâneos.
- Validação de limites de tamanho configurados.
- Envelope encryption no file-service (AES-256 gerenciado pelo serviço).

Se algum critério acima não for satisfeito no Sprint 3, MinIO será avaliado como alternativa.

## Alternativas consideradas

| Área            | Alternativa              | Razão de não escolha                                       |
| --------------- | ------------------------ | ---------------------------------------------------------- |
| Object storage  | MinIO                    | SeaweedFS validado provisoriamente; MinIO é fallback       |
| Gateway         | Nginx                    | Traefik mais simples para dev/k3s; Nginx como backup       |
| Cache           | Redis                    | Substituído por Valkey (protocolo compatível, licença OSS) |
| Secrets         | Vault / External Secrets | Sealed Secrets mais simples para fase inicial              |
| Busca           | OpenSearch               | Alternativa futura opcional ao Meilisearch/PG FTS          |
| Observabilidade | Loki vs OpenSearch       | Decisão adiada para quando observabilidade for implantada  |

## Consequências

### Positivas

- Stack coerente com requisitos do MVP e com time pequeno.
- Validações iniciais automatizadas em CI.
- Boa compatibilidade com k3s/Kubernetes.
- Caminho claro para GitOps com ArgoCD.
- Segurança inicial com TLS obrigatório e Sealed Secrets.
- Valkey validado elimina incerteza para chat/notifications.

### Negativas e riscos

- SeaweedFS ainda exige validação definitiva no Sprint 3.
- Ainda não existem Dockerfiles nem imagens reais nos registries.
- Serviços Go ainda não integram PostgreSQL, Valkey nem SeaweedFS.
- Observabilidade ainda não implantada (Prometheus, Grafana, Jaeger, Loki).
- Backup e failover de PostgreSQL ainda pendentes.
- Chamadas de áudio/vídeo (LiveKit) estão fora do MVP conforme requisitos v5.0.

## Critérios de revisão da decisão

Este ADR deve ser revisado:

- Ao fim do Sprint 3, para confirmar ou substituir SeaweedFS.
- Antes do go-live MVP, para rever todo o estado da stack.
- Se alguma PoC crítica falhar durante o desenvolvimento.
- Se surgirem riscos de licenciamento em qualquer componente.
- Se os requisitos de segurança mudarem de forma que a stack atual não os atenda.

## Links relacionados

- [docs/runbooks/task-15-seaweedfs-poc.md](../runbooks/task-15-seaweedfs-poc.md)
- [docs/runbooks/task-16-valkey-poc.md](../runbooks/task-16-valkey-poc.md)
- [docs/runbooks/task-12-tls-dev-staging.md](../runbooks/task-12-tls-dev-staging.md)
- [docs/runbooks/task-13-sealed-secrets.md](../runbooks/task-13-sealed-secrets.md)
- [docs/runbooks/task-14-health-checks.md](../runbooks/task-14-health-checks.md)
- [infra/k8s/README.md](../../infra/k8s/README.md)
- [infra/traefik/local/README.md](../../infra/traefik/local/README.md)
