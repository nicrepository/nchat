# NChat local Traefik gateway

## Objetivo

Este diretorio configura o Traefik como gateway local padrao do NChat. Ele existe apenas para desenvolvimento e roteia requisicoes HTTP de `nchat.local` para processos rodando no host.

Nginx permanece como alternativa futura porque os requisitos aceitam Traefik ou Nginx.

## Arquivos

- `traefik.yml`: configuracao estatica do Traefik, entrypoints, file provider, logs, dashboard e ping.
- `dynamic.yml`: routers, services e middlewares para web e APIs locais.

## Rotas

| Rota                 | Destino local                      |
| -------------------- | ---------------------------------- |
| `/`                  | `http://host.docker.internal:5173` |
| `/api/auth`          | `http://host.docker.internal:8081` |
| `/api/chat`          | `http://host.docker.internal:8082` |
| `/api/files`         | `http://host.docker.internal:8083` |
| `/api/notifications` | `http://host.docker.internal:8084` |
| `/api/admin`         | `http://host.docker.internal:8085` |
| `/api/search`        | `http://host.docker.internal:8086` |
| `/api/media`         | `http://host.docker.internal:8087` |

Os routers de API aplicam `StripPrefix`, entao `http://nchat.local:8080/api/auth/healthz` chega ao `auth-service` como `/healthz`.

WebSocket nao exige configuracao especial no Traefik para o caso basico; headers `Upgrade` e `Connection` sao encaminhados pelo proxy. Esta tarefa nao implementa WebSocket real nos servicos.

## Seguranca

- O Docker provider nao e habilitado.
- O Docker socket do host nao e montado.
- O dashboard fica publicado apenas em `127.0.0.1` via Docker Compose.
- Nao ha TLS real nesta configuracao.
- Headers basicos de seguranca sao aplicados por middleware.
- Esta configuracao nao deve ser usada em producao.

## Como validar

```bash
make gateway-config-check
make dev-gateway-up
make dev-gateway-validate
make dev-gateway-down
```

Para resolver `nchat.local`, adicione no host:

```text
127.0.0.1 nchat.local
```

## Limitacoes

- Ambiente local apenas.
- Sem TLS real.
- Sem cert-manager.
- Sem Kubernetes.
- Sem Dockerfiles.
- Sem deploy.
- Sem rate limiting avancado.
- Sem WAF.
- Web e servicos Go precisam estar rodando no host para as rotas responderem.

## Proximos passos

- TLS local opcional com mkcert.
- Rate limiting.
- Headers avancados.
- WebSocket real.
- Observabilidade do gateway.
- Escolha final Traefik vs Nginx antes do go-live.
