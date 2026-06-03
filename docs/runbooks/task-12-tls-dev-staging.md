# TASK-12 TLS Dev/Staging Runbook

## Objetivo

Configurar TLS para desenvolvimento local e staging inicial do NChat sem configurar producao real.

## Escopo

- HTTPS local no Traefik via entrypoint `websecure` em `:8443`.
- Certificados locais gerados por `mkcert` ou fallback `openssl`.
- TLS minimo `VersionTLS13` no Traefik local quando suportado.
- Overlay `k3s-staging` com Ingress TLS placeholder e Traefik `TLSOption`.
- Checks locais e CI para configuracao TLS.

## Arquivos criados ou alterados

- `infra/traefik/local/traefik.yml`
- `infra/traefik/local/dynamic.yml`
- `infra/traefik/local/certs/README.md`
- `infra/compose/compose.dev.yml`
- `infra/compose/.env.dev.example`
- `infra/k8s/overlays/k3s-staging/`
- `scripts/dev/dev-tls-generate.sh`
- `scripts/dev/dev-tls-status.sh`
- `scripts/dev/dev-tls-clean.sh`
- `scripts/ci/tls-config-check.sh`

## Comandos

```bash
make dev-tls-generate
make dev-gateway-up
make dev-gateway-status
make tls-config-check
make k8s-render-staging
make k8s-validate-staging
```

## Validacao

```bash
curl -k -i https://nchat.local:8443/
curl -k -i https://nchat.local:8443/api/auth/healthz
```

Se a web ou os servicos Go nao estiverem rodando no host, respostas `502` do Traefik sao aceitaveis para validar que HTTPS respondeu.

## Limitacoes

- Nao configura producao real.
- Nao configura cert-manager.
- Nao configura CA corporativa real.
- Nao commitar certificados ou chaves privadas.
- HTTP local permanece disponivel para compatibilidade de desenvolvimento, mas HTTPS e preferencial.
- `nchat-staging-tls` deve ser criado por SealedSecret ou processo externo aprovado.

## Definition of Done

- [x] HTTPS local configurado.
- [x] Certificados/chaves ignorados no Git.
- [x] `make dev-tls-generate` criado.
- [x] `make dev-gateway-up` suporta HTTPS.
- [x] `make tls-config-check` criado.
- [x] k3s-staging Ingress TLS placeholder criado.
- [x] README/runbook atualizados.
- [ ] PR aberto.
