# Kubernetes secrets

This directory separates examples, ignored local plaintext inputs, encrypted SealedSecrets, and optional local public certificate cache.

- `templates/`: safe examples listing required keys with empty values only.
- `unsealed/`: local plaintext Secret manifests, ignored by Git.
- `sealed/`: encrypted SealedSecret manifests that may be committed after review.
- `public-certs/`: optional cache for Sealed Secrets controller public certificates, ignored by Git.

Do not add `templates/` or `unsealed/` manifests to a Kustomization.
