# TASK-09 - Manifests iniciais K8s/k3s

## Status

Validado localmente na branch `chore/task-009-k8s-k3s-manifests`; PR pendente.

## Objetivo

Criar a base inicial Kubernetes/k3s do NChat.

## Entregas

- Namespace
- Kustomize base
- Overlay k3s-dev
- Deployments
- Services
- Ingress
- ConfigMap
- Secret example
- ServiceAccounts
- ResourceQuota
- LimitRange
- NetworkPolicies
- Scripts
- CI check
- Documentacao

## Workloads

| Workload             | Imagem placeholder                                       | Porta | Probes                 | Service                     |
| -------------------- | -------------------------------------------------------- | ----: | ---------------------- | --------------------------- |
| web                  | `ghcr.io/nicrepository/nchat/web:0.0.0`                  |  8080 | TCP `http`             | `nchat-web:80`              |
| auth-service         | `ghcr.io/nicrepository/nchat/auth-service:0.0.0`         |  8081 | `/healthz` e `/readyz` | `auth-service:8081`         |
| chat-service         | `ghcr.io/nicrepository/nchat/chat-service:0.0.0`         |  8082 | `/healthz` e `/readyz` | `chat-service:8082`         |
| file-service         | `ghcr.io/nicrepository/nchat/file-service:0.0.0`         |  8083 | `/healthz` e `/readyz` | `file-service:8083`         |
| notification-service | `ghcr.io/nicrepository/nchat/notification-service:0.0.0` |  8084 | `/healthz` e `/readyz` | `notification-service:8084` |
| admin-service        | `ghcr.io/nicrepository/nchat/admin-service:0.0.0`        |  8085 | `/healthz` e `/readyz` | `admin-service:8085`        |
| search-service       | `ghcr.io/nicrepository/nchat/search-service:0.0.0`       |  8086 | `/healthz` e `/readyz` | `search-service:8086`       |
| media-service        | `ghcr.io/nicrepository/nchat/media-service:0.0.0`        |  8087 | `/healthz` e `/readyz` | `media-service:8087`        |

## Seguranca inicial

- `runAsNonRoot: true`
- `allowPrivilegeEscalation: false`
- `readOnlyRootFilesystem: true`
- `capabilities.drop: [ALL]`
- `automountServiceAccountToken: false`
- requests/limits em todos os containers
- secrets reais fora do repo
- `secrets.example.yaml` fora do `kustomization.yaml`
- NetworkPolicy inicial com default deny de ingress, trafego interno e Traefik k3s

## Limitacoes

- Sem producao
- Sem TLS
- Sem cert-manager
- Sem Sealed Secrets
- Sem ArgoCD
- Sem Dockerfiles/imagens reais
- Sem banco/cache/storage em K8s
- Sem HA
- Sem Istio

## Comandos

```bash
make k8s-render
make k8s-validate
make k8s-apply-dev
make k8s-status-dev
make k8s-delete-dev
make k8s-ci
```

## Definition of Done

- [x] Base criada
- [x] Overlay k3s-dev criado
- [x] Deployments criados
- [x] Services criados
- [x] Ingress criado
- [x] ConfigMap criado
- [x] Secret example criado fora do kustomization
- [x] NetworkPolicy criada
- [x] ResourceQuota/LimitRange criados
- [x] Scripts criados
- [x] CI check criado
- [x] README atualizado
- [x] Runbook criado
- [x] make k8s-validate passa
- [x] make ci passa
- [ ] PR aberto
