# Entrega C — evidencias locais e handoff (#1129)

**Atualizacao:** a secao "Remediacao final C" ao final consolida os pareceres
e substitui o snapshot/resultados anteriores quando indicado.

Data: 2026-11-09. Baseline/HEAD: `9567adf7471fd427630df749bef40ddd2e39920a`.
Branch: `feature/files-1129-scan-queue-fencing`. Trabalho local, sem commit,
push, PR, merge ou deploy. Nao representa aprovacao independente nem encerramento
integral da issue #1129. [Arquitetura](scan-queue-fencing-1129.md) e
[matriz/operacao futura](../runbooks/scan-queue-fencing-1129.md).

## A. Arquitetura anterior

Fila na linha de attachments, enfileiramento atomico no MarkUploaded, claim
SKIP LOCKED, contador saturado, lease/backoff juntos em scan_next_attempt_at.
Um job por tick de 10s limita admissao de jobs rapidos. Resultado condicionado
somente por status nao determina a tentativa legitima. Uma rejeicao velha podia
alterar um clean mais novo. Evidence B nao substituia identidade do claim.
A fixture de preparacao reproduz a permissao legada antes do cutover.

## B. Implementacao

UUID por tentativa, lease/elegibilidade separados, resultado e retry atomicos,
API v2 protegida por policy de cutover no banco. Consumidores sincronicos por
slot, drain limitado e cooldown compartilhado. Default 1; nenhum tuning de
scanner/replicas ou operacao em cluster. Objetos cifrados, streaming, verificacao
de EOF, formatos, autorizacao e gates de acesso permanecem.

## C. Fencing

Cada claim troca UUID no PostgreSQL. Lock da linha verifica workspace, estado,
exclusao, token e lease antes da escrita. Novo claim invalida A; retry libera a
autoridade anterior; crash recupera por deadline. Token nao e scan_attempts.
Mesmo contador 32000 produz UUID novo. Resultado velho recebe NC005/superseded,
sem veredito, Evidence, preview, agenda ou evento. Replay reconhecido nao publica
outra transicao. Lease e valido no ponto de linearizacao apos lock, conservado
ate commit; sem conexao longa durante I/O.

## D. PostgreSQL

000021 up/down novas; 000020 inalterada. Banco entra com C desativada. Funcao B
privada + wrapper legado preservam assinatura antiga na preparacao. APIs C
exigem token; triggers e wrapper fecham o legado depois do cutover. Role runtime
nao e superuser, nao herda migrator, nao altera policy nem Evidence diretamente.
Regrants preservam essas restricoes. Search path/catalogo nao aceitam sombra de
pg_temp; um teste cria tabela temporaria pg_proc para provar a recusa.

PG16.15 e PG17.11, imagens pinadas ja usadas pela suite B, containers proprios
network=none, migrations aplicadas como nchat_migrator e escritas como nchat_app.
EXPLAIN em 10.000 linhas sinteticas (10% leased): Index Scan no indice existente,
LockRows/Limit, sem Sort. Captura PG16 0,034ms; PG17 0,031ms. Nao e benchmark
operacional. Up/down antes de cutover passa; down/disable apos ativacao e recusado.

## E. N/N+1

Matriz completa no runbook. Antes do cutover, N funciona e N+1 espera; C nao
promete protecao enquanto desligada. Depois, N+1 funciona com B desligada ou
ligada (neste caso exigindo Evidence); N e recusado. Workers mistos devem ser
drenados antes da ativacao para conservar disponibilidade. Down so e permitido
antes da primeira ativacao. Rollback para N depois do corte e fail-closed, sem
capacidade de processar fila; preservar schema e usar binario com v2.

## F. Claims e retries

Lease default 330s; timeout 300s; escrita ate 10s. Retry equal jitter por UUID,
primeira falha 10–20s, teto 1320–2640s; teste deterministico de piso/teto/spread.
Erro permanece pending. Retry velho nao altera o claim novo. Banco indisponivel
mantem recuperacao por lease. Cooldown local 10s tambem limita admissao de jobs
novos, evitando descarregar backlog inteiro no daemon indisponivel.

## G. Drain e concorrencia

1–4 slots validados, default 1 e headroom PGX. Um consumidor so reivindica ao
poder executar. Rodadas de ate 32 jobs ou 1s entre conclusoes, yield de 100ms;
idle/erro aguarda 10s. Sem LISTEN/NOTIFY ou conexao PG retida. WaitGroup aguarda
consumidores no shutdown. Testes com canais medem maximo 2 slots configurados,
sem claims extras, goroutines ativas apos retorno ou aprovacao apos cancelamento.

## H. Testes adversariais

| Cenario                                                                     | Evidencia local                                                                                       |
| --------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| Dois claims e SKIP LOCKED                                                   | Um vencedor; linha explicitamente bloqueada e pulada                                                  |
| Lease expira/reclaim                                                        | UUID novo; token anterior nunca escreve                                                               |
| A clean/rejected antes/depois de B                                          | Ambos recusados, inclusive B final clean ou rejected                                                  |
| Evidence valida de A                                                        | Nao supera fencing, coluna forense preservada                                                         |
| Reinicio                                                                    | Nova instancia de store recusa token antigo                                                           |
| Commits concorrentes                                                        | Transacoes distintas; pg_blocking_pids confirma A bloqueado antes de commit de B                      |
| Rollback de claim                                                           | Token gerado no rollback nao e reutilizado; A expirado nao recupera autoridade                        |
| Retry obsoleto e backoff                                                    | Sem reprogramar B; prazo observado, parametros fora dos limites recusados                             |
| Exclusao concorrente                                                        | Row lock serializa exclusao e resultado, sem ressuscitar arquivo                                      |
| Politica B                                                                  | Sem Evidence e recusado; com prova fresca persiste; rescan nao reutiliza prova antiga sem novo recibo |
| Idempotencia/ACK perdido                                                    | Replay nao reescreve resultado nem publica outro evento                                               |
| Cancelamento tardio                                                         | Scanner que ignora cancelamento e retorna clean nao aprova                                            |
| Resultado superseded e publisher                                            | Teste por canais confirma token A preservado e nenhum evento clean/rejected                           |
| Scanner/Guardian indisponivel, protocolo, tamanho, storage/decrypt, timeout | Suites existentes de service/scanner e ClamAV real passam; erros conservam quarentena                 |
| Publisher indisponivel                                                      | Suites existentes preservam veredito duravel e reconciliacao por leitura                              |
| Banco indisponivel                                                          | Doubles de falha de persistencia/retry; recuperacao real por lease provada no PG                      |
| Crash                                                                       | Abandono/expiracao deterministico do claim; nao houve kill de pod/cluster                             |
| Notificacao perdida                                                         | Nao existe dependencia de notificacao; polling consulta fila persistida                               |

Nao houve caos operacional, anexo real, download E2E contra ambiente compartilhado
ou homologacao de backlog misto em storage real. Casos de banco offline sao
injecoes no limite do store; nao representam desligamento real de PG durante commit.

## I. Benchmarks

Comando reproduzivel no runbook; Go1.26.10, Linux amd64, Ryzen 7 5825U, GOMAXPROCS16,
mesmo processor sintetico sem I/O nas duas modalidades, tres repeticoes:

| Scheduler             | Jobs por rodada | ns/op da rodada       | B/op / allocs/op |
| --------------------- | --------------- | --------------------- | ---------------- |
| Baseline, uma chamada | 1               | 3,451 / 3,318 / 3,438 | 0 / 0            |
| C, drain              | 32              | 1033 / 1012 / 1143    | 0 / 0            |

A medicao prova custo/limite do loop e nao throughput ClamAV. O teste com oito
jobs prova que nao se espera o poll entre eles. Nao se convertem ns/op em
jobs/min de producao. p50/p95/p99 de upload→liberacao, CPU/RSS/conexoes PG sob
scan real, arquivos grandes e backlog misto **nao foram medidos**. Protocolo de
homologacao posterior documenta dataset sintetico, comparacao equivalente,
coleta de recursos e fault injection. Default permanece 1 por essa limitacao.

## J. Observabilidade

Snapshots pending/eligible/leased limitados a uma consulta por 10s/processo;
active/capacity locais, claims_total e histogramas duration/wait/backoff.
scans_total conserva labels de resultado fechados, incluindo superseded e
retry_write_failed; cancelamento e categorizado nos logs. Nenhum id, workspace,
filename, storage key ou usuario e label. Nenhum conteudo e registrado. Sem
novo endpoint de detalhes das assinaturas ou dashboards D.

## K. Quality gates

| Comando                                                          | Resultado observado                                                                                          |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `make test-go`                                                   | PASS, nove modulos                                                                                           |
| `make ci-static-go`                                              | PASS, gofmt/vet/golangci-lint; nenhum lint/errcheck novo                                                     |
| `bash scripts/ci/go-test.sh -race`                               | PASS, nove modulos; complemento App apos ajuste de wiring tambem PASS                                        |
| `bash scripts/testing/scan-queue-postgres.sh`                    | PASS PG16/17, roles reais, up/down e plano SQL                                                               |
| `bash scripts/testing/clamav-signatures-isolated.sh`             | PASS, ClamAV real isolado e cadeia completa B                                                                |
| `make go-coverage-check` sem fixtures                            | FAIL por 83,9% do chat; skips de suites PG nao equivalem a execucao                                          |
| Mesmo gate com fixtures PG/B/C                                   | Uma execucao completa PASS; file-service 90,6%, todos os nove >=90%                                          |
| Repeticao completa de cobertura                                  | FAIL intermitente preexistente em notification-service, detalhado abaixo; file-service mantem 90,6%          |
| `make ci-static-repository`                                      | PASS, formato e contratos do repositorio                                                                     |
| `make ci-infra-migrations ci-architecture-check ci-config-check` | PASS dos scripts; actionlint/yamllint/gitlab-ci-lint ausentes e pulados pelo script oficial                  |
| `gocyclo` / `gocognit`                                           | Funcoes Go de producao novas/alteradas <=10; funcoes preexistentes fora do diff em App continuam acima de 10 |
| `make security-govulncheck`                                      | PASS com binario local v1.1.4 compilado por Go1.26.10; binario no PATH compilado por 1.25 nao suporta o repo |
| `make security-trivy-config`                                     | PASS, render oficial                                                                                         |
| `make security-trivy-fs`                                         | FAIL: CVE-2026-78661 HIGH em golang.org/x/net v0.60.0, preexistente; dependencias inalteradas                |
| `make security-secrets`                                          | FAIL: quatro achados historicos, abaixo; nao ignorados                                                       |
| Gitleaks sobre diff + todos os novos arquivos C                  | PASS, nenhum achado                                                                                          |
| `git diff --check`                                               | PASS                                                                                                         |

Cobertura completa que passou: platform 94,4%; admin 91,3%; auth 90,8%; chat
90,2%; document-converter 92,7%; files 90,6%; media 92,4%; notification 96,0%;
search 98,1%. Perfis PG/B/C sao mesclados pelo gate sem mudar threshold/exclusoes.
O novo ensaio PG pertence ao job Go Coverage existente; nenhum novo job duplicado.

Falha intermitente: TestNotificationClaimOrderIsDeterministicPostgreSQL ordena
resultados de claim no notification-service. Foi reproduzida em 16/20 repeticoes
com PostgreSQL descartavel e fontes do servico inalteradas. Nao foi corrigida
nesta entrega. Uma execucao verde nao apaga esse bloqueio de confiabilidade.

Achados Gitleaks sao generic-api-key no historico do arquivo
services/chat-service/internal/storage/notification_outbox_promotion_postgres_test.go,
linhas 80/396, commits 53cb7b52 e 1d18b8ee. Nenhum valor foi divulgado. Classificar
como historico nao significa aprovar/ignorar o achado. O diff C nao adiciona
segredos, dependencias, excecoes de scanner, configuracao de lint ou reducao de gate.

## L. Arquivos alterados

| Arquivo                                                               | Finalidade                                                             |
| --------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| `.github/workflows/ci.yml`                                            | Executar PG16/17 no owner de cobertura existente                       |
| `scripts/ci/go-coverage-check.sh`                                     | Mesclar os dois perfis C, recusar perfil ausente                       |
| `scripts/db/grant-runtime.sql`                                        | Preservar policy/API privadas em regrants                              |
| `migrations/files/000021_scan_queue_fencing.up.sql`                   | Expansao, APIs de claim/resultado/retry, ACLs, cutover                 |
| `migrations/files/000021_scan_queue_fencing.down.sql`                 | Reversao antes de ativacao, guarda depois dela                         |
| `scripts/testing/scan-queue-postgres.sh`                              | Runner PG16/17 proprio, sem DSN externo                                |
| `scripts/testing/scan-queue-plan.sql`                                 | Plano reproduzivel, 10.000 linhas e rollback                           |
| `services/file-service/internal/app/app.go`                           | Configurar slots e metrica de capacidade antes de iniciar consumidores |
| `services/file-service/internal/config/config.go`                     | Parsing/default/validacao da concorrencia e pool                       |
| `services/file-service/internal/config/config_test.go`                | Invalidos e headroom                                                   |
| `services/file-service/internal/http/attachment_metrics.go`           | Metricas minimas C                                                     |
| `services/file-service/internal/http/attachment_metrics_test.go`      | Slots, contadores, histogramas e nil receivers                         |
| `services/file-service/internal/service/attachment_service.go`        | Token nos inputs de resultado                                          |
| `services/file-service/internal/service/malware_scan_service.go`      | Propagar autoridade, cancelar com seguranca, limitar snapshots         |
| `services/file-service/internal/service/scan_retry.go`                | Backoff, retry fenced e cooldown local                                 |
| `services/file-service/internal/service/scan_retry_test.go`           | Jitter, falhas, canais, cancelamento, eventos e polling de metricas    |
| `services/file-service/internal/service/export_test.go`               | Avanco deterministico de cooldown/snapshot em testes                   |
| `services/file-service/internal/service/malware_scan_service_test.go` | Adaptar testes de recuperacao ao cooldown e snapshots                  |
| `services/file-service/internal/storage/scan_store.go`                | Consumir claim SQL v2 e retornar identidade                            |
| `services/file-service/internal/storage/scan_store_test.go`           | Contrato SQL movido para migration, args e token                       |
| `services/file-service/internal/storage/scan_attempt.go`              | Persistencia v2, retry e estatisticas                                  |
| `services/file-service/internal/storage/scan_attempt_test.go`         | Propagacao, erros e estado da fila                                     |
| `services/file-service/internal/storage/scan_queue_postgres_test.go`  | Adversariais reais com roles, locks e barreiras                        |
| `services/file-service/internal/storage/attachment_store.go`          | Roteamento com token para clean/rejected, mantendo compatibility seam  |
| `services/file-service/internal/storage/signature_approval.go`        | Classificar NC004/NC005                                                |
| `services/file-service/internal/storage/migration_invariants_test.go` | Comparar indice com SQL autoritativo da 000021                         |
| `services/file-service/internal/worker/scan_worker.go`                | Pool sincronico, drain, yield e espera idle                            |
| `services/file-service/internal/worker/scan_worker_test.go`           | Slots, shutdown, drain, logs e microbenchmark                          |
| `services/file-service/internal/worker/preview_worker.go`             | Separar construtor scan; outros loops preservados                      |
| `services/file-service/internal/worker/preview_worker_test.go`        | Adaptar matriz de construtores ao tipo Scan dedicado                   |
| `docs/api/file-attachments.md`                                        | Contrato atual da fila/vereditos                                       |
| `docs/architecture/scan-queue-fencing-1129.md`                        | Decisoes, invariantes e diagnostico                                    |
| `docs/runbooks/scan-queue-fencing-1129.md`                            | Matriz, rollout/rollback e homologacao futura                          |
| Este arquivo                                                          | Evidencias e handoff                                                   |

## M. Riscos residuais

- Codigo: callbacks legados existem apenas para preparacao; a seguranca depois
  do corte depende das guards SQL e da role runtime nao ser owner/superuser.
- Concorrencia: sem garantia de quotas por tenant; medir muitos leases na cabeca,
  replicas e daemon compartilhado. Tokens UUID assumem unicidade criptograficamente
  aleatoria e relogio PG confiavel; nao ha renovacao de lease.
- Seguranca: SEC-B-002 e achados globais continuam abertos; auto-revisao nao e
  Security Review independente. Runtime comprometido ja tem autoridade de scan;
  C nao autentica o recibo Guardian nem remove essa fronteira da B.
- Infraestrutura: capacidade, drift, clocks, pools, RWX e candidatos B nao foram
  homologados. Sem alteracao de Kubernetes, digest ClamAV, politica de frescor,
  NetworkPolicy, CronJob ou replicas.
- Operacao: cutover exige drenagem global e decisao aprovada; rollback depois dele
  exige binario v2. Polling conserva ate 10s de latencia inicial. Publisher e
  best-effort e nao ha metrica/benchmark E2E operacional nesta entrega.

## N. SEC-B-002

**Continua aberto.** Nenhuma PKI, mTLS, criptografia improvisada de Evidence ou
remediacao de autenticidade do Guardian foi introduzida. A entrega C nao foi
apresentada como remediacao desse achado e nao autoriza ativar B.

## O. Estado Git

HEAD e branch permanecem os do inicio. Estado final: 20 arquivos rastreados
modified, 16 untracked (14 novos C e 2 preexistentes), 0 staged. Nenhuma operacao
em main. Os dois arquivos untracked que ja existiam (nomes reportados por git
status como `'` e `"t(files): handle guardian lifecycle errors in ClamAV tests\\\""`)
foram preservados. Novos arquivos C e alteracoes rastreadas permanecem no
worktree para revisao. Nenhum commit/push/PR/merge/rebase/deploy foi realizado.

## P. Handoff

Implementacao local disponivel para Code Quality Review independente e Security
Review independente, com os bloqueios globais e limites explicitados. Nao ha
aprovacao independente. Benchmark/homologacao operacional posterior esta
preparado por protocolo, mas **nao executado nem autorizado**. Nao esta pronta
para merge/liberacao operacional enquanto gates globais, revisoes e homologacao
necessarios permanecerem pendentes. Preservar todas as alteracoes locais.

# Remediacao final C — consolidacao dos pareceres (2026-11-09)

Esta secao A–O atualiza o handoff anterior A–P. Os pareceres foram informados
pelo solicitante como **Code Quality APPROVED WITH COMMENTS** e **Security
APPROVED WITH NON-BLOCKING FINDINGS**; as divergencias abaixo reproduzem as
assertivas recebidas no prompt. A fonte de verdade foi o snapshot local dos
arquivos. Esta execucao implementa correcoes e evidencias para novos re-reviews
direcionados; nao emite aprovacao independente nem autorizacao de rollout.

## A. Snapshot e integridade

Branch `feature/files-1129-scan-queue-fencing`; HEAD e origin/develop iguais a
`9567adf7471fd427630df749bef40ddd2e39920a`. Inicio: 20 modified, 16 untracked
(14 C, 2 preexistentes), 0 staged. Antes de editar, foram copiados os arquivos C
para `/tmp/nchat-c-remediation-u6fpg_wm/before` e registrados hashes em
`initial-sha256.json` no mesmo diretorio privado. Os dois untracked alheios
foram apenas lidos para SHA-256 e permanecem com nomes/conteudo inalterados.
Nenhuma mudanca local alheia foi sobrescrita.

A migration 000020 up/down nao foi modificada. A 000021 ainda local foi ajustada;
nao foi aplicada a ambientes operacionais. `grant-runtime.sql` foi inspecionado,
reaplicado nas fixtures e permaneceu byte a byte igual ao inicio desta remediacao.
Hashes iniciais/finais dos arquivos centrais estao ao final desta secao.

## B. Reconciliacao factual

| Item                   | Code Quality informado           | Security informado               | Implementacao efetiva e referencia                                                                                                                             | Testes / documentacao                                                                                                              |
| ---------------------- | -------------------------------- | -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Autorizacao do trigger | GUC nchat.scan_queue_write       | current_user::regrole = proowner | Security correto: `migrations/files/000021_scan_queue_fencing.up.sql:62`; compara owner em pg_catalog.pg_proc. GUC nao e autorizacao                           | `privilegesRemediation`, `assertFunctionACLs`, `temporaryShadow`; arquitetura ja descrevia owner; teste novo forja GUC sem sucesso |
| search_path            | pg_catalog, files, public        | pg_catalog, files, pg_temp       | Security correto: `000021_scan_queue_fencing.up.sql:31` e `:57`; public nao integra o path das funcoes C                                                       | `assertFunctionACLs` verifica as dez funcoes C; `temporaryShadow` verifica pg_temp; documentacao original C correta                |
| Backoff                | 5s a 300s                        | 20s a 2640s                      | CQ incorreto e Security incompleto: `service/scan_retry.go:27`; teto exponencial inicia 20s, equal jitter produz primeira faixa 10–20s; faixa final 1320–2640s | `TestScanRetryDelayIsBoundedDeterministicAndSpread`; formula ja correta na arquitetura/runbook; nenhum tuning foi feito            |
| Configuracao           | FILE_MALWARE_SCAN_MAX_CONCURRENT | FILE_MALWARE_SCAN_CONCURRENCY    | Security correto: `config/config.go:316` e `:781`; default 1, valores 1–4, headroom PG                                                                         | `TestScanConcurrencyFailsClosed`, `TestScanConcurrencyReservesPoolCapacity`; nao foi criado alias da variavel errada               |
| Drain                  | 32 jobs                          | 32 jobs ou 1 segundo             | Security completo: `worker/scan_worker.go:16` e `:72`; para entre jobs, cede 100ms; idle/erro espera 10s                                                       | `TestScanDrainBoundsAndIdle`, `TestScanDrainRunsConsecutiveJobsWithoutPollDelay`; documentacao C ja incluia ambos os limites       |
| API legada de Evidence | Quatro argumentos                | Nove argumentos                  | Security correto: wrapper `000021_scan_queue_fencing.up.sql:144`; Go `storage/signature_approval.go:17` passa nove. API v2 tem onze                            | `preparation`, `grants`, `TestProvenApprovalOutcomesAreDistinct` e cadeia B real; assinatura legada preservada                     |

Os prefixos Go da tabela sao relativos a
`services/file-service/internal/`. Os numeros de linha referem-se ao resultado
final. Nenhum comportamento correto foi trocado para coincidir com parecer
inexato. O uso de reason no histograma e a mudanca nova de observabilidade,
explicitamente exigida por CQ-C-001, nao uma alegacao sobre o snapshot anterior.

## C. SEC-C-002 — maquina de estados e remocao

**Corrigido, com contraexemplo anterior e regressao real PG16/17.** O trigger
anterior bloqueava novos clean/rejected e mutacoes de agenda em pending_scan,
mas permitia pending_scan→failed sem mudar agenda quando o token era NULL
(antes de claim ou durante backoff), removendo a linha da fila.
Tambem bloqueava CancelDraft/ExpireDrafts porque ambos limpam a agenda ao
fazer soft-delete. `preRemediation` instala a definicao anterior congelada em
transacoes que sempre sofrem rollback, demonstra ambos os resultados usando
current_user=nchat_app e restaura a definicao corrigida. O root da fixture e
usado somente para DDL/SET LOCAL ROLE nessa demonstracao; as demais operacoes
adversariais usam conexoes autenticadas diretamente como nchat_app, sem membership.

A guarda corrigida protege status, id/workspace, token/lease/contador/falhas e
ressurreicao de deleted_at. O trigger UPDATE passou a disparar tambem para id,
workspace_id e deleted_at. Runtime so pode avancar pending_upload para pending_scan
ou failed, conservando as APIs atuais. Nao pode abandonar pending_scan, reabrir
terminal, fabricar token/lease em INSERT/UPDATE ou remover a marca de exclusao.

Excecao estreita: soft-delete com status, identidade e dados de claim inalterados
pode conservar ou limpar a agenda. Nao concede aprovacao, troca de token ou
restauracao. Sem nova funcao privilegiada e sem GUC de autorizacao.

| Caminho inspecionado                    | Transicao / acao real                                                              | Resultado corrigido                                    |
| --------------------------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `MarkUploaded`, attachment_store.go:128 | pending_upload→pending_scan e agenda no mesmo UPDATE                               | Funciona com o store real na fixture                   |
| `MarkFailed`, :186                      | So pending_upload→failed                                                           | Funciona; compensacao tardia nao remove pending_scan   |
| `CancelDraft`, :213                     | Mantem status, exige uploader/draft nao publicado, marca deleted_at e limpa agenda | Funciona, idempotente; outro uploader recebe not_found |
| `ExpireDrafts`, :261                    | Mantem status, exige expirado/nao publicado, soft-delete e cleanup                 | Funciona com lote limitado                             |
| `MarkAttachmentDeleted`, :583           | Mantem status, escopo id+workspace, advisory fence e soft-delete                   | Funciona; workspace incorreto recebe not_found         |
| Cleanup de objetos/previews             | Nao muda attachments.status; muda lifecycle do preview/remove objeto               | Nao precisou de nova autoridade de status              |
| clean/rejected                          | API fenced, token vigente, row lock, Evidence B quando exigida                     | Continua exclusiva das funcoes cercadas                |
| retry                                   | Mantem pending_scan, libera token/lease e agenda erro fechado                      | Continua fenced e sem veredito                         |

`lifecycleRemediation`, `cancelAndRemove` e `uploadTransitions` exercitam falhas
diretas failed/clean/rejected/pending_upload/deleted, sucesso de cancelamento e
exclusao, finalizacao/compensacao reais, impossibilidade de fabricar lease/token,
resultado vigente e recusa de ressurreicao. Scope incorreto e recusado tanto na
remocao quanto no clean/rejected com token correto de outro workspace. As
regressoes antigas continuam verificando tokens obsoletos e exclusao concorrente.

A role runtime continua compartilhada pela aplicacao; C nao introduz RLS nem
prova identidade do usuario final dentro do PostgreSQL. Autorizacao de
cancelamento/remocao continua nos predicados dos stores e na camada de servico.
Nao se afirma proteger contra todos os abusos de uma role runtime comprometida.

## D. CQ-C-001 — categoria de retry e backoff

**Corrigido** em malware_scan_service.go:553: policy_blocked agenda
signature_policy; falha real de banco agenda database. Permanecem UUID, deadline,
backoff e quarentena. O retry aceita somente o conjunto SQL ja fechado de razoes.

O histograma `nchat_file_malware_scan_backoff_seconds` ganhou o unico label
`reason`, com a categoria fechada do retry confirmado; nenhum texto de erro vira
coluna/label. `TestResultRetryClassifiesPolicySeparatelyFromDatabase` verifica
policy inativa/invalida e erro arbitrario de driver, motivo entregue ao store,
motivo observado no backoff, pending_scan e ausencia de aprovacao/evento.
`retryReasons` confirma scan_last_error/pending_scan no banco real.
`TestScanQueueMetricsTrackSlotsAndDelays` verifica a serie reason=signature_policy.
Os perfis anteriores eram sem labels; consultas futuras devem considerar reason.

## E. CQ-C-002 — conflito de versao

**Confirmado e corrigido.** Antes, a versao de formato avanca sob transacao
separada, a aprovacao espera o row lock e o ramo sem Evidence retorna zero linhas:
Go recebe pgx.ErrNoRows, sem SQLSTATE de erro do servidor. Com Evidence, a mesma
condicao ja retornava outcome=conflict / ErrScanConflict.

Agora `RETURN QUERY UPDATE` sem linhas produz explicitamente a linha conflict;
o predicado de versao nao foi relaxado. `formatConflicts` usa pg_blocking_pids
como barreira antes do commit do avanco de versao. Nos dois ramos recebe
ErrScanConflict, conserva pending_scan, nao aprova e, sem Evidence, confirma
literalmente result_outcome=conflict no SQL. Nao e falha de conectividade.
Nenhum SQLSTATE de erro foi inventado para um resultado normal; o mapeamento
existente de NC003/40001/40P01 permanece. O ramo Evidence/000020 nao foi alterado.

## F. CQ-C-003 — standby

**Corrigido** sem comparar mensagens. `require_scan_fencing` distingue somente
preparacao deliberada (enabled=false, activated=false) por NC006. NC004 continua
para policy ausente/incoerente e caminhos legados bloqueados. ClaimDueScans mapeia
NC006 em ErrScanQueueStandby tanto ao abrir a query quanto na iteracao de rows;
preserva a causa SQL com errors.Is/errors.As. ProcessDue conserva o erro tipado.

O consumidor deixa de produzir WARN para esse erro esperado e continua aguardando
o mesmo polling. Nao retorna sucesso artificial para falha verdadeira. Erros de
conexao, NC004 e cancelamento seguem caminhos proprios. Os testes
`TestScanStandbyIsTypedOnQueryAndIterationErrors`,
`TestStandbyIsQuietButPolicyAndConnectionFailuresWarn` e
`TestStandbyPollingResumesWithoutRestart` verificam classificacao, logs e retomada.
`preparation` e os claims posteriores usam a mesma instancia do store antes/depois
do cutover descartavel. Nenhuma ativacao operacional ocorreu.

## G. SEC-C-001 — contrato temporal e LOW residual

**Mantido aberto como LOW, sem mudanca da autoridade temporal.** O contrato e
validade na checagem apos aquisicao do row lock, dentro da transacao que grava
o resultado. Nao e um requisito de deadline absoluto no UPDATE ou no commit.

`leaseLinearization` adquire a autoridade vigente, persiste dentro de transacao
explicita, cruza o deadline real usando uma barreira derivada do relogio PG e
so entao tenta outro claim em conexao separada. Esse claim nao recebe a linha,
pois o lock continua retido. O primeiro commit depois do deadline e aceito; o
estado final e clean. A prova confirma exclusividade, **nao** prazo de commit.

Checar novamente no UPDATE seria pequeno e estreitaria uma janela de espera,
mas nao impediria demora posterior com a transacao aberta. Nao melhoraria a
propriedade essencial de exclusao ja garantida pelo lock nem encerraria o
finding de commit. Nao foi adicionado como falsa remediacao. statement_timeout
nao garante instante de commit; trigger deferred roda antes de concluir commit
e nao prova validade no instante exato de sua conclusao. Limites existentes de
contexto protegem o fluxo normal, sem substituir esse contrato PostgreSQL.
O corpo de lock_scan_attempt permanece identico ao snapshot, assim como UUID,
lease/backoff separados e recuperacao por novo claim.

## H. Privilegios PostgreSQL

Em PG16.15/17.11, apos reexecutar grant-runtime.sql, `assertFunctionACLs` consulta
pg_proc/ACLs e confirma owner nchat_migrator nas dez funcoes C; definer nas oito
APIs/helpers apropriadas e invoker nos dois triggers. Todas usam
pg_catalog,files,pg_temp; nenhuma concede EXECUTE a PUBLIC. nchat_app executa so
scan_fencing_enabled, persist_proven_scan, claim_scans, persist_scan_v2 e retry_scan.
Helpers internos/trigger functions nao sao APIs executaveis pela role.

`privilegesRemediation` confirma: sem membership de migrator; SET ROLE migrator,
SET ROLE postgres e SET SESSION AUTHORIZATION recusados com 42501; sem CREATE
em auth/chat/files/public; sem UPDATE direto de Evidence. A GUC
nchat.scan_queue_write pode ser definida pelo cliente, mas nao muda a autoridade
e a mutacao continua recusada. O teste anterior de sombra pg_temp permanece.
Permissoes de coluna para status/claim continuam condicionadas pelo trigger;
nao se confunde GRANT UPDATE com autorizacao para transicao. Nao foi necessario
alterar grant-runtime.sql, adicionar grant, superuser no runtime ou funcao generica.

## I. N/N+1 e rollout

A matriz completa do runbook permanece valida. Na preparacao, N funciona e
N+1 recebe standby NC006; assinatura legada tem nove argumentos. Com cutover,
N+1 exige token e N continua bloqueado, inclusive na API privilegiada B sem token.
Policy B ligada exige Evidence; desligada nao dispensa fencing. A guarda de
lifecycle so entra com C ativa, preservando expansao para N. Soft-delete,
cancelamento e compensacao restrita de upload continuam disponiveis.

Ativacao continua exclusiva do migrator mediante aprovacao e drenagem globais;
latch continua irreversivel. Up/down antes do corte e recusa de down depois
foram reexecutados. Rollback de worker depois do corte exige binario com API v2.
Nenhuma excecao nova para reabilitar N, reduzir frescor ou liberar pending.
SEC-B-002 permanece **bloqueante de rollout** independentemente desta remediacao.

## J. Testes, evidencias e limites

Logs privados desta execucao: `/tmp/nchat-c-remediation-u6fpg_wm/`.

| Evidencia                                                  | Conteudo                                                                                            |
| ---------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| baseline-pg.log                                            | PG16/17: failed direto aceito antes, cancelamento antes bloqueado, pgx.ErrNoRows antes sem Evidence |
| remediated-pg-final.log                                    | Correcoes e todas as regressoes C, ACLs reais, SET ROLE/GUC, SQL conflict e deadline/row lock       |
| go-test.log / race.log / go-static.log                     | Gates oficiais Go dos nove modulos                                                                  |
| race-final-affected.log                                    | Repeticao do conjunto afetado apos os testes adicionais                                             |
| clamav.log / signature.out                                 | ClamAV real isolado e cadeia B completa                                                             |
| coverage-full.log                                          | Gate oficial completo com perfis PG/B/C                                                             |
| trivy-fs.log / trivy-config.log / govuln.log / secrets.log | Scanners oficiais, sem ignores novos                                                                |
| initial-sha256.json / final-sha256.json                    | Integridade dos arquivos e untracked preexistentes                                                  |

Claim concorrente, skip locked, lease expirado, substituicao, clean/rejected/
Evidence/retry obsoletos, rescan, exclusao concorrente, rollback de transacao,
eventos apos persistencia, publisher falhando, N/N+1, corte recusando legado e
integracao B continuam nos runners/suites executados. Sem testes contra bancos
reais, sem malware real ou acesso a nchat-dev/nchat-prod. Nao foi feito benchmark
operacional adicional: permanece a limitacao do handoff original.

## K. Quality gates desta remediacao

| Comando / verificacao                                                     | Estado real                                                                                                 |
| ------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `make test-go`                                                            | PASS executado, nove modulos                                                                                |
| `make ci-static-go`                                                       | PASS executado, sem novo lint/errcheck, sem nolint                                                          |
| `bash scripts/ci/go-test.sh -race`                                        | PASS executado nos nove modulos; conjunto afetado repetido apos testes adicionais                           |
| `bash scripts/testing/scan-queue-postgres.sh`                             | PASS executado PG16.15 e PG17.11, migrations up/down e grants reais                                         |
| `bash scripts/testing/clamav-signatures-isolated.sh`                      | PASS executado, engine/Guardian real e cadeia B                                                             |
| `make go-coverage-check`, com fixtures PG/B/C                             | PASS executado; nove modulos >=90%; file-service **90,7%**                                                  |
| Mesmo comando sem fixtures                                                | FAIL preexistente/ambiente: chat 83,9% com suites opt-in puladas; nao e cobertura executada das integracoes |
| `make ci-infra-migrations ci-static-repository`                           | PASS executado dos scripts oficiais de migrations, governance de CI, arquitetura e formato                  |
| actionlint / yamllint / gitlab-ci-lint                                    | SKIP justificado: indisponiveis, conforme log do script oficial; nao apresentados como aprovados            |
| `make security-trivy-config`                                              | PASS executado                                                                                              |
| `make security-govulncheck`                                               | PASS executado, ferramenta local Go1.26 compativel; sem mudar ignore/dependencia                            |
| `make security-trivy-fs`                                                  | FAIL preexistente: CVE-2026-78661 HIGH em x/net v0.60.0, abaixo                                             |
| `make security-secrets`                                                   | FAIL preexistente: quatro findings historicos; nenhum ignore novo                                           |
| Gitleaks sobre diff e arquivos novos C                                    | PASS executado; nenhum achado                                                                               |
| `gocyclo` / `gocognit` das funcoes de producao alteradas                  | PASS executado, nenhuma acima de 10                                                                         |
| `git diff --check` e gofmt                                                | PASS executado                                                                                              |
| Benchmark operacional, deploy, checks em cluster e re-review independente | NOT EXECUTED: fora desta autorizacao                                                                        |

Cobertura por modulo nesta execucao completa: platform 94,4%; admin 91,3%; auth
90,8%; chat 90,2%; document-converter 92,7%; files 90,7%; media 92,4%; notification
96,0%; search 98,1%. Thresholds, exclusoes, lint e owners de CI nao foram alterados.
Nao houve FAIL novo remanescente nos testes/gates da remediacao.

A flakiness de TestNotificationClaimOrderIsDeterministicPostgreSQL nao foi
corrigida nem declarada resolvida: a execucao completa desta fase passou, mas o
handoff anterior registrou 16/20 falhas com o servico inalterado. Checks
obrigatorios falhando nao autorizam merge, mesmo com pareceres aprovados.

**Localizacao concreta da CVE:** o Trivy FS reporta a biblioteca
`golang.org/x/net v0.60.0`, pacote afetado `golang.org/x/net/html`, correcao
upstream indicada v0.62.0. Dependencia direta em `libs/go/platform/go.mod:11`;
indireta em admin:38, auth:37, chat:39, files:39, media:86, notification:38 e
search:39 (todos em seus go.mod). Esses sao os oito manifests com o finding;
document-converter apresenta zero. Ha import real de html em
`libs/go/platform/linkfetch/parse.go:10`. Nao e um finding da stdlib Go nem uma
biblioteca introduzida por C. Severidade HIGH continua bloqueando esse gate;
nenhuma atualizacao ou excecao foi criada fora do escopo.

Os quatro findings de secret scan continuam no historico dos commits 53cb7b52 e
1d18b8ee, arquivo notification_outbox_promotion_postgres_test.go do chat-service.
Nenhum valor foi exposto. Nova varredura do diff/novos arquivos nao encontrou
segredo; isso nao resolve nem dispensa a triagem dos findings historicos.

## L. Alteracoes adicionais ao snapshot inicial

| Arquivo                                                                                 | Alteracao nesta fase                                                                                 |
| --------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `migrations/files/000021_scan_queue_fencing.up.sql`                                     | Fechar transicoes/claims artificiais, permitir remocao estreita, outcome conflict e NC006 de standby |
| `services/file-service/internal/service/malware_scan_service.go`                        | Categoria signature_policy para policy_blocked; comentario do contrato de erro                       |
| `services/file-service/internal/service/scan_retry.go`                                  | Erro tipado de standby e categoria no observer de backoff                                            |
| `services/file-service/internal/service/scan_retry_test.go`                             | Categorias, label/motivo, pending e ausencia de sucesso/evento                                       |
| `services/file-service/internal/http/attachment_metrics.go`                             | Histograma backoff com reason fechado                                                                |
| `services/file-service/internal/http/attachment_metrics_test.go`                        | Verificar a serie com reason e nil receiver                                                          |
| `services/file-service/internal/storage/scan_store.go`                                  | NC006 tipado em query/iteracao, preservando causa                                                    |
| `services/file-service/internal/storage/scan_store_test.go`                             | Diferenciar standby de NC004/conexao                                                                 |
| `services/file-service/internal/storage/scan_queue_postgres_test.go`                    | Contraexemplos anteriores, versao concorrente e registro das novas regressoes                        |
| `services/file-service/internal/storage/scan_queue_remediation_postgres_test.go` (novo) | Lifecycle, cancelamento/remocao/escopo, owner/ACL/SET ROLE/GUC, retry e lease/commit                 |
| `services/file-service/internal/worker/scan_worker.go`                                  | Silenciar somente standby tipado, mantendo polling e WARN verdadeiros                                |
| `services/file-service/internal/worker/scan_worker_test.go`                             | Logs e retomada sem restart                                                                          |
| `docs/architecture/scan-queue-fencing-1129.md`                                          | Contratos reais de lifecycle, erros, standby e SEC-C-001                                             |
| `docs/runbooks/scan-queue-fencing-1129.md`                                              | Operacao futura, standby, categorias e LOW residual                                                  |
| Este documento                                                                          | Reconciliacao A–O, hashes e resultados desta fase                                                    |

Sao 14 arquivos do handoff ajustados e um teste novo. grant-runtime.sql,
000021 down, config/concurrency, algoritmo RetryDelay, scripts/workflow CI e os
arquivos historicos B foram preservados. O corpo de lock_scan_attempt tambem
permaneceu identico; nao houve reimplementacao do fencing.

## M. Riscos e pendencias

- Codigo: resultado de formato agora e conflito de dominio; a categoria database
  continua agrupando falhas/conflitos de persistencia, diferenciados pelo log
  database_conflict. Nao se adicionou outro estado externo de attachment.
- Seguranca: SEC-C-002 corrigido; **SEC-C-001 LOW aberto** pelo contrato temporal.
  **SEC-B-002 permanece independente e bloqueante de rollout**. Roles owner e
  runtime compartilhada continuam nas fronteiras documentadas; nao ha nova PKI,
  autenticacao de recibos, RLS ou funcao generica de bypass.
- CI: Trivy FS e secret scanning continuam com FAIL preexistente; flakiness do
  notification-service e ferramentas de lint indisponiveis seguem declaradas.
  Nao foi instalado ignore nem reduzido gate para aceitar o resultado.
- Operacao: preparar nao e ativar. Cutover continua explicito/irreversivel e
  exige autorizacao e drenagem. Sem benchmark/homologacao operacional nova;
  default de concorrencia e capacidade compartilhada nao foram aumentados.
- Reconciliacao: as descricoes CQ divergentes nao correspondem ao snapshot
  hashado. Sem os hashes dos artefatos dos pareceres nao se atribui a causa do
  desencontro; a nova reavaliacao deve usar os hashes finais abaixo.

## N. Git final e preservacao

Branch/HEAD continuam os mesmos do inicio. Estado final: 20 tracked modified,
17 untracked (15 C, incluindo o teste novo; os mesmos 2 preexistentes), 0 staged.
Os hashes dos dois untracked preexistentes conferem com o inicio. Nenhum git
add/commit/push/PR/merge/rebase, alteracao de main ou deploy foi realizado.
Nao houve comando operacional em nchat-dev/nchat-prod nem ativacao de C fora das
fixtures descartaveis. Todos os arquivos locais foram preservados para revisao.

## O. Prontidao para reavaliacao

Pronto para **Code Quality Re-Review e Security Re-Review direcionados** ao diff
adicional e aos hashes finais. Reavaliar especialmente a excecao estreita de
soft-delete, o erro NC006 e a classificacao de conflito. Esta execucao nao emite
nova aprovacao independente. SEC-C-001 permanece LOW aberto por decisao explicita;
SEC-B-002 e gates globais continuam impedindo aprovacao automatica de rollout/merge.

## SHA-256 inicial / final dos arquivos centrais

| Arquivo                                                          | Inicio                                                             | Fim                                                                |
| ---------------------------------------------------------------- | ------------------------------------------------------------------ | ------------------------------------------------------------------ |
| `migrations/files/000020_signature_freshness.up.sql`             | `8d156cf3b3eac46de05dcee754adf715d409cdae074e76c6e4350254c0148afd` | `8d156cf3b3eac46de05dcee754adf715d409cdae074e76c6e4350254c0148afd` |
| `migrations/files/000020_signature_freshness.down.sql`           | `b61b5aba01d8a2ae5cee0deddc980beea736316d7476dedd41c16eb2bd482e5c` | `b61b5aba01d8a2ae5cee0deddc980beea736316d7476dedd41c16eb2bd482e5c` |
| `migrations/files/000021_scan_queue_fencing.up.sql`              | `7cd46c94c985bc0193880e3c07b51630b5430ba94fa1ead819be13c88e082722` | `06caee9235aca2b23aaaeab49482c9b1aa2311eb38c22ae13525fd2fe98278e7` |
| `migrations/files/000021_scan_queue_fencing.down.sql`            | `6ff24a3e88fc1cfdb39d3a42f0f5838f9db4b6fb248c04c5f8ec7464f7e498f4` | `6ff24a3e88fc1cfdb39d3a42f0f5838f9db4b6fb248c04c5f8ec7464f7e498f4` |
| `scripts/db/grant-runtime.sql`                                   | `42da4bb0367d919ff5d751960073f566f378fb989b2d6fb19194d42261f215cc` | `42da4bb0367d919ff5d751960073f566f378fb989b2d6fb19194d42261f215cc` |
| `services/file-service/internal/config/config.go`                | `3f28335c858107fe7aaa12b5ec6d740f8a8ed383914421797de992b89b29b2f4` | `3f28335c858107fe7aaa12b5ec6d740f8a8ed383914421797de992b89b29b2f4` |
| `services/file-service/internal/service/malware_scan_service.go` | `5974a0509cfbd718b15e3cd733ddf71e6b20a210124690b734c8c2a302f32a2c` | `74e4b1817e03ea1c65ce74b5bd78566d895af731c3ae3744a69f414262344530` |
| `services/file-service/internal/service/scan_retry.go`           | `ae3a6172280992d1cbac19d50fcfa2610c127417af0836c3a5d5cc424bf17642` | `85d22d39639e9356ed79116f2f0ed9587ad6b56980a2babbf0ce826ba068639a` |
| `services/file-service/internal/storage/scan_store.go`           | `853b71f59c3182fcc3d9c164cde973b87834109d2c0ad0046dbdc0da350179fa` | `95756f9fc9b03c1112ce87b75b92ad7c1bc7673fdf42bcb1cf4f38e0e6c4aff9` |
| `services/file-service/internal/storage/scan_attempt.go`         | `1331eae7e053b1e4c8fb4d22385a51c62958004980f2932ec872baaba7207290` | `1331eae7e053b1e4c8fb4d22385a51c62958004980f2932ec872baaba7207290` |
| `services/file-service/internal/storage/signature_approval.go`   | `0e49fd867155257ba8418cc4309ea059947b41665d47fbf14de8c2f12b0870b5` | `0e49fd867155257ba8418cc4309ea059947b41665d47fbf14de8c2f12b0870b5` |
| `services/file-service/internal/worker/scan_worker.go`           | `3c98abd202560eec26f615a707a3d15e1b171aabaf33b8834d8268f8aede261d` | `dbffd6b7b2ee528cf9905d68735a68ad0208b1123781a8502bd92b2664f1b24e` |
| `services/file-service/internal/http/attachment_metrics.go`      | `a04509377aecac2279e397d4393ec856b2e82f13d4c3711d6010d0c86c697382` | `fa2e73f234ee02fea9e9575c558a6e419682b95bbc3c817f9e237f6604b74ed6` |

Corpo SQL de lock_scan_attempt, identico no inicio/fim: `e85935186cc80718f6907cea8a816ac988de4d92b2cd4bca2af1fee53d884e67`.
