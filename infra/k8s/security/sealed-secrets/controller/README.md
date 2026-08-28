# Controller installation

Install the pinned controller with:

```bash
make sealed-secrets-install-controller
```

The script verifies the vendored v0.36.6 manifest against
`../CONTROLLER_SHA256` and applies this Kustomization:

```bash
kubectl apply -k infra/k8s/security/sealed-secrets/controller
```

No remote manifest is executed during installation.
