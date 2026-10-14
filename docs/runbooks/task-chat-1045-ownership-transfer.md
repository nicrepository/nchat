# Task #1045 — transferência idempotente de propriedade

## Entrega

- Branch: `feature/chat-1045-ownership-transfer`.
- Base: `upstream/develop`, commit `6978d38`.
- Commit da entrega: `feat(chat): add idempotent ownership transfer`.
- Rota existente: `POST …/ownership/transfer`; body estrito com `new_owner_user_id` e `actor_new_role=admin|member`. Actor e workspace são resolvidos pelo servidor.
- O executor de transfer reutiliza a policy da #1044 dentro da transação `SERIALIZABLE` e do lock de conversa existentes. Requests novos revalidam recurso privado/ativo, workspace ativo, participantes elegíveis e autoridade OWNER. Transferência para si próprio é rejeitada.
- Promove o target antes de rebaixar o actor. Quando o target já é OWNER, escreve apenas o rebaixamento. Roles, audit, outbox e request idempotente permanecem na mesma transação.
- Replay precede a autorização por role, preserva a resposta persistida e não repete efeitos. Reutilização da key com target ou role diferentes gera conflito.
- Nenhum endpoint, migration, UI ou comportamento novo de realtime, sucessão, leave ou transfer-and-leave foi introduzido.

Arquivos alterados: `ownership_store.go`, `ownership_transfer_store.go`, `ownership_transfer_store_test.go`, `ownership_transfer_postgres_test.go`, `ownership_postgres_test.go`, `ownership_handler_test.go` e este relatório.

## Testes

| Evidência                                                                                                                                                                    | Resultado                                                               |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| Matriz PostgreSQL: target MEMBER/ADMIN/OWNER × actor ADMIN/MEMBER, em grupo e canal privados                                                                                 | PASS                                                                    |
| Roles dos dois participantes, contagens de audit/outbox/requests e ausência de órfãos                                                                                        | PASS                                                                    |
| Replay equivalente após rebaixamento; conflito por target e role; efeitos inalterados                                                                                        | PASS                                                                    |
| HTTP: key obrigatória, JSON estrito, rejeição de actor/workspace extras e role inválida                                                                                      | PASS                                                                    |
| Revalidação após remoção da membership; self-transfer, actor sem autoridade, workspace incorreto/desativado, recurso arquivado/público, target suspenso e rollout desativado | PASS                                                                    |
| Erro PostgreSQL no update do actor após promoção do target: rollback de roles, audit, outbox e request                                                                       | PASS                                                                    |
| Transfer/remove: estados finais correspondem a uma ordem serial, sem órfãos                                                                                                  | PASS                                                                    |
| Dois transfers simultâneos do mesmo actor: um vencedor, um request persistido, roles verificadas                                                                             | PASS                                                                    |
| Mock: `40001` e `40P01` causam nova transação e nova leitura da autorização                                                                                                  | PASS                                                                    |
| Departure, transfer-and-leave e demais testes existentes dos pacotes afetados                                                                                                | PASS                                                                    |
| Race detector nos pacotes storage, HTTP e domain, com PostgreSQL real                                                                                                        | PASS                                                                    |
| E2E/Playwright e QA manual                                                                                                                                                   | N/A — delta exclusivamente no backend, comprovado por HTTP e PostgreSQL |

Banco descartável: `nchat-1045-postgres`, porta local `5545`, database `ownership_953_test`. Os testes recriam os schemas desse banco; não usam o ambiente de desenvolvimento compartilhado.

Comandos de referência, em `services/chat-service` (definir `OWNERSHIP_TEST_DATABASE_URL` para o banco descartável):

```sh
go test ./internal/storage ./internal/http -run 'TestOwnershipTransfer|TestOwnershipConcurrentTransfers|TestOwnershipHandlerStrict' -count=1
go test -race -coverprofile=/tmp/nchat-1045-coverage.out ./internal/storage ./internal/http ./internal/domain -count=1
go vet ./internal/storage ./internal/http
go build ./...
golangci-lint run --config ../../.golangci.yml ./internal/storage/... ./internal/http/...
```

## Code Quality Review

Resultado: **APROVADO**, após simplificação dos testes. Nenhum achado relevante pendente; nenhum novo smell confirmado. Escopo: executor, dispatch, testes alterados e contratos reutilizados. Revisão realizada separadamente da revisão de segurança.

A primeira medição identificou matrizes de teste acima do limite cognitivo; foram separadas em runners e verificações com responsabilidades explícitas, preservando os cenários. Medição real com `gocyclo v0.6.0` e `gocognit v1.2.1`:

| Função alterada de produção  | Ciclomática | Cognitiva |
| ---------------------------- | ----------: | --------: |
| `executeOwnershipTransfer`   |           6 |         6 |
| `authorizeOwnershipTransfer` |           5 |         4 |
| `executeOwnershipMutation`   |           8 |         7 |

Funções novas de teste e funções de teste modificadas também atendem ambos os limites ≤10. Funções preexistentes fora do delta não foram refatoradas. SRP, coesão, tratamento de falha e reutilização das policies/transações foram revisados sem achados pendentes.

Formatter Go, lint (incluindo gosec), vet e build do serviço: **PASS**. Typecheck separado: **N/A** para o delta Go. SonarQube: **NÃO EXECUTADO**.

## Security Review

Resultado: **APROVADO**, zero vulnerabilidades confirmadas no delta. Escopo: autenticação e contexto no handler existente, autorização transacional, isolamento entre workspaces, replay, SQL e rollback.

Threat model: um cliente tenta forjar actor/workspace, transferir sem OWNER, usar participante inelegível, repetir requests ou disputar o rebaixamento. O handler rejeita campos extras; a policy carregada na transação decide acesso e autoridade; SQL usa parâmetros; o lock e retry completo protegem a disputa; fingerprint semântico e chave composta limitam replay ao contexto e à identidade autenticada. A resposta persistida contém resultado da mutação, sem perfis de participantes.

Scanners: **PASS** para gosec e demais analisadores executados via golangci-lint nos pacotes afetados. Semgrep dedicado: **NÃO EXECUTADO**. Nenhuma dependência foi alterada. Esta revisão não representa auditoria de segurança integral do repositório.

## Git/CI e limitações

- `git diff --check`: **PASS** antes do commit.
- Gate global oficial: **FAIL**, uma execução de `pnpm run ci` (exit 1): cobertura do chat-service **84,7%**, abaixo do limiar oficial de **90%**. A mesma medição isolada no commit-base `6978d38` deu **84,6%** (testes PASS; profile `/tmp/nchat-1045-base-coverage.out`). O déficit é preexistente, sem regressão percentual introduzida pelo delta. Log global: `/tmp/nchat-1045-ci.log`. Checks estáticos, infraestrutura, release safety, testes Go e frontend passaram antes da falha. Os builds globais não foram alcançados; o build separado do chat-service passou.
- Push, PR e merge: **NÃO EXECUTADO**, conforme escopo solicitado.
- Timeout de transporte após commit: **NÃO EXECUTADO**. Replay de uma operação realmente commitada comprova retry seguro, sem simular perda da resposta de transporte.
- A primeira execução das fixtures falhou por expectativas incorretas de role inicial/status e por tentar arquivar também o canal geral obrigatório. As fixtures foram corrigidas; os resultados finais devem ser considerados conforme a execução registrada acima.
- Cobertura final: **PASS** na suíte com race detector: storage **71,2%**, HTTP **91,9%**, domain **93,3%**. As duas funções novas de transfer têm **100%** de statements cobertos; a comparação global sem banco de integração foi **84,6% → 84,7%** para o módulo chat-service.

O gate global registrou três warnings preexistentes de React hooks no web, sem erros de lint. Os testes web passaram (233 arquivos, 6.420 testes) e admin passaram (43 arquivos, 536 testes). O gate global não foi repetido; a verificação da base executou somente a cobertura do chat-service. Depois do gate, as asserções HTTP foram extraídas para atender ao limite cognitivo; o teste HTTP direcionado e o lint final foram executados novamente.
