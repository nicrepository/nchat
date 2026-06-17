# NChat local Traefik gateway

## Objetivo

Este diretorio configura o Traefik como gateway local padrao do NChat. Ele existe apenas para desenvolvimento e roteia requisicoes HTTP e HTTPS de `nchat.local` para processos rodando no host.

Nginx permanece como alternativa futura porque os requisitos aceitam Traefik ou Nginx.

## Arquivos

- `traefik.yml`: configuracao estatica do Traefik, entrypoints, file provider, logs, dashboard e ping.
- `dynamic.yml`: routers HTTP/HTTPS, services, middlewares e opcoes TLS para web e APIs locais.
- `certs/`: certificados locais gerados fora do Git por `make dev-tls-generate`.

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

O gateway local preserva os caminhos esperados pelo frontend:

- `/api/auth/*` e reescrito para `/auth/*` no `auth-service`.
- `/api/chat/*` chega ao `chat-service` sem strip, pois o servico monta rotas em `/api/chat/*`.
- Rotas de probe como `/api/auth/healthz` e `/api/chat/healthz` usam routers explicitos para chegar aos handlers `/healthz` dos servicos.

WebSocket nao exige configuracao especial no Traefik para o caso basico; headers `Upgrade` e `Connection` sao encaminhados pelo proxy. Esta tarefa nao implementa WebSocket real nos servicos.

## Seguranca

- O Docker provider nao e habilitado.
- O Docker socket do host nao e montado.
- O dashboard fica publicado apenas em `127.0.0.1` via Docker Compose.
- HTTPS local usa certificados gerados por `mkcert` ou fallback self-signed com `openssl`.
- `VersionTLS13` e configurado como minimo nos routers HTTPS quando suportado pelo Traefik local.
- Headers basicos de seguranca sao aplicados por middleware.
- Esta configuracao nao deve ser usada em producao.

## Como validar

```bash
make dev-tls-generate
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
- Sem TLS publico real.
- Sem cert-manager.
- Sem Kubernetes.
- Sem Dockerfiles.
- Sem deploy.
- Sem rate limiting avancado.
- Sem WAF.
- Web e servicos Go precisam estar rodando no host para as rotas responderem.

## Proximos passos

- Staging/producao com CA/cert-manager definidos em tarefa futura.
- Rate limiting.
- Headers avancados.
- WebSocket real.
- Observabilidade do gateway.
- Escolha final Traefik vs Nginx antes do go-live.
