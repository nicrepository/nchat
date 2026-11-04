# Issue #1128 — correções de segurança

Implementação de S-001 a S-005 em 2026-10-21. Branch
`feature/files-1128-enterprise-formats`, HEAD/base local `4da2bcf`.
Estado inicial confirmado: 48 arquivos rastreados modificados, 53 novos e staging
vazio. Alterações anteriores preservadas; corpus e migration 000019 mantidos.
Nenhum perfil ativado, deploy, staging, commit, push, PR, merge ou rebase.
Este registro é validação de implementação, não Security Re-review independente.

## S-001 — relacionamentos OOXML

**Status: CORRIGIDO.** A inspeção genérica reconhecia `.RELS`/`.Rels`, mas o
índice de relacionamentos só analisava `.rels`. A reprodução anterior admitiu
relações proibidas nessas variantes em DOCX, XLSX e PPTX.

**Componentes:** `internal/filepolicy/{office,zip}.go` do file-service,
`internal/preview/document.go`, `internal/converter/handler.go` do
document-converter e o novo `libs/go/platform/documentguard`.

**Correção/invariantes:** um único predicado reconhece relationship parts em
qualquer caixa. Todos recebem parsing XML com namespace OPC, atributos não
qualificados e destinos internos existentes. Type admite somente identificadores
HTTP(S) canônicos, sem escapes ou sufixos ignoráveis; URNs/tipos personalizados
fora dessa política são recusados. Macros, OLE, ActiveX, templates,
externalLink e pacotes incorporados são recusados por tipo estrutural, além
dos guardas de caminhos/tipos existentes. Nenhum componente usa substrings da
serialização XML para decidir segurança. Entidades numéricas são decodificadas
pelo parser; whitespace, aspas e prefixos não alteram a decisão. Relações com
URI externa, destino absoluto, fragmento ou recurso inexistente são recusadas.

**Testes/evidência:** regressão em `filepolicy/adversarial_test.go` com três
famílias × três variantes × seis relações; testes `documentguard`, preview e
HTTP do conversor, incluindo TargetMode com entidade/whitespace. Regressões adicionais demonstraram
que Type com espaço final, percent-encoding e query atravessava a comparação
exata inicial; agora a URI é validada antes de classificar seu tipo. O runner do
conversor recebe zero chamadas para os pacotes perigosos; relações internas
válidas continuam aceitas. Suites focadas aprovadas.

**Risco residual:** não é validação XSD integral ou garantia de ausência de
malware; AV e isolamento do conversor continuam necessários.

## S-002 — referências ODF/XML

**Status: CORRIGIDO.** A heurística literal aceitava caminho absoluto Unix,
traversal codificado, query, recurso inexistente e percentuais ambíguos. Esses
casos foram reproduzidos antes da correção.

**Componentes:** `filepolicy/{text,office,zip}.go`, preview, converter e
`documentguard/{reference,xml,archive}.go`.

**Correção/invariantes:** `url.Parse` faz uma única interpretação de URI;
resolução relativa usa a parte de origem e um índice de nomes exatos do ZIP.
Schemes/host/userinfo/opaque/query, caminhos absolutos, backslash, controles,
UTF-8 inválido, escape do pacote e percentuais ambíguos são recusados.
Não há double decoding ou resolução de rede/filesystem. xlink usa namespace URI,
independentemente do prefixo. `xml:base`, DTD e entidades declaradas são recusados.
Fragmentos locais e recursos internos existentes são permitidos; XML standalone
não possui recursos de pacote. XML tem budgets de bytes, profundidade e atributos.
Os consumidores compartilham guardas e inspecionam antes do LibreOffice.

**Testes/evidência:** caminhos Unix/Windows, traversal literal/codificado/duplo,
file/http/https/protocol-relative, queries, namespace alternativo, entidades,
whitespace, nomes inexistentes, XML malformado, encoding inválido e casos legítimos.
Testes atravessam admissão, preview e HTTP do conversor; o runner não é chamado
para entradas perigosas. Documentos ODF do corpus real continuam gerando previews.

**Risco residual:** comprova-se rejeição estrutural e contenção antes da execução;
não se demonstrou exfiltração real pelo LibreOffice, nem se faz essa alegação.
Nomes com escapes percentuais ambíguos podem ser recusados conservadoramente.

## S-003 — custo do derivado MP3

**Status: CORRIGIDO.** A rota iniciava transcodificações sem rate limit ou
admissão de concorrência, mantendo buffers, processos e temporários por pedido.

**Componentes:** router HTTP, `service/{attachment_service,audio_transcode}.go`,
cliente do conversor, `converter/{handler,audio_runner}.go` e testes respectivos.

**Correção/invariantes:** middleware existente, 20 requisições/minuto por usuário
por instância, compartilhado com uploads/cancelamento. Uma conversão/entrega por
file-service, adquirida depois de autorização e antes de abrir o original. O slot
permanece até fechar o reader, limitando temporários em clientes lentos. Saturação
local ou do conversor retorna 503. O conversor admite um corpo de áudio antes de
alocar buffers e o runner protege diretamente a execução de um processo ffmpeg.
Aquisição não bloqueante, liberação em sucesso/erro/cancelamento; timeout existente,
process group kill e cleanup. ffmpeg recebe uma thread de codec/filtro, teto de
arquivo de saída e stdout/stderr descartados, evitando buffer de logs sem limite.

Capacidade fixa conservadora: file-service 512 MiB RAM/64 MiB scratch;
converter 500m CPU/768 MiB RAM/256 MiB work. Não se promete quota global entre
réplicas; cada conversor limita os processos que recebe de várias instâncias.
Não há cache/singleflight ou retenção compartilhada de resultados. Mesmo Range
`bytes=0-0` pode exigir gerar o derivado inteiro, sob os mesmos limites. MP3 já
nativo e download original não executam transcodificação.

**Testes/evidência:** HTTP burst/429/Retry-After, usuários distintos, 206 pequeno,
503, autenticação; barreiras de concorrência para mesmo/diferente ID, saturação,
liberação após entrega/erro/cancelamento, processo simulado com FIFO sem sleeps,
timeout, remoção de temporários, autorização antes da conversão e conversão real
OGG/WAV para MP3 decodificável nas suites existentes. Race detector aprovado.

**Risco residual:** pedidos sequenciais autorizados continuam podendo recalcular o
derivado dentro dos limites. Ataque distribuído continua sujeito aos controles
existentes de autenticação e capacidade de réplicas, sem quota distribuída nova.

## S-004 — namespace de worksheet/fórmulas

**Status: CORRIGIDO.** O bloqueio de `<f>` só reconhecia namespace Transitional.

**Componentes:** `filepolicy/{text,office,zip}.go` e regressões adversariais.

**Correção/invariantes:** worksheet v1 exige raiz/elementos Transitional e recusa
fórmulas. Strict, namespace raiz inesperado e redefinições não são suportados.
A inspeção segue também o destino de cada relação worksheet, inclusive fora do
diretório convencional. Não se bloqueia genericamente qualquer `<f>` de XML.
O worker reusa essa política. Previews legados de planilhas leem somente valores
cacheados e continuam sem avaliador de fórmulas.

**Testes/evidência:** Transitional com fórmula, Strict, `<f xmlns="">`, raiz
inesperada, worksheet relocada com fórmula e workbook passivo aprovado. XLSX
real do corpus e preview/regeneração preservam o original.

**Risco residual:** documentos Strict legítimos continuam recusados; não se
anuncia suporte Strict ou suporte universal ao Excel.

## S-005 — regeneração de previews

**Status: CORRIGIDO.** A rota não tinha rate limit e permitia zerar tentativas
repetidamente após falha, sem cooldown persistido.

**Componentes:** router HTTP, CAS em `storage/attachment_store.go`, testes HTTP,
storage e `TestIntegrationEnterpriseRegenerationCooldown`.

**Correção/invariantes:** middleware existente somente na mutação; GET de páginas
não é limitado. O CAS só reabre falha após cooldown nomeado de 24 h, calculado em
SQL sobre `updated_at`, escrito pelas transições terminais. Nova rodada tem o
orçamento existente de três tentativas; repetir um pedido pendente não reinicia
esse orçamento. O controle sobrevive a reinício e concorrência. A expiração
normal de 30 dias continua regenerável imediatamente. Não há migration/estado novo.

**Testes/evidência:** burst/429, usuário distinto, autenticação e autorização
existentes; PostgreSQL real prova recusa imediata, liberação após cooldown,
concorrência sem múltiplos resets, nova recusa após falha e original preservado.
Os oito previews reais foram regenerados sem alterar o original.

**Risco residual:** atualizações legítimas de metadata podem alongar a espera em
falhas. Um usuário ainda pode solicitar uma rodada por dia para uma falha
persistente; o custo é limitado, não um ciclo de reset ilimitado e barato.

## Regressão, qualidade e comandos reproduzíveis

Ambiente: Go **1.26.9**, Govulncheck **1.1.4** compilado com toolchain compatível,
Trivy **0.75.0**, Gitleaks **8.30.1**, Semgrep **1.178.0**. Caches locais foram
direcionados a diretórios graváveis de teste quando necessário; thresholds e CI
não foram alterados. Não interpretar medições históricas como execução atual.

| Comando/medição                                                                                                                               | Resultado desta rodada                                                                                                                                     |
| --------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `go test ./internal/filepolicy ./internal/preview ./internal/service ./internal/http ./internal/storage ./internal/converter` em file-service | **PASS**                                                                                                                                                   |
| `go test ./internal/converter` em document-converter                                                                                          | **PASS**, incluindo ffmpeg/ffprobe reais quando disponíveis, confirmados no ambiente                                                                       |
| `go test ./documentguard -coverprofile=...` em platform                                                                                       | **PASS**, 96,3% no novo pacote                                                                                                                             |
| `make test-go`                                                                                                                                | **PASS** após corrigir o teste de script temporário                                                                                                        |
| `bash scripts/ci/go-test.sh -race`                                                                                                            | **PASS** em todos os módulos                                                                                                                               |
| Race focado final nos seis pacotes file-service, converter e novos testes de worker                                                           | **PASS**                                                                                                                                                   |
| Integração `TestIntegrationEnterprise*` abaixo                                                                                                | **PASS**, 13,832 s, sem skips                                                                                                                              |
| `make ci-static-go`                                                                                                                           | **PASS**; zero issues nos módulos, sem ignore/supressão novo                                                                                               |
| `make ci-static-web`                                                                                                                          | **PASS**, três warnings existentes do React Compiler, zero erros                                                                                           |
| `make ci-static-repository`                                                                                                                   | **PASS**, `.swarm/` preservado e fora dos inputs conforme configuração anterior                                                                            |
| `make go-coverage-check` com PostgreSQL                                                                                                       | **FAIL na última execução global**, no teste de ordenação de notification; file-service **90,5%**, platform 94,4%, converter 92,7%; ver diagnóstico abaixo |
| `make web-coverage`                                                                                                                           | **PASS**, 262 arquivos, 7.267 testes; statements 95,80%, branches 91,92%, funções 96,21%, linhas 97,60%                                                    |
| `make build`                                                                                                                                  | **PASS**, web e admin-web; aviso existente de tamanho de chunks                                                                                            |
| `make ci-infra-migrations`                                                                                                                    | **PASS**, 103 pares, guardas estáticos Blue/Green                                                                                                          |
| `CI=1 pnpm test:e2e:web enterprise-files.spec.ts voice-message-download.spec.ts --workers=2 --retries=0`                                      | **PASS**, 15 testes em 18,4 s                                                                                                                              |
| `make test-integration-go` genérico                                                                                                           | **NOT RUN**: seu owner não inclui file-service/converter; exige bancos isolados adicionais e Valkey. Integração pertinente real executada explicitamente   |
| Playwright completo                                                                                                                           | **NOT RUN** nesta rodada backend; fluxo pertinente executado, sem alterar o teste de citação conhecido                                                     |
| `make security-govulncheck`                                                                                                                   | **FAIL**, advisory chamado `GO-2026-6809`, sem exceção                                                                                                     |
| `make security-trivy-fs`                                                                                                                      | **FAIL**, três ocorrências HIGH em dependências existentes                                                                                                 |
| `make security-trivy-config`                                                                                                                  | **PASS**                                                                                                                                                   |
| `make security-secrets`                                                                                                                       | **FAIL**, quatro ocorrências históricas, 467 commits examinados                                                                                            |
| `gitleaks dir` sobre cópia do delta rastreado + untracked, com redact                                                                         | **PASS**, sem vazamentos no delta                                                                                                                          |
| Semgrep `scan --config p/golang --metrics off --disable-version-check --error` nos componentes alterados                                      | **PASS**, 42 regras, 13 arquivos de produção, zero achados; filtros padrão de testes mantidos                                                              |
| SonarQube                                                                                                                                     | **NOT RUN**, sem ferramenta conectada, host ou token configurado                                                                                           |

A integração usa PostgreSQL 16, SeaweedFS 4.29, ClamAV 1.4 e o conversor deste
checkout em portas descartáveis, sem ambiente compartilhado/produtivo. Os 26
formatos do [manifesto sintético](enterprise-attachment-corpus.csv) preservaram
bytes, tamanho e SHA-256 através de HTTP, cifra e scan reais. O manifesto também
foi conferido novamente contra todas as fixtures em disco. Oito previews reais
(PNG, PDF, CSV, XLSX, ODS, DOCX, ODT, PPTX) foram gerados e regenerados sem mudar
os originais. Range cruzando chunks, revogação, guest, workspaces, DM/grupo,
quarentena, scanner indisponível/EICAR e reabertura continuaram aprovados.

```bash
cd services/file-service
FILE_TEST_DATABASE_URL='postgresql://nchat@127.0.0.1:55128/nchat_1128_test?sslmode=disable' \
FILE_TEST_SEAWEEDFS_URL=http://127.0.0.1:58130 \
FILE_TEST_CLAMD_ADDRESS=127.0.0.1:53130 \
FILE_TEST_CONVERTER_URL=http://127.0.0.1:58129 \
go test ./internal/service -run '^TestIntegrationEnterprise' -v -count=1 -timeout=180s
```

O gate Go oficial combina perfil unitário e suites PostgreSQL nomeadas, com
`LINK_SAFETY_TEST_DATABASE_URL` apontando a `nchat_1128_cov2_test`. Não foi
substituído por cobertura unitária isolada ou pela estimativa diagnóstica usada
para orientar os testes adicionais. Após o último refinamento de URI, o componente
file-service foi medido novamente com os mesmos pacotes/mode atomic, helper
PostgreSQL e merge do script oficial: **90,5%**. Platform foi medido novamente:
**94,4%**; seu novo pacote **96,3%**. Essas medições por componente não substituem
nem aprovam o gate global que falhou. Os dois bancos são exclusivos desta tarefa.
Os novos testes de worker percorrem claim, conteúdo cifrado, política e scanner,
provando que relações/URIs perigosas são rejeitadas antes de invocar o scanner.
O teste positivo ODF contém imagem PNG real empacotada e referência relativa.

### Intercorrências registradas

- O primeiro gate Go encontrou teste novo tentando reescrever script temporário
  `r-x`. Corrigido recriando somente seu próprio arquivo de fixture. O lint
  identificou abertura de FIFO por caminho variável e cópia ZIP sem limite em
  testes novos; corrigidos com `os.Root`/nome constante e `io.LimitReader`, sem
  supressões. A ampliação para dois objetos revelou que o helper de upload usava
  sempre o primeiro registro de cifra; passou a selecionar o último upload,
  preservando a autenticação correta de cada objeto. Gates posteriores são a
  evidência de correção.
- Uma execução web falhou em dois testes de draft por timeout; os 17 testes do
  arquivo passaram isoladamente e a execução completa posterior passou em
  7.267/7.267. Nenhum teste/timeout frontend foi alterado. A causa última não foi
  estabelecida; não se apresenta a falha como baseline reproduzido.
- Uma consolidação Go falhou em
  `TestNotificationClaimOrderIsDeterministicPostgreSQL`, linha 424. Store e teste
  são byte a byte idênticos ao baseline `4da2bcf`. Dez execuções isoladas e o
  owner PostgreSQL completo do baseline passaram; a falha não foi reproduzida
  nessa comparação. Não se alterou notification-service nem se mascara a falha.
  Duas consolidações recentes repetiram a falha; não se insiste em execuções
  até obter verde. A tentativa de comparação global no arquivo do baseline
  precisou chamar diretamente o script oficial (pnpm da cópia tentou auto-install
  e abortou antes de remover dependências). Como o arquivo não possui `.git`,
  somente nessa comparação se usou `GOFLAGS=-buildvcs=false`, que remove metadata
  de build, sem alterar testes/thresholds. Essa comparação parou antes em
  chat-service **89,9%**, abaixo de 90%, e não chegou a notification. Assim a causa
  da divergência global continua **inconclusiva**; não se declara reprodução da
  falha de notification no baseline nem aprovação global de cobertura.

### Dependências e segredos: gates ainda reprovados

`GO-2026-6809` refere-se a `crypto/x509` do Go 1.26.9, corrigido em Go 1.26.10.
O Govulncheck no media-service do baseline intacto também reportou o mesmo
advisory alcançado. O scanner completo conseguiu executar: não está mais bloqueado
por incompatibilidade do seu binário, mas o gate está **reprovado**.

Trivy FS apontou `markdown-it@14.3.0` (`CVE-2026-111758`, `CVE-2026-111759`) e
`github.com/klauspost/compress@v1.18.0` (`CVE-2026-63209`, media-service).
`pnpm-lock.yaml`, módulos e código do media-service não foram alterados. Nenhuma
versão foi atualizada fora do escopo, nem foi criado ignore. Esses avisos exigem
tratamento separado e comprovação no CI antes de liberação.

O histórico Git permanece no mesmo HEAD inicial. O gate Secrets encontrou as
quatro ocorrências históricas já registradas na validação anterior; o scan do
delta (incluindo arquivos novos) não encontrou vazamentos. Não houve alteração de
histórico ou exceção para forçar aprovação.

### Arquitetura, complexidade e limitações de qualidade

O pacote compartilhado possui responsabilidades pequenas: indexar pacote limitado,
interpretar XML e resolver referências internas. O catálogo/política v1 continua
no file-service; o conversor mantém inspeção independente antes do processo.
Admissão de áudio e entrega do temporário são coesas; não foi criado framework de
semáforos, cache, novas filas, DSL ou locks distribuídos. A regeneração usa dados
já persistidos e CAS, sem migration.

Análise de fonte com gocyclo/gocognit: unidades de **produção novas ou efetivamente
refatoradas** ≤ 10 nas duas métricas. `RenderDocument` permanece **22/22** e
`wireAttachments` **12/11**. No conversor, `ServeHTTP` **19/16**, `validateAudio`
**15/14** e `validatePPT` **11/16** são preexistentes; a primeira só encaminha o
contexto ao validador, sem crescimento de controle. Não houve refatoração ampla.
A análise gocognit de fonte avisa sobre resolução incompleta de chamadas; não se
inventa métrica Sonar ou prova absoluta de ausência de smells/duplicação. O lint
consolidado informa os achados locais; revisão independente continua pendente.

## Compatibilidade e pendências operacionais

Sem dependências runtime novas, mudança de schema, estados, envelope, MIME
persistido ou endpoint do original. Novos helpers são internos à validação; os
serviços já dependiam de platform. Migration 000019 e corpus preservados.
Leitores/workers novos mantêm legado v0 e metadados v1. Binaries antigos não
possuem esses novos guardas e devem ser drenados antes da ativação.

- **INC-001: PENDENTE operacional.** Defaults Data/Office/Archives continuam false
  no código e `.env.example`. Atualizar todos os leitores, workers e conversores
  com perfis off; drenar N-1 antes de ativar famílias. Sem deploy/canary/ensaio
  de rollout nesta rodada.
- **INC-002: PENDENTE de remediação/CI.** Scanners disponíveis executaram:
  Govulncheck, Trivy FS e Secrets reprovaram pelos motivos acima. Não é aprovação
  de segurança nem ausência de ferramentas; correções de dependências/segredos e
  reexecução no CI continuam necessárias.
- **Cobertura global: FAIL.** A falha de ordenação fora do delta ainda exige
  diagnóstico/validação no CI. O threshold do file-service foi atendido pela
  metodologia oficial, sem substituir o gate global por resultado parcial.
- **SonarQube: NOT RUN.** Nenhuma ferramenta conectada disponível. Métricas de
  duplicação, hotspots e Quality Gate Sonar não são inventadas.
- **Canary nchat-dev: NÃO EXECUTADO.** Security Re-review independente pendente.

## Escopo Git verificável

Branch atual: `feature/files-1128-enterprise-formats`; HEAD/base `4da2bcf`.
Staging vazio; `git diff --check` sem erros. O estado total inclui a implementação
anterior e suas correções: **59 rastreados modificados e 63 untracked**.
Os 53 arquivos novos que já existiam foram preservados; esta rodada acrescenta:

- `docs/testing/enterprise-attachment-security-corrections.md`
- `libs/go/platform/documentguard/archive.go`
- `libs/go/platform/documentguard/archive_test.go`
- `libs/go/platform/documentguard/reference.go`
- `libs/go/platform/documentguard/xml.go`
- `libs/go/platform/documentguard/xml_test.go`
- `services/document-converter/internal/converter/audio_admission_test.go`
- `services/file-service/internal/http/attachment_cost_routes_test.go`
- `services/file-service/internal/service/audio_admission_test.go`
- `services/file-service/internal/service/office_scan_policy_test.go`

O inventário completo de arquivos ainda não rastreados é:

```text
apps/web/e2e/messaging/enterprise-files.spec.ts
apps/web/src/chat/attachmentDocumentFixtures.ts
apps/web/src/chat/attachmentDocumentRules.test.ts
docs/adr/0004-enterprise-attachment-policy.md
docs/api/supported-file-types.md
docs/architecture/enterprise-attachment-policy.md
docs/testing/enterprise-attachment-corpus.csv
docs/testing/enterprise-attachment-policy-validation.md
docs/testing/enterprise-attachment-quality-corrections.md
docs/testing/enterprise-attachment-security-corrections.md
libs/go/platform/documentguard/archive.go
libs/go/platform/documentguard/archive_test.go
libs/go/platform/documentguard/reference.go
libs/go/platform/documentguard/xml.go
libs/go/platform/documentguard/xml_test.go
migrations/files/000019_attachment_format_policy.down.sql
migrations/files/000019_attachment_format_policy.up.sql
services/document-converter/internal/converter/audio_admission_test.go
services/file-service/internal/domain/preview_policy_test.go
services/file-service/internal/filepolicy/adversarial_test.go
services/file-service/internal/filepolicy/catalog.go
services/file-service/internal/filepolicy/email.go
services/file-service/internal/filepolicy/failure_test.go
services/file-service/internal/filepolicy/inspect.go
services/file-service/internal/filepolicy/office.go
services/file-service/internal/filepolicy/policy_test.go
services/file-service/internal/filepolicy/testdata/README.md
services/file-service/internal/filepolicy/testdata/bundle.zip
services/file-service/internal/filepolicy/testdata/data.csv
services/file-service/internal/filepolicy/testdata/data.json
services/file-service/internal/filepolicy/testdata/data.tsv
services/file-service/internal/filepolicy/testdata/data.xml
services/file-service/internal/filepolicy/testdata/document.docx
services/file-service/internal/filepolicy/testdata/document.odp
services/file-service/internal/filepolicy/testdata/document.ods
services/file-service/internal/filepolicy/testdata/document.odt
services/file-service/internal/filepolicy/testdata/document.pdf
services/file-service/internal/filepolicy/testdata/document.pptx
services/file-service/internal/filepolicy/testdata/document.xlsx
services/file-service/internal/filepolicy/testdata/image.gif
services/file-service/internal/filepolicy/testdata/image.jpg
services/file-service/internal/filepolicy/testdata/image.png
services/file-service/internal/filepolicy/testdata/image.webp
services/file-service/internal/filepolicy/testdata/message.eml
services/file-service/internal/filepolicy/testdata/notes.md
services/file-service/internal/filepolicy/testdata/notes.txt
services/file-service/internal/filepolicy/testdata/silence.mp3
services/file-service/internal/filepolicy/testdata/silence.ogg
services/file-service/internal/filepolicy/testdata/silence.wav
services/file-service/internal/filepolicy/testdata/video.avi
services/file-service/internal/filepolicy/testdata/video.mov
services/file-service/internal/filepolicy/testdata/video.mp4
services/file-service/internal/filepolicy/testdata/video.webm
services/file-service/internal/filepolicy/text.go
services/file-service/internal/filepolicy/zip.go
services/file-service/internal/http/attachment_cost_routes_test.go
services/file-service/internal/http/attachment_original_test.go
services/file-service/internal/service/attachment_policy.go
services/file-service/internal/service/attachment_policy_test.go
services/file-service/internal/service/audio_admission_test.go
services/file-service/internal/service/enterprise_integration_test.go
services/file-service/internal/service/office_scan_policy_test.go
services/file-service/internal/service/scan_policy.go
```

Comandos executados para conferir o escopo, sem staging:

```bash
git status --short -uall
git branch --show-current
git diff --stat
git diff --name-status
git diff --check
git diff --cached --stat
git ls-files --others --exclude-standard
```

`git diff --stat` mede somente rastreados; não inclui os arquivos acima.
Não foi criado snapshot Git avulso, nem alterado/removido conteúdo de `.swarm/`.

## Estado de entrega

As cinco correções estão implementadas e têm evidência reproduzível para
Security Re-review do delta. Isso não aprova merge/release: cobertura global,
dependências e segredos permanecem reprovados/pendentes conforme registrado.
Os três containers descartáveis foram parados sem remover volumes; o conversor
de teste foi encerrado. Não houve operação em ambiente de produção.

```text
============================================================================
ISSUE #1128 — SECURITY CORRECTIONS
============================================================================
S-001: CORRIGIDO
S-002: CORRIGIDO
S-003: CORRIGIDO
S-004: CORRIGIDO
S-005: CORRIGIDO

Regressões de segurança: nenhuma nova confirmada no delta; re-review pendente
Testes adversariais: PASS, incluindo admissão, worker, preview e converter
Integração real: PASS, 26 originais e oito previews/regenerações
Testes Go: PASS
Race detector: PASS, oficial e pacotes finais
Testes frontend: PASS, 7.267 testes
Playwright: PASS, 15 testes relacionados; suite completa NOT RUN
Cobertura Go: file-service 90,5%; gate global FAIL em notification
Cobertura frontend: 95,80 / 91,92 / 96,21 / 97,60 (statements/branches/functions/lines)

Complexidade: produção nova/refatorada <= 10; dívidas anteriores não agravadas
SRP: responsabilidades separadas; sem framework novo
Code Smells: lint zero issues; Sonar não medido, confirmação independente pendente

Govulncheck: FAIL, GO-2026-6809 preexistente no baseline
Trivy FS: FAIL, três ocorrências HIGH em dependências existentes
Trivy Config: PASS
Gitleaks: FAIL no histórico; PASS no delta
Semgrep: PASS, 42 regras / 13 arquivos de produção / zero achados
SonarQube: NOT RUN

Quality Gates: técnicos aprovados; cobertura global e segurança reprovadas
Bloqueios remanescentes: notification/coverage, toolchain/dependências e histórico de segredos
Riscos residuais: AV/parser/isolamento não são garantia universal; MP3 recalcula sob limite
Pendências operacionais: INC-001, INC-002, CI, Sonar, canary e drain de N-1

Pronto para Security Re-review independente: SIM (escopo S-001 a S-005)

Code Quality Review anterior: APPROVED
Security Review anterior: CHANGES REQUIRED
Security Re-review: PENDENTE
Canary nchat-dev: NÃO EXECUTADO
Commit: NÃO REALIZADO
Push: NÃO REALIZADO
PR: NÃO CRIADA
============================================================================
```
