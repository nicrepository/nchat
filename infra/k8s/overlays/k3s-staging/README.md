# k3s staging overlay

This overlay prepares a local/staging Kubernetes entrypoint for NChat on `staging.nchat.local`.

It includes:

- Namespace `nchat-staging`.
- Traefik Ingress with TLS enabled.
- TLS placeholder secret reference: `nchat-staging-tls`.
- Traefik `TLSOption` requiring `VersionTLS13` with strict SNI.

Limitations:

- This is not production configuration.
- Cert-manager is not configured in this task.
- ArgoCD/GitOps automation is not configured in this task.
- The TLS Secret must be created via SealedSecret or another approved external process.
- Traefik CRDs are required for `TLSOption`; k3s default Traefik usually includes them.
- If CRDs are absent, apply the Ingress TLS placeholder without `tls-option.yaml` or install the Traefik CRDs for the cluster.

Render and validate:

```bash
K8S_OVERLAY=infra/k8s/overlays/k3s-staging make k8s-render
make k8s-render-staging
make k8s-validate-staging
```
