# Issue #1128 — correções pós-Code Quality Review

Implementação de correções F-01 a F-06 em 2026-10-21, sobre o delta local da
branch `feature/files-1128-enterprise-formats`. Base local `origin/develop` e
HEAD `4da2bcf`. Sem staging, commit, push, PR, merge, rebase, deploy ou ativação
de perfis. Não é nova Code Quality Review nem Security Review.

## F-01 — contrato MIME e seleção de visualizador

**Causa raiz:** `attachmentDocumentRules.ts` só admitia PDF, `text/plain` e
`application/zip`; a capa repetia a detecção genérica anterior. O hook de
thumbnail não distinguia JSON de planilha de páginas rasterizadas.
**Contrato violado:** os MIMEs canônicos v1 devem selecionar os renderers já
existentes, preservando registros genéricos antigos e download original.

**Arquivos:** `apps/web/src/chat/attachmentDocumentRules.ts`,
`AttachmentDocumentPreview.tsx`, `MessageAttachments.tsx`, `RecentFileRow.tsx`,
`useAttachmentPreview.ts` e `apps/web/src/search/FileResultRow.tsx`.

**Correção:** regras compartilhadas explícitas para família documental,
renderer (`pages`/`sheets`/ausente) e apresentação. Parâmetros MIME são
normalizados. Nome/extensão só auxiliam o fallback legado de contêineres. Um legado com
preview pronto e nome opaco abre pelo manifesto; sua capa nunca é adivinhada como imagem.
Não há regra genérica para `application/vnd.*`, alteração de MIME persistido,
redesign ou autorização pelo frontend. Planilhas não carregam thumbnails como
imagem; formatos sem renderer conservam o acesso ao original quando aprovado.

**Testes:** nova `attachmentDocumentRules.test.ts` e fixtures sintéticas
compartilhadas; regressões em `MessageAttachments.test.tsx`,
`AttachmentDocumentPreview.test.tsx`, `ConversationDetailsRecentFiles.test.tsx`
e `FileResultRow.test.tsx`. Os seis MIMEs reais (DOCX/XLSX/PPTX/ODT/ODS/CSV),
MIMEs legados, parâmetros, arquivos sem renderer e preview JSON são exercitados.
O teste JSON usa Blob `application/vnd.nchat.preview-sheet+json`, verifica
nenhum `<img>` e nenhuma solicitação ao fetcher de thumbnails. E2E ampliado em
`enterprise-files.spec.ts` cobre os seis MIMEs, visualizador e download intacto.

**Resultado:** reprodução inicial: 24 falhas novas esperadas nos quatro arquivos,
79 testes aprovados. O refinamento de compatibilidade também reproduziu três
falhas esperadas para legado ZIP com nome opaco nos três pontos da UI.
Após correção e testes adicionais: 172 aprovados nas seis
suítes focadas; suíte web completa aprovada abaixo.
**Risco residual:** frontend segue dependente do contrato de metadata/manifesto
publicado pelo backend; não concede acesso nem garante segurança do conteúdo.

## F-02 — autoridade do catálogo sobre preview

**Causa raiz:** `PreviewMIME` adaptava MIME para renderers legados e seu fallback
permitia que TXT/ZIP v1 fossem agendados apesar de `Format.Preview == false`.
**Contrato violado:** admissão documental e suporte a preview são independentes.

**Arquivos:** `internal/filepolicy/catalog.go`, `internal/domain/preview.go`,
`internal/service/attachment_service.go`, `preview_service.go` e
`internal/storage/preview_store.go`, sob `services/file-service/`.

**Correção:** `PolicyPreviewMIME` consulta ID, versão e MIME canônico no catálogo;
versão/ID desconhecidos não herdam renderers genéricos. A decisão compartilhada
`InitialPolicyPreviewStatus` serve à finalização e ao worker. O claim transporta
os metadados já persistidos; o worker recusa antes de abrir o objeto ou chamar
renderer. Versão zero conserva o contrato legado. Nenhuma migration nova,
novo estado ou mudança em `RenderDocument`.

**Testes:** `attachment_policy_test.go`, `preview_policy_test.go`,
`preview_service_test.go`, `preview_store_test.go` e
`enterprise_integration_test.go`. Reprodução inicial confirmou TXT/ZIP v1
persistidos como `pending`. TXT/ZIP/MD/ODP agora são `unsupported`, com zero
attempts e nenhum job reclamável no PostgreSQL. O lifecycle interno é `blocked`,
conforme o mapeamento existente da migration 000018; não se altera esse contrato.
CSV/XLSX/DOCX preservam agendamento. Worker não abre storage para formatos
incompatíveis. CSV `text/plain` e DOCX `application/zip` legados geram preview real.

**Resultado:** testes focados e integração real aprovados; download dos originais
permanece intacto, incluindo formatos sem preview.
**Risco residual:** workers/leitores devem seguir o rollout já documentado;
metadados de classificação são internos e não são uma autorização de acesso.

## F-03 — higiene e evidência durável

**Causa raiz:** `.swarm/ewc-fisher.json` é estado local de agente (configuração e
pesos Fisher), alcançado pelo glob de formatação; snapshots/logs temporários
não são evidência versionável reproduzível.
**Contrato violado:** mecanismos de qualidade devem examinar fontes do projeto,
não exigir a formatação de estado de execução de agentes.

**Arquivos:** `.gitignore`, `.prettierignore`,
`docs/testing/enterprise-attachment-policy-validation.md` e esta evidência.

**Correção:** exclusão somente de `/.swarm/`, seguindo a convenção dos outros
diretórios de ferramentas. Conteúdo preservado: SHA-256 comparado ao estado
inicial, sem edição. O snapshot Git foi preservado localmente fora do conjunto
versionável. A documentação histórica mantém números, versões, limitações e
comandos, sem depender de logs ou links temporários. O manifesto do corpus
sintético permanece versionável. A matriz TXT foi alinhada à política v1.

**Resultado:** `make ci-static-repository` aprovado, sem supressão de fontes,
redução de gates ou alteração do estado do agente.
**Risco residual:** logs completos locais não são prometidos como permanentes;
as verificações devem ser repetidas com os comandos e fixtures indicados.

## F-04 — contratos HTTP

**Causa raiz:** faltavam regressões pela camada HTTP das novas respostas e dos
ramos de falha/capability do handler MP3.
**Contrato:** 415 `file_policy_blocked`, `file_content_unrecognized` e
`file_inspection_limit`; erros de lookup/storage/conversão preservam os status
existentes e não revelam mensagens internas.

**Arquivo/testes:** `internal/http/attachment_original_test.go`. Upload multipart
real através do router verifica status, envelope, código e mensagem pública de
cada 415 com erro interno encapsulado. MP3 cobre sucesso, 404 de lookup, 503 de
abertura, 502 de conversão, 403 de quarentena, 401 sem token e capability ausente
(`ok == false`). Verifica bytes, nome e Content-Type de original/derivado.

**Resultado:** contratos aprovados pela camada HTTP; não foi necessário mudar
as respostas de produção para acomodar os testes.
**Risco residual:** esses testes de contrato usam use cases controlados; a
integração de autorização/cifra/storage é exercitada separadamente abaixo.

## F-05 — MP3 sem consulta duplicada e rota documentada

**Causa raiz:** o ramo já-MP3 reentrava no método público `Download`, repetindo
lookup/autorização e atribuindo filename antes de verificar erro de abertura.
**Contrato violado:** abrir o original já autorizado sem duplicar consulta nem
retornar um resultado parcialmente preenchido em erro.

**Arquivos:** `internal/service/attachment_policy.go`, `attachment_service.go`,
`audio_download_test.go`, `fakes_test.go` e `docs/api/file-attachments.md`.

**Correção:** `originalDownload` concentra a abertura/projeção do original após
a autorização; ambos os endpoints o reutilizam. O ramo já-MP3 verifica erro
antes de ajustar nome. Conversões continuam reautorizadas após sua conclusão.
A tabela de rotas documenta `/api/files/attachments/{attachmentID}/audio.mp3`,
`audio/mpeg`, nome `.mp3`, acesso/scan e sua separação de `/content`.

**Testes/resultado:** regressão inicial comprovou duas consultas; corrigido para
uma, sem converter nem alterar bytes. Erros de abertura retornam download vazio;
cancelamento de lookup propaga e não abre storage. Testes existentes de fechamento,
criptografia e rejeição concorrente continuam passando.
**Risco residual:** a autorização do original continua sendo a decisão anterior
à abertura/transferência, como no endpoint canônico existente.

## F-06 — prop morta e comentários

**Causa raiz:** `sentAt` deixou de participar do nome de download, mas continuou
sendo repassado exclusivamente até `VoiceMessageAttachment`.
**Arquivos:** `MessageAttachments.tsx`, seu teste e o chamador `MessageContent.tsx`.

**Correção:** removida a cadeia sem uso dentro de MessageAttachments e sua prop
no chamador; timestamps utilizados pelas demais funções da mensagem permanecem.
Comentários dos testes agora descrevem bytes, formato e extensão nativos e a
conversão pelo endpoint derivado explícito. Assertions válidas foram preservadas.
**Resultado:** typecheck, testes de mensagens/voz e cobertura web aprovados.
**Risco residual:** nenhum adicional identificado nesta correção.

## F-07 — dívida preexistente não agravada

Medição local por gocyclo/gocognit: `Renderer.RenderDocument` **22/22** e
`App.wireAttachments` **12/11**, iguais ao baseline informado. Os arquivos são
idênticos ao início desta rodada. Sem refatoração ampla. Unidades Go novas ou
refatoradas nesta correção ficaram em **até 10/10** na análise local de fonte;
funções/testes antigos acima disso permanecem fora do escopo. Análise de fonte
não substitui análise Sonar de recursão/tipos e maintainability.

## Comandos e resultados

Os comandos são executados na raiz, salvo os `go test ./internal/...`, que usam
`services/file-service`. Para integração, seguir as variáveis e a infraestrutura
descartável descritas na [arquitetura](../architecture/enterprise-attachment-policy.md).

| Estado                                             | Comando / verificação                                                                                                                    | Resultado                                                                                                                                                                                |
| -------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| PASS                                               | Testes focados frontend                                                                                                                  | 172 testes nas seis suítes MIME/preview/recentes/busca                                                                                                                                   |
| PASS                                               | `go test ./internal/service ./internal/http ./internal/storage ./internal/domain ./internal/filepolicy -count=1`                         | Política, worker, HTTP, original e regressões existentes                                                                                                                                 |
| PASS                                               | `go test ./internal/service -run '^TestIntegrationEnterprise' -v -count=1 -timeout=180s` com PostgreSQL/SeaweedFS/ClamAV/conversor reais | 26 originais, oito previews gerados/regenerados, Range, revogação, tenants, guest, DM/grupo, scanner offline/EICAR, reabertura, quatro famílias sem jobs e dois casos legados; sem skips |
| PASS                                               | `make test-go`                                                                                                                           | Todos os módulos                                                                                                                                                                         |
| PASS                                               | `bash scripts/ci/go-test.sh -race`                                                                                                       | Todos os módulos                                                                                                                                                                         |
| PASS                                               | `make web-coverage`                                                                                                                      | 262 arquivos, 7.267 testes; statements 95,80%, branches 91,91%, funções 96,21%, linhas 97,60%                                                                                            |
| PASS, com ocorrência transitória preservada abaixo | `make go-coverage-check` com `LINK_SAFETY_TEST_DATABASE_URL` descartável                                                                 | Metodologia oficial combinando unitários/PostgreSQL; file-service 90,5%; threshold inalterado em 90%                                                                                     |
| PASS                                               | `make ci-static-web`                                                                                                                     | Três warnings preexistentes React Compiler, zero erros; typecheck aprovado                                                                                                               |
| PASS                                               | `make ci-static-go`                                                                                                                      | gofmt/vet/golangci-lint, zero issues                                                                                                                                                     |
| PASS                                               | `make ci-static-repository`                                                                                                              | Exclusão específica de estado de agente, fontes/documentação verificadas                                                                                                                 |
| PASS                                               | `make build-web`                                                                                                                         | Build com aviso preexistente de tamanho de chunk                                                                                                                                         |
| PASS                                               | `make ci-infra-migrations`                                                                                                               | Migrations 000019 preservadas, ordinal/Blue-Green estático aprovado                                                                                                                      |
| PASS                                               | `pnpm test:e2e:web enterprise-files voice-message-download --workers=4`                                                                  | 15 testes; seis MIMEs v1, contêiner legado com nome opaco, tabela JSON, original e voz                                                                                                   |
| FAIL                                               | `pnpm test:e2e:web --workers=4`                                                                                                          | 417 aprovados, seis skipped, quatro falhas; investigação abaixo                                                                                                                          |
| NOT RUN                                            | `make test-integration-go` genérico                                                                                                      | Não inclui suítes file-service; exige outras bases/Valkey. Integração pertinente executada explicitamente acima, sem mudar a arquitetura CI                                              |
| NOT RUN                                            | SonarQube                                                                                                                                | Sem ferramenta conectada e sem SONAR_HOST_URL/SONAR_TOKEN; nenhuma métrica/Quality Gate Sonar inventado                                                                                  |
| NOT RUN                                            | Canary/Blue-Green operacional em nchat-dev e Security Review                                                                             | Fora desta execução local de correções                                                                                                                                                   |

### Ocorrências preservadas e investigação

O primeiro gate Go encerrou no guard de completude de testes PostgreSQL do
chat-service, apontando `TestLinkSafetyCheckValidationDoesNotBlockWritersPostgreSQL`.
O nome já consta da lista. Script e teste foram comparados byte a byte ao HEAD;
são idênticos e não fazem parte do delta. O helper idêntico passou na cópia
intacta; o gate oficial completo também passou em nova execução, sem mudar
lista, threshold, exclusões ou configuração do CI. A causa da ocorrência
transitória não foi estabelecida; o resultado inicial não é apagado por esse PASS.

A execução Playwright completa teve a falha de foco em citações já conhecida,
e três falhas de navegação/ausência de elementos em presença/avatares.
Comparação no HEAD/origin/develop intacto, com servidor novo, quatro workers e
zero retries: 38 aprovados e somente a falha de citações. A comparação no delta, nas mesmas condições, passou nos 39 testes. As três
falhas adicionais não se reproduziram isoladamente; a causa não foi estabelecida. Nenhum teste de presença/citação foi
editado, nenhum timeout aumentado e nenhum retry/force/sleep foi adicionado.

A execução completa isolada (`CI=1 pnpm test:e2e:web --workers=4 --retries=0`)
terminou com **420 aprovados, seis skipped e somente a falha conhecida de
citações**. As três falhas adicionais não se repetiram. Foi executada antes do
último refinamento do fallback legado; o estado final desse refinamento passou
nos 172 testes focados, nos 7.267 testes web e nos 15 E2E pertinentes. Não se
atribui causa definitiva às falhas transitórias, nem se altera presença/citações
para obter aprovação artificial.

A regra ESLint `complexity: [error, 10]` também passou no classificador documental
e no hook de thumbnails. Complexidade cognitiva TypeScript via Sonar não foi
medida. O gate de formatação inclui as fontes novas; não há exceção para elas.

Nenhum percentual de duplicação, hotspot ou zero code smells Sonar é alegado.
Lint sem issues e testes não substituem a confirmação independente posterior.
Os hashes sintéticos e seus procedimentos continuam na
[evidência de integridade](enterprise-attachment-corpus.csv).

## Estado final para encaminhamento

```text
ISSUE #1128 — CODE QUALITY CORRECTIONS
F-01: CORRIGIDO, testes v1/legado nos três pontos da UI
F-02: CORRIGIDO, catálogo governa agendamento/worker; legado preservado
F-03: CORRIGIDO, gate estático aprovado; estado de agente preservado
F-04: CORRIGIDO, contratos HTTP e capability MP3 cobertos
F-05: CORRIGIDO, uma consulta já-MP3 e rota derivada documentada
F-06: CORRIGIDO, cadeia sentAt sem uso e comentários removidos/corrigidos
F-07 (preexisting): NÃO AGRAVADO, 22/22 e 12/11
Testes: regressões pertinentes PASS; suíte E2E ampla FAIL no baseline de citações
Cobertura: Go oficial combinado 90,5%; frontend acima de 90% nas quatro métricas
Quality Gates: estáticos, formato, coverage, build e migrations PASS
Regressões novas: nenhuma confirmada; ocorrências transitórias descritas acima
Pendências: flakiness de citações, diagnóstico das ocorrências transitórias, Sonar e Security Review
Pronto para re-review independente: SIM
Security Review: PENDENTE
Commit: NÃO REALIZADO
Push: NÃO REALIZADO
PR: NÃO CRIADA
```

Isso não significa aprovação para merge. A confirmação de F-01 a F-06 pertence
à revisão independente posterior; perfis continuam desligados e não houve deploy.

## Arquivos novos disponíveis para revisão

Inventário de fontes/evidências não rastreadas no escopo completo da issue,
incluindo os arquivos preservados da implementação inicial. Não houve staging;
esses arquivos não aparecem em `git diff --stat`.

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
migrations/files/000019_attachment_format_policy.down.sql
migrations/files/000019_attachment_format_policy.up.sql
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
services/file-service/internal/http/attachment_original_test.go
services/file-service/internal/service/attachment_policy.go
services/file-service/internal/service/attachment_policy_test.go
services/file-service/internal/service/enterprise_integration_test.go
services/file-service/internal/service/scan_policy.go
```
