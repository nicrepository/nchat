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

## Prohibitions

- Do not commit the original unsealed Secret.
- Do not commit private keys.
- Do not use `cluster-wide` scope without approved exception.
- Do not paste sensitive values into issues, PRs, docs, logs, terminals, or CI output.
