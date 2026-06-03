# TASK-GOV — Sincronizar cronograma com GitHub Project

## Status

Concluído (parcial — campo Status do Project gerenciado automaticamente pelo GitHub)

## Objetivo

Sincronizar o cronograma macro do NChat com o GitHub Project operacional "NChat MVP — 17 Ago 2026"
(Project #2), garantindo rastreabilidade entre planejamento e execução.

## Fonte

- Planilha: `Cronograma NChat(2).ods` — sheet "Checklist Diário" (175 tarefas, IDs 1–175)
- GitHub Issues existentes antes da sincronização
- GitHub Project "NChat MVP — 17 Ago 2026" (node ID: `PVT_kwHOD1Ztz84BYDrN`)

## Estratégia

- A planilha permanece como cronograma macro (fonte da verdade do planejamento).
- O GitHub Project é o quadro operacional (fonte da verdade da execução).
- Cada tarefa do cronograma vira uma issue com prefixo `[TASK-XX]`.
- Épicos são issues separadas com prefixo `[EPIC-XX]`.
- RF-01..RF-85 **não** são importados individualmente — são rastreados via épicos e tarefas.
- TASK-07B (elevação de coverage) é tarefa extra, sem ID na planilha, mantida no projeto.

## Resultado da sincronização

| Métrica                           | Total |
| --------------------------------- | ----: |
| Tasks encontradas na planilha     |   175 |
| TASK-GOV adicionada manualmente   |     1 |
| Total processado                  |   176 |
| Issues já existentes reutilizadas |    13 |
| Issues criadas                    |   163 |
| Itens adicionados ao Project      |   190 |
| Campos atualizados por item       |     6 |
| Pendências manuais                |     1 |

**Campos atualizados via API:** Type, Target, Priority, Epic, Phase, Due Date

**Campo não atualizado via API:** Status — gerenciado automaticamente pelas automações do GitHub
Projects v2 (issues fechadas → Done; issues abertas → Todo).

## Issues reutilizadas

| Issue | Título                                                     | Status (GitHub) |
| ----: | ---------------------------------------------------------- | --------------- |
|   #16 | [TASK-05] Criar monorepo Go + frontend React               | closed          |
|   #18 | [TASK-06] Criar estrutura base dos serviços                | closed          |
|   #20 | [TASK-07] Configurar lint, formatter e testes base         | closed          |
|   #22 | [TASK-08] Subir ambiente dev com PostgreSQL, Valkey e Sea… | closed          |
|   #24 | [TASK-07B] Elevar thresholds de coverage para >90%         | closed          |
|   #26 | [TASK-09] Criar manifests iniciais K8s/k3s                 | closed          |
|   #28 | [TASK-10] Configurar GitHub Actions/GitLab CI              | closed          |
|   #30 | [TASK-11] Configurar Traefik/Nginx local                   | closed          |
|   #33 | [TASK-12] Configurar TLS dev/staging                       | closed          |
|   #34 | [TASK-13] Configurar Sealed Secrets                        | closed          |
|   #36 | [TASK-14] Criar health checks /healthz e /readyz           | closed          |
|   #38 | [TASK-15] PoC SeaweedFS: upload, download, latência…       | open            |
|   #39 | [TASK-16] PoC Valkey: Pub/Sub, Streams, SETNX, TTL…        | open            |
|   #41 | [TASK-17] Registrar decisão provisória da stack            | open            |

## Issues criadas nesta sincronização (amostra)

| Issue | Task ID  | Título (resumido)                                          | Estado |
| ----: | -------- | ---------------------------------------------------------- | ------ |
|   #43 | TASK-01  | Kickoff técnico do projeto                                 | closed |
|   #44 | TASK-02  | Confirmar escopo do MVP: chat + voz mínima                 | closed |
|   #45 | TASK-03  | Definir repositórios, branch strategy e padrão de PR       | closed |
|   #46 | TASK-04  | Criar board com épicos                                     | closed |
|   #47 | TASK-18  | Configurar Prometheus, Grafana e Jaeger                    | open   |
|   #48 | TASK-19  | Criar dashboard inicial: serviços up/down, latência, erros | open   |
|   #49 | TASK-20  | Revisão da semana + ajustes de backlog                     | open   |
|   #50 | TASK-21  | Modelar usuários, sessões, dispositivos e convites         | open   |
|     … | …        | …                                                          | …      |
|  #204 | TASK-175 | Decidir próximos passos: E2E, DND, Tauri, multi-workspace… | open   |
|  #205 | TASK-GOV | Sincronizar cronograma com GitHub Project                  | open   |

> Issues TASK-01 a TASK-04 foram criadas como fechadas (retroativas, Sprint 0 concluído).

## Fields do Project configurados via API

Os seguintes campos foram configurados via GraphQL `updateProjectV2ItemFieldValue`:

| Campo    | Valores disponíveis                                            |
| -------- | -------------------------------------------------------------- |
| Type     | Epic / Story / Task / Bug / Security / QA / Docs / Infra       |
| Target   | MVP / V1.0 / v1.1 / Backlog                                    |
| Priority | Critical / High / Medium / Low                                 |
| Epic     | EPIC-01 Auth … EPIC-13 Documentação                            |
| Phase    | Sprint 0 … Sprint 15 / MVP / V1.0 / Backlog                    |
| Due Date | Date field — preenchido com data da planilha quando disponível |

## Pendências manuais

| #   | Pendência                                | Como resolver                                                |
| --- | ---------------------------------------- | ------------------------------------------------------------ |
| 1   | Campo **Status** não atualizável via API | Automação do GitHub já faz: fechado→Done, aberto→Todo.       |
|     |                                          | Para "In Progress": setar manualmente no Project UI.         |
| 2   | TASK-GOV (#205) com Status=Todo          | Setar para "In Progress" no Project UI enquanto em execução. |
| 3   | Épicos sem campo Epic próprio no Project | Já são itens do Project com Type=Epic.                       |
| 4   | Due Date de tarefas sem data na planilha | Preencher manualmente conforme sprint progride.              |

## Regras de manutenção

- Toda task nova deve virar issue com prefixo `[TASK-XX]`.
- Todo PR deve referenciar a issue via `Closes #N` ou `Refs #N`.
- Issue concluída deve ter PR mergeado; GitHub fecha automaticamente e Project move para Done.
- Cronograma macro (planilha) e Project devem ser reconciliados ao fim de cada sprint.
- Evitar criar issue por RF individual — rastrear RFs via épicos e tarefas de implementação.
- TASK-07B mantida como tarefa extra sem ID de planilha.

## Próxima revisão

- Ao fim da **Semana 1** (após TASK-20 — Revisão da semana + ajustes de backlog).
- Reconciliar tarefas abertas vs. concluídas e atualizar Due Dates conforme progresso real.
