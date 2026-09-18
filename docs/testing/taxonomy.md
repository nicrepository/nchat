# Taxonomia de testes

Esta taxonomia decide tres coisas de uma vez: **onde o teste mora**, **qual job da CI o
executa** e **de que infraestrutura ele pode depender**. Os nomes dos gates em
`.github/workflows/ci.yml` derivam dela.

| Categoria       | Criterio                                             | Infraestrutura        | Owner na CI                                                          |
| --------------- | ---------------------------------------------------- | --------------------- | -------------------------------------------------------------------- |
| **Unit**        | Uma unidade isolada, sem I/O real                    | Nenhuma               | `Tests / Go Unit`, `Tests / Web Unit`, `Tests / Admin Unit`          |
| **Component**   | Um componente React montado, dependencias mockadas   | jsdom                 | `Tests / Web Unit`, `Tests / Admin Unit`                             |
| **Integration** | Varias unidades juntas **com infraestrutura real**   | PostgreSQL, Docker    | `Tests / Go Integration`, `Infra / Web Image`                        |
| **Contract**    | Um contrato de API, storage ou protocolo             | Depende do contrato   | `Tests / Go Integration`, `Infra / Config`, `Infra / Release Safety` |
| **E2E**         | Comportamento observavel por um usuario no navegador | Browser + dev server  | `E2E / Web`, `E2E / Admin`                                           |
| **Performance** | Benchmark ou budget explicito                        | Variavel              | Fora dos gates obrigatorios; execucao dedicada                       |
| **Security**    | Scanners e propriedades de seguranca                 | Ferramenta do scanner | `Security / *`                                                       |

## O nome do arquivo nao decide a categoria

`*_integration_test.go` **nao** significa "precisa de PostgreSQL". Tres arquivos com esse
nome (4 testes) sao inteiramente in-process e rodam hoje em `Tests / Go Unit`: os dois de
`chat-service/internal/ws` e `auth-service/internal/http/avatar_integration_test.go`.
Elas montam varias unidades com fakes, que e integracao no sentido da tabela acima, mas sem
infraestrutura nenhuma.

O que decide e a **dependencia real**: a suite le um `*_TEST_DATABASE_URL` (direto ou por um
helper do pacote) e da `t.Skip` quando ele nao existe, ou exige uma build tag.

## Onde cada suite Go dependente de infraestrutura roda

| Familia                                                         | Ativacao                                        | Owner                    | Testes |
| --------------------------------------------------------------- | ----------------------------------------------- | ------------------------ | -----: |
| `admin-service/internal/storage`                                | `ADMIN_TEST_DATABASE_URL`                       | `Tests / Go Integration` |     47 |
| `auth-service/internal/storage`                                 | `AUTH_TEST_DATABASE_URL`                        | `Tests / Go Integration` |     31 |
| `chat-service` contrato de membership (#881)                    | `CHAT_TEST_DATABASE_URL` + `-run`               | `Tests / Go Integration` |      4 |
| `media-service/internal/storage`                                | `MEDIA_TEST_DATABASE_URL` + `-tags integration` | `Tests / Go Integration` |      1 |
| `chat-service` Link Safety, outbox, prioridade, ack, lembrete   | `CHAT_TEST_DATABASE_URL`, 127 testes nomeados   | `Tests / Go Coverage`    |    127 |
| `notification-service/internal/storage`                         | `NOTIFICATION_TEST_DATABASE_URL`, nomeados      | `Tests / Go Coverage`    |    113 |
| `file-service` storage + linkpreview (Link Safety)              | `FILE_TEST_DATABASE_URL`, nomeados              | `Tests / Go Coverage`    |      8 |
| **sem owner** — resto de `chat-service/internal/storage` (#935) | `CHAT_TEST_DATABASE_URL`                        | —                        |    114 |
| **sem owner** — resto de `file-service/internal/storage` (#936) | `FILE_TEST_DATABASE_URL`                        | —                        |     18 |
| **sem owner** — `file-service/internal/service` (#937)          | `FILE_TEST_DATABASE_URL` + SeaweedFS            | —                        |     46 |

Comandos: `make test-integration-go` (`scripts/ci/go-integration-test.sh`) e
`make go-coverage-check` (`scripts/ci/go-coverage-check.sh`, que chama
`scripts/ci/link-safety-postgres-coverage.sh`). O cabecalho de
`go-integration-test.sh` traz a receita Docker completa para rodar localmente.

### Por que dois owners, e nao um

`Tests / Go Coverage` **precisa** executar as suites que possui: o perfil delas e mesclado
no threshold de 90% por modulo, e sem ele chat-service, file-service e notification-service
ficam abaixo da linha. Move-las para `Tests / Go Integration` nao eliminaria a execucao,
apenas a duplicaria — exatamente o que a issue #931 existe para remover. Por isso a regra e:

- precisa rodar **para medir cobertura** → `Tests / Go Coverage`;
- precisa rodar **para provar um comportamento** e nao entra no perfil → `Tests / Go Integration`.

### As 178 suites sem owner

Sao uma lacuna **preexistente** a #931 — nunca rodaram em CI, nem antes nem depois — e nao
sao ativaveis sem trabalho de codigo, o que foi verificado executando cada uma:

- **chat-service (114)**: colidem entre si mesmo isoladas das demais. Executadas juntas em um
  banco proprio, 11 falham por colisao de fixture (`users_email_unique`, resets de schema que
  se atropelam). Foram escritas para rodar uma a uma com `-run`.
- **file-service `internal/storage` (18)**: 214 testes passam e entao **um** trava o pacote
  inteiro. `TestUploadAdmissionIntegrationFreesSlotsWhenASessionEnds`
  (`upload_admission_integration_test.go:132`) modela um processo morto tomando um advisory
  lock de sessao e nunca o liberando, e em seguida chama `pgxpool.Pool.Close()` — que espera
  pelas conexoes ainda emprestadas e por isso nunca retorna. O binario e morto pelo timeout.
- **file-service `internal/service` (46)**: alem do banco, exigem **SeaweedFS** real, que
  nenhum job da CI provisiona.

Dar owner a elas significa consertar fixtures, um deadlock e provisionar SeaweedFS — trabalho
de produto, nao de pipeline, e por isso fora da #931.

> **Rastreamento da divida:** as tres familias possuem causas independentes e sao
> acompanhadas separadamente:
>
> - **chat-service `internal/storage` (114):** #935 — isolamento das fixtures PostgreSQL
>   para permitir execucao deterministica como uma unica familia;
> - **file-service `internal/storage` (18):** #936 — correcao do hang em
>   `TestUploadAdmissionIntegrationFreesSlotsWhenASessionEnds` causado pelo lifecycle
>   da conexao/advisory lock;
> - **file-service `internal/service` (46):** #937 — provisionamento de PostgreSQL +
>   SeaweedFS efemeros para executar as suites de upload/preview na CI.
>
> Essas issues registram divida tecnica preexistente identificada durante a #931.
> As suites permanecem explicitamente **sem owner de CI** ate que suas respectivas
> issues sejam implementadas; a existencia das issues nao deve ser interpretada como
> cobertura atual.

## Tenho um teste novo que usa PostgreSQL real. Onde ele entra?

1. Ele precisa entrar na medicao de cobertura de um modulo que depende disso para os 90%?
   → nomeie-o em `scripts/ci/link-safety-postgres-coverage.sh`. Owner: `Tests / Go Coverage`.
2. Caso contrario → acrescente a familia dele a tabela `SUITES` de
   `scripts/ci/go-integration-test.sh`, com **banco proprio**. Owner: `Tests / Go Integration`.
3. Em qualquer dos casos, se ele introduzir um `*_TEST_DATABASE_URL` novo,
   `scripts/ci/check_ci_architecture.py` recusa o commit ate que algum desses dois scripts
   nomeie a variavel. Essa invariante nao prova que o pacote executa; a execucao real e
   comprovada pelos proprios gates de integration ou coverage.

Nunca exporte um DSN mais amplo para "pegar tudo": as suites do mesmo pacote resetam schemas
umas das outras, e um `go test ./...` com o DSN exportado falha (127 falhas em chat-service).
Cada familia recebe um banco vazio proprio, com nome terminando em `_test`.

## Regras que decorrem disso

**Um teste unitario nunca espera infraestrutura.** `scripts/ci/go-test.sh` roda sem nenhum
DSN exportado, entao toda suite opt-in pula. E por isso que `Tests / Go Unit` nao declara
servico de banco: se precisasse, a distincao entre "a logica quebrou" e "o banco nao subiu"
desapareceria do painel do PR.

**Coverage nao e uma categoria, e uma medida.** `Tests / Web Unit` e `Tests / Admin Unit`
rodam `vitest run --coverage`, que e a suite inteira mais os thresholds em uma execucao so.
`Tests / Go Coverage` existe separado de `Tests / Go Unit` porque mede outra coisa: descarta
os entrypoints `cmd/` do denominador e mescla os perfis PostgreSQL.

**E2E prova fluxo, nao unidade.** Um comportamento que um teste de componente consegue provar
nao pertence ao Playwright: E2E e o gate mais caro e mais lento da pipeline.
