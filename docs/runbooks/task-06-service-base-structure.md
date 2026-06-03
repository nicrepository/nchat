# Tarefa 6 - Estrutura base dos servicos

## Status

- [x] Estrutura criada nos 6 servicos.
- [x] Configuracao por servico criada.
- [x] HTTP router padronizado.
- [x] Middlewares criados.
- [x] JSON responses padronizadas.
- [x] Testes adicionados.
- [x] README atualizado.
- [x] Runbook criado.
- [x] CI passando.
- [x] PR aberto: `https://github.com/nicrepository/nchat/pull/19`.

## Objetivo

Criar uma estrutura base consistente para os servicos Go do NChat.

## Servicos no escopo

- auth-service
- chat-service
- file-service
- notification-service
- admin-service
- media-service

## Estrutura criada

- `cmd`: ponto de entrada do binario do servico. Carrega config, compoe a aplicacao e inicia `http.Server`.
- `internal/app`: composicao da aplicacao. Inicializa logger e router, sem conexoes externas.
- `internal/config`: configuracao por ambiente usando `APP_ENV`, `PORT` e `READ_HEADER_TIMEOUT_SECONDS`.
- `internal/domain`: espaco reservado para entidades e invariantes de dominio.
- `internal/http`: rotas, handlers, router e middlewares HTTP.
- `internal/service`: espaco reservado para casos de uso.
- `internal/storage`: espaco reservado para adapters de persistencia.

## Pacotes compartilhados

- `libs/go/platform/config`: leitura segura de variaveis de ambiente com fallback.
- `libs/go/platform/httputil`: envelopes JSON, erros padronizados e middlewares HTTP.
- `libs/go/platform/log`: logger estruturado minimo com `log/slog`.
- `libs/go/platform/health`: resposta padrao de health.

## Endpoints base

- `GET /healthz`
- `GET /readyz`
- `GET /version`

## Seguranca base

- Security headers: `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` e `Cache-Control`.
- Panic recovery com resposta JSON generica.
- `X-Request-ID` reaproveitado ou gerado por request.
- JSON errors sem vazamento de detalhes internos.
- `ReadHeaderTimeout` configuravel e com fallback seguro.
- Configuracao via env sem logar valores sensiveis.

## Fora do escopo

- Auth real
- Banco de dados
- WebSocket
- Upload
- Notificacoes reais
- LiveKit
- Deploy

## Validacao

- `bash scripts/ci/go-fmt-check.sh`
- `bash scripts/ci/go-vet.sh`
- `bash scripts/ci/go-test.sh`
- `make test`
- `make lint`
- `make build`

## Definition of Done

- [x] Estrutura criada nos 6 servicos
- [x] Configuracao por servico criada
- [x] HTTP router padronizado
- [x] Middlewares criados
- [x] JSON responses padronizadas
- [x] Testes adicionados
- [x] README atualizado
- [x] Runbook criado
- [x] CI passando
- [x] PR aberto
