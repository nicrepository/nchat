# TASK-13 Sealed Secrets Runbook

## Objetivo

Criar a base operacional de Sealed Secrets para versionar apenas secrets criptografados no NChat.

## Escopo

- Estrutura de diretorios para templates, unsealed local, sealed versionavel e public cert cache.
- Scripts para instalar controller, validar controller/kubeseal, buscar certificado publico e selar secrets.
- Politica de escopo strict por padrao.
- Owners por secret.
- Runbook de rotacao manual.
- CI guard contra secrets plaintext em locais proibidos.

## Arquivos criados ou alterados

- `infra/k8s/security/sealed-secrets/`
- `infra/k8s/secrets/`
- `scripts/secrets/sealed-secrets-install-controller.sh`
- `scripts/secrets/sealed-secrets-fetch-cert.sh`
- `scripts/secrets/sealed-secrets-validate.sh`
- `scripts/secrets/sealed-secrets-seal.sh`
- `scripts/secrets/sealed-secrets-rotate-runbook-check.sh`
- `scripts/ci/sealed-secrets-policy-check.sh`
- `docs/security/secrets-owners.md`
- `docs/runbooks/sealed-secrets-rotation.md`

## Comandos

```bash
make sealed-secrets-install-controller
make sealed-secrets-validate
make sealed-secrets-fetch-cert
scripts/secrets/sealed-secrets-seal.sh \
  infra/k8s/secrets/unsealed/nchat-secrets.yaml \
  infra/k8s/secrets/sealed/nchat-secrets.sealed.yaml \
  nchat
make sealed-secrets-policy-check
```

## Validacao

```bash
make sealed-secrets-policy-check
git ls-files infra/k8s/secrets/unsealed
```

O segundo comando deve listar apenas `.gitkeep` e `README.md` quando esses arquivos estiverem versionados.

## Limitacoes

- Nao cria secrets reais.
- Nao configura producao real.
- Nao configura External Secrets, Vault, ArgoCD ou cert-manager.
- Scripts que usam `kubectl`/`kubeseal` exigem cluster e ferramentas locais.
- O cache de certificado publico e ignorado por padrao, apesar de o certificado ser publico.

## Definition of Done

- [x] Scripts de Sealed Secrets criados.
- [x] Templates criados sem valores reais.
- [x] Diretorios unsealed ignorados.
- [x] Runbook de rotacao criado.
- [x] CI valida politica basica.
- [x] Nenhum secret plaintext versionado.
- [ ] PR aberto.
