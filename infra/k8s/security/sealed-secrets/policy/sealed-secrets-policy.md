# Sealed Secrets Policy

## Scope

`strict` scope is the default and required for NChat dev/staging secrets. A strict SealedSecret is bound to its Secret name and namespace.

`namespace-wide` scope requires a written justification in the PR and a named owner in `docs/security/secrets-owners.md`.

`cluster-wide` scope is prohibited unless an approved security exception exists before the PR is merged.

## Ownership

Every secret must have an owner and rotation cadence in `docs/security/secrets-owners.md` before a SealedSecret is committed.

## Rotation

Manual rotation must follow `docs/runbooks/sealed-secrets-rotation.md`.

## Cluster binding

A SealedSecret must be generated for the intended cluster and environment. Do not reuse SealedSecret manifests between clusters without confirming the target controller public key.

## Prohibitions

- Do not commit unsealed Secret manifests.
- Do not commit private keys or real TLS certificates.
- Do not paste secret values into issues, PRs, docs, logs, or CI output.
- Do not fetch or store the controller private key.
