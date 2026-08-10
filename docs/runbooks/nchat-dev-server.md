# Runbook — nchat-dev no servidor

Todos os comandos deste documento são manuais. Eles **não foram executados** durante
esta entrega. Execute cada comando somente no ambiente indicado pela respectiva tag —
`[notebook]`, `[srv-apps-01]` ou `[GitHub]` —, com revisão por outra pessoa e
confirmação do contexto antes de cada bloco.

`srv-apps-01` é um servidor Kubernetes (k3s) compartilhado que já hospeda outras
aplicações em produção, o Traefik global, Docker e o UniFi. Este runbook autoriza
alterações **apenas** no namespace `nchat-dev` e nos recursos explicitamente listados
abaixo. Ele **não autoriza** alteração em workloads, Services, Ingresses,
NetworkPolicies, Secrets, ConfigMaps, PVs ou namespaces de outras aplicações.

Legenda usada nos blocos de comando:

- **[notebook]** — executar no notebook administrativo (fora de `srv-apps-01`).
- **[srv-apps-01]** — executar no servidor compartilhado.
- **[GitHub]** — ação feita na interface web do GitHub.

## 0. Regras de segurança e escopo

Antes de qualquer alteração, confirme o alvo e o estado atual do cluster. Estes
comandos são **somente leitura**:

```bash
# [srv-apps-01]
hostname
kubectl config current-context
kubectl get nodes -o wide
kubectl get pods -A
kubectl get ingress -A
kubectl get svc -A
kubectl get pv
sudo ss -ltnup
ip -br -4 address
ip -4 route
```

**Pare imediatamente** se `hostname` não for `srv-apps-01` ou se
`kubectl config current-context` não apontar para o cluster esperado. Não prossiga
"para conferir" — pare e escale para revisão humana.

Regras que valem para todo o restante deste runbook:

- Nenhum recurso fora do namespace `nchat-dev` deve ser alterado, com as únicas
  exceções enumeradas abaixo:
  - os três PVs cluster-scoped explicitamente nomeados `nchat-dev-*`
    (`nchat-dev-postgres`, `nchat-dev-valkey`, `nchat-dev-seaweedfs`);
  - o CertificateSigningRequest exclusivo usado pelo deployer;
  - os recursos RBAC explicitamente versionados em
    `infra/k8s/overlays/nchat-dev-server/server/runner-rbac.yaml`;
  - o controller Sealed Secrets em `kube-system`, **somente se ele ainda não
    existir** e após revisão específica do manifesto.

  Nenhum outro recurso cluster-scoped ou em outro namespace está autorizado.

- Nenhum restart de k3s, Traefik, Docker ou do servidor é permitido.
- Nenhum `kubectl delete` genérico (namespace, PV, coringa) é permitido.
- Nenhum namespace existente pode ser reutilizado; `nchat-dev` deve ser criado do zero
  ou já pertencer exclusivamente a este projeto.
- Nenhum PV existente pode ser modificado; PVs pré-existentes exigem comparação
  manual antes de qualquer `apply` (seção 5).
- Nenhuma regra global de firewall pode ser removida ou reordenada.
- `kubectl apply --force`, `kubectl replace --force`, `kubectl delete namespace` e
  `kubectl delete pv` são proibidos neste runbook.

Este runbook não autoriza alteração em workloads, Services, Ingresses,
NetworkPolicies, Secrets, ConfigMaps, PVs ou namespaces de outras aplicações.

Várias seções usam caminhos relativos, `make` e scripts do repositório. Defina uma
vez, no servidor, o diretório aprovado do clone e execute os blocos seguintes a
partir dele:

```bash
# [srv-apps-01]
NCHAT_REPO_DIR='/CAMINHO/APROVADO/DO/CLONE/NCHAT'

test -d "$NCHAT_REPO_DIR/.git"
cd "$NCHAT_REPO_DIR"

git status --short
git branch --show-current
```

Para ações operacionais, o clone deve estar em `develop` atualizado e com working
tree limpo. A única exceção é a branch operacional específica usada na geração dos
SealedSecrets (seções 9–10).

## 1. Conferência de workflows de deploy pendentes

O workflow de deploy usa runner self-hosted com label `nchat-dev-deploy`. Como
`deploy-nchat-dev.yml` só aceita `workflow_call`, ele não gera execução
independente: o job self-hosted pertence à execução do workflow chamador,
`images.yml`, que dispara em push para `develop`. É `images.yml` que deve ser
consultado. Antes de instalar ou iniciar o runner, verifique se já existe uma
execução em andamento que possa disparar um deploy prematuro assim que o runner
ficar disponível:

```bash
# [notebook]
gh run list \
  --workflow images.yml \
  --branch develop \
  --limit 10 \
  --json databaseId,status,conclusion,headSha,displayTitle,url \
  --jq '.[]'
```

Analise qualquer execução com estado `queued`, `waiting` ou `in_progress`. Se for uma
execução prematura do deploy (por exemplo, disparada antes do ambiente estar pronto),
cancele-a explicitamente:

```bash
# [notebook]
gh run cancel <RUN_ID>
```

Esta checagem é repetida na seção 15, imediatamente antes de habilitar o runner.

Atenção: `images.yml` executa em **qualquer** push para `develop`, sem filtro por
caminho. O merge de uma alteração exclusivamente documental — inclusive o merge
deste próprio runbook — também constrói todas as imagens e chama o deploy; o job
reutilizável ficará aguardando o runner com label `nchat-dev-deploy`. Portanto,
execute esta checagem (e o eventual cancelamento) imediatamente após o merge deste
runbook.

## 2. Pré-checagens somente leitura

Copie o exemplo, substitua todos os placeholders por valores aprovados e mantenha o
arquivo local ignorado. Não use valores deste documento e nunca faça commit do
arquivo. O validador lê o formato sem executar o `.env` como shell:

```bash
# [notebook]
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
```

O `topology.env` criado acima existe apenas no notebook. As variáveis
(`TURN_LISTEN_PORT`, `LIVEKIT_RTC_TCP_PORT`, `LIVEKIT_RTC_UDP_PORT`, etc.) também
são necessárias nas sessões de shell em `srv-apps-01`. **Não copie o arquivo por
canal inseguro** (e-mail, chat, issue). Em vez disso, crie um `topology.env` local e
ignorado também no clone do repositório em `srv-apps-01`, preenchendo os mesmos
valores aprovados:

```bash
# [srv-apps-01]
umask 077
cp infra/k8s/overlays/nchat-dev-server/topology.env.example \
  infra/k8s/overlays/nchat-dev-server/topology.env

chmod 0600 infra/k8s/overlays/nchat-dev-server/topology.env

# Edite os valores aprovados e confirme que correspondem às GitHub Environment Variables.
! grep -q REPLACE_ME infra/k8s/overlays/nchat-dev-server/topology.env

source scripts/deploy/nchat-dev/topology.sh
load_nchat_dev_topology \
  infra/k8s/overlays/nchat-dev-server/topology.env

git check-ignore \
  infra/k8s/overlays/nchat-dev-server/topology.env
```

Repita `source` + `load_nchat_dev_topology` em cada nova sessão de shell no servidor
que use variáveis `${...}` da topologia. Os comandos abaixo exigem acesso ao cluster
e ao host, com a topologia carregada na mesma sessão:

```bash
# [srv-apps-01]
kubectl get node srv-apps-01
sudo ss -ltnup | grep -E ":(80|443|3478|${TURN_LISTEN_PORT}|${LIVEKIT_RTC_TCP_PORT}|${LIVEKIT_RTC_UDP_PORT})\\b" || true
```

A checagem de portas acima só busca números — para inspecionar processo, protocolo e
endereço de bind antes de decidir qualquer coisa, use sempre a saída completa:

```bash
# [srv-apps-01]
sudo ss -ltnup
```

Interprete o resultado assim:

- 80 e 443 podem estar ocupadas pelo Traefik — isso é esperado e não deve ser tocado.
- 3478 pode estar ocupada pelo UniFi — não deve ser alterada.
- As portas escolhidas para NChat (`TURN_LISTEN_PORT`, `LIVEKIT_RTC_TCP_PORT`,
  `LIVEKIT_RTC_UDP_PORT`) devem estar livres antes do deploy.
- Nunca mate processos, pare containers ou reinicie serviços para "liberar" uma
  porta. Se houver conflito, resolva alterando a topologia do NChat (nova revisão de
  código), não o ambiente do servidor.

## 3. Topologia local

```bash
# [srv-apps-01]
sudo install -d -m 0750 -o 70 -g 70 /mnt/hdd-geral/k3s/nchat-dev/postgres
sudo install -d -m 0750 -o 999 -g 999 /mnt/hdd-geral/k3s/nchat-dev/valkey
sudo install -d -m 0750 -o 65532 -g 65532 /mnt/hdd-geral/k3s/nchat-dev/seaweedfs
```

As portas do contrato vêm do example versionado; não as duplique em templates.

## 4. Configuração do GitHub Environment

No GitHub, crie o Environment protegido `nchat-dev` e configure, sem registrar os
valores em issues ou logs, estas variables não secretas:

- `NCHAT_DEV_NODE_IP`;
- `NCHAT_DEV_NODE_CIDR`, exatamente o mesmo IP com `/32`;
- `NCHAT_DEV_HOST`, que deve ser um hostname RFC 1123 completo: rótulos de 1 a
  63 caracteres alfanuméricos com hífen apenas internamente (nunca inicial ou
  final), sem rótulo vazio, sem ponto inicial/final e com no máximo 253
  caracteres no total. Maiúsculas, `_`, espaços e quebras de linha são
  rejeitados.

O workflow falha antes da renderização se alguma estiver ausente ou inválida. As
variables não substituem Secrets; credenciais continuam em SealedSecrets.

Além da validação de `NCHAT_DEV_HOST`, o deploy falha antes de qualquer
`kubectl apply` se o manifesto renderizado (`data.yaml`, `migrations.yaml` ou
`application.yaml`) ainda contiver um placeholder não resolvido no formato
`REPLACE_ME_[A-Z0-9_]+` — não apenas `REPLACE_ME_HOST`. A mensagem de erro
aponta arquivo e linha do token, nunca o conteúdo completo da linha (que pode
pertencer a um Secret/SealedSecret).

## 5. Namespace, RBAC e PVs

Antes de aplicar qualquer manifesto, revise o diff lógico contra o estado atual do
cluster:

```bash
# [srv-apps-01]
# kubectl diff retorna 0 (sem diferenças) ou 1 (diferenças encontradas); códigos
# maiores indicam erro real de API, autenticação ou manifesto e não podem ser
# silenciados com `|| true`.
kubectl_diff_review() {
  local manifest="$1"
  local rc

  if kubectl diff -f "$manifest"; then
    return 0
  else
    rc=$?
  fi

  if [ "$rc" -eq 1 ]; then
    return 0
  fi

  echo "ERRO: kubectl diff falhou para $manifest, código $rc." >&2
  return "$rc"
}

kubectl_diff_review \
  infra/k8s/overlays/nchat-dev-server/server/runner-rbac.yaml

kubectl_diff_review \
  infra/k8s/overlays/nchat-dev-server/server/persistent-volumes.yaml

kubectl get pv \
  nchat-dev-postgres nchat-dev-valkey nchat-dev-seaweedfs \
  --ignore-not-found
```

Se qualquer um desses PVs já existir, compare manualmente antes de prosseguir:
`hostPath`, `storageClassName`, `capacity`, `persistentVolumeReclaimPolicy`,
`nodeAffinity` e `claimRef`. Não sobrescreva um PV preexistente automaticamente.

> Se qualquer recurso com prefixo `nchat-dev` já existir e não corresponder
> exatamente ao manifesto revisado, pare o procedimento. Não use `--force`,
> `--replace`, delete/recreate ou edição manual para contornar a divergência.

Somente após a revisão:

```bash
# [srv-apps-01]
kubectl apply -f infra/k8s/overlays/nchat-dev-server/server/runner-rbac.yaml
kubectl apply -f infra/k8s/overlays/nchat-dev-server/server/persistent-volumes.yaml

kubectl get namespace nchat-dev
kubectl get pv nchat-dev-postgres nchat-dev-valkey nchat-dev-seaweedfs \
  -o custom-columns=NAME:.metadata.name,CLASS:.spec.storageClassName,RECLAIM:.spec.persistentVolumeReclaimPolicy,NODE:.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]
```

Os PVs são deliberadamente cluster-scoped e não pertencem ao workflow. Nunca altere
`Retain` para `Delete` neste ambiente.

O Role `nchat-dev-deployer` inclui `get/list/watch/create/patch/update` em
`ingressroutes.traefik.io`, necessário desde que o overlay passou a incluir
`IngressRoute/nchat-dev-uploads` (RF-32/#455). Sem esse grant, o apply falha a
meio caminho com `cannot get resource "ingressroutes"`. Valide com:

```bash
# [srv-apps-01]
kubectl auth can-i get ingressroutes.traefik.io -n nchat-dev --as=nchat-dev-deployer
kubectl auth can-i create ingressroutes.traefik.io -n nchat-dev --as=nchat-dev-deployer
kubectl auth can-i patch ingressroutes.traefik.io -n nchat-dev --as=nchat-dev-deployer
kubectl auth can-i update ingressroutes.traefik.io -n nchat-dev --as=nchat-dev-deployer
```

Todos devem responder `yes`. O deploy também verifica isso, sob a identidade real
do runner (sem `--as`), antes de renderizar qualquer overlay — uma permissão
ausente falha o deploy imediatamente, sem aplicar nenhum recurso.

## 6. Usuário e kubeconfig do runner

Criação idempotente do usuário — não altere grupos, UID, home ou shell de um usuário
já existente automaticamente. Se `nchat-runner` já existir com configuração
inesperada, pare para revisão manual:

```bash
# [srv-apps-01]
if ! id nchat-runner >/dev/null 2>&1; then
  sudo useradd --create-home --shell /bin/bash nchat-runner
fi
id nchat-runner
```

Gere certificado exclusivo; não copie o kubeconfig administrativo do k3s:

```bash
# [srv-apps-01]
umask 077
openssl genrsa -out /tmp/nchat-dev-deployer.key 3072

# Nome de objeto exclusivo por execução; o CN permanece estável. Nenhuma exclusão
# de CSR anterior é necessária.
CSR_NAME="nchat-dev-deployer-$(date +%Y%m%d%H%M%S)"

openssl req -new \
  -key /tmp/nchat-dev-deployer.key \
  -out /tmp/nchat-dev-deployer.csr \
  -subj '/CN=nchat-dev-deployer'

kubectl create -f - <<EOF
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata:
  name: ${CSR_NAME}
spec:
  request: $(base64 -w0 /tmp/nchat-dev-deployer.csr)
  signerName: kubernetes.io/kube-apiserver-client
  expirationSeconds: 31536000
  usages: [client auth]
EOF
kubectl certificate approve "$CSR_NAME"

# A emissão do certificado pode não estar disponível imediatamente após o approve.
for attempt in $(seq 1 30); do
  CERTIFICATE_DATA="$(
    kubectl get csr "$CSR_NAME" \
      -o jsonpath='{.status.certificate}'
  )"

  if [ -n "$CERTIFICATE_DATA" ]; then
    break
  fi

  sleep 2
done

if [ -z "${CERTIFICATE_DATA:-}" ]; then
  echo "ERRO: certificado do CSR $CSR_NAME não foi emitido." >&2
  exit 1
fi

printf '%s' "$CERTIFICATE_DATA" |
base64 -d > /tmp/nchat-dev-deployer.crt

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

## 7. Download, checksum e configuração do runner (sem iniciar)

Crie em `Settings > Actions > Runners` um runner novo para este repositório. Use o
token efêmero da interface e não reutilize o runner `nic-chat`:

```
# [GitHub]
Settings > Actions > Runners > New self-hosted runner
```

Copie da interface oficial do GitHub a URL do pacote e o SHA256 publicado — nunca uma
URL ou hash copiado de issue, comentário ou outra fonte não oficial:

```bash
# [srv-apps-01]
sudo install -d -m 0750 -o nchat-runner -g nchat-runner /opt/actions-runner-nchat-dev
sudo -u nchat-runner bash
cd /opt/actions-runner-nchat-dev

RUNNER_URL='<URL_OFICIAL_FORNECIDA_PELO_GITHUB>'
RUNNER_SHA256='<SHA256_OFICIAL_FORNECIDO_PELO_GITHUB>'
test "$RUNNER_URL" != '<URL_OFICIAL_FORNECIDA_PELO_GITHUB>'
test "$RUNNER_SHA256" != '<SHA256_OFICIAL_FORNECIDO_PELO_GITHUB>'
printf '%s\n' "$RUNNER_SHA256" | grep -Eq '^[a-fA-F0-9]{64}$'

curl --proto '=https' --tlsv1.2 \
  --fail \
  --location \
  --show-error \
  --output actions-runner.tar.gz \
  "$RUNNER_URL"

printf '%s  %s\n' \
  "$RUNNER_SHA256" \
  actions-runner.tar.gz |
sha256sum --check -

tar xzf actions-runner.tar.gz
read -r -s -p 'Token efêmero de registro do runner: ' RUNNER_TOKEN; echo
./config.sh --unattended \
  --url https://github.com/nicrepository/nchat \
  --token "$RUNNER_TOKEN" \
  --name srv-apps-01-nchat-dev \
  --labels nchat-dev-deploy \
  --work _work
unset RUNNER_TOKEN
exit
cd /opt/actions-runner-nchat-dev
sudo ./svc.sh install nchat-runner
```

**Não execute `sudo ./svc.sh start` agora.** O runner deve ficar instalado, mas
parado, até que o ambiente completo (Environment do GitHub, diretórios, PVs,
namespace, RBAC, kubeconfig, Sealed Secrets, pull secret do GHCR, firewall e
validações pré-deploy) esteja concluído. O início controlado acontece na seção 15.

Valide o acesso ao Docker socket — o comando anterior deste runbook só checava a
existência do arquivo, não a permissão do usuário:

```bash
# [srv-apps-01]
if id -nG nchat-runner | tr ' ' '\n' | grep -qx docker; then
  echo 'ERRO: nchat-runner pertence ao grupo docker.' >&2
  exit 1
fi

if sudo -u nchat-runner test -r /var/run/docker.sock ||
   sudo -u nchat-runner test -w /var/run/docker.sock; then
  echo 'ERRO: nchat-runner consegue acessar o socket Docker.' >&2
  exit 1
fi

echo 'OK: runner sem acesso ao Docker socket.'

id nchat-runner
```

O deploy não usa o Kustomize embutido no `kubectl`: o workflow executa
`scripts/deploy/nchat-dev/install-kustomize.sh`, que baixa, verifica por SHA-256 e
instala um binário standalone da versão aprovada. Valide exatamente esse caminho —
o script imprime o diretório de instalação; o binário é `kustomize` dentro dele.
A execução pressupõe a raiz do clone do repositório:

```bash
# [srv-apps-01]
cd "$NCHAT_REPO_DIR"

sudo -u nchat-runner bash -lc '
  set -Eeuo pipefail

  KUSTOMIZE_BIN="$(scripts/deploy/nchat-dev/install-kustomize.sh)/kustomize"

  test -x "$KUSTOMIZE_BIN"
  "$KUSTOMIZE_BIN" version
'
```

Se `/var/run/docker.sock` não existir no host, isso não é um erro — os testes `-r`/
`-w` simplesmente falham e o bloco acima já reporta sucesso.

O usuário não pertence ao grupo `docker`. O deploy precisa somente de `kubectl`,
`curl`, `tar` e utilitários POSIX; builds permanecem nos runners GitHub-hosted.

## 8. Controller Sealed Secrets

O instalador confere o manifesto versionado antes do comando local
`kubectl apply -k`; não aplica URL remota. Como o controller vive em `kube-system`
(namespace compartilhado), instale-o **somente se ainda não existir**:

```bash
# [srv-apps-01]
if kubectl get deployment sealed-secrets \
  -n kube-system >/dev/null 2>&1; then
  echo 'Sealed Secrets já está instalado.'
  kubectl get deployment sealed-secrets -n kube-system -o wide
  kubectl get crd sealedsecrets.bitnami.com
  echo 'Revise versão e compatibilidade. Não reinstale automaticamente.'
else
  make sealed-secrets-install-controller
fi
```

Se o controller já existir com configuração ou versão diferente do manifesto
versionado, pare o procedimento para revisão humana. Não atualize automaticamente um
controller compartilhado.

## 9. Geração dos Secrets

Antes de gerar ou validar qualquer SealedSecret, confirme que a sessão aponta para o
cluster correto e que o controller e o `kubeseal` estão disponíveis:

```bash
# [srv-apps-01]
kubectl config current-context
# Confirme que o contexto acima é o cluster esperado antes de continuar.
kubectl get deployment sealed-secrets -n kube-system
kubeseal --version
```

Renderize a topologia e edite os valores secretos somente em diretório temporário:

```bash
# [srv-apps-01]
umask 077
WORKING="$(mktemp -d "${TMPDIR:-/tmp}/nchat-dev-secrets.XXXXXX")"
trap 'shred -u "$WORKING/dockerconfig.json" 2>/dev/null; rm -rf "$WORKING"' EXIT INT TERM
scripts/deploy/nchat-dev/render-topology-templates.sh "$WORKING"
cp infra/k8s/secrets/templates/nchat-dev-postgres-admin.template.yaml "$WORKING/postgres-admin.yaml"
cp infra/k8s/secrets/templates/nchat-dev-postgres-migrator.template.yaml "$WORKING/postgres-migrator.yaml"
cp infra/k8s/secrets/templates/nchat-dev-postgres-runtime.template.yaml "$WORKING/postgres-runtime.yaml"
install -m 0600 /dev/null "$WORKING/nchat-dev.env"

# Edite os seis arquivos no diretório temporário: nchat-dev.env, livekit.yaml,
# turnserver.conf, postgres-admin.yaml, postgres-migrator.yaml e
# postgres-runtime.yaml. Use nchat_admin como admin, nchat_migrator na
# MIGRATIONS_DATABASE_URL e nchat_app na DATABASE_URL. O segredo TURN deve
# coincidir entre livekit.yaml e turnserver.conf.

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
```

As URLs usam o Service `postgres`, banco `nchat` e identidades distintas. Codifique
caracteres reservados das senhas no userinfo da URL. `VALKEY_URL` usa o Service
`valkey`. Não imprima os arquivos temporários e não use `set -x` em nenhum momento
deste procedimento — isso vale para os seis arquivos de Secret, o `.env`, URLs com
senha, o Docker config do GHCR, o token do runner, o token do GHCR e chaves privadas.

No coturn, `allowed-peer-ip` prevalece sobre o range `denied-peer-ip` que o contém.
O renderizador produz exatamente uma exceção para `NCHAT_DEV_NODE_IP`. Como coturn e
LiveKit compartilham `hostNetwork`, o risco residual abrange outros processos no
mesmo host. Não adicione peers permitidos.

Crie o pull secret do GHCR sem passar a credencial a subprocessos por argumento:

```bash
# [srv-apps-01]
read -r -p 'GHCR username: ' GHCR_USER
read -r -s -p 'GHCR read:packages token: ' GHCR_TOKEN; echo
AUTH="$(printf '%s:%s' "$GHCR_USER" "$GHCR_TOKEN" | base64 -w0)"
printf '{"auths":{"ghcr.io":{"auth":"%s"}}}\n' "$AUTH" > "$WORKING/dockerconfig.json"
unset GHCR_USER GHCR_TOKEN AUTH
kubectl create secret generic ghcr-pull -n nchat-dev \
  --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson="$WORKING/dockerconfig.json" \
  --dry-run=client -o yaml | \
kubeseal --scope strict --controller-name sealed-secrets \
  --controller-namespace kube-system --format yaml \
  > infra/k8s/secrets/sealed/nchat-dev-ghcr-pull.yaml
shred -u "$WORKING/dockerconfig.json" 2>/dev/null || rm -f "$WORKING/dockerconfig.json"
```

## 10. Revisão e aprovação do PR de SealedSecrets

Os arquivos gerados acima —

```
infra/k8s/secrets/sealed/nchat-dev.yaml
infra/k8s/secrets/sealed/nchat-dev-postgres-admin.yaml
infra/k8s/secrets/sealed/nchat-dev-postgres-migrator.yaml
infra/k8s/secrets/sealed/nchat-dev-postgres-runtime.yaml
infra/k8s/secrets/sealed/nchat-dev-ghcr-pull.yaml
```

— contêm ciphertext seguro para versionamento, mas continuam sendo alterações de
infraestrutura. Padrão a seguir:

1. Gere os SealedSecrets em uma branch operacional separada (não em `develop`).
2. Valide cada arquivo com `kubeseal --validate`.
3. Confirme que nenhum Secret aberto foi criado no repositório.
4. Revise o diff.
5. Abra um PR separado para os SealedSecrets.
6. Não faça commit direto em `develop`.
7. Aplique no cluster somente após esse PR estar **aprovado e integrado** (merged).

```bash
# [srv-apps-01]
kubectl config current-context
# Confirme que o contexto acima é o cluster esperado antes de continuar.
kubectl get deployment sealed-secrets -n kube-system
kubeseal --version

SEALED_SECRET_FILES=(
  infra/k8s/secrets/sealed/nchat-dev.yaml
  infra/k8s/secrets/sealed/nchat-dev-postgres-admin.yaml
  infra/k8s/secrets/sealed/nchat-dev-postgres-migrator.yaml
  infra/k8s/secrets/sealed/nchat-dev-postgres-runtime.yaml
  infra/k8s/secrets/sealed/nchat-dev-ghcr-pull.yaml
)

for sealed in "${SEALED_SECRET_FILES[@]}"; do
  test -f "$sealed"

  kubeseal --validate \
    --controller-name sealed-secrets \
    --controller-namespace kube-system \
    <"$sealed"
done

git status --short
git diff --check

# git grep só pesquisa arquivos rastreados e ignoraria arquivos recém-gerados
# ainda não commitados; por isso o grep opera na lista explícita.
if grep -nH -E \
  '^[[:space:]]*kind:[[:space:]]*Secret([[:space:]]*(#.*)?)?$' \
  "${SEALED_SECRET_FILES[@]}"; then
  echo 'ERRO: Secret aberto encontrado entre os arquivos selados.' >&2
  exit 1
fi

echo 'OK: somente SealedSecrets encontrados.'
```

`kind: SealedSecret` é esperado nesses arquivos; `kind: Secret` não é — se o `grep`
acima encontrar `kind: Secret`, pare e não abra o PR.

Como alternativa emergencial, é possível aplicar manualmente sem versionamento, mas
**somente com autorização explícita**, registrando a decisão fora de logs públicos.

## 11. Aplicação dos SealedSecrets

Somente após o PR da seção 10 estar aprovado **e integrado** (merged). Antes de
aplicar, atualize o clone do servidor para garantir que ele contém o merge aprovado:

```bash
# [srv-apps-01]
cd "$NCHAT_REPO_DIR"

if [ -n "$(git status --porcelain)" ]; then
  echo 'ERRO: working tree não está limpa antes da atualização de develop.' >&2
  git status --short
  exit 1
fi

git fetch origin
git switch develop
git pull --ff-only origin develop

test "$(git branch --show-current)" = develop

if [ -n "$(git status --porcelain)" ]; then
  echo 'ERRO: working tree não está limpa após atualizar develop.' >&2
  git status --short
  exit 1
fi
```

Então valide e aplique os cinco arquivos, usando a mesma lista explícita da seção 10:

```bash
# [srv-apps-01]
SEALED_SECRET_FILES=(
  infra/k8s/secrets/sealed/nchat-dev.yaml
  infra/k8s/secrets/sealed/nchat-dev-postgres-admin.yaml
  infra/k8s/secrets/sealed/nchat-dev-postgres-migrator.yaml
  infra/k8s/secrets/sealed/nchat-dev-postgres-runtime.yaml
  infra/k8s/secrets/sealed/nchat-dev-ghcr-pull.yaml
)

for sealed in "${SEALED_SECRET_FILES[@]}"; do
  test -f "$sealed"

  kubeseal --validate \
    --controller-name sealed-secrets \
    --controller-namespace kube-system \
    <"$sealed"

  kubectl apply -f "$sealed"
done
```

## 12. SeaweedFS e imagens externas

O `file-service` atual não possui cliente filer ou S3. O gateway S3 fica desabilitado,
sem porta 8333, Ingress ou credenciais. Não o habilite apenas por estar provisionado.
Uma futura ativação exige cliente real, autenticação obrigatória por Secret
strict-scope, NetworkPolicy exclusiva `file-service` → S3 e teste de acesso anônimo
negado. O master 9333 nunca deve ser liberado para aplicações.

Para atualizar uma imagem externa, consulte a tag oficial, confirme o manifest list
e `linux/amd64`, revise notas da release e altere tag e digest no mesmo diff:

```bash
# [notebook]
docker buildx imagetools inspect 'REPLACE_ME_IMAGE:REPLACE_ME_TAG'
pnpm k8s:ci
```

Não copie digest de comentário, issue ou resultado de busca. O digest deve ter
`sha256:` mais 64 caracteres hexadecimais e pertencer exatamente à tag revisada.

## 13. Firewall restrito à LAN

Comandos genéricos como `sudo ufw allow "${TURN_LISTEN_PORT}/tcp"` liberam a porta em
**todas** as interfaces, incluindo qualquer uplink WAN presente no host. Isso é
proibido. As regras deste runbook devem ficar restritas à interface e ao CIDR da LAN
interna.

Primeiro, identifique a topologia de rede real do host e o estado atual do firewall:

```bash
# [srv-apps-01]
ip -br -4 address
ip -4 route
sudo ufw status verbose
```

Confirme na saída acima que o UFW está **ativo** (`Status: active`). Se estiver
inativo, **não o habilite automaticamente**: habilitar o UFW em um servidor
compartilhado pode bloquear serviços existentes (Traefik, UniFi, outras aplicações).
Nesse caso, pare aqui e leve o estado do firewall para revisão humana antes de
qualquer decisão.

Tire um snapshot do estado atual antes de qualquer alteração:

```bash
# [srv-apps-01]
sudo ufw status numbered | tee "/root/ufw-before-nchat-dev-$(date +%Y%m%d-%H%M%S).txt"
sudo nft list ruleset > "/root/nft-before-nchat-dev-$(date +%Y%m%d-%H%M%S).txt"
```

Defina manualmente, após revisão humana da saída acima, a interface e o CIDR da LAN
interna — nunca copie estes valores de outro ambiente:

```bash
# [srv-apps-01]
LAN_IFACE='<INTERFACE_LAN>'
LAN_CIDR='<CIDR_DA_REDE_INTERNA>'
test "$LAN_IFACE" != '<INTERFACE_LAN>'
test "$LAN_CIDR" != '<CIDR_DA_REDE_INTERNA>'
ip link show "$LAN_IFACE"
```

Somente então, aplique regras restritas à interface e ao CIDR da LAN:

```bash
# [srv-apps-01]
sudo ufw allow in on "$LAN_IFACE" \
  from "$LAN_CIDR" \
  to any port "$TURN_LISTEN_PORT" proto tcp \
  comment 'nchat-dev coturn LAN TCP'

sudo ufw allow in on "$LAN_IFACE" \
  from "$LAN_CIDR" \
  to any port "$TURN_LISTEN_PORT" proto udp \
  comment 'nchat-dev coturn LAN UDP'

sudo ufw allow in on "$LAN_IFACE" \
  from "$LAN_CIDR" \
  to any port "$LIVEKIT_RTC_TCP_PORT" proto tcp \
  comment 'nchat-dev livekit RTC LAN TCP'

sudo ufw allow in on "$LAN_IFACE" \
  from "$LAN_CIDR" \
  to any port "$LIVEKIT_RTC_UDP_PORT" proto udp \
  comment 'nchat-dev livekit RTC LAN UDP'

sudo ufw allow in on "$LAN_IFACE" \
  from "$LAN_CIDR" \
  to any port "${TURN_RELAY_MIN_PORT}:${TURN_RELAY_MAX_PORT}" proto udp \
  comment 'nchat-dev coturn relay LAN'
```

Depois das mudanças, capture o novo estado e faça revisão humana do diff lógico —
**não** restaure nem sobrescreva o firewall automaticamente:

```bash
# [srv-apps-01]
sudo ufw status numbered
sudo nft list ruleset
```

Proibições explícitas nesta seção:

- Não usar `sudo ufw reset`.
- Não usar `sudo ufw disable`.
- Não apagar regras existentes.
- Não alterar a policy default.
- Não executar `nft flush ruleset`.
- Não publicar estas portas em WAN, NAT, OPNsense ou port-forward.
- Preservar 80/443 do Traefik.
- Preservar 3478 do UniFi.
- Não alterar regras de outras aplicações.

Se o host usa nftables/firewalld em vez de ufw, traduza somente estas regras
restritas à LAN — não altere a configuração global do Traefik. O hardening futuro é
separar coturn e LiveKit em IP/nó dedicado antes de qualquer mudança de exposição.

## 14. Validações pré-deploy

Nesta etapa a aplicação ainda não foi implantada; valide apenas o que já deve
existir — renderização, namespace, RBAC, PVs, Secrets, runner e portas. Checagens de
Ingress, certificado, NetworkPolicy, Services e endpoint HTTPS pertencem à validação
pós-deploy (seção 17).

```bash
# [srv-apps-01]
# Use o mesmo binário standalone verificado que o workflow usa; não use
# `kubectl kustomize` nesta validação.
KUSTOMIZE_BIN="$(scripts/deploy/nchat-dev/install-kustomize.sh)/kustomize"
test -x "$KUSTOMIZE_BIN"

"$KUSTOMIZE_BIN" build \
  infra/k8s/overlays/nchat-dev-server/data \
  >/tmp/nchat-dev-data.yaml

"$KUSTOMIZE_BIN" build \
  infra/k8s/overlays/nchat-dev-server/migrations \
  >/tmp/nchat-dev-migrations.yaml

"$KUSTOMIZE_BIN" build \
  infra/k8s/overlays/nchat-dev-server \
  >/tmp/nchat-dev-application.yaml

kubectl get namespace nchat-dev
kubectl get pv nchat-dev-postgres nchat-dev-valkey nchat-dev-seaweedfs
kubectl get sealedsecrets,secrets -n nchat-dev

sudo -u nchat-runner kubectl auth can-i patch deployments -n nchat-dev
sudo -u nchat-runner kubectl auth can-i get secrets -n nchat-dev

(cd /opt/actions-runner-nchat-dev && sudo ./svc.sh status)

sudo ss -ltnup
sudo ss -ltnup | grep -E \
  ":(80|443|3478|${TURN_LISTEN_PORT}|${LIVEKIT_RTC_TCP_PORT}|${LIVEKIT_RTC_UDP_PORT})\\b" || true
```

Resultados esperados do RBAC: `yes` e `no`, respectivamente. O runner deve constar
como instalado e **parado** — ele só é iniciado na seção 15.

Registre um snapshot do cluster inteiro antes do deploy, para comparação na seção 17:

```bash
# [srv-apps-01]
kubectl get deployments,statefulsets,daemonsets,pods,svc,ingress -A \
  -o wide > /tmp/k8s-before-nchat-dev.txt

kubectl get \
  deployments,statefulsets,daemonsets,services,ingresses \
  -A -o json |
jq '
  del(
    .items[].metadata.managedFields,
    .items[].metadata.resourceVersion,
    .items[].metadata.uid,
    .items[].metadata.creationTimestamp,
    .items[].status
  )
' > /tmp/k8s-spec-before-nchat-dev.json
```

O snapshot `-o wide` cobre inventário e status; o snapshot JSON normalizado cobre a
configuração (spec) e é o que permite detectar mudança de spec na seção 17.

## 15. Nova conferência de workflows pendentes e habilitação controlada do runner

Repita a checagem da seção 1 imediatamente antes de iniciar o runner, para garantir
que nenhuma execução ficou pendente durante o bootstrap:

```bash
# [notebook]
gh run list \
  --workflow images.yml \
  --branch develop \
  --limit 10 \
  --json databaseId,status,conclusion,headSha,displayTitle,url \
  --jq '.[]'
```

Cancele qualquer execução `queued`, `waiting` ou `in_progress` que seja prematura,
como na seção 1. Somente então, habilite o runner:

```bash
# [srv-apps-01]
cd /opt/actions-runner-nchat-dev
sudo ./svc.sh start
sudo ./svc.sh status
```

## 16. Build e deploy

Como o pipeline é disparado:

- `deploy-nchat-dev.yml` usa `workflow_call` — ele não roda sozinho e **não possui**
  `workflow_dispatch`; não é possível dispará-lo manualmente pela interface.
- `images.yml` dispara em push para `develop`. Ele primeiro constrói e publica as
  imagens em runners GitHub-hosted e, ao final, chama o deploy passando os digests
  `@sha256:` das imagens publicadas.
- O job de deploy roda no runner self-hosted `srv-apps-01-nchat-dev`, agora
  habilitado.
- **Não crie commit vazio** em `develop` apenas para disparar o pipeline sem
  autorização explícita. O caminho normal é o próximo merge em `develop`.

Acompanhe a execução pela interface do GitHub Actions. Confirme que o build terminou
antes do deploy, que o runner foi `srv-apps-01-nchat-dev` e que as imagens
renderizadas usam `@sha256:`. O timeout do workflow para migrations é 330s,
ligeiramente superior ao deadline de 300s do Job.

## 17. Validação pós-deploy

```bash
# [srv-apps-01]
kubectl get events -n nchat-dev \
  --sort-by='.lastTimestamp'

kubectl get pods -n nchat-dev -o wide
kubectl get pvc -n nchat-dev
kubectl get ingress,certificate -n nchat-dev
kubectl describe certificate nchat-dev-tls -n nchat-dev
kubectl get networkpolicy -n nchat-dev
kubectl describe networkpolicy nchat-allow-livekit-api-egress -n nchat-dev
kubectl get service -n nchat-dev -o wide
curl -fsS "https://${NCHAT_DEV_HOST}/" >/dev/null

kubectl auth can-i --as=nchat-dev-deployer \
  patch deployments -n nchat-dev

kubectl auth can-i --as=nchat-dev-deployer \
  get secrets -n nchat-dev
```

Resultados esperados do RBAC: `yes` para `patch deployments`, `no` para
`get secrets`.

### 17.1 Headers de segurança e CSP (issue #388)

O repositório emite **uma única** CSP, no nginx do web
(`infra/docker/web/nginx.conf`). Qualquer segunda política — em especial
`Content-Security-Policy-Report-Only` — vem de camada operacional fora do Git
(Traefik/Ingress fora do overlay, ou Cloudflare) e precisa ser removida lá.

```bash
# [notebook]
bash scripts/ci/web-security-headers-check.sh "https://${NCHAT_DEV_HOST}"

# Inspeção manual dos headers da resposta inicial:
curl -sS -o /dev/null -D - "https://${NCHAT_DEV_HOST}/" |
  grep -i '^content-security-policy'
```

Esperado: exatamente uma linha `content-security-policy:` e **nenhuma**
`content-security-policy-report-only:`.

Ações operacionais obrigatórias, fora do repositório (zona Cloudflare de
`nchat-dev`; a resposta traz `server: cloudflare`):

1. **Desativar JavaScript Detections** (Security → Bots). O Cloudflare injeta no
   `<body>` um script **inline** que faz o bootstrap de
   `/cdn-cgi/challenge-platform/scripts/jsd/main.js` — é ele que dispara "blocked
   an inline script because it violates script-src 'self'". Não há como
   autorizá-lo: o conteúdo inline muda a cada resposta (parâmetros `r` e `t`), o
   que inviabiliza hash, e o Cloudflare não insere nonce. Liberar exigiria
   `'unsafe-inline'`, o que a política de segurança proíbe. **Desativar é a
   única saída.**
2. **Desativar Web Analytics/Browser Insights**. O Cloudflare injeta
   `https://static.cloudflareinsights.com/beacon.min.js/...`, bloqueado por
   `script-src 'self'`. O ambiente dev não depende dele. Se o produto decidir
   mantê-lo, a única alteração aceita é acrescentar
   `https://static.cloudflareinsights.com` ao `script-src` do nginx — nunca
   `'unsafe-inline'` nem curinga.

**Sobre a CSP Report-Only do issue #388**: sondagens diretas do host (inclusive
com User-Agent de navegador) retornam **um** header `content-security-policy` e
**nenhum** `content-security-policy-report-only`; não há `<meta http-equiv>` de
CSP no HTML e nenhum `Middleware` do repositório define headers. Ou seja, a
política Report-Only com `script-src 'unsafe-inline' 'unsafe-eval'` e
`connect-src 'none'` não vem da origem nesse caminho. Antes de caçá-la na
infraestrutura, reproduza em Firefox e Brave **com extensões desabilitadas** e em
janela limpa — extensões e o Brave Shields injetam CSP Report-Only própria. Se
persistir sem extensões, verifique nesta ordem:

```bash
# [notebook]
curl -sS -o /dev/null -D - "https://${NCHAT_DEV_HOST}/" | grep -i 'report-only'

# [srv-apps-01]
# Nenhum Middleware do repositório define headers; qualquer contentSecurityPolicy
# aqui é resíduo operacional a remover.
kubectl get middleware -A -o yaml | grep -i -n 'contentSecurityPolicy' || true
```

E, no painel Cloudflare, as Transform Rules / Managed Transforms de response
header da zona.

Para diagnóstico de política, identifique primeiro os labels do pod e depois confira
somente a política daquele fluxo; não desative o default-deny nem crie egress amplo.

Compare o estado do cluster inteiro com o snapshot da seção 14, para confirmar que
nenhum outro namespace foi alterado:

```bash
# [srv-apps-01]
kubectl get deployments,statefulsets,daemonsets,pods,svc,ingress -A \
  -o wide > /tmp/k8s-after-nchat-dev.txt

diff -u \
  /tmp/k8s-before-nchat-dev.txt \
  /tmp/k8s-after-nchat-dev.txt || true

kubectl get \
  deployments,statefulsets,daemonsets,services,ingresses \
  -A -o json |
jq '
  del(
    .items[].metadata.managedFields,
    .items[].metadata.resourceVersion,
    .items[].metadata.uid,
    .items[].metadata.creationTimestamp,
    .items[].status
  )
' > /tmp/k8s-spec-after-nchat-dev.json

diff -u \
  /tmp/k8s-spec-before-nchat-dev.json \
  /tmp/k8s-spec-after-nchat-dev.json || true
```

Ao ler o diff `-o wide`, considere que colunas como `AGE`, `RESTARTS` e `IP` mudam
naturalmente com o tempo, e que reconciliações independentes de outros controllers
podem gerar ruído fora do controle deste procedimento. Essas diferenças não são
motivo de bloqueio. O diff dos snapshots JSON normalizados é o que detecta mudança
de configuração. O critério de interrupção é: **criação, remoção, rollout ou mudança
de spec** de recursos fora do namespace `nchat-dev`. Qualquer ocorrência dessas
exige interrupção imediata e investigação.

### 17.2 Stack de anexos (issue #483)

**Pré-requisito bloqueante.** O Secret `nchat-file-encryption` é provisionado
por um PR operacional separado (§9–§11) e precisa estar aplicado **antes** do
deploy que liga `FILE_UPLOADS_ENABLED=true`. Com uploads habilitados e a chave
ausente, `Config.Validate` recusa a inicialização — o pod novo nunca fica
`ready`, o `maxUnavailable: 0` mantém o pod antigo servindo e o deploy aborta.
Confirme apenas a existência, nunca o conteúdo:

```bash
# [srv-apps-01]
kubectl get secret nchat-file-encryption -n nchat-dev
```

**SeaweedFS Filer.** O StatefulSet passou a rodar `weed server -filer=true`;
antes disso a porta 8888 era declarada mas nada escutava nela, e o
`SeaweedFSStore.Ping` do file-service consulta exatamente esse endpoint:

```bash
# [srv-apps-01]
kubectl -n nchat-dev exec sts/seaweedfs -- wget -qO- http://127.0.0.1:8888/ >/dev/null && echo filer-ok
kubectl -n nchat-dev get pod -l app.kubernetes.io/component=file \
  -o jsonpath='{.items[*].status.containerStatuses[*].ready}'
```

**ClamAV.** Não expõe nada e não sai para lugar nenhum:

```bash
# [srv-apps-01]
kubectl -n nchat-dev get deploy,svc -l app.kubernetes.io/component=clamav
kubectl -n nchat-dev get svc clamav -o jsonpath='{.spec.type}{"\n"}'   # ClusterIP
kubectl -n nchat-dev describe networkpolicy nchat-allow-clamav
```

A primeira subida carrega ~110 MiB de assinaturas e leva dezenas de segundos; o
`startupProbe` tem folga para isso. O ClamAV **não** entra em
`wait_for_rollouts` nem na readiness do file-service: enquanto ele não estiver
pronto os anexos apenas se acumulam em `pending_scan`, não baixáveis.

**Fluxo funcional**, nesta ordem: upload responde `201` com `pending_scan`;
download, preview e Range respondem `403 file_not_scanned`; dentro de ~1 poll
(10 s) mais a duração do scan o estado vira `clean` e a entrega é liberada. Com
a fixture padrão EICAR o estado vira `rejected` e permanece bloqueado.

### 17.3 Convergência das NetworkPolicies criadas à mão (issue #483)

Três policies foram criadas manualmente no cluster durante o diagnóstico e não
estavam versionadas. Uma delas passa a existir no repositório **com o mesmo
nome**, deliberadamente, para ser adotada em vez de duplicada.

1. `kubectl apply` do deploy versionado **adota**
   `nchat-allow-upload-guard-file-ingress`. Ela **não deve ser removida**, nem
   antes nem depois.
2. Confirme que o objeto vivo convergiu para o manifest:

   ```bash
   # [srv-apps-01]
   kubectl -n nchat-dev get networkpolicy nchat-allow-upload-guard-file-ingress -o yaml
   ```

   Como o objeto foi criado fora do `apply`, pode não ter a anotação
   `last-applied-configuration`; nesse caso o merge de três vias preserva campos
   que existam só no objeto vivo. Se sobrar campo residual, resolva com um
   `kubectl replace` nominal **desse único objeto** — nunca com `delete`.
3. **Somente depois** de validar a convergência, remova nominalmente as duas
   policies cujos fluxos passaram a ser cobertos por
   `nchat-allow-traefik-http` (agora incluindo `upload-guard`),
   `nchat-allow-dns-egress` e `nchat-allow-upload-guard-file-egress`:

   ```bash
   # [srv-apps-01]
   kubectl delete networkpolicy nchat-allow-traefik-upload-guard-ingress -n nchat-dev
   kubectl delete networkpolicy nchat-allow-upload-guard-egress          -n nchat-dev
   ```

Proibido em qualquer etapa: `--prune`, curingas, `delete` genérico, ou remover
qualquer policy antes da validação.

### 17.4 Rollback do stack de anexos

O rollback primário é de **configuração, não de schema**:
`FILE_UPLOADS_ENABLED=false` no `configmap-patch.yaml` seguido de rollout. As
rotas voltam a `503`, o worker não inicia e o serviço fica health-only. Nenhum
anexo é perdido, nada em `pending_scan` ou `rejected` passa a ser servido, o
Secret permanece e a capacidade de decriptar objetos existentes é preservada.

Nunca, como forma de "destravar" uploads:

- `FILE_MALWARE_SCAN_REQUIRED=false` — `APP_ENV=nchat-dev` está no allowlist que
  faria o serviço aceitar, o que torna isto disciplina, não barreira técnica;
- `AlertExceedsMax no` — restaura exatamente a condição em que um limite
  atingido volta a responder `OK`;
- `MaxScanTime` igual ou abaixo do timeout externo — inverte a ordem dos
  deadlines e devolve o desfecho ao engine;
- `UPDATE ... SET status='clean'`;
- `migrate.sh down`;
- remover `nchat-file-encryption`;
- abrir NetworkPolicy ou remover as default-deny.

## 18. Rollback

Rollback de aplicação não reverte schema. Confirme compatibilidade da migration;
migration destrutiva exige correção forward.

O rollback opera exclusivamente sobre a lista explícita `NCHAT_DEV_APPLICATION_DEPLOYMENTS`.
Antes do loop, confirme que cada Deployment pertence de fato ao namespace `nchat-dev`:

```bash
# [srv-apps-01]
# A sessão pode ter saído do clone (ex.: `cd /opt/actions-runner-nchat-dev` na
# seção 15); volte ao repositório antes de qualquer `source` com caminho relativo.
cd "$NCHAT_REPO_DIR"

set -Eeuo pipefail

source scripts/deploy/nchat-dev/topology.sh
load_nchat_dev_topology \
  infra/k8s/overlays/nchat-dev-server/topology.env

source scripts/deploy/nchat-dev/lib.sh

kubectl rollout history deployment -n nchat-dev

printf '%s\n' "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}"

for deployment in "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}"; do
  kubectl get deployment "$deployment" -n nchat-dev
done

for deployment in "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}"; do
  kubectl rollout undo "deployment/$deployment" -n nchat-dev
  kubectl rollout status \
    "deployment/$deployment" \
    -n nchat-dev \
    --timeout=180s
done

curl -fsS "https://${NCHAT_DEV_HOST}/" >/dev/null
```

Proibições explícitas para rollback em servidor compartilhado:

- Não apagar PVC.
- Não apagar PV.
- Não apagar diretórios.
- Não apagar namespace.
- Não reverter schema automaticamente.
- Não reiniciar o cluster.
- Não reiniciar o Traefik.
- Não reiniciar o servidor.

Para falha de dados, não apague PVC, PV ou diretórios. Preserve os PVs `Retain` e
restaure somente por procedimento de backup validado.

> Um `apply` parcial anterior (ex.: falha após alguns recursos terem sido
> aplicados) não é revertido automaticamente por nenhum mecanismo deste
> workflow. O `rollout undo` acima só cobre `Deployment`s; Ingress, Certificate,
> IngressRoute e ConfigMap aplicados antes da falha permanecem no estado em que
> o `apply` parcial os deixou até o próximo deploy bem-sucedido os sobrescrever.
> Revise manualmente o estado desses recursos (seção 17) após qualquer falha de
> deploy antes de assumir que o ambiente está consistente.

## 19. Rotação e TLS local

Rotacione credenciais de PostgreSQL, Valkey, GHCR, LiveKit e coturn pelo processo de
SealedSecret strict-scope (seções 9–11), uma identidade por vez, validando rollout e
revogando o valor anterior. Nunca imprima o Secret aberto nem o armazene em
ConfigMap. A exceção coturn deve continuar sendo exatamente o node IP validado;
exposição WAN permanece proibida.

O material de desenvolvimento é gerado localmente e ignorado:

```bash
# [notebook]
make dev-tls-generate
```

Remover a chave do HEAD não remove cópias do histórico Git. Após merge, rotacione o
par anteriormente compartilhado. Uma limpeza de histórico exige janela coordenada,
autorização explícita e comunicação aos clones; ela não faz parte deste runbook. Não
adicione allowlist global ao Gitleaks para esconder chaves futuras.
