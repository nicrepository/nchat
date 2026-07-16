# Runbook — nchat-dev no servidor

Todos os comandos deste documento são manuais. Eles **não foram executados** durante
esta entrega. Execute-os somente em `srv-apps-01`, com revisão por outra pessoa e
confirmação do contexto antes de cada bloco.

## 1. Topologia fora do Git e pré-checagens

Copie o exemplo, substitua todos os placeholders por valores aprovados e mantenha o
arquivo local ignorado. Não use valores deste documento e nunca faça commit do
arquivo. O validador lê o formato sem executar o `.env` como shell:

```bash
umask 077
cp infra/k8s/overlays/nchat-dev-server/topology.env.example \
  infra/k8s/overlays/nchat-dev-server/topology.env
chmod 0600 infra/k8s/overlays/nchat-dev-server/topology.env
# Edite NCHAT_DEV_NODE_IP, NCHAT_DEV_NODE_CIDR e NCHAT_DEV_HOST.
# Confirme que nenhum REPLACE_ME permanece.
! grep -q REPLACE_ME infra/k8s/overlays/nchat-dev-server/topology.env

source scripts/deploy/nchat-dev/topology.sh
load_nchat_dev_topology infra/k8s/overlays/nchat-dev-server/topology.env
git check-ignore infra/k8s/overlays/nchat-dev-server/topology.env

hostname
kubectl config current-context
kubectl get node srv-apps-01
ss -ltnup | grep -E ":(80|443|3478|${TURN_LISTEN_PORT}|${LIVEKIT_RTC_TCP_PORT}|${LIVEKIT_RTC_UDP_PORT})\\b" || true
```

3478 deve continuar pertencendo ao UniFi. Pare se qualquer porta configurada para
LiveKit/coturn já estiver ocupada. As portas do contrato vêm do example versionado;
não as duplique em templates.

No GitHub, crie o Environment protegido `nchat-dev` e configure, sem registrar os
valores em issues ou logs, estas variables não secretas:

- `NCHAT_DEV_NODE_IP`;
- `NCHAT_DEV_NODE_CIDR`, exatamente o mesmo IP com `/32`;
- `NCHAT_DEV_HOST`.

O workflow falha antes da renderização se alguma estiver ausente ou inválida. As
variables não substituem Secrets; credenciais continuam em SealedSecrets.

```bash
sudo install -d -m 0750 -o 70 -g 70 /mnt/hdd-geral/k3s/nchat-dev/postgres
sudo install -d -m 0750 -o 999 -g 999 /mnt/hdd-geral/k3s/nchat-dev/valkey
sudo install -d -m 0750 -o 65532 -g 65532 /mnt/hdd-geral/k3s/nchat-dev/seaweedfs
```

## 2. Namespace, PVs e RBAC

```bash
kubectl apply -f infra/k8s/overlays/nchat-dev-server/server/runner-rbac.yaml
kubectl apply -f infra/k8s/overlays/nchat-dev-server/server/persistent-volumes.yaml

kubectl get namespace nchat-dev
kubectl get pv nchat-dev-postgres nchat-dev-valkey nchat-dev-seaweedfs \
  -o custom-columns=NAME:.metadata.name,CLASS:.spec.storageClassName,RECLAIM:.spec.persistentVolumeReclaimPolicy,NODE:.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]
```

Os PVs são deliberadamente cluster-scoped e não pertencem ao workflow. Nunca altere
`Retain` para `Delete` neste ambiente.

## 3. Kubeconfig do runner

Gere certificado exclusivo; não copie o kubeconfig administrativo do k3s:

```bash
umask 077
openssl genrsa -out /tmp/nchat-dev-deployer.key 3072
openssl req -new -key /tmp/nchat-dev-deployer.key \
  -out /tmp/nchat-dev-deployer.csr -subj '/CN=nchat-dev-deployer'

kubectl delete csr nchat-dev-deployer --ignore-not-found
kubectl create -f - <<EOF
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata:
  name: nchat-dev-deployer
spec:
  request: $(base64 -w0 /tmp/nchat-dev-deployer.csr)
  signerName: kubernetes.io/kube-apiserver-client
  expirationSeconds: 31536000
  usages: [client auth]
EOF
kubectl certificate approve nchat-dev-deployer
kubectl get csr nchat-dev-deployer -o jsonpath='{.status.certificate}' | base64 -d > /tmp/nchat-dev-deployer.crt

sudo useradd --create-home --shell /bin/bash nchat-runner
sudo install -d -m 0700 -o nchat-runner -g nchat-runner /home/nchat-runner/.kube
CLUSTER="$(kubectl config view --minify -o jsonpath='{.clusters[0].name}')"
SERVER="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > /tmp/nchat-ca.crt

KUBECONFIG=/tmp/nchat-dev.kubeconfig kubectl config set-cluster "$CLUSTER" --server="$SERVER" --certificate-authority=/tmp/nchat-ca.crt --embed-certs=true
KUBECONFIG=/tmp/nchat-dev.kubeconfig kubectl config set-credentials nchat-dev-deployer --client-certificate=/tmp/nchat-dev-deployer.crt --client-key=/tmp/nchat-dev-deployer.key --embed-certs=true
KUBECONFIG=/tmp/nchat-dev.kubeconfig kubectl config set-context nchat-dev-deployer --cluster="$CLUSTER" --user=nchat-dev-deployer --namespace=nchat-dev
KUBECONFIG=/tmp/nchat-dev.kubeconfig kubectl config use-context nchat-dev-deployer
sudo install -m 0600 -o nchat-runner -g nchat-runner /tmp/nchat-dev.kubeconfig /home/nchat-runner/.kube/config
rm -f /tmp/nchat-dev-deployer.key /tmp/nchat-dev-deployer.csr /tmp/nchat-dev-deployer.crt /tmp/nchat-ca.crt /tmp/nchat-dev.kubeconfig

sudo -u nchat-runner kubectl auth can-i patch deployments -n nchat-dev
sudo -u nchat-runner kubectl auth can-i get secrets -n nchat-dev
```

Resultados esperados: `yes` e `no`, respectivamente.

## 4. Runner GitHub Actions dedicado

Crie em `Settings > Actions > Runners` um runner novo para este repositório. Use o
token efêmero da interface e não reutilize o runner `nic-chat`.

```bash
sudo install -d -m 0750 -o nchat-runner -g nchat-runner /opt/actions-runner-nchat-dev
sudo -u nchat-runner bash
cd /opt/actions-runner-nchat-dev
curl -fL -o actions-runner.tar.gz 'REPLACE_WITH_OFFICIAL_LINUX_X64_RUNNER_URL'
tar xzf actions-runner.tar.gz
./config.sh --unattended \
  --url https://github.com/nicrepository/nchat \
  --token 'REPLACE_WITH_EPHEMERAL_REGISTRATION_TOKEN' \
  --name srv-apps-01-nchat-dev \
  --labels nchat-dev-deploy \
  --work _work
exit
cd /opt/actions-runner-nchat-dev
sudo ./svc.sh install nchat-runner
sudo ./svc.sh start
sudo ./svc.sh status

sudo -u nchat-runner test ! -S /var/run/docker.sock
sudo -u nchat-runner kubectl version --client -o yaml | grep 'kustomizeVersion: v5.7.1'
id nchat-runner
```

O usuário não pertence ao grupo `docker`. O deploy precisa somente de `kubectl`,
`curl`, `tar` e utilitários POSIX; builds permanecem nos runners GitHub-hosted.

## 5. Controller e Secrets strict-scope

O instalador confere o manifesto versionado antes do comando local
`kubectl apply -k`; não aplica URL remota:

```bash
make sealed-secrets-install-controller
```

Renderize a topologia e edite os valores secretos somente em diretório temporário:

```bash
umask 077
WORKING="$(mktemp -d "${TMPDIR:-/tmp}/nchat-dev-secrets.XXXXXX")"
trap 'rm -rf "$WORKING"' EXIT INT TERM
scripts/deploy/nchat-dev/render-topology-templates.sh "$WORKING"
cp infra/k8s/secrets/templates/nchat-dev-postgres-admin.template.yaml "$WORKING/postgres-admin.yaml"
cp infra/k8s/secrets/templates/nchat-dev-postgres-migrator.template.yaml "$WORKING/postgres-migrator.yaml"
cp infra/k8s/secrets/templates/nchat-dev-postgres-runtime.template.yaml "$WORKING/postgres-runtime.yaml"
install -m 0600 /dev/null "$WORKING/nchat-dev.env"

# Edite os cinco arquivos no diretório temporário. Use nchat_admin como admin,
# nchat_migrator na MIGRATIONS_DATABASE_URL e nchat_app na DATABASE_URL.
# O segredo TURN deve coincidir entre livekit.yaml e turnserver.conf.

if grep -R -q 'REPLACE_ME' "$WORKING"; then
  echo 'Há placeholders não preenchidos; recuse o sealing.' >&2
  exit 1
fi

kubectl create secret generic nchat-secrets -n nchat-dev \
  --from-env-file="$WORKING/nchat-dev.env" \
  --from-file=LIVEKIT_CONFIG="$WORKING/livekit.yaml" \
  --from-file=COTURN_CONFIG="$WORKING/turnserver.conf" \
  --dry-run=client -o yaml | \
kubeseal --scope strict --controller-name sealed-secrets \
  --controller-namespace kube-system --format yaml \
  > infra/k8s/secrets/sealed/nchat-dev.yaml

for secret in postgres-admin postgres-migrator postgres-runtime; do
  kubeseal --scope strict --controller-name sealed-secrets \
    --controller-namespace kube-system --format yaml \
    < "$WORKING/$secret.yaml" \
    > "infra/k8s/secrets/sealed/nchat-dev-$secret.yaml"
done

for sealed in infra/k8s/secrets/sealed/nchat-dev*.yaml; do
  kubeseal --validate --controller-name sealed-secrets \
    --controller-namespace kube-system < "$sealed"
  kubectl apply -f "$sealed"
done
```

As URLs usam o Service `postgres`, banco `nchat` e identidades distintas. Codifique
caracteres reservados das senhas no userinfo da URL. `VALKEY_URL` usa o Service
`valkey`. Não imprima os arquivos temporários nem habilite `set -x`.

No coturn, `allowed-peer-ip` prevalece sobre o range `denied-peer-ip` que o contém.
O renderizador produz exatamente uma exceção para `NCHAT_DEV_NODE_IP`. Como coturn e
LiveKit compartilham `hostNetwork`, o risco residual abrange outros processos no
mesmo host. Não adicione peers permitidos.

Crie o pull secret sem passar a credencial a subprocessos por argumento:

```bash
read -r -p 'GHCR username: ' GHCR_USER
read -r -s -p 'GHCR read:packages token: ' GHCR_TOKEN; echo
AUTH="$(printf '%s:%s' "$GHCR_USER" "$GHCR_TOKEN" | base64 -w0)"
printf '{"auths":{"ghcr.io":{"auth":"%s"}}}\n' "$AUTH" > "$WORKING/dockerconfig.json"
unset GHCR_TOKEN AUTH
kubectl create secret generic ghcr-pull -n nchat-dev \
  --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson="$WORKING/dockerconfig.json" \
  --dry-run=client -o yaml | \
kubeseal --scope strict --controller-name sealed-secrets \
  --controller-namespace kube-system --format yaml \
  > infra/k8s/secrets/sealed/nchat-dev-ghcr-pull.yaml
kubectl apply -f infra/k8s/secrets/sealed/nchat-dev-ghcr-pull.yaml
```

## 6. Firewall

Preserve 80/443 do Traefik e 3478 do UniFi. Não publique portas de mídia por WAN,
NAT ou port-forward:

```bash
sudo ufw allow "${TURN_LISTEN_PORT}/tcp" comment 'nchat-dev coturn'
sudo ufw allow "${TURN_LISTEN_PORT}/udp" comment 'nchat-dev coturn'
sudo ufw allow "${LIVEKIT_RTC_TCP_PORT}/tcp" comment 'nchat-dev livekit rtc'
sudo ufw allow "${LIVEKIT_RTC_UDP_PORT}/udp" comment 'nchat-dev livekit rtc'
sudo ufw allow "${TURN_RELAY_MIN_PORT}:${TURN_RELAY_MAX_PORT}/udp" comment 'nchat-dev coturn relay'
sudo ufw status numbered
```

Se o host usa nftables/firewalld, traduza somente essas regras. Não altere a
configuração global do Traefik. O hardening futuro é separar coturn e LiveKit em
IP/nó dedicado antes de qualquer mudança de exposição.

## 7. SeaweedFS e imagens externas

O `file-service` atual não possui cliente filer ou S3. O gateway S3 fica desabilitado,
sem porta 8333, Ingress ou credenciais. Não o habilite apenas por estar provisionado.
Uma futura ativação exige cliente real, autenticação obrigatória por Secret
strict-scope, NetworkPolicy exclusiva `file-service` → S3 e teste de acesso anônimo
negado. O master 9333 nunca deve ser liberado para aplicações.

Para atualizar uma imagem externa, consulte a tag oficial, confirme o manifest list
e `linux/amd64`, revise notas da release e altere tag e digest no mesmo diff:

```bash
docker buildx imagetools inspect 'REPLACE_ME_IMAGE:REPLACE_ME_TAG'
pnpm k8s:ci
```

Não copie digest de comentário, issue ou resultado de busca. O digest deve ter
`sha256:` mais 64 caracteres hexadecimais e pertencer exatamente à tag revisada.

## 8. Validação

```bash
kubectl kustomize infra/k8s/overlays/nchat-dev-server/data >/tmp/nchat-dev-data.yaml
kubectl kustomize infra/k8s/overlays/nchat-dev-server/migrations >/tmp/nchat-dev-migrations.yaml
kubectl kustomize infra/k8s/overlays/nchat-dev-server >/tmp/nchat-dev-application.yaml
kubectl get pods,pvc,ingress,certificate -n nchat-dev
kubectl get pv nchat-dev-postgres nchat-dev-valkey nchat-dev-seaweedfs
kubectl describe certificate nchat-dev-tls -n nchat-dev
kubectl get networkpolicy -n nchat-dev
kubectl describe networkpolicy nchat-allow-livekit-api-egress -n nchat-dev
kubectl get service -n nchat-dev -o wide
curl -fsS "https://${NCHAT_DEV_HOST}/" >/dev/null
ss -ltnup | grep -E ":(3478|${TURN_LISTEN_PORT}|${LIVEKIT_RTC_TCP_PORT}|${LIVEKIT_RTC_UDP_PORT})\\b"
```

No GitHub, confirme que o build terminou antes do deploy, que o runner foi
`srv-apps-01-nchat-dev` e que as imagens renderizadas usam `@sha256:`. O timeout do
workflow para migrations é 330s, ligeiramente superior ao deadline de 300s do Job.

Para diagnóstico de política, identifique primeiro os labels do pod e depois confira
somente a política daquele fluxo; não desative o default-deny nem crie egress amplo.

## 9. Rollback

Rollback de aplicação não reverte schema. Confirme compatibilidade da migration;
migration destrutiva exige correção forward.

```bash
kubectl rollout history deployment -n nchat-dev
set -Eeuo pipefail
source scripts/deploy/nchat-dev/lib.sh
for deployment in "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}"; do
  kubectl rollout undo "deployment/$deployment" -n nchat-dev
  kubectl rollout status "deployment/$deployment" -n nchat-dev --timeout=180s
done
curl -fsS "https://${NCHAT_DEV_HOST}/" >/dev/null
```

Para falha de dados, não apague PVC, PV ou diretórios. Preserve os PVs `Retain` e
restaure somente por procedimento de backup validado.

## 10. Rotação e TLS local

Rotacione credenciais de PostgreSQL, Valkey, GHCR, LiveKit e coturn pelo processo de
SealedSecret strict-scope, uma identidade por vez, validando rollout e revogando o
valor anterior. Nunca imprima o Secret aberto nem o armazene em ConfigMap. A exceção
coturn deve continuar sendo exatamente o node IP validado; exposição WAN permanece
proibida.

O material de desenvolvimento é gerado localmente e ignorado:

```bash
make dev-tls-generate
```

Remover a chave do HEAD não remove cópias do histórico Git. Após merge, rotacione o
par anteriormente compartilhado. Uma limpeza de histórico exige janela coordenada,
autorização explícita e comunicação aos clones; ela não faz parte deste runbook. Não
adicione allowlist global ao Gitleaks para esconder chaves futuras.
