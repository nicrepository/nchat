# RF-23 — estratégia de testes

- Domínio/storage: tipos, criação idempotente, papéis, transições, estados
  terminais, relógio PostgreSQL, timeout único e `SKIP LOCKED`.
- Serviço: validação da identidade autenticada, publicação somente após mudança,
  timeout materializado por comando atrasado e falha de dependência.
- WebSocket: payload estrito, erros estáveis, fan-out para dois clientes
  autenticados, isolamento de terceiros e todos os eventos terminais.
- Media-service: somente `call_id`, sessão ativa, chamada `active`, participação,
  sala/identidade derivadas e ausência de token em falhas/logs.
- Frontend: reducer por versão, duplicatas/fora de ordem, chamada recebida,
  atender, recusar, cancelar, encerrar, timeout, erro e início áudio/vídeo.

Executar concorrência Go com `go test -race ./...` nos módulos alterados.
