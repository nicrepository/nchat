# NChat Kubernetes/k3s manifests

## Objetivo

Esta pasta contem os manifests iniciais Kubernetes/k3s do NChat para ambientes dev/staging revisaveis. A entrega prepara o repositorio para uma futura adocao de GitOps, mas nao configura ArgoCD, Helm, cert-manager ou manifests de producao.

## Estrutura

```text
infra/k8s/
├── base/
│   ├── namespace.yaml
│   ├── resource-quota.yaml
│   ├── limit-range.yaml
│   ├── network-policy.yaml
│   ├── configmap.yaml
│   ├── secrets.example.yaml
│   ├── serviceaccounts.yaml
│   ├── web/
│   └── services/
└── overlays/
    ├── k3s-dev/
    │   ├── kustomization.yaml
    │   ├── ingress.yaml
    │   ├── namespace-patch.yaml
    │   ├── configmap-patch.yaml
    │   └── patches/
    └── k3s-staging/
        ├── kustomization.yaml
        ├── ingress.yaml
        ├── namespace-patch.yaml
        ├── tls-option.yaml
        └── patches/
```

O arquivo `labels.yaml` foi omitido porque os labels sao declarados diretamente nos recursos e os overlays adicionam `app.kubernetes.io/instance` via o transformador `labels` do Kustomize. Tags de imagem tambem ficam nos `kustomization.yaml` dos overlays no bloco `images`.

## Renderizacao

```bash
make k8s-render
# ou
pnpm k8s:render
```

O script usa `kubectl kustomize` quando `kubectl` existe e faz fallback para `kustomize build` quando apenas `kustomize` esta instalado.

## Validacao

```bash
make k8s-validate
make k8s-ci
```

A validacao CI renderiza `k3s-dev` e `k3s-staging` para `/tmp`. Use `K8S_OVERLAY=infra/k8s/overlays/k3s-staging make k8s-ci` para validar apenas um overlay. Quando `kubectl` esta disponivel e o API server configurado responde rapidamente, o script executa `kubectl apply --dry-run=client --validate=false`. Se o cluster configurado estiver inacessivel, o dry-run e pulado para manter a validacao independente de cluster real. Se `kubeconform` existir localmente, ele tambem e executado com `-ignore-missing-schemas` para permitir CRDs como Traefik `TLSOption`.

## Aplicar em k3s dev

```bash
make k8s-apply-dev
make k8s-status-dev
```

O overlay assume Traefik padrao do k3s e cria o Ingress `nchat-k3s-dev` para o host `nchat.local`. Em um k3s/k3d local, adicione o host no `/etc/hosts` se necessario:

```text
127.0.0.1 nchat.local
```

Depois acesse `http://nchat.local/` ou use `curl` com header `Host: nchat.local`.

## Staging TLS placeholder

O overlay `k3s-staging` expoe `staging.nchat.local` via Ingress Traefik com TLS habilitado e referencia o Secret `nchat-staging-tls`. O recurso `TLSOption` define `VersionTLS13` e `sniStrict: true`.

```bash
make k8s-render-staging
make k8s-validate-staging
```

Esse overlay exige Traefik CRDs para aplicar `TLSOption`. Em k3s com Traefik padrao, eles normalmente existem. Se nao existirem, aplique apenas o Ingress TLS ou instale os CRDs do Traefik para o cluster. Cert-manager nao esta configurado nesta tarefa.

## Remocao

```bash
make k8s-delete-dev
```

O script exige confirmacao digitando `DELETE` antes de remover os recursos do overlay.

## Workloads

| Workload             | Image placeholder                                        | Port |
| -------------------- | -------------------------------------------------------- | ---: |
| web                  | `ghcr.io/nicrepository/nchat/web:0.0.0`                  | 8080 |
| auth-service         | `ghcr.io/nicrepository/nchat/auth-service:0.0.0`         | 8081 |
| chat-service         | `ghcr.io/nicrepository/nchat/chat-service:0.0.0`         | 8082 |
| file-service         | `ghcr.io/nicrepository/nchat/file-service:0.0.0`         | 8083 |
| notification-service | `ghcr.io/nicrepository/nchat/notification-service:0.0.0` | 8084 |
| admin-service        | `ghcr.io/nicrepository/nchat/admin-service:0.0.0`        | 8085 |
| search-service       | `ghcr.io/nicrepository/nchat/search-service:0.0.0`       | 8086 |
| media-service        | `ghcr.io/nicrepository/nchat/media-service:0.0.0`        | 8087 |

As imagens sao placeholders versionados. Dockerfiles reais, build e push de imagens entram em tarefas futuras.

## Configuracao e secrets

`base/configmap.yaml` contem apenas dados nao sensiveis. As referencias para PostgreSQL, Valkey e SeaweedFS apontam para nomes de servicos placeholder (`postgres`, `valkey`, `seaweedfs-filer`, `seaweedfs-s3`). Esses data services nao sao criados nesta tarefa; serao modelados em tarefa futura ou apontados para servicos externos.

`base/secrets.example.yaml` e apenas modelo com chaves vazias e nao entra no `base/kustomization.yaml`. Nao aplique esse arquivo em producao. Secrets reais devem ficar fora do repositorio. Para o MVP, secrets versionados devem ser gerados como SealedSecrets com escopo `strict`; External Secrets fica fora desta tarefa.

### Auth trusted proxy and email handoff secrets

For auth-service behind Traefik, set `AUTH_TRUSTED_PROXY_CIDRS` to the Traefik
pod/source CIDR so public auth rate limiters can safely use validated
`X-Forwarded-For` or `X-Real-IP`. Empty keeps the safe default: ignore forwarded
headers and use `RemoteAddr`. The k3s-dev overlay uses the common local k3s pod
CIDR `10.42.0.0/16`; replace it if your cluster differs.

`AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` is a secret, not ConfigMap data. Provide it
through `nchat-secrets` or SealedSecrets as standard base64 that decodes to
exactly 32 bytes, for example `openssl rand -base64 32`. It enables encrypted
password reset and invite token handoff rows; SMTP/notification workers remain
out of scope for these manifests.

## Seguranca inicial

- `runAsNonRoot: true` nos Pods.
- `allowPrivilegeEscalation: false` nos containers.
- `readOnlyRootFilesystem: true` nos containers.
- `capabilities.drop: [ALL]` nos containers.
- `automountServiceAccountToken: false` nos ServiceAccounts e Pods.
- Requests e limits definidos para todos os workloads.
- `ResourceQuota` e `LimitRange` para o namespace `nchat`.
- `NetworkPolicy` com default deny de ingress, liberacao entre Pods do namespace e liberacao inicial para Traefik no `kube-system` quando rotulado.

## Limitacoes

- Nao e producao.
- Sem TLS publico real.
- Sem cert-manager.
- Sealed Secrets estruturado, mas sem controller aplicado por CI.
- Sem ArgoCD.
- Sem Helm.
- Sem Istio.
- Sem StatefulSets reais para PostgreSQL, Valkey ou SeaweedFS.
- Sem Dockerfiles e sem imagens reais.
- Sem HA.
- A aplicacao de `NetworkPolicy` em k3s depende do CNI/controlador ativo.
- Egress ainda nao recebe default deny neste overlay para nao quebrar cenarios dev antes de data services K8s/external services estarem definidos.

## Proximos passos

- Criar Dockerfiles dos servicos Go e do web.
- Criar pipeline de build/push de imagens.
- Aplicar controller Sealed Secrets em clusters dev/staging.
- Configurar TLS publico com cert-manager ou mecanismo definido para o MVP.
- Adotar GitOps/ArgoCD.
- Definir manifests de data services ou contratos para servicos externos.
- Adicionar observabilidade de workloads, metrics scraping e traces.
