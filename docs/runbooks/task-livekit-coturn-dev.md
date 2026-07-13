# TASK — LiveKit + coturn (dev) — TURN/STUN para media-service

## Status

Preparação técnica de infraestrutura dev (Docker Compose). Não implementa frontend de
chamadas nem o `media-service` funcional — apenas a base LiveKit + coturn prevista para
V1.0, Sprints 9-10.

## Objetivo

Subir LiveKit (SFU) + coturn (TURN/STUN) em ambiente dev via Docker Compose (profile
`media`), documentar as portas TCP/UDP necessárias e validar com um smoke test real:
criação de sala e conexão de um participante real (sem mocks).

## Decisão

- LiveKit e coturn rodam como serviços opt-in no `infra/compose/compose.dev.yml`
  existente, sob o profile `media` (mesmo padrão de `gateway` e `observability`) — não
  fazem parte do stack padrão de dev (Postgres/Valkey/SeaweedFS), que continua inalterado.
- coturn roda como serviço standalone (não o TURN embutido do LiveKit), referenciado via
  `rtc.turn_servers` no config do LiveKit. Isso mantém coturn testável/observável
  isoladamente e é o padrão recomendado para produção futura.
- Configs versionadas são apenas _templates_ sem segredo real:
  - `infra/compose/livekit/livekit.yaml.template`
  - `infra/compose/coturn/turnserver.conf.template`
  - Renderizados por `scripts/dev/_media_env.sh` (via `envsubst`) em
    `livekit.runtime.yaml` / `turnserver.runtime.conf` (gitignored) antes do `up`.
- O LiveKit YAML **não suporta** expansão de `${VAR}` nativamente, e a chave/segredo de
  API não pode ir no arquivo de config de qualquer forma — por isso são injetados via a
  variável de ambiente `LIVEKIT_KEYS` no container (mecanismo oficial do LiveKit), nunca
  escritos em disco.
- Segredo do coturn (`static-auth-secret`, mecanismo TURN REST API/HMAC) precisa estar no
  arquivo de config (coturn não lê API keys de env), por isso passa pelo passo de render.
- Imagens fixadas (sem `latest`, conforme padrão do repo):
  `livekit/livekit-server:v1.13.3`, `coturn/coturn:4.14.0-r0`,
  `livekit/livekit-cli:v2.17` (usado apenas pelo smoke test, via `docker run`).

## Arquivos criados/alterados

- `infra/compose/livekit/livekit.yaml.template`
- `infra/compose/livekit/README.md`
- `infra/compose/coturn/turnserver.conf.template`
- `infra/compose/coturn/README.md`
- `infra/compose/compose.dev.yml` (serviços `livekit` e `coturn`, profile `media`)
- `infra/compose/.env.dev.example` (variáveis LiveKit/coturn, placeholders)
- `.gitignore` (ignora os `*.runtime.*` renderizados)
- `scripts/dev/_media_env.sh`
- `scripts/dev/dev-media-up.sh`
- `scripts/dev/dev-media-down.sh`
- `scripts/dev/dev-media-status.sh`
- `scripts/dev/dev-media-logs.sh`
- `scripts/dev/dev-media-validate.sh`
- `scripts/ci/media-config-check.sh`
- `package.json` (`dev:media:*`, `media:config-check`, incluído em `pnpm run ci`)
- `Makefile` (`dev-media-*`, `media-config-check`)
- `README.md` (link curto para este runbook)
- `docs/runbooks/task-livekit-coturn-dev.md` (este arquivo)

## Portas TCP/UDP

| Serviço | Porta (host) | Protocolo | Uso                                    | Variável                                          |
| ------- | ------------ | --------- | -------------------------------------- | ------------------------------------------------- |
| LiveKit | 7880         | TCP       | HTTP/WebSocket (signaling)             | `LIVEKIT_HOST_PORT`                               |
| LiveKit | 7881         | TCP       | RTC fallback (TURN/TLS ou ICE-TCP)     | `LIVEKIT_RTC_TCP_HOST_PORT`                       |
| LiveKit | 50100-50110  | UDP       | RTC (ICE/SRTP) — range estreito de dev | `LIVEKIT_RTC_UDP_PORT_START` / `..._END`          |
| coturn  | 3478         | TCP + UDP | STUN/TURN                              | `COTURN_LISTENING_PORT`                           |
| coturn  | 49160-49200  | UDP       | Relay TURN — range estreito de dev     | `COTURN_RELAY_MIN_PORT` / `COTURN_RELAY_MAX_PORT` |

Todas as portas são publicadas em `127.0.0.1` (não `0.0.0.0`), seguindo o padrão de
segurança já usado por `gateway` e `observability` neste repositório.

## Como usar (Windows 11 + Docker Desktop, PowerShell)

```powershell
# 1. Copiar (se ainda não existir) infra/compose/.env.dev a partir do exemplo.
#    Os scripts abaixo fazem isso automaticamente também.
Copy-Item infra\compose\.env.dev.example infra\compose\.env.dev -ErrorAction SilentlyContinue

# 2. Subir LiveKit + coturn
make dev-media-up
# equivalente: pnpm dev:media:up

# 3. Ver status
make dev-media-status

# 4. Validar (cria sala real, conecta participante real, testa coturn)
make dev-media-validate

# 5. Logs (se algo falhar)
make dev-media-logs

# 6. Derrubar
make dev-media-down
```

Todos os scripts são Bash (`scripts/dev/*.sh`), executados via `bash` — funcionam no
Git Bash / WSL que acompanha o Docker Desktop no Windows. Não há dependência de
comandos DOS/PowerShell-only.

## Troubleshooting (Windows 11 / Docker Desktop)

- **Docker Desktop deve estar rodando** com o backend WSL2 (padrão). Verifique com
  `docker info` antes de `make dev-media-up`.
- **Firewall do Windows**: na primeira vez que o coturn/LiveKit expõem uma porta UDP,
  o Firewall do Windows pode pedir permissão para o processo `com.docker.backend.exe` —
  aceite para redes privadas. Se a conexão do participante falhar silenciosamente
  (ICE nunca completa), verifique se as portas UDP `50100-50110` e `49160-49200` não
  estão bloqueadas.
- **Range de portas UDP grande**: Docker Desktop no Windows (via WSL2/Hyper-V) pode ser
  mais lento para publicar ranges grandes de porta; por isso o range de dev foi mantido
  propositalmente pequeno (11 portas LiveKit + 41 portas de relay coturn).
- **`node_ip` do LiveKit**: fixado em `127.0.0.1` (`LIVEKIT_NODE_IP`) para uso no mesmo
  host. Para testar de outro dispositivo na mesma LAN, troque `LIVEKIT_NODE_IP` para o IP
  da máquina Windows na rede e garanta que o firewall libera as portas UDP/TCP acima para
  a rede local.
- **`docker compose` vs `docker-compose`**: use `docker compose` (Compose v2, integrado
  ao Docker Desktop); não é necessário instalar `docker-compose` separado.

## Ameaças e mitigações (threat model)

| Risco                                       | Mitigação                                                                                                                                                                                                                    |
| ------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| coturn como open relay                      | `use-auth-secret` + `static-auth-secret` (TURN REST API/HMAC); smoke test **falha** se uma alocação TURN anônima for aceita.                                                                                                 |
| Vazamento do LiveKit API secret             | Nunca escrito em arquivo; injetado via env `LIVEKIT_KEYS` no container. Não aparece em `docker ps`/args. Não versionado (apenas placeholder em `.env.dev.example`).                                                          |
| Tokens/segredos de participante em logs     | Scripts passam credenciais via variáveis de ambiente para o `livekit-cli`, nunca como argumento de CLI; nenhum script imprime `LIVEKIT_API_SECRET`/`COTURN_STATIC_AUTH_SECRET`.                                              |
| Portas UDP/TCP expostas sem documentação    | Tabela de portas acima; `media-config-check` falha se portas usarem `0.0.0.0` em vez de `127.0.0.1`.                                                                                                                         |
| Uso acidental de config dev em staging/prod | Serviços só existem sob o profile `media` no compose de **dev**; não há overlay/manifesto de staging/prod criado nesta tarefa; README dos serviços marca explicitamente "dev only".                                          |
| Validação falsa sem participante real       | `dev-media-validate.sh` usa `livekit-cli` real (`room join --publish-demo`), fazendo handshake WebRTC de verdade — não há mock. O script falha se o participante não aparecer em `room participants list` dentro do timeout. |

## Implementação mínima (fora de escopo, propositalmente não implementado)

- Frontend de chamadas / UI.
- Lógica funcional completa do `media-service` (o serviço Go continua apenas o esqueleto
  existente).
- Helm/Kubernetes ou qualquer manifesto de staging/produção.
- TLS real / certificados públicos para LiveKit ou coturn (dev-only, sem exposição
  externa).
- Alta disponibilidade / modo distribuído do LiveKit (Redis), TURN sobre TLS/DTLS.

## Validação local

Executado nesta sessão de autoria:

- Leitura do código-fonte oficial do LiveKit (`pkg/config/config.go`) para confirmar os
  campos exatos de `rtc.turn_servers[].secret` e `turn.enabled` usados no template.
- Leitura do código-fonte oficial do coturn (`src/apps/uclient/*.c`) para confirmar que
  uma falha de alocação TURN autenticada causa `exit(-1)` no `turnutils_uclient` (usado
  pelo smoke test para diferenciar sucesso/falha via exit code) e que a flag `-W` permite
  passar o segredo TURN REST API diretamente, sem calcular HMAC manualmente no script.
- `bash -n` (checagem de sintaxe) em todos os scripts novos.

**Não executado nesta sessão** (ambiente de autoria não tem o daemon do Docker Desktop
acessível — apenas o cliente Docker estava disponível): `docker compose config`,
`make dev-media-up`, `make dev-media-validate`, `make dev-media-down`, `pnpm run ci`.
Estes comandos precisam ser executados por quem aplicar esta mudança em uma máquina
Windows 11 com Docker Desktop rodando, seguindo exatamente os passos da seção
"Como usar" acima. Qualquer ajuste necessário após essa execução real (por exemplo,
mensagens de erro do `turnutils_uclient` diferentes do esperado) deve ser tratado como
follow-up.

## Definition of Done

- [x] Compose/profile dev de LiveKit criado
- [x] Compose/profile dev de coturn criado
- [x] coturn não está como open relay (`use-auth-secret` + `static-auth-secret`, sem `no-auth`)
- [x] Secrets reais não foram versionados (apenas placeholders em `.env.dev.example`; `*.runtime.*` gitignored)
- [x] Portas TCP/UDP documentadas neste runbook
- [x] Runbook contempla Windows 11/Docker Desktop
- [x] Smoke test cria sala básica (`dev-media-validate.sh` → `lk room create`)
- [x] Smoke test conecta um participante real (`lk room join --publish-demo`, sem mock)
- [ ] `docker compose config` executado e validado (pendente — requer Docker Desktop rodando)
- [ ] `make ci` / `media-config-check` executados nesta máquina (pendente — requer Docker Desktop rodando)
- [x] Nenhum arquivo fora do escopo alterado
