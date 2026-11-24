# Entrega F — composição estrutural e antimalware (#1129 / #1128)

## Preflight e escopo

Implementação local em `omarchy`, `/home/alvaroneto/Projects/nchat`, remoto
`https://github.com/nicrepository/nchat`, branch
`security/files-1129-structural-analysis`. HEAD inicial, merge-base e
`origin/develop` local: `85421bb2e6167b73612e5e4912f1d750f28717d3`.
Não foi feito fetch; a comparação é com a referência local. Não havia delta
tracked/staged. Dois untracked preexistentes foram preservados literalmente:
`'` e `t(files): handle guardian lifecycle errors in ClamAV tests"`.

Governança consultada: CONTRIBUTING.md, SECURITY.md, README.md, ADR-0004,
ADR-0005, contratos de anexos/formatos, arquitetura #1128, baseline e assinaturas B,
fila/fencing C, observabilidade D, runbook de capacidade E e Blue/Green.
Não há AGENTS.md aplicável nem cópia local encontrada do Prompt Mestre NChat v4;
aplicam-se as instruções fornecidas nesta tarefa e os documentos versionados.

Artefatos B–E presentes: `signatures`, `signatureguard`, scanner Guarded,
migrations files/000020–000022, `persist_scan_v2`, `lock_scan_attempt`,
`claim_scans`, reservas globais, métricas e workers por papel. Não foram
reestruturados. Não houve branch/commit/push/PR/merge/rebase/deploy, mudança de
cluster, acesso a dados reais ou alteração produtiva.

## Diagnóstico e design antes da implementação

`filepolicy` da #1128 já existe e continua dono do catálogo v1, identificação,
estratégias e budgets. Textos/JSON/XML/CSV/TSV, EML, ZIP, OOXML e ODF têm
inspetores efetivos. Imagens/mídia usam assinatura; PDF tem framing e guardas
lexicais, sem parser semântico integral. CAD, CFBF/Office legado, MSG,
RAR/7z/TAR/GZIP, macros e conteúdo opaco continuam restritos/bloqueados.
Não se promete identificação universal de polyglots nem suporte SAP completo.

O upload autoriza sessão/destino/workspace, aplica quota/admissão, cifra o
original e chama `AttachmentService.inspectPending`. Reautoriza antes de
`MarkUploaded`, persistindo formato/versão e agendando scan/preview. O worker
revalida `filepolicy.Inspect`, lê/decripta o original e chama ClamAV.
`MalwareScanService` compõe a decisão; `PGXAttachmentStore.MarkScanClean` e
`files.persist_scan_v2` são a autoridade transacional de aprovação do worker.

Exceção já existente: configuração explícita de desenvolvimento permite
`finalizeUpload` gravar clean sem AV para formatos não empresariais; config
recusa isso fora da allowlist de desenvolvimento e perfis empresariais exigem
AV. F não amplia nem remove essa exceção. Nenhuma rota HTTP aceita veredito.

Lacunas confirmadas em `scan_policy.go`: o resultado tipado era descartado;
revalidação estrutural usava apenas o timeout total AV; erros intermediários
não ficavam registrados no leitor de completude. Métricas de inspeção existiam
no upload, mas não recebiam o resultado estrutural assíncrono.

Decisão mínima: reter o resultado #1128 e compor explicitamente com evidência AV
e leitura íntegra; reutilizar o timeout de 10s e o observador de formatos.
Sem parser, catálogo, dependência, flag, schema, política ou estado público novo.
Alternativas rejeitadas: lista de extensões, duplicação de classificadores,
policy engine, spool plaintext, buffer integral e novo ledger SQL de parsers.

Fluxo: autorização/admissão → original cifrado → inspeção #1128 → quarentena
→ revalidação #1128 → AV → composição → persistência fenced → evento → acesso
autorizado. O resultado depende das evidências, não da ordem das inspeções.
Executar primeiro a política evita enviar ao AV conteúdo já bloqueado.

## Contrato interno e precedência

`scanInspection` retém `filepolicy.Result` (formato/família, decisão, código,
versão e estratégia), execução efetiva do scanner, veredito, prova B,
completude da leitura e erro. A estratégia do catálogo define a inspeção
obrigatória de v1; `recognized` atesta sua execução bem-sucedida, sem afirmar
que uma estratégia `signature` é um parser completo. Não há lista genérica de
checklists nem inspector que permite tudo.

| Evidência                                                                         | Composição / estado externo                                                       |
| --------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| Malware confirmado por chamada válida do AV                                       | Rejeição; prevalece mesmo se outra evidência estiver incompleta                   |
| Cancelamento/deadline, erro de I/O/decrypt ou AV                                  | Retry fenced; permanece pending_scan                                              |
| Versão do job futura ou resultado sem versão conhecida                            | unsupported_policy; retry, sem aprovação                                          |
| Política não allow, código não recognized, mismatch ou formato/estratégia ausente | policy_rejected; rejected                                                         |
| Limite estrutural, inválido, unsupported, opaco ou perfil desabilitado            | Mesmo bloqueio externo anterior; decisão/código estrutural na métrica de inspeção |
| AV ausente, desconhecido, leitura sem EOF/tamanho exato                           | Erro de protocolo; retry, sem aprovação                                           |
| Política aprovada + AV clean válido + leitura integral sem falha                  | Elegível; ainda depende da autorização transacional B/C                           |
| Banco/assinaturas/fencing recusam persistência                                    | Nenhum evento de aprovação; retry ou superseded conforme C                        |

Limites determinísticos/indeterminação de formato continuam rejeitados como no
baseline; não são malware. Falhas operacionais são reprocessáveis. A categoria
do retry SQL conserva o vocabulário C. O resultado estruturado não é enviado ao
cliente e erros brutos não são logados. `scanCompletionReader` registra erros:
um EOF posterior não os apaga. `readComplete` exige EOF, tamanho exato e ausência
de erro. Corrupção/integridade nunca aprova o objeto.

Limitação herdada do protocolo: `VerdictInfected` representa FOUND, inclusive
`Heuristics.Limits.Exceeded.*`; o scanner atual descarta o nome da assinatura e
não distingue esses limites de uma detecção na categoria infected. Apenas
StreamMaxLength já tem erro separado. F não inventa essa precisão nem altera
esse contrato de B–E; o limite continua bloqueante. As métricas estruturais,
por sua vez, distinguem inspection_limit, invalid_structure e active_content.

## Recursos, persistência e compatibilidade

Inspeção e AV têm leitores/contextos próprios do mesmo objeto autenticado.
Isso permite cancelar I/O estrutural em 10s, inclusive Open/OpenRange, sem
cancelar o stream AV seguinte. O deadline total do job continua cobrindo ambos;
lease permanece timeout+30s, sem renovação nova. Os parsers existentes cooperam
com cancelamento; não há sandbox de CPU nem recuperação de panic nova.
Nenhum parser foi adicionado nesta entrega.

Os budgets #1128 continuam: texto retido 2 MiB, contêiner/expandido 20 MiB,
256 entradas, razão 100, nesting zero, nomes 255 bytes, XML profundidade/atributos
64, sem extração para disco. Streaming de TXT/mídia conserva o teto de upload.
F não aumenta slots, MaxThreads, memória ou limites. Todo custo estrutural do
worker ocorre depois do claim sob a reserva global E; o upload já usa sua
admissão própria. Há duas passagens lógicas; parsers com seeks podem abrir mais
streams. Não há promessa de redução de latência nem benchmark novo.

`persist_scan_v2` recebe a decisão apenas após composição. O mesmo token/lease,
workspace e row lock protegem status, versão de formato e prova B; writer antigo
ou lease expirado não sobrescreve resultado novo. Falha/remoção/retry perde
autoridade; queda recupera por lease/reserva. Não há publicação antes de commit.
Como no baseline B/C, o SQL confia no worker autorizado para atestar execução;
fencing não autentica resultados fabricados por um processo comprometido.

Evidência estrutural durável continua sendo `format_scan_version` sob o contrato
v1; o resultado detalhado é interno à tentativa e observado pelas métricas,
sem novo histórico persistente por parser. Não se atribui evidência estrutural
a anexos legados clean/version=0. Pendências legadas são revalidadas.

N e N+1 desta F usam a mesma versão v1 e os mesmos parsers/requisitos; ambos já
revalidam a #1128 e exigem leitura AV completa. Não há migration ou aprovação de
família nova que um worker N possa contornar. Workers anteriores à #1128/C
continuam sujeitos às barreiras SQL existentes. Rollback conserva schema e
gates. Um novo requisito estrutural/política incompatível exige evolução de
versão/fence antes da ativação, não apenas editar um booleano.

Avanço de format_policy_version antes do lock final é recusado como conflict.
Flags são configuração local: mudança restritiva exige alinhamento e drenagem
de todas as réplicas conforme #1128; não existe revogação dinâmica global por
flag, nem F invalida históricos indiscriminadamente. Assinatura, engine e
versão de política continuam identidades diferentes.

## Threat model de implementação

| Fronteira / ameaça                                                                   | Mitigação efetiva e lacuna residual                                                               | Evidência de teste                                                   |
| ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| Cliente → gateway → API; MIME/extensão falsa, ativo disfarçado                       | Backend #1128 decide; MIME/extensão não aprovam; catálogo fechado                                 | filepolicy adversarial e StructuralScanUsesRealPolicyBeforeClean     |
| API → storage; original alterado/plaintext                                           | NCF1, DEK vinculada a ID/workspace/tamanho; original imutável                                     | crypto, round-trip HTTP com SHA-256/bytes/tamanho e Range            |
| Worker → storage; truncamento, corrupção, leitura parcial                            | Chunks/tamanho/EOF autenticados; erro latched; passes independentes                               | ScanCompletion*, EarlyScannerApproval*, corrupção existente          |
| Worker → classificador; ZIP/nested bomb, senha, traversal, offset, parser exhaustion | Budgets #1128, CRC/layout, nenhum disco; opacos restritos; 10s e cancelamento                     | filepolicy adversarial, limites, FuzzInspect, StructuralDeadline\*   |
| Parser crash/panic ou polyglot desconhecido                                          | Nenhum parser novo; crash não persiste clean; lease recupera; mídia/PDF têm limitações explícitas | FuzzInspect sem panic no intervalo executado; sem promessa universal |
| Worker → ClamAV; malware, resposta parcial, offline, limite                          | Protocolos estritos, prova B, scan integral e timeout                                             | Scanner/Guarded, EICAR real, early OK, scan limits                   |
| Assinaturas antigas ou geração divergente                                            | Guardian B e revalidação SQL transacional; limitações/ativação B não resolvidas por F             | Suítes B existentes; integração real usa fixture histórica isolada   |
| Worker → PG; lease expirado, stale verdict, política alterada                        | Claim atômico/token, lease/row lock, versão e capacidade E                                        | PG16/17 fencing, stale, conflito de versão, rollback e retry         |
| Persistência → realtime; publicação prematura/perdida                                | Evento somente depois de commit; refetch autoritativo                                             | lostEfficacyEvent verifica banco antes de publicar; refetch          |
| Download/preview → cliente; TOCTOU, IDOR/BOLA/cross-tenant                           | Membership/sessão relidas, workspace derivado, status clean; Range usa mesmo gate                 | Gates HTTP, metadata cross-tenant e suites de autorização existentes |
| Métricas/logs; conteúdo/topologia                                                    | Vocabulário fechado, sem filename/hash/tenant labels; sem erro bruto                              | Testes de métricas/logs; nenhum campo externo novo                   |

Links compartilhados públicos não existem neste contrato. Download, Range,
preview, thumbnails, documentos e áudio derivado mantêm gate autoritativo,
attachment/nosniff/cache existentes. Publicação de mensagens continua permitindo
representar pending/rejected, sem conceder entrega de bytes. Nenhuma UI mudou.

## Etapas e validação

F.1: diagnóstico/design/threat model neste documento; #1128 real reutilizada.
F.2: scanInspection/scanPolicyDecision e testes de completude/precedência.
F.3: coordenação no MalwareScanService; persistência/fencing C inalterados,
suíte PostgreSQL 16/17 reaproveitada.
F.4: resultados dos parsers existentes, casos cifrados negativos, deadline no
storage e fuzz nativo do inspetor existente; nenhuma família nova habilitada.
F.5: métricas #1128 recebem resultados do worker; duração storage_open tem uma
amostra por abertura inicial de passagem, format_validation só após abrir o
objeto. Gates HTTP/original/Range exercitados com corpus sintético e AV real.
F.6: comandos, resultados, limitações e inventário Git registrados abaixo.

Os testes de integração usam runners existentes network-none com PostgreSQL
descartável 16/17, roles runtime reais, ClamAV/Guardian e corpus versionado
montado read-only. O object store HTTP é uma fixture de ciphertext em memória;
não é validação operacional de SeaweedFS nem de Kubernetes. EICAR é sintético,
não prova detecção universal. Não foi feita revisão independente de qualidade
ou segurança, PR review ou aprovação de merge.

## Evidências da execução local

Logs e perfis locais em `/tmp/nchat-1129-f.KiixI49r/` (não versionados).
PASS significa execução concluída; skips de outras integrações não contam como
validação operacional. As execuções B/C/D usam exclusivamente containers próprios.

| Comando                                                                              | Resultado | Evidência                                | Observação                                                                                               |
| ------------------------------------------------------------------------------------ | --------- | ---------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| go test nos pacotes service/http/storage/filepolicy/scanner                          | PASS      | Execução direcionada inicial             | Inclui regressões existentes e novos casos                                                               |
| go test -race -coverprofile nos sete pacotes afetados/dependentes                    | PASS      | race.log, file-service.cover             | service/http/storage/filepolicy/scanner/worker/app                                                       |
| go test -race -coverprofile no service final                                         | PASS      | negative-final.log, service-final.cover  | 92,6%; todos os cenários novos e falhas de storage                                                       |
| bash scripts/testing/scan-queue-postgres.sh                                          | PASS      | postgres.log                             | PG16/17: autoridade, expiração, stale writer, retry, concorrência, rollback, versão e capacidade         |
| bash scripts/testing/clamav-efficacy-isolated.sh                                     | PASS      | clamav-final.log                         | PG16/17 + Guardian/ClamAV real; EICAR, perfil bloqueado, 26 originais e Range por versão                 |
| Reexecução consolidada do runner real                                                | PASS      | clamav-consolidated.log                  | Mesmo corpus após organização final dos asserts                                                          |
| go test filepolicy -run '^$' -fuzz '^FuzzInspect$' -fuzztime=10s -parallel=2         | PASS      | fuzz.log                                 | Fuzz nativo existente: 363.424 execuções, 41 seeds iniciais; não é fuzz de parser novo                   |
| make format-check                                                                    | PASS      | format.log                               | Formatação geral; documentação final também passa Prettier direcionado                                   |
| make ci-static-go                                                                    | PASS      | static-go.log                            | gofmt/vet/golangci-lint de todos os módulos                                                              |
| golangci-lint final no file-service                                                  | PASS      | lint-consolidated.log                    | Zero diagnósticos                                                                                        |
| make test-go                                                                         | PASS      | test-go.log                              | Todos os módulos; integrações opt-in sem ambiente são skips                                              |
| make test-integration-go                                                             | BLOCKED   | integration-go.log                       | CHAT_TEST_DATABASE_URL ausente; alternativa relevante: runners PG16/17/ClamAV acima                      |
| make go-coverage-check                                                               | FAIL      | coverage-gate.log                        | Para antes do file-service: chat-service 83,9% < 90%; infraestrutura de coverage ownership não fornecida |
| make ci-infra-migrations ci-infra-release-safety                                     | PASS      | infra.log                                | Gates estáticos/fixtures; nenhum acesso ao cluster                                                       |
| go build ./services/file-service/...                                                 | PASS      | build.log                                | Backend afetado                                                                                          |
| make build                                                                           | NOT RUN   | package.json                             | Target só compila frontends; sem alteração web/contrato                                                  |
| make security-govulncheck (binário instalado)                                        | BLOCKED   | govulncheck.log                          | Binário compilado com Go 1.25 não carrega pacotes Go 1.26                                                |
| Mesmo gate com govulncheck v1.1.4 recompilado em diretório temporário com Go 1.26.11 | PASS      | govulncheck-compatible.log               | Sem mudar dependência ou ferramenta do workspace                                                         |
| make security-trivy-fs                                                               | PASS      | trivy-fs.log                             | Nenhum HIGH/CRITICAL reportado pelo gate                                                                 |
| make security-trivy-config                                                           | PASS      | trivy-config.log                         | Configuração renderizada em diretório temporário, sem apply                                              |
| make security-secrets / gitleaks no histórico                                        | FAIL      | secrets.log, history-secrets.json        | Quatro ocorrências históricas em dois commits anteriores; nenhuma exceção criada                         |
| gitleaks dir dos arquivos do delta                                                   | PASS      | delta-secrets.log                        | Inclui tracked modificados e untracked da implementação, não os dois preexistentes                       |
| SonarQube / duplicação percentual                                                    | NOT RUN   | Ferramenta indisponível para este agente | Nenhuma alegação de Quality Gate, ratings ou code smells do Sonar                                        |

Gitleaks histórico: `generic-api-key`, arquivo
`services/chat-service/internal/storage/notification_outbox_promotion_postgres_test.go`,
linhas históricas 80/396 nos commits
`53cb7b52b5d7584a7141cfb497b582038d3149c9` e
`1d18b8eea4885becd3d6e7376a2637b0810c20fd`. O conteúdo não foi divulgado nem
modificado nesta entrega; requer triagem própria. Relatórios locais são redigidos.

Cobertura comparativa: HEAD inicial exportado por git archive para diretório
temporário, com GOWORK=off e sem alterar checkout/branch, passa os testes service
com 92,3% (`baseline-real.log`). Delta final: 92,6%; funções decision,
scanPolicyDecision, inspectScanContent, Read e complete: 100%.
Não se confunde cobertura unitária do pacote com o gate de 90% do monorepo.

Complexidade medida com gocyclo/gocognit: nas funções de produção novas ou
alteradas, máxima ciclomática 10 e cognitiva 7. Novas funções de teste também
ficam <=10. A função preexistente refreshQueueDepth continua 12/18 e não foi
alterada. Arquivo malware_scan_service.go já excedia 400 linhas; não houve
fragmentação transversal para satisfazer indicador. Tabelas de teste maiores
que 50 linhas foram mantidas coesas. Nenhum TODO/FIXME/supressão/dependência
introduzido; zero diagnósticos novos do linter, sem métrica inventada de smells.

| Etapa | Arquivos/responsabilidade                                                                                                                                     | Testes e status                                                                                                   |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| F.1   | Este documento; contratos API/formatos                                                                                                                        | CONCLUÍDA; owners #1128/#1129 separados                                                                           |
| F.2   | service/scan_policy.go, scan_policy_test.go                                                                                                                   | CONCLUÍDA; composição, ausência, contradição, precedência, versão, EOF/erro                                       |
| F.3   | service/malware_scan_service.go, structural_scan_test.go                                                                                                      | CONCLUÍDA; pipeline cifrado e falhas; persistência C reaproveitada, PG16/17 PASS                                  |
| F.4   | service/structural_scan_test.go; parsers #1128 inalterados                                                                                                    | CONCLUÍDA; Office benigno/ativo, ZIP falso, macro, CAD, desconhecido, JSON/limites, cancelamento e I/O; fuzz PASS |
| F.5   | service/scan_observability_test.go; http/scan_efficacy_integration_test.go e structural_scan_integration_test.go; scripts/testing/clamav-efficacy-isolated.sh | CONCLUÍDA; métricas existentes, gates, AV real e corpus SHA-256/Range                                             |
| F.6   | Validações e inventário Git local                                                                                                                             | CONCLUÍDA; pendências gerais explícitas, sem revisão independente                                                 |

## Pendências e veredito de implementação

- Bloqueadores para declarar todos os gates PASS: triagem do gitleaks histórico,
  ambiente das integrações gerais/coverage ownership e gate global de cobertura.
- Dependências #1128: inspeção mais profunda de mídia/PDF e parsers de famílias
  restritas exigem trabalho próprio, corpus, budgets e versão compatível; F não
  os habilita nem declara proteção inexistente.
- Validações operacionais pendentes: storage SeaweedFS real nesta execução,
  desempenho/SLO, enforcement CNI/isolamento, assinatura operacional B e rehearsal
  de dois releases. Nenhuma mudança no cluster foi autorizada/executada.
- Melhoria futura: precisão de limites heurísticos AV, ledger detalhado por
  inspeção ou revogação global de política só com necessidade/contrato próprios;
  não são novas garantias desta implementação.

IMPLEMENTAÇÃO CONCLUÍDA — CANDIDATA A CODE QUALITY REVIEW.
Isso não aprova merge, ativação de perfis ou deploy, nem substitui as revisões
independentes posteriores. Delta local preservado para auditoria.

## Inventário Git final

Sete tracked modificados (158 adições, 59 remoções no git diff):

- docs/api/file-attachments.md
- docs/api/supported-file-types.md
- scripts/testing/clamav-efficacy-isolated.sh
- services/file-service/internal/http/scan_efficacy_integration_test.go
- services/file-service/internal/service/malware_scan_service.go
- services/file-service/internal/service/scan_observability_test.go
- services/file-service/internal/service/scan_policy.go

Quatro untracked criados por F, que não aparecem no git diff:

- docs/architecture/structural-analysis-1129.md
- services/file-service/internal/http/structural_scan_integration_test.go
- services/file-service/internal/service/scan_policy_test.go
- services/file-service/internal/service/structural_scan_test.go

Os dois untracked do preflight continuam preexistentes e fora do delta. Não há
staged. Saídas integrais dos seis comandos Git finais em
`/tmp/nchat-1129-f.KiixI49r/git-evidence.txt`, incluindo status, stat, diff check,
name-status, numstat e inventário de untracked. `git diff --check` sem erros.
