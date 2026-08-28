# Sealed Secrets

This directory documents the NChat Sealed Secrets foundation for dev and staging clusters.

Version pinned for operational scripts:

```text
sealed-secrets-v0.36.6
```

The controller manifest is vendored under `controller/`; its expected SHA-256 is
stored in `CONTROLLER_SHA256` and checked before the manual install command runs.

Use the scripts under `scripts/secrets/` to install the controller, validate the cluster, fetch the public certificate, and seal local unsealed Secret manifests.

Rules:

- Commit only encrypted `SealedSecret` manifests.
- Keep unsealed Secret manifests under `infra/k8s/secrets/unsealed/`; this directory is ignored by Git.
- Use strict scope by default.
- Do not commit controller private keys, kubeconfigs, plaintext secrets, or TLS private keys.
