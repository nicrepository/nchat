# Issue #1149 — remediação de CVE-2026-116587

Validação local em 2026-10-29, branch `security/deps-1149-go-jose-livekit`,
base `develop` / `581c75f11d2203a1f957b8f7e33aab9ddff555eb`.
Implementação encerrada após validação; revisões independentes ficam para a etapa seguinte.

## Causa e escolha de versão

`media-service/internal/service` importava `livekit/protocol/auth@v1.44.1`,
que usava `go-jose/v3`; MVS selecionava v3.0.5. A CVE permite consumo excessivo
de CPU no parsing de JWKs com chaves RSA. O
[advisory upstream](https://github.com/go-jose/go-jose/security/advisories/GHSA-mgcc-cww5-fp2q)
não oferece versão corrigida de v3. A primeira correção em v4 é
[v4.1.6](https://github.com/go-jose/go-jose/releases/tag/v4.1.6).

Foram consultadas todas as versões publicadas entre v1.44.1 e v1.53.1 pelo
proxy oficial Go, incluindo seus manifests. A menor atualização oficial que
remove v3 é **LiveKit Protocol v1.46.7**:

| LiveKit Protocol | Go mínimo   | go-jose declarado                   |
| ---------------- | ----------- | ----------------------------------- |
| v1.44.1          | 1.24.0      | v3.0.4; selecionado v3.0.5 no NChat |
| v1.45.0–v1.46.6  | 1.24.0–1.26 | v3.0.4 ou v3.0.5                    |
| v1.46.7          | 1.26        | v4.1.4                              |
| v1.53.0          | 1.26.0      | v4.1.4                              |
| v1.53.1          | 1.26.0      | v4.1.5                              |

Manifests oficiais: [v1.46.6](https://github.com/livekit/protocol/blob/v1.46.6/go.mod),
[v1.46.7](https://github.com/livekit/protocol/blob/v1.46.7/go.mod) e
[v1.53.1](https://github.com/livekit/protocol/blob/v1.53.1/go.mod).
O projeto, CI e imagem Go já usam **1.26.10**; nenhuma atualização de toolchain foi necessária.

O módulo exige explicitamente `github.com/go-jose/go-jose/v4 v4.1.6`.
O teste de interoperabilidade usa essa biblioteca para verificar tokens do SDK
com outro parser e mantém a exigência estável após `go mod tidy`.
MVS eleva os requisitos v4.1.4 de LiveKit e gRPC para v4.1.6.
Nenhum `replace` entre majors, fork, suppression ou redução de gate foi introduzido.

## Grafo antes e depois

```text
Antes, pacotes de produção:
media-service/internal/service
  -> livekit/protocol/auth@v1.44.1
    -> go-jose/v3@v3.0.5

Depois, pacotes de produção:
media-service/internal/service
  -> livekit/protocol/auth@v1.46.7
    -> golang-jwt/jwt/v5@v5.3.1

Depois, seleção de módulos por MVS:
media-service -> go-jose/v4@v4.1.6 (exigência explícita e testes de interoperabilidade)
livekit/protocol@v1.46.7 -> go-jose/v4@v4.1.4 (requisito upstream)
grpc@v1.83.2 -> go-jose/v4@v4.1.4 (requisito upstream)
Versão efetiva de v4: v4.1.6
```

Tanto com `GOWORK=off` quanto no workspace:

- `go mod verify`: todos os módulos verificados.
- `go list -m all`: somente `github.com/go-jose/go-jose/v4 v4.1.6` para go-jose.
- `go mod graph`: nenhuma ocorrência de `go-jose/v3`.
- `go mod why -m github.com/go-jose/go-jose/v3`: módulo não necessário.
- `go mod why -m github.com/go-jose/go-jose/v4`: caminho pelo teste de interoperabilidade.
- `GOWORK=off go mod tidy` e `go mod tidy -diff`: sucesso, sem delta pendente.

Os requisitos v4.1.4 nas arestas upstream e os checksums históricos de manifests
em `go.work.sum` não representam a versão selecionada.
`go-jose/v3` foi removido de `media-service/go.mod` e `go.sum`.

## Atualizações transitivas

A mudança explícita foi limitada a LiveKit e ao piso de segurança de go-jose.
As demais versões decorrem dos requisitos do novo LiveKit e de seu fechamento
transitivo; não foi usado `go get -u`. No grafo completo de workspace, incluindo
dependências de testes e ferramentas upstream, há 67 seleções adicionadas ou
alteradas e 20 módulos removidos. Isso não significa 67 novos imports de produção.

Exemplos dos requisitos obrigatórios do LiveKit v1.46.7:

| Dependência                | Antes   | Depois  |
| -------------------------- | ------- | ------- |
| livekit/psrpc              | v0.7.1  | v0.7.2  |
| pion/webrtc/v4             | v4.1.2  | v4.2.11 |
| pion/dtls/v3               | v3.0.6  | v3.1.3  |
| pion/stun/v3               | v3.0.0  | v3.1.4  |
| nats-io/nats.go            | v1.43.0 | v1.52.0 |
| redis/go-redis/v9          | v9.11.0 | v9.20.0 |
| prometheus/client_golang   | v1.22.0 | v1.23.2 |
| google/cel-go              | v0.25.0 | v0.28.1 |
| buf.build/go/protovalidate | v0.13.1 | v1.2.0  |
| go.uber.org/zap            | v1.27.0 | v1.28.0 |

Pion passa também a exigir transport/v4 e turn/v5; xsync/v3 é substituído por
xsync/v4 no fechamento carregado pelo media-service. `go-strcase` deixa de ser
exigido diretamente no manifest gerado por tidy; no grafo completo passa de
v1.3.1 para v1.3.0, exigido por protoyaml. A comparação completa, com módulos
requisitantes, está na evidência local `/tmp/nchat-1149/dependency-delta.tsv`.
Nenhum manifest de outro serviço foi editado. Os testes de todo o workspace
verificam o efeito compartilhado dessas seleções.

## Compatibilidade funcional

O LiveKit mudou a implementação de `auth` para `golang-jwt/jwt/v5`.
`Verify` retorna agora `*jwt.RegisteredClaims`; apenas a leitura de `Expiry`
foi adaptada para `ExpiresAt.UTC()`, no signer e no teste existente.

HS256, header JWT, issuer, subject/identidade, nome, sala, fontes permitidas,
TTL e expiração UTC foram verificados. A emissão passa a adicionar `iat`,
com o mesmo instante de `nbf`; tokens com o formato anterior, sem `iat`,
continuam sendo aceitos. A tolerância temporal do verificador permanece em
um minuto, conforme o código upstream. Os testes negativos usam distância
de cinco minutos para evitar ambiguidade nessa tolerância.

Os casos negativos cobrem segredo errado/ausente, assinatura adulterada,
token expirado, `nbf` futuro, HS384, token sem assinatura e formato inválido.
Os novos tipos de erro do SDK são exercitados com `errors.Is`; o serviço
continua retornando seus erros genéricos, sem expor detalhes criptográficos.
Os testes existentes de grants mínimos, segregação de ambientes, deadline,
HTTP e aplicação continuam passando. Contratos HTTP e políticas de autorização
não foram alterados.

## Validação e quality gates

| Comando / gate                                                                                     | Resultado                                                                          |
| -------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| Linha de base: `go test -count=1 ./...` em media-service                                           | PASS                                                                               |
| `make ci-static-go` — gofmt, vet e golangci-lint em 9 módulos                                      | PASS; zero issues de lint                                                          |
| `make test-go` — 9 módulos                                                                         | PASS                                                                               |
| `bash scripts/ci/go-test.sh -race` — 9 módulos                                                     | PASS                                                                               |
| `make test-integration-go`                                                                         | PASS; PostgreSQL 16 e Valkey descartáveis, incluindo media com `-tags integration` |
| `make go-coverage-check`                                                                           | PASS; threshold 90% em todos os módulos, com banco separado para Link Safety       |
| `go build ./...` em cada um dos 9 módulos                                                          | PASS                                                                               |
| Build media isolado e workspace com `CGO_ENABLED=0`, `-trimpath`; workspace com `-ldflags='-s -w'` | PASS                                                                               |
| Media isolado: `go test -race -count=1 ./...`                                                      | PASS                                                                               |
| `make security-govulncheck`                                                                        | PASS                                                                               |
| govulncheck direto de media, sem gate de exceções                                                  | Zero vulnerabilidades alcançáveis                                                  |
| `make security-trivy-fs` antes                                                                     | FAIL esperado: exatamente CVE-2026-116587 HIGH em v3.0.5                           |
| `make security-trivy-fs` depois                                                                    | PASS: zero HIGH/CRITICAL nos 10 manifests do repositório                           |
| gocyclo / gocognit no signer e novos testes                                                        | Máximos 10 / 8                                                                     |
| `git diff --check`                                                                                 | PASS                                                                               |

Cobertura total por módulo, excluindo entrypoints `cmd` conforme o gate:
platform 94,4%; admin 91,3%; auth 90,8%; chat 90,1%; converter 92,7%; file 90,5%;
**media 92,4%**; notification 96,0%; search 98,1%.
`encodeLiveKitToken`: 5 ciclomática / 4 cognitiva; nenhuma nova função de produção,
camada ou fluxo de autenticação foi criado. Os testes não usam sleeps nem
dependem de cruzar fronteiras de segundos. O lint passou sem novos achados;
não foi executado SonarQube nem certificação independente de smells/duplicação.

O primeiro lint identificou dois seletores redundantes `.Time.UTC()` adicionados
na adaptação. Foram corrigidos para `.UTC()` e o gate completo foi repetido com sucesso.
O primeiro govulncheck falhou por ser um binário local compilado com Go 1.25;
foi recompilado em `/tmp/nchat-1149/bin` com **Go 1.26.10**, mantendo **v1.1.4**,
a versão do CI. O workflow e as configurações de scanners permaneceram intactos.

Trivy 0.75.0, DB atualizada em 2026-10-29 13:09:46 UTC. Mesmo gate HIGH/CRITICAL,
`exit-code 1`, sem ignorar vulnerabilidades sem correção, conforme o workflow.

## Riscos residuais e evidências

Govulncheck detalhado, comparado com cópia limpa de HEAD em `/tmp`, mantém
os mesmos seis advisories não alcançáveis: GO-2026-6165, GO-2026-6163,
GO-2026-6094, GO-2026-6866, GO-2026-6860 e GO-2026-5932. Nenhum advisory novo
foi detectado nessa comparação. Eles não foram ocultados nem remediados fora
do escopo desta issue; zero HIGH/CRITICAL no Trivy não significa ausência
de todos os advisories em todas as severidades e bases.

Não foi realizado join contra um servidor LiveKit real. A compatibilidade
foi validada no SDK, nos parsers JWT, na aplicação HTTP e nos testes de autorização
com PostgreSQL. Testes frontend não foram executados nesta mudança Go.
O Prompt Mestre NChat v4 não foi encontrado como arquivo local; foram aplicados
os gates fornecidos na solicitação e os comandos oficiais do repositório.

Logs, comandos, códigos de saída, grafos antes/depois, comparação de versões,
perfis de cobertura e binários de teste estão em `/tmp/nchat-1149/`.
Os dois containers descartáveis foram parados após a validação.

## Arquivos e estado final do Git

- `services/media-service/go.mod` e `go.sum`: upgrade mínimo, pin e tidy.
- `go.work.sum`: checksums da resolução do workspace.
- `services/media-service/internal/service/livekit_signer.go`: adaptação de timestamp.
- `services/media-service/internal/service/livekit_signer_test.go`: adaptação da asserção existente.
- `services/media-service/internal/service/livekit_token_compatibility_test.go`: interoperabilidade e casos negativos.
- Este relatório.

HEAD permanece em `581c75f11d2203a1f957b8f7e33aab9ddff555eb`, sem commit,
push, criação/alteração de PR ou merge. As quatro pastas `.swarm/` não rastreadas
que já existiam no início foram preservadas. Nenhuma alteração em Meetings,
RSVP, calendar sync, LiveKit join da épica #681 ou na implementação da issue #1053.
Code Quality Review, Security Review e PR Review não foram executadas nem simuladas.
