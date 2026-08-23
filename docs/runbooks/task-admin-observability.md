# Admin Console — Dashboard e Health Center (issue #581)

Runbook da visão operacional do Admin Console: o que cada número significa, o
que cada verificação realmente faz, o que este deployment consegue observar e o
que ele deliberadamente não observa.

O contrato HTTP está em [`../api/admin-endpoints.md`](../api/admin-endpoints.md),
seção 7. A autorização está em
[`../security/rbac-matrix.md`](../security/rbac-matrix.md).

---

## 1. O que a tela responde

Uma pergunta, nesta ordem: **a plataforma está saudável, e existe algo que exige
ação agora?** Volume vem depois, porque volume não é decisão.

Não há gráfico nenhum na tela. Uma série temporal de um número sobre o qual
ninguém pode agir ocupa espaço melhor usado pelo número em si; histórico de
longo prazo continua sendo Prometheus e Grafana
([`task-18-observability-stack.md`](task-18-observability-stack.md)), e esta
issue não os substitui.

## 2. Os cinco estados

| Estado        | Significado                                                 |
| ------------- | ----------------------------------------------------------- |
| `healthy`     | A verificação rodou e a dependência respondeu corretamente. |
| `degraded`    | Respondeu, mas algo na resposta exige atenção.              |
| `unavailable` | A verificação rodou e a dependência não respondeu.          |
| `disabled`    | Integração desligada na configuração. **Não é falha.**      |
| `unknown`     | Nenhuma verificação rodou. **Não é saudável.**              |

Três invariantes que o código sustenta e os testes fixam:

- **`configured` não é `healthy`.** O estado é produzido por uma tentativa de
  conexão, nunca pela presença de um valor de configuração.
- **`unknown` nunca vira `healthy`.** O estado geral cai para `degraded` quando
  qualquer linha está desconhecida.
- **`disabled` não é `unavailable`.** Não gera alerta e não afeta o estado geral.

Estado nunca depende só de cor: cada badge carrega palavra, forma e cor, e as
três dizem a mesma coisa.

## 3. Quais integrações são realmente verificadas

Esta é a parte que um operador precisa entender antes de interpretar a tela.

`admin-service` monta o ConfigMap compartilhado (`nchat-config`) e, no overlay
`nchat-dev-server`, apenas dois Secrets — `DATABASE_URL` e
`AUTH_JWT_HMAC_SECRET`. Um endpoint que vive num Secret escopado a outro
workload é **genuinamente invisível daqui**. Isso não é defeito a contornar: é a
mesma distinção `observable` / `configured` que a #580 estabeleceu no catálogo
de configuração, e é exatamente para isso que o estado `unknown` existe.

| Serviço       | Variáveis que o alvo exige                         | O que a sonda faz                                                         |
| ------------- | -------------------------------------------------- | ------------------------------------------------------------------------- |
| PostgreSQL    | (usa o pool que o serviço já tem)                  | `Ping` pelo pool: conectividade, slot disponível e servidor respondendo   |
| Valkey        | `VALKEY_HOST`, `VALKEY_PORT` (+ `VALKEY_PASSWORD`) | `AUTH` quando há credencial, depois `PING`, em RESP                       |
| Keycloak/OIDC | `OIDC_ENABLED`, `OIDC_ISSUER_URL`                  | discovery, coerência do `issuer`, e o JWKS **na mesma origem**            |
| SMTP          | `SMTP_WORKER_ENABLED`, `SMTP_HOST`, `SMTP_PORT`    | conecta e lê a saudação `220`; envia `QUIT` e nada mais                   |
| LiveKit       | `LIVEKIT_ENABLED`, `LIVEKIT_API_URL`               | um `GET` limitado no mesmo host, com `wss://` normalizado para `https://` |
| TURN/coturn   | —                                                  | não há sonda: nada neste pod nomeia o coturn                              |
| ClamAV        | `FILE_MALWARE_SCANNER_ADDRESS`                     | `PING` e `VERSION` do clamd                                               |
| SeaweedFS     | `SEAWEEDFS_FILER_URL`                              | um `GET` limitado no filer; o corpo é descartado sem ser lido             |
| Link Scan     | `CHAT_LINK_SAFETY_ENABLED`                         | não há sonda: a credencial é escopada a chat/file e a quota é de terceiro |
| WebSocket     | `VALKEY_WS_BROADCAST_ENABLED`                      | não há sonda: o hub vive no chat-service                                  |

**Falta alguma variável exigida → `unknown` / `not_observable`, e nenhum socket
é aberto.** É tudo ou nada: um alvo montado com metade das variáveis seria um
endereço meio inventado.

Nos deployments de hoje isso significa que TURN, Link Scan e WebSocket aparecem
como `unknown`, e que LiveKit e SMTP aparecem como `disabled` ou `unknown`
conforme o ambiente. É a resposta honesta. Tornar qualquer um deles verificável
é uma mudança de manifesto — colocar a variável no ambiente do `admin-service` —
e **nenhuma mudança de código**: a resolução é declarativa e a sonda passa a
rodar sozinha assim que a variável aparecer.

### O que as sondas nunca fazem

Login real de usuário no provedor de identidade; envio de e-mail pelo relay;
criação de sala persistente no LiveKit; escrita de amostra EICAR no antimalware;
listagem de objetos armazenados; qualquer chamada que gaste quota de terceiro.

## 4. Segurança das verificações

**Nenhuma requisição administrativa fornece um destino.** Não existe campo,
parâmetro ou corpo que carregue URL, host, IP, porta, DSN, namespace ou path, e
não existe rota `/health/{service}`. O único parâmetro é `?service=<id>`,
resolvido contra o registro fechado em
`services/admin-service/internal/domain/health.go` antes de qualquer leitura.

Os endereços vêm exclusivamente de `lookupEnv` sobre o ambiente do próprio pod.
Não há caminho de código de uma requisição HTTP até um alvo de conexão — é o que
impede o health detalhado de virar um SSRF autenticado.

Complementos que sustentam isso:

- só `http` e `https` são discados; qualquer outro esquema é
  `invalid_configuration`;
- redirects **não** são seguidos, então um `302` não nomeia um segundo destino;
- o `jwks_uri` vem da resposta do provedor, não da configuração, e por isso é
  exigido na mesma origem do issuer — caso contrário nenhuma segunda requisição
  é feita;
- verificação TLS nunca é relaxada. Não existe `InsecureSkipVerify` no serviço.

Nada que uma dependência diz volta na resposta: erros são classificados numa
categoria sanitizada de conjunto fechado e o texto original é descartado. A
única informação vinda de fora que sai é `version`, filtrada por allowlist de
caracteres e truncada.

## 5. Timeouts, isolamento e refresh

- **timeout curto por sonda**, não um orçamento único para a coleta inteira:
  uma dependência travada consumiria o orçamento de todas as outras;
- **concorrência limitada**;
- a coleta roda sob contexto **desacoplado** da requisição que a iniciou, porque
  outros esperam por ela;
- uma sonda que falha, estoura o prazo ou entra em pânico vira **a linha
  daquele serviço**. A resposta continua `200`.

Refresh:

| Camada            | Comportamento                                                         |
| ----------------- | --------------------------------------------------------------------- |
| Cache do servidor | Uma leitura comum é servida do último snapshot enquanto ele é recente |
| Coalescing        | Requisições concorrentes compartilham **uma** coleta em execução      |
| Intervalo mínimo  | Um refresh forçado antes dele devolve o snapshot atual                |
| Frontend          | Recarga periódica moderada, pausada em aba oculta, mais botão manual  |

Segurar o botão, ou abrir o console em dez abas, custa uma coleta por intervalo.

## 6. As métricas

Toda métrica declara chave, definição verificável, janela, unidade e origem. A
tabela vive em `services/admin-service/internal/domain/platform_metrics.go` e é
validada por teste: uma métrica sem contador atrás dela quebra o build.

| Chave                         | Janela    | Conta                                                                 |
| ----------------------------- | --------- | --------------------------------------------------------------------- |
| `users.active_now`            | instante  | contas com sessão de chat viva (não revogada, dentro dos dois prazos) |
| `users.active_24h`            | 24 h      | contas cuja sessão registrou uso nas últimas 24 h                     |
| `users.total`                 | acumulado | contas não excluídas, incluindo suspensas e não ativadas              |
| `channels.active`             | acumulado | canais ativos, públicos e privados                                    |
| `conversations.groups_active` | acumulado | grupos ativos                                                         |
| `conversations.direct_active` | acumulado | conversas 1:1 ativas                                                  |
| `messages.last_24h`           | 24 h      | mensagens criadas nas últimas 24 h, inclusive as depois apagadas      |
| `calls.active`                | instante  | chamadas tocando ou em andamento                                      |
| `uploads.last_24h`            | 24 h      | anexos criados nas últimas 24 h e ainda não excluídos                 |
| `files.blocked_24h`           | 24 h      | anexos recusados pelo antimalware, pelo momento da recusa             |
| `links.blocked_24h`           | 24 h      | mensagens retidas por veredito malicioso do Link Scan                 |
| `storage.stored_bytes`        | acumulado | soma do tamanho cifrado dos anexos vivos                              |

**Indisponível não é zero.** Se a consulta não roda, o campo `value` não é
enviado e o card diz "Indisponível". "Nada aconteceu" e "não conseguimos
descobrir" são respostas opostas.

**Armazenamento não exibe percentual.** O backend de objetos não informa
capacidade confiável, então a plataforma mostra o que guarda e não inventa um
total para dividir por ele.

**Webhooks não aparecem.** Não existe worker, tabela ou fila de entrega de
webhook no repositório, e a issue menciona a métrica: inventá-la seria pior que
omiti-la.

### Custo

Uma consulta, uma ida ao banco, doze subqueries escalares. Sem junção, sem N+1,
e a janela de 24 h é calculada uma vez na CTE para que dois cards da mesma tela
não discordem sobre onde ela começa.

As duas janelas de tempo sobre as tabelas grandes são cobertas por índices BRIN
(`chat` 000036, `files` 000013): as duas tabelas são append-only e sua ordem
física acompanha `created_at`, que é exatamente a correlação que o BRIN explora,
então o índice custa alguns kilobytes onde o B-tree equivalente custaria uma
fração relevante da tabela. Os índices existentes lideram por `workspace_id` e
não servem uma faixa de tempo global.

## 7. Alertas

Derivados do snapshot atual a cada coleta, e **nunca persistidos**: um alerta
que deixou de descrever a plataforma simplesmente para de ser produzido. Por
isso não há reconhecimento, soneca nem ciclo de vida para dessincronizar.

No máximo **um alerta por serviço**. Uma dependência lenta e recusando conexões
é um problema, e emitir duas linhas para ela é como se ensina um operador a
parar de ler a lista.

`healthy` e `disabled` não geram nada. `unknown` também não: pontos cegos
aparecem como estado no Health Center, e transformar cada um em alerta enterraria
os reais.

Cada alerta carrega título, impacto, ação recomendada, o instante em que a
condição foi observada e um destino estático (runbook e, quando existe, a chave
de configuração). `since` é a idade da **observação**, não da indisponibilidade:
este serviço não guarda histórico, e afirmar o contrário seria inventar um fato.

## 8. Observabilidade da própria tela

`nchat_admin_health_check_duration_seconds{service}`,
`nchat_admin_health_check_results_total{service,state}`,
`nchat_admin_health_cache_events_total{result}`,
`nchat_admin_dashboard_build_duration_seconds{outcome}`.

Todo valor de label vem de conjunto fechado declarado em código. Não há user id,
e-mail, URL, request id, channel id nem file id em label algum. Exportadas só
com `PROMETHEUS_METRICS_ENABLED`, como o resto da plataforma.

## 9. Diagnóstico rápido

| Sintoma na tela                              | Onde olhar                                                                   |
| -------------------------------------------- | ---------------------------------------------------------------------------- |
| Tudo `unknown` menos PostgreSQL              | O pod não recebeu o ConfigMap. Confira o `envFrom` do Deployment.            |
| Serviço `unknown` isolado                    | Esperado quando a variável de alvo é escopada a outro workload — ver §3.     |
| `authentication_failed` no Valkey            | `VALKEY_PASSWORD` deste pod difere do que o servidor exige.                  |
| `invalid_configuration` no OIDC              | `OIDC_ISSUER_URL` não é URL utilizável, ou o provedor responde outro issuer. |
| `tls_error`                                  | Certificado da dependência não é confiável. A verificação não é desligável.  |
| Cards "Indisponível" com Health Center cheio | A agregação de métricas falhou; o banco é a primeira suspeita.               |
| `503` em toda a superfície                   | A Admin API não está configurada neste pod (sem `DATABASE_URL`/JWT).         |

## 10. Fora de escopo

Substituir Prometheus/Grafana; APM; histórico de longo prazo dentro do NChat;
alerting externo; qualquer persistência nova para alertas.
