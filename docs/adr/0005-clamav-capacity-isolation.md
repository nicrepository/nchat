# ADR-0005 — Papéis de arquivos e admissão global de scans

## Status

Proposed — Entrega E da #1129; implementação candidata sujeita a revisões
independentes, resolução dos bloqueios B e autorização operacional.

## Decisão

Reutilizar a imagem file-service em três papéis explícitos: API, scan-worker e
preview-worker. Conservar combined para compatibilidade, migrations externas,
contratos públicos e fencing C. Separar Pods, recursos, identidades e fluxos de
rede sem alterar a autoridade de autorização de anexos.

Limitar admissão global no PostgreSQL, junto ao claim, com capacidade inicial 1.
Manter reserva conservadora após interrupção para não presumir que perder lease
ou socket encerrou trabalho no daemon. Só aumentar capacidade com medições.

Não ativar autoscaling sem pipeline de métricas de trabalho processável e
controller validado. Não substituir backlog autoritativo por CPU. Não criar
pools por custo antes de classificação confiável da Entrega F.

## Consequências

Três processos consomem mais conexões/requests Kubernetes, mas preview não disputa
limite de memória da API nem slots AV. Recuperação após falha pode aguardar até
780s com limites padrão; expansão sobre C ativa impõe pausa conservadora 4080s.
Manter o schema de admissão no rollback permite que N/N+1 compartilhem teto.
Credenciais SQL/keyring ainda não têm separação de autorização por papel.

Alternativas, threat model, recursos, testes e migração estão no
[runbook E](../runbooks/clamav-capacity-isolation-1129.md).
