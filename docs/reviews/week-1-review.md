# NChat — Revisão da Semana 1

## Status

Concluída

## Período

Semana 1 — Kickoff e fundação técnica (2026-05-18 a 2026-05-22)

## Resumo executivo

A Semana 1 consolidou integralmente a fundação técnica do NChat. Foram executadas 19 tarefas
(TASK-01 a TASK-20, incluindo TASK-07B e TASK-GOV) que cobrem monorepo, CI/CD, qualidade de
código, ambiente de desenvolvimento local, Kubernetes, gateway, segurança, observabilidade,
PoCs de infraestrutura e governança do projeto.

O resultado é uma plataforma de desenvolvimento pronta para receber as primeiras features reais
de produto. Todos os 18 PRs foram mergeados em `develop`. O CI (`make ci` / `pnpm run ci`) está
passando com cobertura Go ≥ 90% em todos os 7 serviços. Nenhum segredo foi exposto no
repositório.

O projeto ainda não possui features de produto. A autenticação, o chat, os arquivos, as
notificações e a voz são tarefas futuras a partir da Semana 2.

---

## Entregas concluídas

| Task     | Issue |    PR | Status          | Entrega principal                                              |
| -------- | ----: | ----: | --------------- | -------------------------------------------------------------- |
| TASK-01  |    43 |     — | ✅ Done         | Kickoff técnico do projeto                                     |
| TASK-02  |    44 |     — | ✅ Done         | Escopo MVP confirmado                                          |
| TASK-03  |    45 |     1 | ✅ Done         | Governança: branch strategy, PR template, CODEOWNERS           |
| TASK-04  |    46 |    15 | ✅ Done         | GitHub Project, milestones, épicos, issues estruturadas        |
| TASK-05  |    16 |    17 | ✅ Done         | Monorepo Go workspace + app React/TypeScript/Vite              |
| TASK-06  |    18 |    19 | ✅ Done         | 7 serviços Go base com `/healthz`, `/readyz`, `/version`       |
| TASK-07  |    20 |    21 | ✅ Done         | golangci-lint, Prettier, testes base, coverage Go e web        |
| TASK-07B |    24 |    25 | ✅ Done         | Thresholds de coverage ≥ 90% em todos os serviços              |
| TASK-08  |    22 |    23 | ✅ Done         | Docker Compose: PostgreSQL 16, Valkey 8, SeaweedFS             |
| TASK-09  |    26 |    27 | ✅ Done         | Manifests K8s/k3s com Kustomize (dev + staging)                |
| TASK-10  |    28 |    29 | ✅ Done         | GitHub Actions + GitLab CI com quality gate                    |
| TASK-11  |    30 | 31,32 | ✅ Done         | Traefik gateway local com TLS e roteamento por host            |
| TASK-12  |    33 |    35 | ✅ Done         | TLS 1.3 dev/staging com mkcert e cert-manager preparado        |
| TASK-13  |    34 |    35 | ✅ Done         | Sealed Secrets: estrutura, policy CI e runbook de rotação      |
| TASK-14  |    36 |    37 | ✅ Done         | Health/readiness padronizados; `/metrics` em todos os serviços |
| TASK-15  |    38 |    40 | ✅ Done         | PoC SeaweedFS: upload, download, latência e replicação básica  |
| TASK-16  |    39 |    40 | ✅ Done         | PoC Valkey: Pub/Sub, Streams, SETNX, TTL, sliding window       |
| TASK-17  |    41 |    42 | ✅ Done         | ADR-0002: decisão provisória da stack tecnológica              |
| TASK-GOV |   205 |   206 | ✅ Done         | Cronograma sincronizado com GitHub Project (173 issues)        |
| TASK-18  |    47 |   207 | ✅ Done         | Prometheus, Grafana, Jaeger + OpenTelemetry SDK + `/metrics`   |
| TASK-19  |    48 |   208 | ✅ Done         | Dashboard inicial NChat Overview com 11 painéis PromQL         |
| TASK-20  |    49 |   209 | 🔄 Em andamento | Revisão da Semana 1 (este documento)                           |

---

## Decisões técnicas confirmadas

| Área             | Decisão                                  | Status     | Evidência                  |
| ---------------- | ---------------------------------------- | ---------- | -------------------------- |
| Backend          | Go 1.25, 7 microsserviços                | Confirmado | TASK-05, TASK-06, monorepo |
| Frontend         | React + TypeScript + Vite                | Confirmado | TASK-05, apps/web          |
| Banco relacional | PostgreSQL 16                            | Confirmado | TASK-08, Docker Compose    |
| Cache/filas      | Valkey 8                                 | Confirmado | TASK-16, PoC completo      |
| Gateway          | Traefik (dev/k3s)                        | Confirmado | TASK-11                    |
| TLS              | TLS 1.3 dev/staging                      | Confirmado | TASK-12                    |
| Secrets          | Sealed Secrets                           | Confirmado | TASK-13                    |
| CI/CD            | GitHub Actions + GitLab CI               | Confirmado | TASK-10                    |
| K8s              | k3s + Kustomize                          | Confirmado | TASK-09                    |
| Health checks    | `/healthz`, `/readyz`, `/version`        | Confirmado | TASK-14                    |
| Qualidade        | coverage ≥ 90%, golangci-lint, Prettier  | Confirmado | TASK-07, TASK-07B          |
| Observabilidade  | Prometheus + Grafana + Jaeger + OTEL SDK | Confirmado | TASK-18, TASK-19           |
| Métricas         | `nchat_http_requests_total` et al.       | Confirmado | TASK-14, TASK-18           |
| ADR              | ADR-0001 (repo), ADR-0002 (stack)        | Confirmado | docs/adr/                  |

---

## Decisões provisórias

| Área         | Decisão provisória                                                                                                         | Critério de revisão                        |
| ------------ | -------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------ |
| Storage      | SeaweedFS aceito até Sprint 3                                                                                              | Validações completas no Sprint 3           |
| Áudio/voz    | LiveKit referenciado como MVP em algumas issues (EPIC-09, TASK-124–138), mas marcado como V1.0 em ADR-0002 e decisão-stack | Decisão explícita antes de iniciar EPIC-09 |
| Cloud/HA     | Nenhum provider de produção definido                                                                                       | ADR explícito antes de deploy real         |
| Logs         | Loki vs OpenSearch: adiado                                                                                                 | Decisão quando stack logs for iniciada     |
| Cert-manager | Certificados reais de produção ainda não configurados                                                                      | Quando ambiente de produção for criado     |

---

## Pendências técnicas

| Pendência                                        | Impacto         | Próxima ação                             |
| ------------------------------------------------ | --------------- | ---------------------------------------- |
| Node local v22, projeto pede Node 24             | Baixo (avisos)  | Atualizar para Node 24 no ambiente local |
| Serviços sem integração PostgreSQL real          | Alto            | TASK-21, TASK-22 (Semana 2)              |
| Serviços sem integração Valkey real              | Alto            | Após EPIC-01 auth                        |
| file-service sem integração SeaweedFS real       | Alto            | TASK-74+ (EPIC-04)                       |
| Dockerfiles multi-stage não criados              | Médio           | Bloqueia GHCR/deploy; tarefa futura      |
| GHCR build/push não configurado                  | Médio           | Após Dockerfiles                         |
| Alertas Prometheus/Alertmanager não configurados | Médio           | TASK-120 (Semana futura)                 |
| Logs centralizados Loki/OpenSearch pendentes     | Médio           | Tarefa futura pós-decisão                |
| Backup/restore não validado                      | Alto (produção) | TASK-150                                 |
| HA PostgreSQL/Patroni pendente                   | Alto (produção) | Tarefa futura                            |
| Autenticação real não implementada               | Crítico         | TASK-21..TASK-45 (início Semana 2)       |
| Chat/WebSocket não implementado                  | Crítico         | EPIC-02, EPIC-03                         |
| Testes E2E não configurados                      | Médio           | TASK-34, TASK-141                        |
| Staging/produção reais não criados               | Alto            | Sprint tardio                            |

---

## Riscos

| Risco                                                           | Severidade | Mitigação                                                     |
| --------------------------------------------------------------- | ---------- | ------------------------------------------------------------- |
| Escopo de chamadas de áudio inconsistente (MVP vs V1.0)         | **Alto**   | Decisão formal antes de EPIC-09; ver seção de inconsistências |
| SeaweedFS provisório — pode ser substituído no Sprint 3         | Médio      | PoC documentado; ADR-0002 explícito; evitar acoplamento forte |
| Falta de Dockerfiles — bloqueia deploy real                     | Alto       | Criar antes do primeiro release candidate                     |
| Falta de autenticação real — sem feature de produto             | Crítico    | Prioridade máxima Semana 2 (EPIC-01)                          |
| Falta de integração com dados — serviços são shells vazios      | Crítico    | TASK-21/22 primeiras da Semana 2                              |
| Observabilidade sem alertas/logs                                | Médio      | TASK-120 planejado; aceito para MVP inicial                   |
| Node local v22 vs v24 esperado                                  | Baixo      | Atualizar ambiente; warnings não travam CI                    |
| Deriva entre cronograma e Project se não houver revisão regular | Médio      | Revisão semanal (este processo)                               |

---

## Inconsistências de escopo/requisitos

### 1. Chamadas de áudio: MVP ou V1.0?

**Situação:**

- Em `docs/architecture/provisional-stack.md` e em `ADR-0002`, chamadas de áudio/vídeo
  (LiveKit) são listadas como **"fora do MVP"** e **"V1.0 futuro"**.
- Entretanto, TASK-02 (#44) tem título "[TASK-02] Confirmar escopo do MVP: chat + voz mínima",
  sugerindo que "voz mínima" foi confirmada como parte do MVP.
- EPIC-09 ([TASK-124] a [TASK-138]) contempla LiveKit e coturn como issues com milestone
  "MVP Interno" e labels `mvp`.
- Issues TASK-130 e TASK-133 são "chamada de voz 1:1" e "sala de voz por canal", com
  `priority:high`.

**Conflito:** há duas versões conflitantes do escopo de voz dentro do mesmo repositório. As
issues do Project assumem voz no MVP; os documentos de arquitetura a excluem do MVP.

**Ação recomendada:** criar ADR ou decisão formal documentada respondendo a pergunta
"voz mínima com LiveKit entra no MVP até 17 de agosto de 2026 ou vai para V1.0?" antes de
iniciar qualquer tarefa do EPIC-09.

**Não resolvido neste documento** — requer decisão do responsável pelo produto.

---

### 2. SeaweedFS: provisório até Sprint 3

Registrado em ADR-0002 e `docs/architecture/provisional-stack.md`. PoC passou com sucesso
(TASK-15). Decisão definitiva pendente — aceitável para continuar.

---

### 3. Observabilidade: alertas e logs pendentes

Prometheus/Grafana/Jaeger configurados (TASK-18, TASK-19). Alertmanager e logs
centralizados não configurados. Aceitável para MVP inicial — TASK-120 prevista.

---

### 4. GitHub Project: atualizações de status exigem edição manual

O campo `Status` do Project (TASK-18, TASK-19 → Done; TASK-20 → In Progress) não pode ser
atualizado automaticamente via `gh project item-edit` com a API disponível neste ambiente.
**Ação manual necessária:** ajustar status no Project após merge dos PRs.

---

## Backlog da Semana 2

Baseado nas issues abertas do repositório, a ordem recomendada é:

| Ordem | Task    | Issue | Prioridade | Observação                                            |
| ----: | ------- | ----: | ---------- | ----------------------------------------------------- |
|     1 | TASK-20 |    49 | Medium     | Revisão Semana 1 — este PR (em andamento)             |
|     2 | TASK-21 |    50 | Medium     | Modelar usuários, sessões, dispositivos no PostgreSQL |
|     3 | TASK-22 |    51 | Medium     | Criar migrations iniciais                             |
|     4 | TASK-23 |    52 | Medium     | Implementar cadastro manual pelo admin                |
|     5 | TASK-24 |    53 | Medium     | Implementar login com e-mail/senha                    |
|     6 | TASK-25 |    54 | Medium     | Implementar JWT access + refresh token                |
|     7 | TASK-26 |    55 | Medium     | Implementar expiração de sessão                       |

**Observações técnicas:**

- As TASK-21 e TASK-22 (modelo de dados + migrations) devem preceder qualquer implementação
  de feature de autenticação.
- Dockerfiles multi-stage não aparecem ainda como issues explícitas no backlog, mas serão
  necessários antes de qualquer release candidate. Verificar se existem tarefas específicas
  ou se devem ser inseridas no backlog.
- Antes de iniciar EPIC-09 (Voz/LiveKit), a inconsistência de escopo descrita acima deve
  ser resolvida.
- TASK-120 (alertas Prometheus/Alertmanager) pode ser encaixada na trilha de observabilidade
  quando a carga de autenticação/chat permitir.

---

## Ações recomendadas

1. **Urgente:** Confirmar formalmente se voz (LiveKit) entra no MVP até 17 de agosto ou
   vai para V1.0. Criar ADR ou decisão documentada.
2. Iniciar TASK-21 (modelo PostgreSQL) como primeira tarefa da Semana 2.
3. Manter o GitHub Project atualizado manualmente após cada merge.
4. Atualizar Node local para v24 (projeto define `>=24 <25`).
5. Não iniciar Dockerfiles/GHCR fora da ordem do cronograma, salvo decisão explícita.
6. Realizar revisão semanal ao final de cada semana para manter rastreabilidade.

---

## Conclusão

**Semana 1 concluída com fundação técnica sólida.**

19 issues fechadas, 18 PRs mergeados, CI passando, coverage ≥ 90%, observabilidade básica
operacional. O projeto está apto a iniciar o desenvolvimento das features de produto
a partir da Semana 2, começando pelo EPIC-01 (Auth) com TASK-21 e TASK-22.

A principal pendência de governança é a resolução da inconsistência de escopo sobre voz/LiveKit.
