# TASK-16 — PoC Valkey

## Status

Script criado. Todos os padrões de uso do NChat validados localmente.

## Objetivo

Validar Valkey para padrões de uso do NChat: Pub/Sub, Streams, locks, TTL e sliding window.

## Ambiente

- Docker Compose local
- Valkey 8 (Alpine)
- Senha via `.env.dev` (`VALKEY_PASSWORD`)
- Dados locais descartáveis (prefixo `nchat:poc:*`)

## Comando

```bash
make poc-valkey
# ou
pnpm poc:valkey
```

## O que é validado

| Teste                 | Descrição                                               |
| --------------------- | ------------------------------------------------------- |
| PING                  | Conectividade básica                                    |
| SET/GET               | Operações básicas de chave-valor                        |
| TTL/EXPIRE            | Expiração de chaves (`EX 5`)                            |
| SETNX lock acquire    | Aquisição de lock com `SET NX EX`                       |
| SETNX lock reject     | Segunda tentativa de lock retorna `nil`                 |
| SETNX owner preserved | GET confirma owner original                             |
| XADD                  | Inserção em stream                                      |
| XRANGE                | Leitura de range no stream                              |
| XREAD                 | Leitura de mensagens do stream                          |
| Pub/Sub               | Subscribe + Publish + captura de mensagem               |
| Sliding window        | Sorted set com janela deslizante: 3 allowed + 1 blocked |
| Latência              | Média de N iterações para PING, SET, GET, XADD, XREAD   |

## Resultados

Os resultados são gerados em `poc-results/valkey/` com:

- `<timestamp>-summary.md` — tabela de resultados e latências
- `<timestamp>-metrics.json` — métricas estruturadas

Os resultados **não são versionados** (gitignored).

## Critérios de aceite

- PING retorna PONG
- SET/GET funciona corretamente
- TTL é positivo e dentro do esperado
- SETNX impede lock duplicado
- Stream armazena e permite leitura via XRANGE/XREAD
- Pub/Sub entrega mensagem (captura ou confirmação server-side)
- Sliding window bloqueia a 4ª requisição quando limite=3
- Latências são registradas

## Variáveis de ambiente relevantes

| Variável                | Default | Descrição                           |
| ----------------------- | ------- | ----------------------------------- |
| `VALKEY_HOST_PORT`      | `6379`  | Porta do Valkey no host             |
| `VALKEY_PASSWORD`       | —       | Senha (obrigatória, via `.env.dev`) |
| `VALKEY_POC_ITERATIONS` | `10`    | Iterações para medição de latência  |

## Sliding window — implementação

```
key: nchat:poc:sliding
window: 60s (60000ms)
limit: 3

Para cada request:
  1. ZREMRANGEBYSCORE key 0 (now_ms - window_ms)   # remove entradas expiradas
  2. ZADD key now_ms unique-request-id              # registra request atual
  3. count = ZCARD key                              # conta requests na janela
  4. EXPIRE key 60                                   # renova TTL
  5. Se count <= limit: ALLOWED; caso contrário: BLOCKED
```

## Limitações

- Não é benchmark final de produção
- Não testa cluster Valkey
- Não testa Sentinel
- Não testa alta concorrência real (sem workers paralelos)
- Não testa failover
- Pub/Sub: o subscriber usa `timeout` dentro do container; se `timeout` não disponível
  no container, usa timeout do host via `docker compose exec`
- Não integra com chat-service ainda

## Próximos passos

1. Integração com `chat-service` para Pub/Sub de mensagens
2. WebSocket presence via sorted set
3. Outbox pattern real com Streams
4. Rate limit middleware HTTP usando sliding window
5. Teste de carga (Valkey Benchmark ou k6)
6. Estratégia de alta disponibilidade (Sentinel vs. cluster)

## Definition of Done

- [x] Script criado (`scripts/poc/valkey-poc.sh`)
- [ ] Pub/Sub validado (execução local)
- [ ] Streams validados (execução local)
- [ ] SETNX validado (execução local)
- [ ] TTL validado (execução local)
- [ ] Sliding window validado (execução local)
- [ ] Latência registrada (execução local)
- [x] Runbook criado
- [ ] make ci passa
- [x] PR aberto
