# #1042 — Policy local de roles e capabilities

## Resultado e escopo

Entrega preparatória conforme o plano aprovado. A policy pura atende grupos e
canais privados, reutilizando `ConversationRole` (`owner`, `admin`, `member`) de
`internal/domain/ownership.go`, já presente na base `upstream/develop`
`623d849b42674c5fa634b786c4239e79d2708a5f`. Nenhum commit foi importado da branch
de trabalho da #953. A implementação e este relatório pertencem exclusivamente
à branch `feature/chat-1042-conversation-role-capabilities`.

O contexto exige workspace ativo, conversa ativa, tipo suportado e roster
completo obtido pelo servidor em snapshot coerente. Actor e target são buscados
nesse roster. Identidade, workspace, conversa, membership ativa, acesso e roles
conhecidos são revalidados. Duplicatas ou participantes declarados acessíveis
com contexto inconsistente negam autorização. Role do workspace só participa
da elegibilidade de acesso; não concede autoridade local.

| Capability         | Decisão                                                                                                                                        |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| Assign role        | Somente OWNER; role pretendido conhecido; no-op negado; rebaixar OWNER exige outro owner ativo, inclusive em self-demotion.                    |
| Remove member      | Alvo diferente do actor; OWNER remove MEMBER/ADMIN, ADMIN remove MEMBER; OWNER nunca é removido.                                               |
| Add member         | Qualquer participante acessível com role válido; preserva #705. Elegibilidade dos convidados e validação do lote continuam no fluxo existente. |
| Transfer ownership | Elegibilidade de OWNER para outro participante acessível; nenhuma transferência executada.                                                     |
| Leave              | Estado final vazio ou com owner ativo; nenhuma sucessão automática.                                                                            |
| Edit metadata      | Acesso local e permissão da operação específica fornecida pelo servidor; ownership não amplia essa permissão.                                  |

Guests com acesso explícito mantêm add e podem receber promoção manual. A policy
não seleciona sucessores; os testes existentes de ownership continuam verificando
a exclusão de guests na sucessão automática legada.

## Evidências de validação

Executadas no checkout persistente `nchat-1042`, após restaurar os arquivos que
não persistiram em `/tmp` na interrupção de turno. Checks sobre a base sem os
arquivos novos foram descartados.

- `git diff --check`: passou.
- `gofmt`: aplicado aos dois arquivos Go.
- `go test ./services/chat-service/internal/domain -run TestConversationPolicy -count=1`: passou.
- `go test ./services/chat-service/internal/domain -count=1`: passou.
- Tabela principal: 39 cenários com expectativas explícitas, incluindo matriz
  actor/target, três roles pretendidos, self-demotion, remoção, leave, guests,
  último participante, contexto ausente, roles inválidos e fronteiras de acesso.
- Regressões existentes de serviço: passaram com
  `go test ./services/chat-service/internal/service -run 'Test(AddGroupParticipants|PermissionService_CanRead|ChannelService_GetChannelDetails_ReportsAdd|SearchGroupParticipantCandidatesChecksAccess|GetGroupDetailsChecksAccess)' -count=1`.
- `go vet ./services/chat-service/internal/domain`: passou.
- `golangci-lint run ./services/chat-service/internal/domain`: **0 issues**, versão 2.13.2.
- `go build -buildvcs=false -o /tmp/nchat-1042-chat-service ./services/chat-service/cmd/chat-service`: passou.
  O build padrão encontrou falha ao obter VCS status no ambiente; somente o
  stamping VCS foi desabilitado, sem mudança no código.
- Coverage de domínio: **93,2%**; todas as 11 funções/métodos de
  `conversation_policy.go`: **100% de statements**. Medido com `go test
./services/chat-service/internal/domain -coverprofile=/tmp/nchat-1042-domain.cover
-count=1` e `go tool cover -func=/tmp/nchat-1042-domain.cover`.
  Isso não equivale ao gate de coverage do serviço inteiro ou ao CI completo,
  que não foram executados nesta entrega de domínio.

Complexidade medida por `gocyclo` v0.6.0 e `gocognit` v1.2.1, respectivamente:

| Função/método novo                  | Ciclomática | Cognitiva |
| ----------------------------------- | ----------: | --------: |
| valid                               |           6 |         2 |
| accessible                          |           6 |         1 |
| eligibleConversationWorkspaceMember |           3 |         2 |
| participant                         |           8 |        10 |
| remaining                           |           5 |         6 |
| CanAssignConversationRole           |           7 |         3 |
| CanRemoveConversationMember         |           7 |         4 |
| CanAddConversationMember            |           1 |         0 |
| CanTransferConversationOwnership    |           4 |         1 |
| CanLeaveConversation                |           3 |         2 |
| CanEditConversationMetadata         |           2 |         1 |
| TestConversationPolicy              |           4 |         7 |
| conversationPolicyFixture           |           2 |         1 |
| conversationPolicyDecisions         |           2 |         1 |
| assertConversationPolicyBoundaries  |           7 |         6 |

`gocognit` omite funções com resultado zero. Comandos de medição:
`go run github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0` e
`go run github.com/uudashr/gocognit/cmd/gocognit@v1.2.1`, cada um aplicado aos
dois arquivos novos. Nenhuma função nova ultrapassa 10.

## Code Quality Review

**APROVADO no escopo preparatório.** Zero achados críticos, altos, médios ou
baixos. Revisão manual separada da medição de complexidade e do lint.

SRP preservado: a policy decide autorização sobre estado recebido; não consulta
storage, não executa writers, não serializa contratos HTTP e não seleciona
sucessores. Helpers concentram elegibilidade, localização de participantes e
contagem de owners. O modelo de role existente foi reutilizado. Não foram
identificados novos smells relevantes no diff. A busca percorre o roster em
O(n), com O(n) de memória para recusar identidades duplicadas; não é adequada
para avaliar uma lista inteira de participantes repetindo cada capability sem
considerar esse custo na futura integração.

Plano de correção: nenhum bloqueador no escopo. A integração futura deverá
fornecer roster completo e reutilizar as decisões, evitando duplicar checks.

## Security Review

**APROVADO no escopo preparatório.** Nenhuma vulnerabilidade confirmada no código
novo. Revisão manual de autorização e fronteiras de identidade/workspace,
separada da revisão de qualidade; lint inclui gosec. O domínio não adiciona
dependências; a atualização de segurança posterior está registrada abaixo.

A policy nega membership/acesso inválido, roles desconhecidos, workspace e
conversa divergentes, identidade ausente e duplicatas. Workspace ADMIN com role
local MEMBER não ganha gestão de roles; workspace MEMBER com role local OWNER
recebe autoridade local. Role pretendido é validado e não modifica estado.
Não há logs, consultas SQL, rede, segredos ou mass assignment nos arquivos novos.

Plano de correção: nenhum bloqueador no domínio puro. Na ativação, o servidor
deve derivar identidade, roster, `HasAccess` e `operationAllowed`; a policy não
pode receber capacidades do cliente como autoridade.

## Pendências e riscos residuais

Integração operacional **não concluída**. Os endpoints e writers existentes
mantêm seu comportamento, inclusive a sucessão legada. A nova policy não é
consumida por API/UI nesta entrega; esse critério da issue permanece pendente.
Persistência/ativação dos roles e validação transacional das mutações ficam para
as próximas tasks da epic. A policy não garante correção se o chamador fornecer
snapshot parcial, obsoleto ou estado autorizado pelo cliente.

Sem migrations, backfill, conversão automática de `created_by`, workspace roles
ou `moderator`, alterações HTTP/UI, realtime, transferência executável ou E2E.
Merge fora do escopo desta entrega.

## Correções durante o monitoramento do CI

- Governança: retirado o sufixo `(#1042)` do título da PR porque o checker
  proíbe `#` em títulos Conventional Commits. Título e subjects validados pelo
  checker do repositório; o novo CI aprovou governança.
- Static / Repository: relatório formatado com Prettier; check local e CI
  passaram.
- Security / Govulncheck: a base continha OpenTelemetry SDK 1.44.0 e exporter
  OTLP/HTTP 1.43.0, afetados por GO-2026-6505. O
  [advisory oficial](https://github.com/open-telemetry/opentelemetry-go/security/advisories/GHSA-8wmf-6v46-5gfg)
  indica que a versão 1.45.0 corrige exposição de configuração de exporters
  em logs internos Info, condicionada à instalação de logger verboso.
  SDK e exporter atualizados para 1.45.0, com sincronização de módulos e
  checksums dos consumidores via `go work sync`; Go permanece 1.25.13.
  O gate completo local `bash scripts/security/govulncheck.sh` passou após
  a atualização, sem nova exceção de segurança.

- `bash scripts/ci/go-test.sh`: passou para os nove módulos Go, após a
  atualização. Testes PostgreSQL condicionais dependem do ambiente de CI;
  a execução local não substitui os jobs de integração e coverage.
