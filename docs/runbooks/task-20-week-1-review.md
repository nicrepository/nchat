# TASK-20 — Revisão da semana + ajustes de backlog

## Status

Concluída — PR aberto para `develop`.

## Objetivo

Revisar as entregas da Semana 1 do NChat, consolidar decisões técnicas, registrar
pendências, riscos e inconsistências de escopo, e preparar o backlog da Semana 2.

## Entradas analisadas

- Issues do repositório (173 issues, issues #2–#205)
- PRs: #1, #15, #17, #19, #21, #23, #25, #27, #29, #31, #32, #35, #37, #40, #42, #206, #207, #208
- GitHub Project "NChat MVP — 17 Ago 2026"
- Runbooks em `docs/runbooks/`
- ADRs em `docs/adr/`
- `docs/architecture/provisional-stack.md`
- Cronograma sincronizado via TASK-GOV

## Entregas

| Artefato                        | Descrição                       |
| ------------------------------- | ------------------------------- |
| `docs/reviews/week-1-review.md` | Revisão formal da Semana 1      |
| `README.md`                     | Seção Weekly reviews adicionada |

## Validação

```bash
pnpm format:check
make ci
```

Ambos passando sem erros.

## Pendências manuais

| Item                                                    | Responsável | Prioridade |
| ------------------------------------------------------- | ----------- | ---------- |
| Atualizar Status do Project: TASK-18 → Done             | Manual      | Médio      |
| Atualizar Status do Project: TASK-19 → Done             | Manual      | Médio      |
| Atualizar Status do Project: TASK-20 → In Progress/Done | Manual      | Médio      |
| Decisão formal sobre voz/LiveKit no MVP vs V1.0         | Produto     | Alto       |

## Inconsistências registradas

1. **Voz/LiveKit:** EPIC-09 está no Project com milestone MVP Interno e `priority:high`,
   mas ADR-0002 classifica voz como V1.0 futuro. Requer decisão formal antes de EPIC-09.

2. **SeaweedFS provisório:** Decisão definitiva em Sprint 3. Aceitável.

3. **Observabilidade incompleta:** Alertas e logs centralizados pendentes. Aceitável.

## Próximo passo

Iniciar **TASK-21** (#50): Modelar usuários, sessões, dispositivos e convites no PostgreSQL.
