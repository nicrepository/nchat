# Evidências locais — anexos empresariais #1128

Execução em 2026-10-20, branch `feature/files-1128-enterprise-formats`, HEAD
`4da2bcf`, com delta sem commit. Implementação local; sem deploy, ativação em
`nchat-dev`, acesso a produção ou reviews formais independentes.

## Implementação e arquitetura

- `services/file-service/internal/filepolicy/`: catálogo v1 com IDs, famílias,
  estratégias, decisões e riscos tipados; parsadores de texto, EML, ZIP, OOXML e
  ODF. Extensão seleciona a gramática; não autoriza conteúdo desconhecido.
- `internal/service/attachment_policy.go`: classificação do objeto já cifrado,
  antes da finalização, compensação existente, reautorização do destino.
  `scan_policy.go`: revalidação antes do AV e consumo completo autenticado.
- `internal/storage/` e migration files `000019`: metadados de versão e formato;
  constraint impede um worker antigo de aprovar upload v1 só mudando status.
- `internal/http/attachment_handler.go`: original sempre nativo; derivado MP3
  explícito em `/attachments/{id}/audio.mp3`, com o mesmo gate de acesso/scan.
  HTTP attachment, nosniff e private/no-store/no-transform.
- `apps/web/src/chat/`: nome/extensão originais também para áudio; erros de
  política compreensíveis. Sem redesign nem mudança da lógica do draft.
- Três novos perfis no mecanismo de configuração do file-service, ausentes/desligados
  por padrão, exigem antimalware; não existe motor paralelo de flags ou regras.
  Métricas usam família/decisão/código, sem nome, hash, usuário ou workspace.

As regras v1 recusam macros, senha, anexos incorporados não inspecionáveis e
conteúdo ativo; `allow` é somente admissão preliminar, nunca resultado AV.
O envelope NCF1, estados, leases, idempotência, storage e pipeline de previews
existentes foram preservados. Não há dependência runtime nova.

Design, alternativas descartadas, threat model, integridade e plano N/N+1 estão
em [arquitetura](../architecture/enterprise-attachment-policy.md) e
[ADR proposto](../adr/0004-enterprise-attachment-policy.md).

## Matriz de suporte e SAP

Consulte [a matriz efetiva](../api/supported-file-types.md), com referências
oficiais a cinco produtos SAP e lacunas do landscape empresarial.

Disponíveis nas condições da matriz: TXT/MD, CSV/TSV, JSON/XML, EML plaintext
single-part, PDF, DOCX/XLSX/PPTX, ODT/ODS/ODP, ZIP com folhas passivas, quatro
formatos de imagem e sete de mídia. Os perfis novos continuam desligados.
Formatos de mídia preservam identificação por assinatura; guardas PDF não são
um parser semântico integral. Essas garantias são diferentes das de ZIP/Office.

DOC/XLS/PPT/MSG/XLSB, RTF/YAML, iWork e outros compactadores permanecem
restritos; macros/HTML/SVG/executáveis/segredos são bloqueados. CAD depende de
validação empresarial e de um inspetor adequado, sem novos uploads liberados.
Outros tipos são não suportados. Nenhuma alegação de suporte universal SAP.

## Integridade e integração real

Comando reproduzível na documentação de arquitetura. Infraestrutura descartável:
PostgreSQL 16 em `127.0.0.1:55128`, banco `nchat_1128_test` com 103 migrations;
SeaweedFS 4.29 em `58130`; ClamAV 1.4 em `53130`; conversor local em `58129`.
Somente fixtures sintéticas, sem dados de clientes/SAP. A suíte final terminou
com `PASS` em 14,941 s, sem skips:

- 26 formatos: sessão/JWT, autorização SQL, admission, multipart HTTP, cifra,
  objeto real, worker/ClamAV, clean e download original real. Comparações de
  bytes, tamanho e SHA-256 em todos. Nomes com Unicode e caixa preservados.
- Oito previews reais gerados e regenerados: PNG, PDF, CSV, XLSX, ODS, DOCX,
  ODT e PPTX. Hash original permanece igual antes/depois de cada derivado.
- Reconstrução por Range cruzando chunks de 64 KiB; revogação de membership;
  negação de original, preview, manifesto e MP3 para pendentes/inacessíveis.
- Isolamento entre workspaces; guest com/sem acesso explícito; DM e grupo,
  incluindo perda de acesso após arquivamento; reabertura do serviço e download.
- Scanner indisponível permanece pendente, retry recupera; EICAR real é
  rejeitado. Tentativa SQL de aprovação por worker anterior falha na constraint.

[Manifesto dos hashes sintéticos](enterprise-attachment-corpus.csv). Hashes de
documentos reais não são persistidos, retornados ou logados; autenticação do
envelope e tamanho bastam ao modelo de integridade existente. Falha após início
do streaming aborta a transferência; não se promete mudar status após headers.

## Testes e gates

Os resultados abaixo registram a implementação de 2026-10-20 e não dependem
da permanência de logs locais. Os comandos e o corpus versionado permitem
repetir as verificações. As correções posteriores estão em
[correções pós-review](enterprise-attachment-quality-corrections.md).

| Comando/medição                                                                   | Resultado observado                                                                                                               |
| --------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| Baseline Go focado antes do delta                                                 | Passou                                                                                                                            |
| `make test-go`                                                                    | Passou nos módulos                                                                                                                |
| `bash scripts/ci/go-test.sh -race`                                                | Passou nos módulos                                                                                                                |
| `go test -race ./internal/filepolicy ./internal/service ./internal/http -count=1` | Passou após os últimos testes e tipos                                                                                             |
| `make ci-static-go`                                                               | Passou: gofmt/vet/golangci-lint, zero issues                                                                                      |
| `make ci-static-web`                                                              | Passou; três warnings preexistentes do React Compiler, zero erros                                                                 |
| `make web-coverage`                                                               | 261 arquivos, 7.206 testes; statements 95,78%, branches 91,90%, funções 96,21%, linhas 97,58%                                     |
| `make go-coverage-check` com PostgreSQL descartável                               | Gate de 90% preservado; file-service 90,5%                                                                                        |
| `make build-web`                                                                  | Passou; aviso de tamanho de chunk existente                                                                                       |
| `make ci-infra-migrations`                                                        | Passou: 103 pares, ordem e verificações Blue/Green estáticas                                                                      |
| `make security-govulncheck`                                                       | Passou em todos os módulos, mesma versão 1.1.4 recompilada com Go 1.26.9 por incompatibilidade do binário local antigo            |
| `make security-trivy-fs` e `make security-trivy-config`                           | Passaram, sem achados na severidade configurada                                                                                   |
| `make security-secrets`                                                           | Falhou: quatro ocorrências históricas preexistentes, abaixo                                                                       |
| `gitleaks dir` sobre cópia dos arquivos do delta                                  | Passou, nenhum vazamento; sem novos ignores                                                                                       |
| `make format-check` / `make ci-static-repository`                                 | Bloqueados somente por formatação de `.swarm/ewc-fisher.json`, já não rastreado no início                                         |
| Subgates restantes de repository                                                  | Configuração, arquitetura, required gates, eligibility e Dockerfile module inputs passaram; ferramentas opcionais ausentes abaixo |
| `python3 scripts/ci/git-conventions-check.py`                                     | Script executado: skipped fora de evento pull_request; não equivale a validação de PR                                             |
| Playwright focado de anexos/voz                                                   | Oito testes passaram                                                                                                              |
| `pnpm test:e2e:web --workers=4`                                                   | 414 passaram, seis skipped, uma falha de foco preexistente confirmada abaixo                                                      |
| Fuzz `FuzzInspect`, 5 s, dois workers                                             | Passou; 13.938 execuções; nenhuma campanha prolongada                                                                             |
| Benchmark texto streaming, uma iteração                                           | 7,5 MiB: 27,15 ms, 289,65 MB/s, 25.608.816 bytes alocados totais/365 alocações; não é pico RSS nem capacidade de produção         |
| Benchmark XLSX sintético, uma iteração                                            | 2.126 bytes: 207 µs, 106.160 bytes alocados/767 alocações; inspeção local, sem rede/cifra/conversão                               |
| gocyclo/gocognit local                                                            | Máximos 10 nos componentes novos e funções de produção alteradas; método HTTP upload preexistente 24/29 permanece inalterado      |

Os testes incluem spoofing/PE, ZIP arbitrário como Office, XML/XXE, relações e
manifestos inconsistentes, macros, fórmulas ativas, senha, duplicados, caminhos,
symlinks, CRC/offset/descriptors, expansão/nesting, budgets, cancelamento, falhas
de storage, scan antecipado/inconclusivo/futuro e compensação. Tests de áudio
distinguem original/MP3 no HTTP e negam derivado após reprovação concorrente.

Complexidade foi medida por análise de fonte. O modo type-aware do gocognit
local reportou erros de carregamento de tipos; não se apresenta sua saída como
medição exata de recursão. SonarQube não foi executado: sem ferramenta conectada
nem `SONAR_HOST_URL`/`SONAR_TOKEN`. Duplicação percentual, hotspots e Quality Gate
Sonar não foram medidos; zero issues de lint não substitui essas medições nem
as reviews futuras.

### Baseline comprovado e ferramentas ausentes

- A falha Playwright é `quote-history-navigation.spec.ts:193`, foco do composer
  após busca por citação, linha 211. Foi reproduzida em cópia intacta de HEAD
  a partir do HEAD `4da2bcf`: mesma falha, outros 14 testes aprovados. Sem
  retries/sleeps/force para mascarar. O resultado histórico das duas execuções está registrado aqui. Não foi alterado o subsistema independente de citações.
- As quatro ocorrências do gitleaks são nas linhas 80/396 de
  `services/chat-service/internal/storage/notification_outbox_promotion_postgres_test.go`,
  nos commits anteriores `53cb7b52` e `1d18b8ee`. O gate percorreu 467 commits;
  o delta sem commit não integra esse histórico. Não se reproduzem valores
  sensíveis, não se altera histórico nem se cria exceção.
- `.swarm/ewc-fisher.json` não foi editado nem ignorado para forçar formatação.
- `yamllint`, `actionlint` e lint remoto GitLab ausentes no subgate de config;
  o script relata skips. Nenhum CI YAML foi alterado.
- `make test-integration-go` genérico não foi executado: seu owner cobre outros
  serviços e exige bancos separados/Valkey; não inclui as suítes file-service.
  A integração real pertinente foi executada explicitamente acima, preservando
  a arquitetura CI existente. Não houve ensaio operacional Blue/Green completo.

## Compatibilidade, rollout e pendências

Migration aditiva, sem estados novos; versão zero identifica legado. Sem backfill
nem hash plaintext persistido. Desabilitar perfis bloqueia novas admissões e
revalida pendências, preservando acesso autorizado aos anexos já clean. Down da
migration recusa remover o fence enquanto houver linhas v1.

Primeiro disponibilizar todos os leitores/workers corrigidos com perfis off;
somente depois ativar famílias gradualmente em `nchat-dev`. O leitor N anterior
converte áudio implicitamente: não garante original nativo nessa operação. Por
isso o plano exige essa fase inicial e mantém a correção de original em rollback.
Ativação/deploy, compatibilidade de duas versões completas em execução e canary
não foram realizados nesta tarefa local.

Pendências reais: baseline de formatação/segredos/Playwright, medições Sonar,
validação do landscape SAP e ativação/ensaio em `nchat-dev`. Tipos restritos
continuam recusados até haver capacidade técnica e política apropriadas.
Não se declara aprovação integral de todos os critérios enquanto esses itens
de validação permanecerem pendentes. As reviews independentes não foram feitas.

Sem commit, push, PR, merge ou mudança em produção. Snapshots de sessão não
integram a evidência versionada. Para inspecionar o estado atual, executar:

```bash
git status --short -uall
git branch --show-current
git diff --stat
git diff --check
git diff --name-status
git ls-files --others --exclude-standard
```

Arquivos novos não rastreados não entram em `git diff --stat`.
