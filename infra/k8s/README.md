# NChat Kubernetes/k3s manifests

## Objetivo

Esta pasta contem os manifests iniciais Kubernetes/k3s do NChat para ambientes dev/staging revisaveis. A entrega prepara o repositorio para uma futura adocao de GitOps, mas nao configura ArgoCD, Helm, Sealed Secrets, cert-manager ou manifests de producao.

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
    └── k3s-dev/
        ├── kustomization.yaml
        ├── ingress.yaml
        ├── namespace-patch.yaml
        ├── configmap-patch.yaml
        └── patches/
```

O arquivo `labels.yaml` foi omitido porque os labels sao declarados diretamente nos recursos e o overlay adiciona `app.kubernetes.io/instance: k3s-dev` via o transformador `labels` do Kustomize. Tags de imagem tambem ficam em `overlays/k3s-dev/kustomization.yaml` no bloco `images`.

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

A validacao renderiza o overlay `infra/k8s/overlays/k3s-dev` para `/tmp`. Quando `kubectl` esta disponivel e o API server configurado responde rapidamente, o script executa `kubectl apply --dry-run=client --validate=false`. Se o cluster configurado estiver inacessivel, o dry-run e pulado para manter a validacao independente de cluster real. Se `kubeconform` existir localmente, ele tambem e executado.

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

`base/secrets.example.yaml` e apenas modelo e nao entra no `base/kustomization.yaml`. Nao aplique esse arquivo em producao. Secrets reais devem ficar fora do repositorio e futuramente serao substituidos por Sealed Secrets ou External Secrets.

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
- Sem TLS real.
- Sem cert-manager.
- Sem Sealed Secrets.
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
- Substituir secrets manuais por Sealed Secrets ou External Secrets.
- Configurar TLS publico com cert-manager ou mecanismo definido para o MVP.
- Adotar GitOps/ArgoCD.
- Definir manifests de data services ou contratos para servicos externos.
- Adicionar observabilidade de workloads, metrics scraping e traces.
