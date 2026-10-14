# #1046 — Sucessão automática determinística de OWNER

## Implementação

Branch `feature/chat-1046-owner-succession`, base `upstream/develop`
`ff4c329f0bb063b70c172de3dd32cda92bf67f98` (referência atualizada na execução).

A saída e a remoção passam por autorização e seleção na mesma transação
SERIALIZABLE, após o lock da conversa. O participante afetado é excluído da
consulta antes de contar owners e participantes restantes. A view
`chat.active_ownership_participants` define acesso e elegibilidade. Guests ativos
contam como participantes e owners guest existentes preservam a invariante,
mas guests nunca são candidatos automáticos.

Sem owner restante, ADMIN precede MEMBER; dentro da role, `joined_at ASC` e
`user_id ASC` (UUID no banco) resolvem antiguidade e empate. A promoção usa
`chat.assign_ownership` com actor e motivo `succession`, antes da operação
original. Sem candidato, participantes restantes causam conflito 409; uma
conversa vazia permite a saída. Qualquer falha reverte a transação completa.
Cada retry de `40001`/`40P01` reabre a transação e repete autorização e seleção.

Não há mudança em contratos HTTP, UI, migrations, backfill, transferência manual
ou tipos públicos. Os guards existentes continuam protegendo outros writers e
SQL direto. Não foi criado lock global de workspace nem evento/outbox novo.

Arquivos: `ownership_store.go`, `ownership_succession_store.go`,
`ownership_succession_store_test.go`, `ownership_succession_postgres_test.go`,
`ownership_postgres_test.go` e este relatório.

## Testes e evidências

Banco dedicado: container `nchat-1046-postgres`, PostgreSQL 16, porta local 5546,
database `ownership_953_test`. O harness recria schemas; os casos são sequenciais.
O container foi parado após os testes e preservado para reprodução.

- PASS: seleção e elegibilidade para grupo e canal privado, incluindo OWNER
  restante, guest owner, ADMIN/MEMBER por antiguidade, prioridade ADMIN,
  empate UUID, exclusão do participante em saída, guests, usuários
  suspended/locked/invited/deleted, `deleted_at`, workspace membership
  suspended/left e conversation membership inativa.
- PASS: concorrência nas duas ordens de aquisição do lock: dois owners saindo e
  saída do último owner versus remoção do candidato. Barreiras e transações
  reais, sem sleeps; após a saída, a remoção pelo actor que saiu perde autorização.
  Nenhum candidato removido é promovido, nem ficam conversas órfãs.
- PASS: regressões mock: retry dos dois SQLSTATEs com outro candidato e rollback
  quando a operação original falha após promoção.
- PASS: pacote storage completo com PostgreSQL e race detector (123,139 s),
  incluindo a regressão existente de promoção concorrente. Coverage do pacote:
  71,4%; `succeedOwnership`: 90%; `prepareOwnershipDeparture`: 100%.
  A comparação com a base pelo gate sem PostgreSQL está registrada abaixo.
- PASS: rodada direcionada com race detector após qualificar o ORDER BY UUID
  (56,332 s): seleção/elegibilidade, rollback, concorrência, rejoin, demotion
  direta e regressão preexistente de promoção concorrente.
- N/A: E2E/Playwright e QA de UI; não houve mudança de interface.

O teste de seleção instala um BEFORE trigger que exige owner restante antes da
saída, distinguindo a promoção da aplicação daquela feita pelo guard AFTER.
A regressão PostgreSQL de rollback injeta falha na saída e verifica roles,
evento, audit e outbox. SQL direto faz demotion numa transação explícita e falha
no commit. O rejoin do grupo foi fortalecido para verificar o reset da role;
a regressão do canal verifica role e antiguidade da membership nova.

## Code Quality Review

Resultado: APROVADO, zero achados novos confirmados. Escopo: somente o delta.
A orquestração chama um helper coeso de sucessão; a consulta concentra o mesmo
predicado de acesso da view oficial. Não há novas abstrações públicas ou regra
de elegibilidade duplicada. Os testes verificam estados e efeitos persistidos.

Complexidade medida com `gocyclo v0.6.0` e `gocognit v1.2.1`:

| Unidade alterada de produção | Ciclomática | Cognitiva |
| ---------------------------- | ----------- | --------- |
| `executeOwnershipMutation`   | 9           | 8         |
| `succeedOwnership`           | 5           | 4         |
| `prepareOwnershipDeparture`  | 3           | 1         |

Testes novos também ficam em até 10 nas duas métricas após separar enumeração
de tabelas e execução de cenários. `removeOwnershipMembership` tem complexidade
cognitiva 11 preexistente e não foi alterada. SRP/smells: PASS no delta.
Formatter: PASS (gofmt). Lint: PASS (golangci-lint, zero issues).
Vet/typecheck Go: PASS. Build chat-service: PASS.
SonarQube: NÃO EXECUTADO; ferramenta indisponível para este agente.

## Security Review

Resultado: APROVADO, zero vulnerabilidades novas confirmadas. Revisão separada
da revisão de qualidade. Threat model: identidade e scope autenticados entram
na autorização; a seleção e a promoção são writers internos. A view filtra
workspace, tipo e conversa, e todas as entradas SQL usam parâmetros. A consulta
roda após autorização sob lock da conversa e isolamento SERIALIZABLE; conflitos
reiniciam todo o fluxo para impedir autorização e candidatura obsoletas.
Ausência de sucessor falha fechada. Guests não recebem promoção automática.
Remoção de OWNER continua proibida pelas regras existentes. Guards deferred
impedem commit de estado órfão em SQL direto.

Scanners: PASS (gosec incluído no golangci-lint). Nenhuma dependência mudou.
A revisão local não equivale a um pentest de toda a aplicação.

## Git/CI e limitações

- `git diff --check`: PASS.
- Gate global `make ci`: FAIL na inicialização do pnpm
  (`ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY`), antes dos checks. Os links de
  dependências temporários foram removidos e `pnpm install --frozen-lockfile`
  passou, sem alterar o lockfile.
- Gate equivalente `pnpm run ci` (comando chamado pelo Makefile): FAIL no
  threshold de cobertura do chat-service: 85,2%, exigência 90%. Formato, lint,
  tipos, infra, migrations, release, testes Go e cobertura web passaram antes
  desse gate. Os builds globais posteriores não foram executados.
- Comparação de cobertura com a base oficial: PASS (medição), usando somente
  os pacotes não-cmd do chat-service e o mesmo modo atomic do checker.
  Base `ff4c329`: 10.346/12.216 statements cobertos, 84,7%.
  Branch #1046: 10.418/12.232, 85,2% (+0,5 ponto percentual arredondado).
  O threshold de 90% já falha na base. Não foi reduzido o threshold nem
  expandido o escopo para corrigir essa condição preexistente.
- Commit: `feat(chat): add deterministic owner succession` (entrega local).
- Push/PR/merge: não solicitados; parar após o commit.
- EXPLAIN (ANALYZE, BUFFERS, VERBOSE) da query nova: PASS, grupo e canal.
  O plano usa filtros por workspace/conversa e os índices existentes
  `idx_dm_members_conversation_active`, `idx_dm_conversations_active`,
  `channel_members_pkey`, `workspace_members_pkey` e `users_pkey`. A chave
  de ordenação é CASE role, `p.joined_at`, `p.user_id` UUID. Execução: 0,389 ms
  (grupo), 0,240 ms (canal) nas fixtures pequenas. Não há evidência que
  justifique índice novo; estas medições não equivalem a teste de carga.
- Coverage: PASS, medição acima; outras famílias PostgreSQL sem suas variáveis
  próprias foram puladas pelo harness e não contam como evidência de integração.

As execuções iniciais expuseram erros nas fixtures (role de remoção, tipos e
argumentos do mock), corrigidos antes da validação final. Registrar somente a
execução final como evidência de aceite. A família PostgreSQL não é executada
automaticamente pelo gate global e deve ser considerada separadamente.

## Reprodução

```sh
# Retomar o container dedicado preservado nesta máquina:
docker start nchat-1046-postgres

# No módulo services/chat-service, com o banco dedicado disponível:
OWNERSHIP_TEST_DATABASE_URL=postgres://postgres:ownership1046@127.0.0.1:5546/ownership_953_test \
  go test -race -coverprofile=/tmp/nchat-1046-storage.cover ./internal/storage -count=1

# No root da worktree, após instalar as dependências pelo lockfile:
pnpm run ci
```

Logs locais desta execução: `/tmp/nchat-1046-storage-test.log`,
`/tmp/nchat-1046-targeted-final.log`, `/tmp/nchat-1046-ci.log`,
`/tmp/nchat-1046-ci-final.log`, `/tmp/nchat-1046-explain.log` e
`/tmp/nchat-1046-base-service.log`. O perfil de comparação da base está em
`/tmp/nchat-1046-base-service.cover`; o perfil do gate da branch está em
`coverage/go/services_chat-service.threshold.out`.
O script EXPLAIN foi extraído da query de produção, sem analisar queries
preexistentes. As credenciais acima pertencem apenas ao banco descartável.

Limitação de entrega: CI global permanece FAIL pelo threshold de cobertura
preexistente. Build direcionado do chat-service, lint, vet, formatter,
PostgreSQL e race detector estão PASS; os builds globais após o gate de cobertura
são NÃO EXECUTADOS. Nenhuma alteração fora da #1046 foi feita para esconder
a falha. A worktree temporária de comparação da base foi removida após a medição.

## Correção do runner de segurança após publicação

O CI remoto da primeira publicação passou nos testes, integração, coverage,
race detector, E2E e builds, mas Govulncheck falhou ao baixar
`https://vuln.go.dev/index/modules.json.gz`: `connection reset by peer`.
O agregador `CI / Required` reprovou corretamente por depender desse scan.

O runner agora faz até três tentativas com espera de 10 e 20 segundos,
exclusivamente para erros transitórios identificados no download da base de
vulnerabilidades. Cada tentativa sobrescreve o relatório anterior. Exit 3
(achados) continua sendo avaliado pelo gate existente; erros de argumentos,
carregamento e falhas persistentes continuam reprovando. Nenhum gate ou advisory
foi desabilitado e o agregador não foi alterado.

Validação: PASS nos 15 testes do gate/runner e nos 12 testes do agregador;
PASS em `bash -n`, ShellCheck e `git diff --check`. Dois testes de comportamento
cobrem recuperação com relatório novo e preservação de erro/achados, incluindo
o esgotamento das três tentativas. As esperas são substituídas apenas na fixture.
Code Quality Review: helper coeso, fluxo limitado e sem alteração de contrato.
Security Review: retries restritos ao download, veredicto preservado e falha
fechada após esgotamento. A correção não altera código Go ou dependências.

Scan real com o runner final: PASS em todos os nove módulos, com
`Go vulnerability gate passed.` (log `/tmp/nchat-1046-govulncheck-local-fixed.log`).
