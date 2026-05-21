# Controller installation

Install the pinned controller with:

```bash
make sealed-secrets-install-controller
```

The script applies the official release manifest:

```bash
kubectl apply -f https://github.com/bitnami-labs/sealed-secrets/releases/download/v0.36.6/controller.yaml
```

The remote manifest is intentionally not included in mandatory CI checks to avoid network-dependent validation.
