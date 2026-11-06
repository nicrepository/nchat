# #1129 B — evidências locais e handoff

Histórico das execuções iniciais. A [remediação consolidada](clamav-signatures-1129-remediation.md)
registra a rodada posterior aos pareceres externos e seus resultados atuais.
Os estados de revisão abaixo pertencem às datas históricas, não aprovam o delta novo.

Implementação: 2026-11-04. Revalidação: 2026-11-05 (seção ao final).
Branch `security/files-1129-clamav-signatures`, base/HEAD
`1e7c91ec35ce638696fe9e68809771929153a679` = origin/develop = merge-base.
**Implementação local testada; entrega não liberada para implantação.**
[Design/threat model](clamav-signatures-1129.md),
[runbook e critérios de abortar](../runbooks/clamav-signatures-1129.md).

## Classificação final

| Área                                                                                                                                          | Estado                                                                                   |
| --------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| FreshClam isolado, verificação oficial CVD, staging/gerações/retention                                                                        | IMPLEMENTED / TESTED em containers descartáveis                                          |
| Snapshot privado, VERSION/PING/sintéticos, restart e evidência por scan                                                                       | IMPLEMENTED / TESTED localmente                                                          |
| Frescor até UPDATE PGX e triggers antes do commit; sem política, novas aprovações fechadas; writers antigos recusados quando a policy está on | IMPLEMENTED / TESTED: cadeia real, PostgreSQL 16/17 e negativos                          |
| CI e cobertura, recursos/identidades/NP candidatas, runbook                                                                                   | IMPLEMENTED / TESTED estático / DOCUMENTED                                               |
| Idade máxima, referência de aprovação e EICAR compartilhado                                                                                   | BLOCKED BY POLICY DECISION; proposta48h, sem default aprovado                            |
| RWX/CNI/DNS/mirror/IP/CA/fsGroup/PIDs/quota/capacidade/clock/baseline                                                                         | PENDING OPERATIONAL VALIDATION / BLOCKED BY INFRASTRUCTURE                               |
| Digest ClamAV atual e imagem instaladora promovida                                                                                            | BLOCKED BY INFRASTRUCTURE / SECURITY: imagem atual com HIGH/CRITICAL; installer só local |
| Code Quality Review / Security Review independentes                                                                                           | PENDENTES; não executados nesta implementação                                            |

## Gates executados

Resultados finais dos comandos abaixo, sem remoção de gates, alteração de
threshold, continue-on-error, alteração do agregador Required ou supressão de CVEs.
Os arquivos de saída foram locais em /tmp; nenhum log contém anexos/credenciais.

| Comando / escopo                                                                                                | Resultado real                                                                                                                                  |
| --------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `make test-go`                                                                                                  | PASS, todos os módulos; suites que exigem infraestrutura continuam opt-in                                                                       |
| `make ci-static-go`                                                                                             | PASS: gofmt, vet e golangci-lint, todos os módulos                                                                                              |
| `go vet ./services/file-service/...`                                                                            | PASS                                                                                                                                            |
| `go test -race ./services/file-service/internal/{scanner,signatures,signatureguard,service,storage,config,app}` | PASS                                                                                                                                            |
| `bash scripts/testing/clamav-signatures-isolated.sh` com perfil retido                                          | PASS: imagem oficial sem rede, rootless, recursos/PIDs limitados, HTTPS local e cadeia PGX real                                                 |
| Testes complementares de retenção e codec HTTP + merge oficial dos perfis                                       | PASS; 621/687 statements = **90,3930%** em scanner/signatures/signatureguard; complementos são locais e não exigem download/engine              |
| `make go-coverage-check` com fixture descartável PostgreSQL16, migrations oficiais e perfil ClamAV              | **PASS em todos os nove módulos**; file-service **90,6%**. Sem variáveis apontando para bancos operacionais                                     |
| `python3 scripts/testing/test_clamav_signature_policy.py -v`                                                    | PASS: oito testes PostgreSQL17                                                                                                                  |
| Mesmo comando com `CLAMAV_POLICY_TEST_POSTGRES=16`                                                              | PASS: oito testes PostgreSQL16                                                                                                                  |
| `make ci-infra-migrations`                                                                                      | PASS: convenções/transações, gate ordinal e invariantes Blue/Green existentes                                                                   |
| `make ci-infra-kubernetes`                                                                                      | PASS: render/checks oficiais + Sealed Secrets; nenhum apply                                                                                     |
| `make ci-infra-config`                                                                                          | PASS: configuração/health/observabilidade/gateway e checks existentes                                                                           |
| Renderer de candidatos com mirror/IP/SC/digest fictícios de teste                                               | PASS Kustomize; CronJob suspenso, consumidor0; valores **não aprovados**                                                                        |
| `trivy config --severity HIGH,CRITICAL --exit-code 1` no candidato renderizado                                  | PASS, zero HIGH/CRITICAL                                                                                                                        |
| `make security-trivy-config` / `make security-trivy-fs`                                                         | PASS, nenhum HIGH/CRITICAL no escopo desses gates                                                                                               |
| `make security-govulncheck` com v1.1.4 recompilado por Go1.26.10 em diretório temporário                        | PASS em todos os módulos; mesma versão da ferramenta, sem alteração de dependências do repo                                                     |
| `make security-secrets`                                                                                         | **FAIL preexistente**, quatro generic-api-key históricos; classificação anterior SR-003 consta em A, sem nova triagem independente nem ignore   |
| Gitleaks dir `--redact` sobre cópia privada do delta                                                            | PASS, sem detecção no delta                                                                                                                     |
| `trivy image --image-src docker --scanners vuln --severity HIGH,CRITICAL --exit-code 1` no digest ClamAV atual  | **FAIL: 8 achados de bibliotecas (1 CRITICAL,7 HIGH)**; impede rollout/liberação até imagem corrigida e revisada                                |
| `CGO_ENABLED=0 go build` do file-service + `bash scripts/testing/build-clamav-signatures-local.sh`              | PASS; só imagem local nchat-clamav-signatures:local-test, nenhum registry/push                                                                  |
| Gocyclo/Gocognit `-over 10` em arquivos Go novos (incluindo testes/entrypoint)                                  | PASS, nenhuma unidade nova acima de10; sem funções recursivas novas                                                                             |
| Python py_compile / bash -n dos scripts novos                                                                   | PASS                                                                                                                                            |
| `make format-check` / `git diff --check`                                                                        | PASS                                                                                                                                            |
| `make ci-static-repository` / `python3 scripts/ci/check_ci_architecture.py`                                     | PASS; job de cobertura é o único owner do ensaio real                                                                                           |
| Semgrep OSS, três regras locais (shell Go/Python e TLS inseguro), metrics off                                   | PASS, zero achados em9 arquivos; cinco arquivos de testes ignorados pelos padrões padrão da ferramenta; não é Security Review independente      |
| SonarQube / E2E frontend / execução GitHub CI Required                                                          | Não executados: Sonar indisponível, UI não alterada, nenhuma PR/push/run remoto. Agregador sem contexto JSON real recusa sucesso, como esperado |

A cobertura oficial por módulo foi 94,4% platform;91,3% admin;90,8% auth;
91,0% chat;92,7% converter;90,6% file;92,4% media;96,0% notification;98,1% search.
Perfis atômicos de unitários, PostgreSQL e ClamAV foram mesclados pelo helper
existente; nenhum statement foi excluído para alterar o threshold. CI futuro
executa a mesma integração com imagens pinadas e retém o perfil somente depois
do sucesso dos testes negativos e da cadeia até o banco.

A função preexistente writeStream tem complexidade cognitiva24 e ficou intacta;
a troca de network e novos comandos locais não alteram seu corpo. Não se alega
redução de dívida preexistente. Smells/rating/duplicação Sonar não foram medidos;
linters não apontam achados novos, o que não equivale a aprovação independente.

## Evidência de segurança da imagem (sem triagem de explorabilidade)

Trivy analisou o digest já existente d97f…4c2, Alpine3.24.1. Achados reportados:

| Pacote instalado              | CVE / severidade                                      | Correção indicada pelo scanner |
| ----------------------------- | ----------------------------------------------------- | ------------------------------ |
| brotli-libs1.2.0-r1           | CVE-2026-111497 / CRITICAL                            | 1.2.1-r0                       |
| libcrypto3 e libssl3 3.5.7-r0 | CVE-2026-14456 / HIGH (dois achados)                  | 3.5.8-r0                       |
| libcurl8.21.0-r0              | CVE-2026-92392 / HIGH                                 | 8.23.0-r0                      |
| pcre2 10.47-r1                | CVE-2026-103111 / HIGH                                | 10.49-r0                       |
| pcre2 10.47-r1                | CVE-2026-86145, CVE-2026-89157, CVE-2026-89161 / HIGH | 10.48-r0                       |

Não há conclusão de falso positivo/mitigação aprovada. Não foi feita instalação
apk, rebuild improvisado do engine nem troca automática de major/minor/digest.
Atualizar assinaturas não corrige essas bibliotecas. O scanner de imagem é um
bloqueio real adicional aos de política/infra, mesmo com testes funcionais verdes.

## Negativos e limites

Foram exercitados: identidade/data ausentes/futuras, expiração exata e em voo,
max-age inválida, versão/flevel incompatível, CVD corrompido, header externo com timestamp rejuvenescido sem mudar o payload (rejeitado pelas ferramentas oficiais), geração com ID falso,
CLD não rastreado, conjunto incompleto, symlink/traversal, lock concorrente,
publicação interrompida antes de state, staging interrompido, falhas de criação/
leitura/escrita, TLS não confiável, mirror offline/429, update sem mudança,
rollback vencido e repetido, falta de original completo/EOF, erro de scanner,
OK precoce, resposta HTTP excedendo4KiB mesmo após JSON válido, child indisponível, perda de state e clocks que retrocedem. Checks
concorrentes de readiness não podem inventar retrocesso do relógio.

Retenção mantém a última geração **carregada**, mesmo após candidatas sucessivas.
Shared lock durante ativação e exclusive lock do publisher/GC foram testados.
Acknowledge de geração já desaparecida bloqueia publicação, preservando rollback.
Esse lock é de ciclo de assinaturas, não fencing/ownership de claims da Entrega C.

A cadeia integrada usa reader cifrado real, guardian/clamd reais, cliente real,
MalwareScanService, PGXAttachmentStore e migration/triggers reais. Prova fresca
persistiu clean+geração; reduzir policy DB a1s manteve o novo objeto pending_scan
sem metadata parcial. Claim e object store são doubles em memória; não é teste
completo SeaweedFS, HTTP upload/download, nem baseline operacional. Regressões de
Range/preview/autorização/envelope/formatos continuam cobertas pela suite Go
existente, sem novo contrato público ou alterações nesses caminhos.

Não foram demonstrados em cluster: RWX/locks entre nós, falha física/ENOSPC do
PVC, NodeLocalDNS/CNI/egress real, reboot/attach, agendamento/quota, carga de
arquivos512MiB+archives com snapshot no mesmo scratch, saúde/alertas no Prometheus,
rede/mirror operacional, digests executados, ou integridade dos originais reais.
Scans bem-sucedidos de fixtures não demonstram detecção de malware desconhecido.

Execuções preliminares detectaram e corrigiram montagem de fixture read-only,
uso inadequado de /dev/null como benigno de compatibilidade, wrappers SQL,
campos/lint e confirmação/GC de geração. Uma execução de script foi invalidada
por edição enquanto rodava e sua saída foi descartada; a execução final ocorreu
com scripts estáveis. Govulncheck instalado com Go1.25 não carregava Go1.26 e foi
recompilado, sem atualizar sua versão. O gate global sem DB falhou em chat por
coverage ausente; a primeira fixture DB limitada a512MiB sofreu ENOSPC. A repetição
com tmpfs4Gi limitada e 6Gi de memória passou, com limpeza dos recursos próprios.
Nada disso foi usado como medição de nchat-dev ou para remover gates.

## Handoff e prontidão

Artefatos locais concretos para revisão independente; **não prontos para rollout**.
Aprovar política e origem, corrigir/revisar imagem vulnerável, promover installer
por digest, confirmar baseline/capacidade/storage/CNI/clock e executar rollout
GitOps autorizado segundo runbook. A integração operacional e configuração de
scrape/alertas continuam pendentes. Não há autorização para nchat-prod/main.

Não houve commit, staging, push, PR, deploy ou edição de recursos Kubernetes
compartilhados. Kustomizations ativos e fatos da Entrega A não foram alterados.
A alteração em CI acrescenta evidência e merge de perfis; Required e thresholds
permanecem. Nenhuma Code Quality Review ou Security Review independente foi
executada/aprovada por este implementador.

O binário novo exige política explícita também na composição da aplicação: sem
idade máxima, a API inicia com o gate de scan obrigatório e o worker não é
iniciado; não existe fallback automático para aprovações do protocolo legado.
As fixtures de testes de lifecycle receberam política explícita de teste. O
codec HTTP verifica o corpo completo limitado antes de aceitar JSON; seu novo
perfil substituiu somente ranges obsoletos do arquivo alterado, com todos os
statements atuais medidos pela suite scanner e merge oficial. O gate global foi
reexecutado com o perfil atualizado e passou. O teste real adicional de header
CVD rejuvenescido passou isoladamente, sem mudanças no payload assinado.

## Revalidação local — 2026-11-05

A solicitação repetida foi atendida retomando os mesmos artefatos locais.
Antes de qualquer edição, branch, HEAD, origin/develop, merge-base, status,
último commit e diff-check foram conferidos novamente: mesma base acima,
Entrega A presente, 15 arquivos modificados e 29 novos, nada staged. Não houve
reescrita da implementação nem alterações no baseline. Nesta retomada, somente
este relatório recebeu novas evidências; os scripts/fontes permaneceram estáveis
durante as execuções.

| Comando / escopo                                                                                                | Resultado desta execução                                                                                             |
| --------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `make test-go` e `go test ./services/file-service/...`                                                          | PASS; todos os módulos no primeiro, incluindo regressões existentes; resultados Go podem reutilizar cache válido     |
| `go test -race ./services/file-service/internal/{scanner,signatures,signatureguard,service,storage,config,app}` | PASS                                                                                                                 |
| `make ci-static-go`                                                                                             | PASS, todos os módulos                                                                                               |
| `CLAMAV_SIGNATURE_COVERAGE_PROFILE=... bash scripts/testing/clamav-signatures-isolated.sh`                      | PASS, nova execução real das ferramentas oficiais, HTTPS local, negativos e cadeia até persistência PGX              |
| Perfil produzido integralmente pelo runner acima                                                                | **621/687 statements = 90,3930%**; nenhum complemento manual ou perfil histórico foi usado nesta revalidação         |
| `make go-coverage-check`, com esse perfil e fixture PostgreSQL16 descartável                                    | PASS, nove módulos; file-service **90,6%**, demais percentuais iguais aos registrados acima                          |
| `python3 scripts/testing/test_clamav_signature_policy.py -v`, PostgreSQL17 e depois16                           | PASS, oito testes em cada versão                                                                                     |
| `make ci-infra-migrations ci-infra-kubernetes ci-infra-config ci-static-repository` (alvos individualmente)     | PASS; checks/render estáticos, sem acesso operacional ao cluster                                                     |
| `make format-check`, sintaxe Python/Bash, renderer com valores fictícios e Trivy config do candidato            | PASS; candidato continua suspenso/zero réplicas e fora dos overlays ativos                                           |
| `make security-trivy-config security-trivy-fs` (alvos individualmente)                                          | PASS                                                                                                                 |
| `make security-govulncheck`, mesma ferramenta v1.1.4 recompilada com Go1.26.10 temporariamente                  | PASS, todos os módulos                                                                                               |
| Gocyclo e Gocognit sobre Go novo; Gocognit v1.3.0 com `-exact-recursion -test`, recompilado com Go1.26.10       | PASS; 107 funções novas analisadas pelo Gocognit, máximo10, sem diagnóstico de tipos na execução final               |
| Gitleaks sobre cópia privada dos 44 arquivos do delta                                                           | PASS, zero detecções                                                                                                 |
| `make security-secrets`                                                                                         | FAIL, mesmas quatro detecções históricas; nenhum ignore ou redução do gate                                           |
| Trivy image no digest oficial existente, HIGH/CRITICAL                                                          | FAIL, mesmos **8 achados: 1 CRITICAL e 7 HIGH**; permanece bloqueio de liberação                                     |
| Semgrep OSS, mesmas três regras preventivas locais, metrics off                                                 | PASS, zero achados em9 arquivos; cinco testes ignorados pelos padrões da ferramenta, sem alegar revisão independente |

Gocognit instalado com Go1.25 emitia diagnóstico de tipos ao resolver chamadas
Go1.26. Foi recompilada a **mesma versão** em diretório temporário, como no
govulncheck; a execução final por pacote, incluindo testes e resolução exata de
recursão, não emitiu diagnósticos. Nenhuma dependência do projeto foi alterada.

A documentação oficial de gestão de assinaturas, Docker e suporte do ClamAV foi
consultada novamente. Não foram obtidos novos dados de StorageClass, CNI,
capacidade, baseline, mirror ou política humana. SonarQube continua indisponível
a este executor. O build local já documentado não foi repetido nesta retomada.
Os containers e volumes das fixtures desta execução foram removidos pelos
runners; nenhuma limpeza ampla de Docker foi usada.

**Estado preservado: IMPLEMENTED / TESTED localmente / DOCUMENTED; não pronto
para rollout.** Permanecem BLOCKED BY POLICY DECISION (idade máxima/origem e
autorização operacional), BLOCKED BY INFRASTRUCTURE (imagem corrigida/aprovada,
installer promovido e pré-requisitos reais) e PENDING OPERATIONAL VALIDATION.
Sem commit, staging, push, PR, deploy, revisão independente ou aprovação AppSec.
