# Task #953 — Ownership de grupos e canais privados

## Contrato

O papel na conversa é independente do papel no workspace. OWNER administra
papéis e membros; ADMIN remove MEMBER e edita o nome; MEMBER mantém a adição de
participantes autorizada pela #705. OWNER não pode ser removido diretamente.
Rebaixar o último OWNER retorna 409. Guests podem receber OWNER manualmente e
contam como proprietários válidos, mas não recebem ownership automaticamente.

Participantes válidos têm conta ativa, não excluída, membership ativa no workspace
ativo e membership ativa na conversa ativa. Quando o último proprietário perde
acesso, a sucessão escolhe ADMIN e depois MEMBER, ordenados por `joined_at,
user_id`, excluindo guests e usuários sem acesso. Retornar ao grupo renova a
antiguidade. O último participante pode sair. Se restarem participantes sem
candidato automático, toda a operação é desfeita, inclusive suspensão e
revogação de sessões.

## APIs

As rotas usam o workspace resolvido pelo servidor e a identidade autenticada:

- `GET /api/chat/{channels|dm}/{id}/ownership`: participantes, papéis, ações,
  capabilities e previsão da saída.
- `PATCH /api/chat/{channels|dm}/{id}/members/{userId}/role`: `{ "role":
"owner|admin|member" }`.
- `POST /api/chat/{channels|dm}/{id}/ownership/transfer`: `{ "new_owner_user_id":
"uuid", "actor_new_role": "admin|member" }`.
- `POST /api/chat/{channels|dm}/{id}/ownership/transfer-and-leave`: mesmo corpo;
  transfere e remove a membership do ator atomicamente.

Transferências exigem `Idempotency-Key`, com 1–128 caracteres. O registro e a
resposta são persistidos na transação; repetir o mesmo pedido não duplica
auditoria, evento ou saída. Reutilizar a chave com outro corpo retorna 409.
Recursos privados sem acesso retornam 404; ações proibidas, 403; conflitos de
ownership, 409. Leave e remove conservam as rotas e os eventos da #469.

A UI só aceita capabilities estritamente iguais a `true`. Mostra Proprietário,
Administrador e `[Você]` independentemente, oferece busca/filtro em “Ver todos”,
escolha do papel após transferência e confirmação com sucessor previsto. O
submit sempre revalida no banco. Menus e modais mantêm o composer montado.

## Blue/Green

1. Publicar o release de compatibilidade nos serviços chat, auth e admin. Aplicar
   migrações 60–64. `ownership_role` guarda o estado transitório; os valores
   legados de `role` permanecem intactos. `chat.ownership_rollout.enabled` começa
   em `false`. A UI e os endpoints existentes continuam com o contrato anterior.
2. Retirar explicitamente todos os binários incompatíveis, inclusive workers e
   instâncias sem tráfego. O banco não conhece o inventário de deploys: a
   declaração abaixo é responsabilidade do operador.
3. Executar, usando a conexão autorizada do ambiente:

   ```sh
   psql -X -v legacy_retired=true \
     -v rollback_target_sha=SHA_COMPLETO_DO_ARTEFATO_COMPATIVEL \
     -v retirement_evidence=REFERENCIA_DO_REGISTRO_OPERACIONAL \
     -f scripts/db/ownership/activate.sql
   ```

   A operação bloqueia as tabelas envolvidas, repete o backfill, recusa órfãos,
   amplia constraints, copia papéis e ativa ownership numa transação. A ausência
   da declaração explícita recusa a execução. Programar uma janela para o lock.

4. Rollback de aplicação aponta para o release de compatibilidade, conservando
   constraints expandidas e armazenamento transitório. Não retornar ao binário
   anterior à #953. Não remover `ownership_role` nesta task. A migration down de
   compatibilidade recusa um banco com ownership ativado.

A #1043 endurece a ativação e registra o SHA exato do rollback target; consultar
[o runbook de migrations](task-chat-1043-ownership-migrations.md) antes de executar.
O backfill preserva owners existentes e prefere criador elegível. Canais mapeiam
moderator para ADMIN e priorizam ADMIN como fallback; grupos usam diretamente o
membro mais antigo. É idempotente; as atribuições ficam
em `chat.ownership_audit` com motivo `backfill`.

## Transações, auditoria e realtime

Mutações de ownership usam isolamento serializável, locks por conversa e até
três tentativas para `40001`/`40P01`. Invalidação de conta bloqueia as conversas
afetadas em ordem estável antes da conta. Triggers fazem sucessão na mesma
transação e constraints diferidas recusam commits órfãos, inclusive SQL fora dos
endpoints. SQL externo que inverte locks pode receber deadlock e deve repetir a
transação inteira; não pode ignorar a falha.

Auditoria registra atribuições manuais, transferências, sucessão e invalidação.
O outbox agrupa atualizações por transação/conversa. O worker publica somente
linhas já commitadas, usando o fan-out existente, e conserva pedidos quando o
bus falha. Um crash após publicação pode repetir uma invalidation; clientes
refazem a leitura autorizada. Reconexão também atualiza detalhes. O fan-out
revalida acesso, retirando subscriptions de quem saiu ou foi invalidado.

Diagnóstico, sem reconciler como autoridade:

```sql
SELECT kind, conversation_id, workspace_id, active_members
FROM chat.orphaned_private_conversations
ORDER BY kind, conversation_id;

SELECT reason, count(*) FROM chat.ownership_audit GROUP BY reason;
SELECT count(*) FROM chat.ownership_outbox WHERE published_at IS NULL;
```

## Validação

Os testes de integração de chat usam exclusivamente a base `ownership_953_test`
e recusam resets em qualquer outro nome. Configurar `OWNERSHIP_TEST_DATABASE_URL`
e executar `go test ./internal/storage -run 'TestOwnership.*PostgreSQL'` em
`services/chat-service`. O gate de cobertura cria uma base separada para essa
família e combina seu perfil com a cobertura unitária e Link Safety. Auth e admin incluem testes dos stores reais de suspensão/rollback,
ativados pelos respectivos `AUTH_TEST_DATABASE_URL` e `ADMIN_TEST_DATABASE_URL`.

Os testes de componentes cobrem capabilities, filtros, transferência, retry,
conflito, foco e saída bloqueada. O E2E de messaging usa APIs e sockets simulados;
esse teste prova o comportamento do navegador, não a entrega pelo bus real.

## Relatório de entrega — 2026-10-01

### Resumo e mudanças

Implementados ownership local, capabilities, transferência idempotente, saída
atômica com sucessão, integração com invalidação de acesso, proteção no banco,
auditoria e outbox. O painel oferece badges, menus autorizados, filtros e
confirmações. As migrations mantêm a compatibilidade e a ativação explícita.
A branch de segurança preserva o develop anterior; a feature parte de
upstream/develop, sem merge, rebase ou cherry-pick.

### Como testar

No ambiente de QA, aplicar as migrations e ativar somente após retirar os
binários legados, conforme o procedimento acima. Abrir um grupo ou canal privado
com dois usuários; promover outro owner, transferir ownership e sair. Conferir
papéis, restrições de MEMBER/ADMIN e atualização no segundo cliente. Para a
checagem rápida do navegador, executar
`pnpm test:e2e:web e2e/messaging/ownership.spec.ts --project=chromium`.

### Quality Gates — evidências

- Verificação final de componentes/API: 2 arquivos, 9 testes passaram (1,63 s).
- E2E final Chromium: 1 teste passou (6,3 s), incluindo preservação de draft,
  reply e attachment durante transferência e convergência nos dois clientes.
- Integração final PostgreSQL: 13 casos, incluindo concorrência, backfill,
  rollback, invalidação, idempotência e retorno de participante; passou (11,95 s).
- Stores reais de suspensão em auth/admin passaram na base isolada; rollback
  conserva status e sessões quando não existe sucessor elegível.
- QA real anterior com dois usuários, duas réplicas, PostgreSQL e Valkey passou:
  múltiplos owners, transferência manual, sucessão, acesso revogado, permissões,
  drafts preservados e nenhum erro de página. Esse QA foi de grupo privado.
- Serviços locais atualizados; smoke autenticado final: login, sidebar, detalhes
  de dois canais e detalhes de grupo responderam 200.
- `make ci` foi executado uma vez: checks do repositório, formatação, lint e
  typecheck frontend, gofmt e go vet passaram. Parou no lint Go por uso de
  strings.Index em fixture nova; corrigido e lint direcionado passou nos três
  serviços alterados. O pipeline completo não chegou ao fim e não foi repetido,
  conforme a orientação posterior de reduzir testes. Frontend teve três warnings
  de React Compiler em código existente.
- `git diff --check` passou.

### Code Quality Review

Resultado: APROVADO COM RESSALVAS. Escopo: código de produção alterado, UI,
protocolo compartilhado, migrations e testes direcionados. Nenhum bloqueador
confirmado. Responsabilidades foram separadas em política, persistência,
transporte, outbox e diálogo de UI. Novas funções de produção de ownership
verificadas pelo contador AST Go e ESLint ficaram em complexidade ciclomática
até 10; isso não substitui uma medição formal de complexidade cognitiva.

CQ-01 — informativa, confiança alta: PeopleSection em
`apps/web/src/chat/ConversationDetailsPanel.tsx` conserva complexidade 16,
igual à base. Também permanecem funções legadas de storage acima de 10; não
houve reescrita ampla desses contratos. Impacto: manutenção futura mais custosa.
Melhoria não bloqueante: decompor essas funções numa task específica e repetir
os testes dos contratos existentes. Não há correção funcional pendente desse
achado.

### Security Review

Resultado: APROVADO no escopo revisado, sem vulnerabilidade confirmada. Revisão
separada de identidade autenticada, workspace resolvido no servidor, acesso à
conversa, autorização local de ator/target, JSON estrito, SQL parametrizado,
serialização, replay, auditoria e rollback. A leitura filtra workspace e exige
membership válida; cada mutação revalida a política dentro da transação.
Replay fica restrito à identidade autenticada e ao mesmo workspace/conversa,
sem devolver perfis de participantes após uma saída. ADMIN não promove papéis
nem remove OWNER/ADMIN. Chave com corpo diferente produz conflito. A remoção
pelo console administrativo também aplica a autoridade local após ativação.
Testes PostgreSQL cobrem isolamento, ações negadas, último owner, conflitos e
atomicidade. Não foi realizada uma auditoria externa de toda a aplicação.

### Riscos e limitações

A ativação depende da retirada explícita dos binários incompatíveis e exige
janela de locks. Ownership permanece desativado no ambiente local padrão.
Rollback deve usar o release de compatibilidade. Invalidation pode ser repetida
após crash do publisher e é tratada por refetch. O QA real de duas réplicas não
cobriu canais privados nem todos os dispositivos móveis; canais estão cobertos
pelos testes PostgreSQL. Os gates posteriores à interrupção de make ci não
foram executados nesta rodada final. Não há resultado formal de Sonar/cognitive
complexity. A validação final foi reduzida por solicitação do usuário.

### Checklist

- [x] Implementação e documentação da task.
- [x] Verificações direcionadas e carregamento local.
- [x] Code Quality Review e Security Review separados.
- [x] Rollout compatível e ativação explícita documentados.
- [x] Sem push, PR ou merge automático.

## Ajuste visual posterior

Ownership foi ativado no banco local a pedido do usuário; a consulta diagnóstica
confirmou zero conversas órfãs. O estado desativado mencionado no relatório
anterior corresponde ao momento daquela entrega.

O painel recebeu badges, nomes e ações alinhados ao restante do chat. O menu
agora fica fora do scrollport para evitar cortes, fecha ao escolher uma ação,
por Escape ou clique externo e restaura o foco. Confirmações explicam o papel
antes do submit. Menus e diálogos portalled adotam `chat-theme`: sem essa classe,
herdavam os tokens globais de autenticação em vez da paleta do chat.

Verificação curta: sete testes de componentes passaram; lint direcionado com
complexidade máxima 10 passou. Foi feita inspeção de painel, menu e diálogo no
navegador local sem executar alterações de participantes.

## Integração com develop e correções do CI

A branch local incorporou o merge remoto `12d6b62`, que contém
`upstream/develop` (`2f9be02`), sem conflitos. A verificação curta depois do merge
passou: nove testes de ownership, dois de criação de grupos e typecheck.

A execução CI `36920556170` revelou três falhas corrigidas:

- Adição e remoção no console usavam isolamentos diferentes. Os dois writers
  agora usam transações serializáveis e o mesmo retry limitado. Os testes
  PostgreSQL de contagens concorrentes passaram em três execuções direcionadas.
- A cobertura não media a suíte PostgreSQL de ownership. Essa suíte passou a ser
  executada pelo gate de cobertura, numa base exclusiva, sem duplicação no gate
  de integração. Perfis existentes do CI combinados com testes direcionados
  corrigidos mediram 90,0%, mantendo o threshold de 90%.
- O contrato antigo de detalhes não previa invalidação de papéis sem rename.
  O painel revalida capabilities mesmo com nome igual e evita um segundo reload
  quando a projeção já contém o nome canônico; o callback duplicado do header
  foi removido. Os seis testes de foco/convergência passaram.

Foram acrescentadas verificações curtas de rejeição de entrada, falha de banco,
rollback, fallback de compatibilidade e replay sem duplicação de evento.

O E2E direcionado também identificou que o texto do badge incluía o nome do
ícone decorativo no seletor textual. O rótulo foi separado em um elemento
próprio, mantendo o ícone com aria-hidden.
