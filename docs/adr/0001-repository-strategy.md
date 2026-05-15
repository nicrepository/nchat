# ADR-0001 — Monorepo para o NChat

## Status

Accepted

## Contexto

O NChat e um projeto grande, com frontend, multiplos servicos backend, banco de dados, cache, armazenamento de arquivos, realtime, notificacoes, voz MVP e componentes de seguranca.

Inicialmente, o desenvolvimento sera feito por uma pessoa com apoio de IAs. Mesmo nesse formato, o projeto precisa de rastreabilidade, governanca, revisao por PR, CI centralizado e documentacao de decisoes tecnicas.

## Decisao

Usar monorepo para o NChat.

## Consequencias positivas

- Governanca centralizada.
- CI unificado.
- Versionamento mais simples.
- Refatoracoes cross-service mais faceis.
- Padroes de seguranca e testes aplicados em um unico lugar.

## Consequencias negativas

- O repositorio pode crescer.
- Exige organizacao rigida de diretorios, ownership e workflows.
- Mudancas compartilhadas podem aumentar o impacto de PRs se o escopo nao for controlado.

## Revisao

Reavaliar antes da V1.0 comercial.
