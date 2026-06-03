# TASK-17 — Registrar decisão provisória da stack

## Status

Concluído.

## Objetivo

Registrar formalmente a stack tecnológica provisória do NChat após as validações do Sprint 0,
consolidando as decisões confirmadas, provisórias e pendentes em documentação rastreável.

## Entradas utilizadas

| Fonte                             | Papel                                     |
| --------------------------------- | ----------------------------------------- |
| Documento de requisitos v5.0      | Base para escopo MVP e versões futuras    |
| Runbook TASK-15 (SeaweedFS PoC)   | Evidência de validação provisória         |
| Runbook TASK-16 (Valkey PoC)      | Evidência de validação confirmada         |
| Runbook TASK-12 (TLS dev/staging) | Evidência de TLS confirmado               |
| Runbook TASK-13 (Sealed Secrets)  | Evidência de Sealed Secrets confirmado    |
| Runbook TASK-14 (Health checks)   | Evidência de health/readiness confirmados |
| Workflows GitHub Actions          | Evidência de CI/CD confirmado             |
| `infra/k8s/` e `infra/traefik/`   | Evidência de K8s/Traefik confirmados      |

## Entregas

| Artefato                                           | Localização                                           |
| -------------------------------------------------- | ----------------------------------------------------- |
| ADR-0002 — Decisão provisória da stack             | `docs/adr/0002-provisional-stack-decision.md`         |
| Documento de stack provisória                      | `docs/architecture/provisional-stack.md`              |
| Runbook TASK-17                                    | `docs/runbooks/task-17-provisional-stack-decision.md` |
| README atualizado com seção Architecture decisions | `README.md`                                           |

## Decisões confirmadas

As seguintes decisões foram confirmadas no Sprint 0 e estão formalizadas no ADR-0002:

- **Go 1.25:** monorepo com sete serviços base, estrutura padronizada, CI completo.
- **React + TypeScript + Vite:** frontend SPA/PWA com lint, typecheck, testes e coverage.
- **PostgreSQL 16:** provisionado no ambiente de desenvolvimento local.
- **Valkey 8:** validado para Pub/Sub, Streams, SETNX, TTL e sliding window.
- **Traefik:** gateway local e K8s ingress para dev/k3s.
- **Sealed Secrets:** política strict, runbook documentado, CI policy check.
- **TLS 1.3:** configurado via Traefik para dev/staging.
- **GitHub Actions + GitLab CI:** workflows completos com required checks.
- **Kubernetes/k3s + Kustomize:** manifests base e overlays dev/staging.
- **Health/readiness:** `/healthz` e `/readyz` em todos os serviços Go.

## Decisões provisórias

### SeaweedFS

SeaweedFS foi aceito **provisoriamente** como solução de armazenamento de arquivos após a
PoC do Sprint 0. A PoC validou upload/download, integridade SHA-256, latência básica e
replicação básica.

**Esta não é uma decisão definitiva.** A decisão definitiva ocorrerá ao fim do Sprint 3,
após validar:

- Upload grande realista.
- Preview inline.
- ClamAV async scan.
- Backup/restore operacional.
- Falha de nó e recuperação.
- Concorrência com múltiplos uploads.
- Limites de tamanho.
- Envelope encryption no file-service.

Se os critérios não forem atendidos, MinIO será avaliado como alternativa.

## Decisões pendentes

As seguintes decisões estão intencionalmente adiadas:

- Cloud provider de failover para produção real.
- Estratégia final de backup do PostgreSQL.
- Decisão definitiva sobre SeaweedFS (Sprint 3).
- Provedor de e-mail transacional.
- Integração FCM/APNs para notificações mobile.
- Estratégia de HA PostgreSQL (Patroni, pgBouncer, Barman).
- Observabilidade: Loki vs OpenSearch.
- LiveKit deployment topology (self-hosted vs managed).
- Certificados reais de produção e cert-manager.
- Estratégia de DR (RTO/RPO).

## Itens fora do MVP

Conforme documento de requisitos v5.0:

- E2E com MLS RFC 9420 — V1.0.
- Chamadas de áudio/vídeo com LiveKit + coturn — V1.0.
- Cliente desktop com Tauri — V1.0.
- Cliente mobile com React Native — V1.1.

## Validação

```bash
pnpm format:check
pnpm ci:config-check
pnpm run ci
make ci
```

## Definition of Done

- [x] ADR-0002 criado (`docs/adr/0002-provisional-stack-decision.md`)
- [x] Documento de stack provisória criado (`docs/architecture/provisional-stack.md`)
- [x] SeaweedFS marcado como provisório (decisão definitiva no Sprint 3)
- [x] Valkey marcado como confirmado Sprint 0
- [x] Decisões pendentes documentadas
- [x] Itens fora do MVP documentados (chamadas como V1.0, não MVP)
- [x] README atualizado com seção Architecture decisions
- [x] `make ci` passa
- [x] PR aberto para develop
