# ClamAV #1129 — Entrega E: evidências locais

Execução: 2026-11-18. Base `be68f7f79f3e93c19752fa46a44db09d1d05662e`,
branch `feature/files-1129-clamav-capacity-isolation`, merge-base=origin/develop.
Dois arquivos untracked preexistentes preservados. Sem commit/push/PR/merge,
sem criação de branch, sem alteração de cluster/main/nchat-prod.

Design, alternativas A–E, threat model, transições, rollout/rollback e limitações:
[runbook E](../runbooks/clamav-capacity-isolation-1129.md).
Decisão duradoura: [ADR-0005 proposta](../adr/0005-clamav-capacity-isolation.md).
Recursos/renderização: [candidato inativo](../../infra/k8s/candidates/clamav-capacity-isolation/README.md).
O overlay ativo não mudou. Guardian candidato permanece replicas=0/updater suspenso.

## Resultado verificável — implementação original (herdado nesta consolidação)

| Teste/check                     | Comando                                                                                                                 | Resultado                                                                                | Evidência local                         |
| ------------------------------- | ----------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- | --------------------------------------- |
| Preflight                       | pwd/hostname/status/branch/rev-parse/merge-base                                                                         | Branch correta, HEAD igual a origin/develop; untracked preservados                       | SHA acima; hostname omarchy             |
| Unitários file-service          | `go test ./services/file-service/... -covermode=atomic -coverprofile=/tmp/nchat-1129-e-unit.out`                        | PASS, integrações opt-in ausentes não contam como executadas                             | `/tmp/nchat-1129-e-unit.log`            |
| Race file-service               | `go test -race ./services/file-service/...`                                                                             | PASS; não inclui PG opt-in                                                               | `/tmp/nchat-1129-e-race.log`            |
| PostgreSQL C+E                  | `SCAN_QUEUE_COVERAGE_DIR=/tmp/nchat-1129-e-scan-coverage bash scripts/testing/scan-queue-postgres.sh`                   | PASS PG16 e PG17 reais, network=none, roles não superuser                                | `/tmp/nchat-capacity-tests.log`         |
| Assinaturas B                   | `bash scripts/testing/clamav-signatures-isolated.sh` com profile e segunda imagem pinada                                | PASS, clamd real, expiração/corrupção/upgrade e cadeia de persistência                   | `/tmp/nchat-1129-e-signatures.log`      |
| Eficácia HTTP B/C/E             | `bash scripts/testing/clamav-efficacy-isolated.sh`                                                                      | PASS PG16/17, benigno/EICAR em canal/DM/grupo, original íntegro e negação antes de clean | `/tmp/nchat-1129-e-efficacy.log`        |
| Storage/URL PG coverage         | `bash scripts/ci/link-safety-postgres-coverage.sh services/file-service /tmp/nchat-1129-e-link.out` com DSN descartável | PASS, PG17 próprio sem rede/portas publicadas                                            | `/tmp/nchat-1129-e-link.log`            |
| Cobertura módulo                | merge oficial awk + `go tool cover -func=/tmp/nchat-1129-e-merged.out`                                                  | **90,9%**, threshold 90% respeitado no módulo alterado                                   | `/tmp/nchat-1129-e-coverage.txt`        |
| Formatação, vet, lint Go        | `make ci-static-go`                                                                                                     | PASS em todos os módulos, zero issues                                                    | `/tmp/nchat-1129-e-static-final.log`    |
| Config operacional              | `make ci-infra-config`                                                                                                  | PASS                                                                                     | `/tmp/nchat-1129-e-infra-config.log`    |
| Kubernetes                      | `make ci-infra-kubernetes`                                                                                              | PASS, schemas/policies/sealed secrets e candidato E                                      | `/tmp/nchat-1129-e-kubernetes.log`      |
| Regressões config E             | `python3 scripts/ci/test_file_worker_isolation.py`                                                                      | PASS, render e seis mutações inseguras recusadas                                         | Incluído no gate Kubernetes             |
| Migrations                      | `make ci-infra-migrations`                                                                                              | PASS                                                                                     | `/tmp/nchat-1129-e-migrations.log`      |
| Release safety                  | `make ci-infra-release-safety`                                                                                          | PASS, fixtures/stubs locais; não executa rollout                                         | `/tmp/nchat-1129-e-release.log`         |
| Arquitetura CI                  | `make ci-architecture-check`                                                                                            | PASS                                                                                     | `/tmp/nchat-1129-e-architecture.log`    |
| Playwright existente            | `pnpm --dir apps/web exec playwright test e2e/messaging/enterprise-files.spec.ts --project=chromium --retries=0`        | **12 PASS**; API de browser mockada, não prova scanner                                   | `/tmp/nchat-1129-e-playwright.log`      |
| Build                           | `go build ./services/file-service/cmd/...`                                                                              | PASS                                                                                     | `/tmp/nchat-1129-e-build.log`           |
| Segurança IaC                   | `make security-trivy-config`                                                                                            | PASS, zero HIGH/CRITICAL                                                                 | `/tmp/nchat-1129-e-trivy.log`           |
| Segurança candidato renderizado | `trivy config --severity HIGH,CRITICAL --exit-code 1` nos workloads/policies/SAs renderizados                           | Zero HIGH/CRITICAL; não confundir patches parciais com recursos completos                | `/tmp/nchat-e-trivy-rendered-final.log` |
| Whitespace                      | `git diff --check`                                                                                                      | PASS                                                                                     | Execução local final                    |

Logs e perfis acima ficam fora do Git; não são artefatos permanentes de CI.
O inventário SHA256 acompanha este relatório e identifica o conteúdo entregue.
Não registrar secrets/DSNs reais/conteúdo de clientes. Todos os bancos usados
foram criados e removidos em containers próprios. Não foram acessados bancos
compartilhados. Fixtures de assinaturas usam somente snapshots oficiais pinados.

## Cobertura e qualidade

Profiles medidos: unitários atuais, integrações storage/URL, ClamAV/assinaturas e
PG16/17 C+E. Merge usa `scripts/ci/merge-go-coverprofiles.awk`; `cmd` é excluído
conforme a regra existente. Não foram reduzidos thresholds, alterados ignores,
excluídos novos arquivos ou mascarados skips. Esse resultado é do módulo
file-service; **não é execução do gate completo make go-coverage-check dos nove
módulos**. Nenhuma cobertura de PL/pgSQL é inferida de coverage Go.

Gocyclo/Gocognit executados sobre código Go tocado e testes novos. Máximos das
funções novas/alteradas: ciclomática **10**, cognitiva **10**, incluindo testes.
Na implementação original, `Shutdown` preexistente conservava 13/20. A correção
pós-review abaixo reduz essa função para 10/10. SQL/Python não tiveram
métricas de complexidade por SonarQube; nenhuma análise SonarQube foi executada.
Não há declaração de Quality Gate Sonar PASS nem de review independente.

Falhas preliminares corrigidas antes dos resultados finais: fixture de scan-worker
sem idade B positiva (startup corretamente recusado); método closePool tornou-se
morto após cleanup centralizado (removido); dois Rollback não tratados em testes
(corrigidos); teste novo acima de complexidade10 (dividido). O harness temporário
de cobertura URL foi corrigido para aguardar startup final do PG, usar nullglob
nos schemas opcionais e nome de banco terminado em `_test`; bancos/arquivos
próprios foram removidos. Nenhuma falha foi convertida em PASS por retry de teste.

## Contratos exercitados e limites da prova

- Papéis API/scan/preview, negação de modos inválidos, credenciais mínimas,
  ausência de consumidores/rotas indevidos, startup falho e cancelamento inicial.
- Oito consumidores concorrentes, teto global2, redução para1, release terminal,
  reserva após timeout/cancelamento/storage/unavailable, crash, remoção,
  recuperação, token antigo recusado e rollback de claim/veredito.
- Upgrade com C ativa impõe admit_after futuro (4080s), grants não permitem
  bypass, snapshot REPEATABLE READ não admite acima do teto, policy ausente falha
  fechada e down após ativação é recusado.
- Testes B/D existentes exercitam engine real e persistência, mas storage HTTP
  E2E é fixture cifrada em memória; Playwright usa mocks. Nenhum deles prova
  enforcement CNI, custo por formato ou reinício real de Pods.
- Compatibilidade N/N+1 é preservação/teste da assinatura SQL e fencing, não
  homologação operacional com dois releases implantados.

## Aceite e pendências

| Grupo de critérios                                       | Estado                                                                                                        |
| -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Topologia, boot modes, papéis e migrations externas      | Implementados/testados localmente                                                                             |
| Concorrência local/global, claims, fencing, backpressure | Implementados/testados PG16/17                                                                                |
| Shutdown/cancelamento e recovery de lease/reserva        | Testados localmente; kill/rolling de Pods pendente                                                            |
| Manifests, recursos, SAs, rede, probes, digest único     | Candidato validado localmente; CNI/quota/enforcement pendentes                                                |
| B/C/D e fail-closed                                      | Contratos preservados e regressões locais PASS; ativação B/C não inferida                                     |
| Métricas e autoscaling                                   | Famílias D preservadas; discovery Kubernetes pendente; capacidade fixa entregue, autoscaling não implementado |
| N/N+1, rollout e rollback                                | Documentados/contratos locais; execução operacional pendente                                                  |
| Coverage/lint/build/IaC/segurança                        | Resultados locais acima; revisão independente ainda requerida                                                 |
| Capacidade/SLO/benchmarks e eficácia em nchat-dev        | Não verificados                                                                                               |

Bloqueios **preexistentes**: SEC-B-001/002/007, Guardian/idade/policy B não aprovados,
cutover C não comprovado, configuração operacional raw clamd incompatível com
NewGuarded. **Ambiente**: kubectl sem contexto; nenhuma autorização de rollout;
RWX/CNI/quotas/collector não homologados. **Delta**: reserva e pausa de expansão
reduzem disponibilidade de processamento após falha; requerem janela planejada.
**Entrega F**: classificação por custo/inspeção estrutural, sem novos detectores.

Estado na implementação original: candidata concluída e validada localmente;
validação operacional pendente. Apta para Code Quality Review e Security Review independentes,
com os bloqueios explícitos. **Não pronta para merge/rollout** sem reviews,
resolução das dependências aplicáveis e aprovação humana.

## Consolidação pós-Code Quality e Security Review

As aprovações dos dois reviews independentes foram informadas pelo usuário,
sem P0/P1 ou Critical/High/Medium. Os textos/inventários completos desses reviews
não foram disponibilizados neste checkout; não se declara nova revisão independente
nem aprovação dos ajustes abaixo por aqueles revisores.

### Preflight e reconciliação antes de editar

Repositório `/home/alvaroneto/Projects/nchat`, hostname `omarchy`, branch e HEAD
iguais ao início deste relatório; `origin/develop` e merge-base no mesmo SHA.
Nenhum arquivo staged. Inventário comparado ao conjunto completo retornado por
`git diff HEAD --name-only -z` e `git ls-files --others --exclude-standard -z`,
excluindo somente os dois arquivos preexistentes identificados abaixo e o próprio
manifesto. Todos os 23 hashes foram verificados **antes** das alterações.

| Contagem                                    | Preflight atual | Code Quality informado | Security informado |
| ------------------------------------------- | --------------- | ---------------------- | ------------------ |
| Entradas do manifesto, sem auto-inclusão    | 23              | 22                     | 23                 |
| Tracked modificados                         | 7               | não discriminado       | não discriminado   |
| Untracked da Entrega E, incluindo manifesto | 17              | 16                     | 17                 |
| Total do delta E, incluindo manifesto       | 24              | 23 inferidos           | 24 inferidos       |

O conjunto atual é completo: zero faltantes, zero entradas extras e nenhuma
entrada duplicada. O manifesto não inclui seu próprio hash. Portanto a diferença
de um arquivo do Code Quality **não é omissão no inventário atual**. Sem a lista
ou manifesto utilizado pelo revisor, não é possível identificar qual arquivo foi
contado diferentemente nem distinguir snapshot anterior de erro de contagem.
Não se atribui a diferença a um arquivo hipotético. A lista foi solicitada para
reconciliação histórica, sem bloquear correções independentes.

SHA256 do manifesto recebido antes desta rodada:
`35feacd79bb5ae6e5b626a59c149d916506e306df00ce1b5065da88a5846600e`.

Untracked preexistentes preservados, fora do inventário E e sem staging:

| Nome literal na raiz                                          | Bytes | SHA256                                                             |
| ------------------------------------------------------------- | ----- | ------------------------------------------------------------------ |
| `'`                                                           | 17499 | `58e245fe68fbb2e6c34c061a796c031a29d538158549c13438b63c52e82bb952` |
| `t(files): handle guardian lifecycle errors in ClamAV tests"` | 6367  | `26614cfe743c56c21eeba339d2d89170b1138abff7ad07ad7adb677807cfc3c7` |

### Ajustes pequenos após os reviews

- **P3 conversor:** `Config.Validate()` valida o conversor somente para
  preview-worker antes de qualquer abertura de PG. Reutiliza `converter.NewClient`,
  que valida localmente sem I/O; não duplica regras de URL/timeout. Mantidos os
  defaults carregados de produção, fallbacks combined/API e tratamento defensivo
  de erro do construtor em wireWorker. Testes cobrem configuração inválida,
  ausência de abertura do pool, timeout de fronteira, default35 e inicialização
  válida do preview-worker existente.
- **P3 shutdown:** log genérico `worker_shutdown_incomplete` e mensagem de erro
  sem atribuição incorreta a preview. Dois helpers locais conservam ordem de
  fechamento pool→publisher, tracing com o mesmo contexto e execução mesmo quando
  shutdown incompleto. Cancelamento, canais, deadline, retenção de recursos e
  shutdownOnce permanecem intactos. Teste lê o log JSON para scan-worker e confirma
  que o pool não é fechado; testes existentes de ordem e idempotência continuam.
- **Low starvation:** runbook explicita ausência de fairness, interferência entre
  workspaces e reserva780s com capacidade inicial1. Mitigação requer evolução
  separada, benchmark e validação multi-tenant. Nenhuma alteração de max_scans,
  TTL, migration000022, claim, fencing, reserva ou agendamento.

Arquivos com conteúdo alterado nesta rodada (comparação com o snapshot recebido):

1. `services/file-service/internal/config/config.go`
2. `services/file-service/internal/config/boot_mode.go`
3. `services/file-service/internal/config/boot_mode_test.go`
4. `services/file-service/internal/app/app.go`
5. `services/file-service/internal/app/worker_test.go`
6. `docs/runbooks/clamav-capacity-isolation-1129.md`
7. `docs/observability/clamav-1129-delivery-e.md`
8. `docs/observability/clamav-1129-delivery-e.sha256` (hashes atualizados)

Nenhum arquivo novo foi acrescentado ao conjunto. worker.go e converter.Client
foram inspecionados e preservados. Manifests ativos/candidatos, SQL, grants,
pipelines e produção não foram alterados nesta consolidação.

### Checks reexecutados após os ajustes

| Teste/check                    | Comando                                                                                                                                 | Resultado                                                          | Evidência local                                                               |
| ------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ | ----------------------------------------------------------------------------- |
| Unitários relacionados         | `go test ./services/file-service/internal/config ./services/file-service/internal/app ./services/file-service/internal/converter`       | PASS                                                               | `/tmp/nchat-e-postreview-unit.log`                                            |
| Unitários módulo e perfil novo | `go test ./services/file-service/... -covermode=atomic -coverprofile=/tmp/nchat-e-postreview-unit.out`                                  | PASS                                                               | `/tmp/nchat-e-postreview-module.log`                                          |
| Race relacionado               | `go test -race ./services/file-service/internal/config ./services/file-service/internal/app ./services/file-service/internal/converter` | PASS                                                               | `/tmp/nchat-e-postreview-race.log`                                            |
| Estática Go                    | `make ci-static-go`                                                                                                                     | PASS, zero issues nos módulos                                      | `/tmp/nchat-e-postreview-static.log`                                          |
| Complexidade Go                | `gocyclo` e `gocognit` nos arquivos Go afetados                                                                                         | Funções novas/alteradas ≤10/10; Shutdown10/10, Config.Validate10/9 | `/tmp/nchat-e-postreview-gocyclo.log`, `/tmp/nchat-e-postreview-gocognit.log` |
| Candidato Kubernetes           | `python3 scripts/ci/test_file_worker_isolation.py`                                                                                      | PASS, candidato inativo e seis mutações recusadas                  | `/tmp/nchat-e-postreview-kubernetes.log`                                      |
| Cobertura consolidada módulo   | merge awk oficial + `go tool cover -func=/tmp/nchat-e-postreview-merged.out` + assert threshold90                                       | **90,9%**                                                          | `/tmp/nchat-e-postreview-coverage.log`                                        |
| Formatação                     | gofmt dos arquivos Go; Prettier dos documentos afetados                                                                                 | PASS                                                               | Execução local                                                                |
| Integridade final              | `git diff --check`, comparação de conjuntos e `sha256sum -c docs/observability/clamav-1129-delivery-e.sha256`                           | PASS, 23 entradas + manifesto, sem omissões                        | Verificação local final                                                       |

Cobertura: unitários do módulo reexecutados; perfis PG16/17 C+E, URL/storage PG e
assinaturas ClamAV **herdados da implementação anterior**, pois seus arquivos de
produção não mudaram. Resultado90,9 não é nova execução de integração PG/engine
nem do gate completo dos nove módulos. Unitários atuais cobrem Validate100%,
validatePreviewConverter100% e Shutdown100%. Não houve redução de threshold,
ignore, supressão ou análise SonarQube.

PG16/17 real, EICAR/Guardian, Playwright, Trivy e gates extensos de infraestrutura
da tabela original não foram repetidos nesta rodada: não houve mudança de SQL,
scanner, manifests ou frontend. Não confundir resultados herdados com reexecutados.

### Riscos preservados e revalidação direcionada

Confirmados no runbook: role PG `nchat_app` e keyring compartilhados, ausência de
autorização SQL/criptográfica por papel, autoscaling ausente, reserva780s e pausa
4080s, CNI/RWX/Guardian e descoberta Prometheus pendentes, nenhum benchmark
operacional, candidato inativo e dependências B/C intactas.

Code Quality deve revalidar os dois P3, helpers de shutdown e regressões de
configuração/inicialização. Security deve revalidar o log/retenção de recursos,
a antecipação da validação e o registro Low de starvation; não houve mudança
nos contratos de autorização, SQL, rede ou retenção que justificasse reabrir
suas decisões arquiteturais nesta rodada.

Aprovações anteriores se referem ao snapshot revisado, não automaticamente a estes
ajustes. Entrega E permanece **candidata inativa, sem commit e sem staging**.
Nenhuma autorização de merge, PR, push, rollout ou alteração no GitHub foi emitida.
Revalidação direcionada dos ajustes e pendências operacionais continuam explícitas.
