# Go #1173 — GO-2026-6859 / CVE-2026-97054

Validação local em 2026-11-11. Parecer: **IMPLEMENTATION READY FOR REVIEW** para a correção da toolchain. Não constitui aprovação independente, autorização de merge ou de rollout.

## A. Diagnóstico e baseline

Base capturada de `origin/develop`: `aec70c896bd76e37e3815852f5c7d97aba39f919`. O fetch final confirmou o mesmo SHA. Branch isolada: `security/go-1173-stdlib-nethttp-race`, worktree `/home/alvaroneto/Projects/nchat-go-security`. HEAD permanece na base; nenhuma alteração foi commitada.

O [advisory oficial](https://pkg.go.dev/vuln/GO-2026-6859), também disponível como [JSON oficial](https://vuln.go.dev/ID/GO-2026-6859.json), trata da **stdlib**, não de `golang.org/x/net`. Sob HTTP/1, ultrapassar o limite de `MaxBytesReader`/`MaxBytesHandler` enquanto outra goroutine altera headers pode provocar escrita concorrente no mapa de headers e derrubar o processo. O [backport oficial](https://go.dev/issue/82563) substitui a alteração de headers em `response.requestTooLarge` por sinalização atômica. [Go 1.26.11 foi publicado em 2026-11-10](https://go.dev/doc/devel/release), com a correção; a outra linha corrigida é 1.27.4. Mantivemos a linha 1.26.

O Go efetivo do baseline era `go1.26.10`, `GOTOOLCHAIN=auto`, com GOROOT da toolchain baixada. O launcher do host continua Go 1.25.10; não foi alterado globalmente. O run 43021422185 da PR #1172, head `302b0650e886955304ee990fdb494b1fc68361f9`, confirmou o gate recusando o advisory. Logs e metadados foram obtidos com acesso somente de leitura.

A primeira execução local do scanner instalado falhou ao carregar pacotes: govulncheck v1.1.4 havia sido compilado com Go 1.25.13. Isso foi registrado como falha de ferramenta. Recompilamos **a mesma versão** do scanner com 1.26.10 e reexecutamos o script oficial no baseline: os nove módulos foram analisados e o gate terminou com `GO-2026-6859 is called and not accepted`.

Uma análise JSON adicional do baseline registrou os seguintes números de findings com símbolo para o advisory: platform 44, admin 47, auth 48, chat 54, document-converter 21, file 54, media 48, notification 50, search 49. São contagens de findings do scanner, não de endpoints exploráveis. Exemplos de caminhos estáticos incluem `AttachmentHandler.Download → serveDownload → http.ServeContent` e `TokenValidator.ValidateAccessToken → jwt.Parser → json.Decoder → (*maxBytesReader).Read`. O callgraph abstrato alcança símbolos da stdlib; isso não demonstra que cada handler realiza a concorrência necessária ao exploit. Não executamos exploit nem testes contra ambientes operacionais.

Exposição potencial (stdlib afetada), alcançabilidade estática (diagnóstico do scanner) e explorabilidade real (pré-condições de concorrência e entradas) são evidências distintas. A remoção da versão afetada independe de comprovar um exploit específico do NChat.

## B. Inventário de versões

| Referência                                                   | Classificação e estado final                                                                                                                       |
| ------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `go.work`                                                    | Mínimo `go 1.26.11`, `toolchain go1.26.11`; os nove módulos foram preservados                                                                      |
| Nove `go.mod`, listados abaixo                               | Mínimo 1.26.11 para builds independentes; nenhuma dependência alterada                                                                             |
| `.github/workflows/ci.yml`, `.github/workflows/security.yml` | `GO_VERSION=1.26.11`, `GOTOOLCHAIN=local`; setup-go usa a variável existente                                                                       |
| `.gitlab-ci.yml`                                             | Versão antes 1.25.10 e imagens 1.25-bookworm; agora 1.26.11, seleção local e imagens oficiais pinadas por digest                                   |
| `Dockerfile.service`                                         | Builder oficial 1.26.11-alpine pinado; seleção local                                                                                               |
| `Dockerfile.document-converter`                              | Builder oficial 1.26.11-bookworm pinado; seleção local, `GOWORK=off` preservado                                                                    |
| `README.md`                                                  | Bootstrap ativo exige Go 1.26.11                                                                                                                   |
| Workflows de build/publicação                                | Compilam pelos Dockerfiles acima; nenhuma seleção alternativa de Go encontrada                                                                     |
| Utilitário `clamav-signatures`                               | Script existente compila pelo workspace; Dockerfile scratch recebe o novo executável                                                               |
| Migrations                                                   | Scripts Bash/psql e PostgreSQL; não dependem de binário Go próprio                                                                                 |
| Web/admin                                                    | Node/nginx; não compilam os serviços Go                                                                                                            |
| Documentos históricos/ADRs/relatórios anteriores             | Referências antigas preservadas como histórico, não como versão ativa de build                                                                     |
| Traefik, Prometheus, Jaeger, Trivy e Gitleaks de terceiros   | Distribuições binárias externas; não são recompiladas pelo pin do NChat. Versões embarcadas de Go não foram auditadas nem atualizadas nesta tarefa |

A busca incluiu arquivos ocultos, workflows, scripts, infra, Dockerfiles, módulos, Makefile e documentação. `go.work.sum` e todos os `go.sum` permanecem idênticos ao baseline. Não houve atualização de `golang.org/x/net`: a correção do Caio, PR #1171, já integra a base com v0.62.0.

## C. Implementação e arquivos

São 16 arquivos rastreados modificados e este documento novo:

| Arquivo                                        | Alteração                                                  |
| ---------------------------------------------- | ---------------------------------------------------------- |
| `.github/workflows/ci.yml`                     | Toolchain corrigida e seleção local                        |
| `.github/workflows/security.yml`               | Mesma toolchain para análise de segurança                  |
| `.gitlab-ci.yml`                               | Alinhamento dos dois jobs Go ao builder corrigido e pinado |
| `Dockerfile.service`                           | Builder corrigido para sete serviços                       |
| `Dockerfile.document-converter`                | Builder corrigido do conversor                             |
| `go.work`                                      | Versão mínima e toolchain corrigidas                       |
| `libs/go/platform/go.mod`                      | Somente a diretiva `go`                                    |
| `services/admin-service/go.mod`                | Somente a diretiva `go`                                    |
| `services/auth-service/go.mod`                 | Somente a diretiva `go`                                    |
| `services/chat-service/go.mod`                 | Somente a diretiva `go`                                    |
| `services/document-converter/go.mod`           | Somente a diretiva `go`                                    |
| `services/file-service/go.mod`                 | Somente a diretiva `go`                                    |
| `services/media-service/go.mod`                | Somente a diretiva `go`                                    |
| `services/notification-service/go.mod`         | Somente a diretiva `go`                                    |
| `services/search-service/go.mod`               | Somente a diretiva `go`                                    |
| `README.md`                                    | Requisito de bootstrap                                     |
| `docs/security/go-1173-stdlib-nethttp-race.md` | Diagnóstico, validação e handoff                           |

Comparações estruturais dos workflows confirmaram que somente seleção de Go/GOTOOLCHAIN e as duas imagens GitLab mudaram. Jobs, required checks, permissões, actions pinadas, caches, thresholds e contratos de release foram preservados. Cada `go.mod` é exatamente o baseline com uma única mudança da diretiva `go`. Não houve alteração de handlers, autenticação, sessões, workers, fencing, migrations, manifests operacionais ou configurações de scanners.

## D. Supply chain e artefatos

Os índices multiarch foram obtidos de `docker.io/library/golang`, namespace da [imagem oficial](https://hub.docker.com/_/golang). O SHA-256 do corpo de cada manifesto foi comparado ao header `Docker-Content-Digest` e ao RepoDigest efetivamente obtido pelo Docker:

| Builder                   | Digest do índice usado                                                    |
| ------------------------- | ------------------------------------------------------------------------- |
| `golang:1.26.11-alpine`   | `sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb` |
| `golang:1.26.11-bookworm` | `sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0` |

Ambas as imagens responderam `go version go1.26.11 linux/amd64`, sem rede e com filesystem somente leitura. A toolchain local foi obtida pelo mecanismo de módulos do Go, com verificação sumdb. Os metadados oficiais de download também confirmam a versão; não alegamos ter baixado/verificado separadamente o tarball de go.dev.

Construímos as oito imagens finais de serviços, incluindo o conversor, e a imagem scratch do utilitário. Extraímos os executáveis **das imagens finais**; `go version -m` confirmou `go1.26.11` em todos. Govulncheck em modo binário encontrou **zero findings de GO-2026-6859** nos nove executáveis. A biblioteca platform está ligada aos serviços, não é um décimo executável.

SHA-256 dos executáveis extraídos (linux/amd64, artefatos locais desta execução):

| Executável             | SHA-256                                                            |
| ---------------------- | ------------------------------------------------------------------ |
| `admin-service`        | `78df09584adf40dbbb826eb1a57226439e2c25b65e5d64f14fc1084c6786cc0b` |
| `auth-service`         | `a0d64f569f638c4ac706a8270750f1729758113a8b251423abcd930703e82861` |
| `chat-service`         | `c2f9ba2ee304a014f535c8dca264f1ca7b4c6f551ecf974ead0455a5a702a2ac` |
| `clamav-signatures`    | `a01ebfe9ec954353819beed7876500a51172875f6053e4c8e648e40aa6a0c46b` |
| `document-converter`   | `85a0f6b2ec8233ce20bad88c4be7cd479de121612e21660d95a67dc40872dcd8` |
| `file-service`         | `2b29b0ca1b7f23c118ccadbac33cd4a76da6fff4473bd5d3c390e3d615ad735f` |
| `media-service`        | `adcc09a58a0a40d7aaa0745b5ea72004078a009b23e4607baa38f1cd7771d235` |
| `notification-service` | `e35dcf28bb078dc312542609ee72b51c562da267dd0defe1ebf4d58507cfa076` |
| `search-service`       | `08dee804415deec006a832de3a518fcadbdfde1a04290f47b8b980fa4af6376d` |

As tags locais usam o prefixo `nchat-go1173-` e não foram publicadas. Os image IDs, comandos completos e build infos estão em `image-results.json` e `candidate-image-result.json` no diretório de evidências. O novo digest de builder invalida os layers de compilação antigos; não copiamos executáveis de imagens anteriores. Mantivemos o fluxo multi-stage e o usuário de runtime existentes. As bases runtime preexistentes têm aliases mutáveis: este patch fixa o builder, não promete reprodução bit a bit de todo o sistema operacional da imagem. Builds de outras arquiteturas não foram executados.

Rebuild/rollout futuro: revisar e versionar este snapshot; executar os builds oficiais a partir dele; conferir `go version -m`/proveniência dos executáveis; promover somente os novos digests após gates e autorização operacional. Instâncias já implantadas não foram corrigidas por uma mudança local. Para rollback de aplicação, recompilar o código anterior com toolchain corrigida; não reutilizar imagens compiladas com 1.26.10. Nenhum deploy ou promoção ocorreu.

## E. Compatibilidade

A mudança é um patch da linha 1.26, sem mudanças de APIs da aplicação. Os nove módulos continuam organizados como antes. Go 1.26.10 com `GOTOOLCHAIN=local` recusou tanto o workspace quanto `document-converter` com `GOWORK=off`, citando o mínimo 1.26.11: isso verifica que os builds não aceitam silenciosamente o compilador vulnerável.

`GOTOOLCHAIN=local` nos builders e runners impede substituição automática de compilador. Ferramentas instaladas via `@latest` que futuramente exigirem Go mais recente poderão falhar explicitamente; isso não deve ser contornado removendo o mínimo corrigido. Nenhuma versão de ferramenta no repositório foi atualizada por este patch.

## F. Segurança e revisão focada

Executados com Go 1.26.11, govulncheck v1.1.4 recompilado com essa toolchain, golangci-lint 2.15.0 e Trivy 0.75.0:

| Comando                                                 | Resultado real     |
| ------------------------------------------------------- | ------------------ |
| `python3 scripts/security/test_govulncheck_gate.py`     | PASS, 15 testes    |
| `bash scripts/security/govulncheck.sh`                  | PASS, nove módulos |
| `make security-govulncheck`                             | PASS, nove módulos |
| `make security-trivy-fs`                                | PASS               |
| `make security-trivy-config`                            | PASS               |
| `python3 scripts/ci/governance-secret-markers-check.py` | PASS               |

A base Trivy utilizada foi atualizada em 2026-11-11. Nenhuma exceção para GO-2026-6859 foi criada. `.govulncheckignore.yaml`, gate Python, scripts de análise e thresholds estão intactos; o gate permanece fail-closed.

**Resultado adicional que não deve ser confundido com os gates acima:** ao fornecer os relatórios de análise binária ao gate, a verificação suplementar terminou com exit 1 para GO-2026-5932, GO-2026-6094, GO-2026-6163, GO-2026-6165 e GO-2026-6865. O pipeline oficial usa análise de código-fonte/callgraph e passou. Um binário de media-service reconstruído do baseline imutável com Go 1.26.10 reportou os mesmos cinco IDs, além de GO-2026-6859: não foram introduzidos por este patch.

A contraprova de imports mostrou que os pacotes openpgp (5932) e fse/huff0 (6865) reportados nesse modo não estão no conjunto de imports dos entrypoints correspondentes. Já cel-go/ext (6094), pion/stun/v3 (6163) e pion/dtls/v3/pkg/protocol/handshake (6165) estão importados em media-service; a análise de código-fonte `./...` repetida não encontrou chamadas vulneráveis. As conclusões de modo binário são mais amplas e não equivalem a provar uma chamada explorável. Esses diagnósticos precisam de triagem separada; não foram suprimidos, aceitos como risco ou declarados corrigidos aqui.

Revisão manual de segurança pelo próprio implementador, **sem independência**: assets são disponibilidade HTTP e integridade dos artefatos; fronteiras são entrada HTTP não confiável e supply chain runner/registry/binário. O invariante é que todo executável próprio reconstruído use stdlib corrigida, sem exceção no scanner. Foram verificados os dois builders, seleção dos runners, mínimos por módulo, origem/digests, artefatos finais e invariantes do gate. Não foram introduzidas permissões, credentials, rotas ou caminhos de aprovação. O advisory alvo está corrigido nos artefatos locais; a explorabilidade dos outros diagnósticos e os binários de fornecedores permanecem fora desta correção. Isso não comprova ausência geral de vulnerabilidades nem autoriza merge/deploy.

## G. Validações funcionais e limitações

| Comando/suite                                             | Resultado                                                                     |
| --------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `make test-go`                                            | PASS, nove módulos                                                            |
| `make ci-static-go`                                       | PASS, gofmt/vet/lint dos nove módulos                                         |
| `bash scripts/ci/go-test.sh -race`                        | PASS, nove módulos                                                            |
| `make test-integration-go`                                | PASS, fixtures próprias PostgreSQL 16.15 e Valkey 8.1.3                       |
| `make go-coverage-check`                                  | PASS, perfis complementares reais, threshold original 90%                     |
| `make ci-static-repository`                               | PASS do target; SKIPs opcionais descritos abaixo                              |
| `make ci-infra-config`                                    | PASS do target; SKIP opcional de promtool                                     |
| `make ci-infra-kubernetes`                                | PASS                                                                          |
| `make ci-infra-migrations`                                | PASS                                                                          |
| `make ci-architecture-check`                              | PASS                                                                          |
| `git diff --check`                                        | PASS                                                                          |
| `python3 scripts/testing/test_clamav_signature_policy.py` | PASS, 16 testes em cada PostgreSQL descartável 16/17                          |
| `python3 scripts/testing/test_qualify_clamav_rwx.py`      | PASS, três testes locais                                                      |
| `bash scripts/testing/clamav-signatures-isolated.sh`      | PASS, clamd/guardian reais isolados, stream cifrado e persistência PostgreSQL |

Cobertura por módulo:

| Módulo               | Cobertura |
| -------------------- | --------: |
| platform             |     94.4% |
| admin-service        |     91.3% |
| auth-service         |     90.8% |
| chat-service         |     91.0% |
| document-converter   |     92.7% |
| file-service         |     90.7% |
| media-service        |     92.4% |
| notification-service |     95.9% |
| search-service       |     98.1% |

A integração oficial exercitou ownership/presence/read de chat, criação de canais, admin, auth, media com tag integration, autorização/link safety de search e WebSocket/Valkey com race detector. A cobertura usou `MIGRATIONS_DATABASE_URL`, `LINK_SAFETY_TEST_DATABASE_URL`, `OWNERSHIP_COVERAGE_DATABASE_URL` e `CLAMAV_SIGNATURE_COVERAGE_PROFILE` nas fixtures próprias. Isso fornece regressão de handlers, middlewares, sessões, uploads/streaming, HTTP, realtime e workers dentro das suites existentes; não é um teste de todos os fluxos operacionais.

SKIP pelos scripts oficiais: yamllint, actionlint e gitlab-ci-lint não instalados; promtool via Docker indisponível na execução opcional. O parser YAML Ruby e os validadores próprios executaram. Tests opt-in fora da seleção das suites oficiais, incluindo famílias legadas de PostgreSQL/SeaweedFS, não foram todos executados; skips de unit/race não foram contabilizados como integrações aprovadas. Não houve E2E web, execução remota do Actions, benchmark de produção ou testes com anexos reais. Os fixtures ClamAV e PostgreSQL não ativaram políticas em clusters.

Duas falhas de preparação foram preservadas nas evidências e corrigidas antes dos resultados finais: (1) GOROOT herdado apontava para Go 1.25.10, mesmo com binário 1.26.11 no PATH; causou erros de compilador/stdlib e scanner. Os reruns removeram GOROOT/GOTOOLDIR herdados e confirmaram ambos em 1.26.11. (2) o primeiro PostgreSQL descartável tinha tmpfs de 512 MiB, lotado por WAL; houve SQLSTATE 53100. A fixture foi substituída por volume próprio e a integração e a cobertura completas passaram. Nenhuma falha foi corrigida alterando código da aplicação, ignorando exit codes ou reduzindo gates.

## H. Integração com frentes paralelas

A base já contém #1171 (Caio/x/net); não trouxemos alterações de #1168 ou #1172. Nenhum arquivo de frontend, migration de arquivos, implementação de fencing, ClamAV candidato ou política de segurança operacional foi modificado. SEC-B-002 não é remediado por esta atualização.

Se outra frente editar `go.mod`, preservar seus blocos require/replace e manter o mínimo 1.26.11 ao resolver conflitos. Não houve rebase ou merge; origin/develop permaneceu no SHA capturado até a última conferência. Worktrees de outras tarefas e seus arquivos não foram modificados.

## I. Git e evidências

Branch e HEAD informados na seção A; zero staged, 16 modified e um untracked novo deste relatório no worktree da tarefa. O worktree original `feature/files-1129-scan-queue-fencing`, HEAD `640cc67bef04a68646e9c5cdfa89e53bdb8beb93`, e seus dois arquivos untracked preexistentes foram conferidos por SHA-256 e preservados. Não houve git add, commit, push, PR, merge, rebase, deploy ou operação em nchat-dev/nchat-prod.

Evidências locais: `/tmp/nchat-go-1173/`. Incluem JSON oficial, manifests/digests, baseline do scanner e run do CI, resultados por comando, logs de integrações/cobertura/ClamAV, build infos e hashes dos binários, contraprovas de imports e snapshots de integridade. O manifesto final `final-sha256.json` é externo ao repositório para não ser autorreferente. São artefatos locais da sessão, não armazenamento permanente; preservar os logs necessários antes de descartá-los.

## J. Parecer e próximo passo

**IMPLEMENTATION READY FOR REVIEW**, com a correção local da toolchain e dos artefatos próprios demonstrada. Submeter o diff e as evidências a revisão independente, versionar somente após aprovação humana e então reexecutar o CI remoto. Não equivale a autorizar rollout, a resolver todos os advisories de terceiros ou a aprovar SEC-B-002.

Riscos residuais: binários já implantados exigem rebuild/rollout autorizado; três diagnósticos de dependências importadas demandam triagem separada; imagens/ferramentas de fornecedores e arquiteturas além de amd64 não foram auditadas neste patch. Os linters opcionais indisponíveis precisam executar no ambiente de CI que os forneça.
