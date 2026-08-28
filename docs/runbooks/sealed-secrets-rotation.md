# Sealed Secrets Rotation Runbook

## When to rotate

Rotate a secret when any of these events occurs:

- Security incident.
- Owner or responsible person changes.
- Certificate or credential expiration.
- Suspected exposure.
- Environment or cluster key changes.

## Manual rotation steps

1. Create or update the unsealed Secret manifest locally under `infra/k8s/secrets/unsealed/`.
2. Seal it with strict scope using `scripts/secrets/sealed-secrets-seal.sh`.
3. Commit only the generated SealedSecret under `infra/k8s/secrets/sealed/`.
4. Apply the SealedSecret with `kubectl` or a future Flux/ArgoCD flow.
5. Validate the generated Kubernetes Secret exists without printing its value.
6. Restart workloads that require the new value.
7. Record the rotation date, reason, and operator in the relevant ticket or change log.

## Pending: nchat-link-safety (RF-21)

Every supported overlay — `k3s-dev`, `k3s-staging` and `nchat-dev-server` —
enables `CHAT_LINK_SAFETY_ENABLED` and `FILE_LINK_SAFETY_ENABLED`, and both
services refuse to start without the credentials. So this Secret must be sealed
**before** the next deploy of any of them, or chat-service and file-service will
CrashLoopBackOff. That is the intended failure: enabled with
no checker would accept every link unchecked.

The template is versioned; the ciphertext is not, because sealing needs the real
Cloudflare account id and a token scoped to Account > URL Scanner > Edit. Steps 1-3 above, with:

```
cp infra/k8s/secrets/templates/nchat-link-safety.template.yaml \
   infra/k8s/secrets/unsealed/nchat-link-safety.yaml
# fill in the four values (the account id and token are the same in both
# CHAT_* and FILE_* keys), then:
scripts/secrets/sealed-secrets-seal.sh \
  infra/k8s/secrets/unsealed/nchat-link-safety.yaml \
  infra/k8s/secrets/sealed/nchat-dev/nchat-link-safety.yaml \
  nchat-dev
```

Add the generated file to `infra/k8s/secrets/sealed/nchat-dev/kustomization.yaml`
and delete the unsealed copy.

## Prohibitions

- Do not commit the original unsealed Secret.
- Do not commit private keys.
- Do not use `cluster-wide` scope without approved exception.
- Do not paste sensitive values into issues, PRs, docs, logs, terminals, or CI output.
