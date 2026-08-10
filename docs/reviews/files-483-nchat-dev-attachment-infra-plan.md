# Plano de implementação — attachments no nchat-dev (issue #483)

> Documento de **planejamento e Design Review**. Nenhuma alteração de código,
> manifest, cluster, migration ou Secret foi executada na produção deste plano.
>
> - Issue: [#483](https://github.com/nicrepository/nchat/issues/483) — `fix(infra): enable secure attachment uploads in nchat-dev`
> - Branch: `fix/files-483-nchat-dev-attachment-infra`
> - Base: `develop` @ `3a94395`
> - **Revisão 3 — corrigida pelo Threat Model** (2026-08-09; Rev 2 aprovada no Design Review; Rev 1: 2026-08-09)

## Histórico de revisões

| Rev | Data       | Estado                                                                                | Mudança                                                                                                                                                                                                                                                                                                                               |
| --- | ---------- | ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | 2026-08-09 | Design Review **REPROVADO**, risco Médio                                              | versão inicial, baseada apenas em inspeção estática do repositório                                                                                                                                                                                                                                                                    |
| 2   | 2026-08-09 | Design Review **APROVADO**, risco geral **Médio**                                     | incorpora verificações read-only no cluster, inspeção da imagem ClamAV e benchmark INSTREAM real; fecha R2, R3, R4, R5, R6, R13 e corrige A4, A8, A9, A10. **Ajuste final determinado pelo reviewer: `cpu.limit` do ClamAV de 1500m para 1250m**, preservando 500m de folga em `limits.cpu` do namespace sem alterar a ResourceQuota. |
| 3   | 2026-08-09 | Threat Model **REPROVADO**, risco **Alto** → plano corrigido, pronto para revalidação | incorpora TM-01 (`clean` tem de significar scan completo), TM-06 (limites do ClamAV explícitos e verificáveis) e TM-10 (upload-guard é fronteira L7, não de rede). Nenhuma decisão do Design Review aprovado foi revertida; os recursos permanecem `500m/1536Mi` e `1250m/3Gi`.                                                       |

## Threat Model — findings e mitigações de desenho

| #         | Severidade | Finding                                                                                    | Mitigação de desenho                                                                                                                                                                                                                            | Onde                                                          |
| --------- | ---------- | ------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------- |
| **TM-01** | **Alta**   | `clean` não prova scan completo sob `MaxScanTime` / limites internos do engine             | `AlertExceedsMax yes` promove limite de **conteúdo** a `FOUND`; `MaxScanTime 420000` coloca o limite **de tempo** do engine estritamente atrás do deadline externo de 300 s do file-service, que passa a ser a autoridade fail-closed declarada | §Configuração do clamd · §CI · §Testes                        |
| **TM-06** | Média      | Limites de parser e scratch do ClamAV dependiam de defaults ocultos                        | Todos os limites relevantes viram política versionada e verificada por CI; `/tmp` e `/var/lib/clamav` ganham `sizeLimit` finito e justificado; `ephemeral-storage` explícito                                                                    | §Configuração do clamd · §Writable paths · §Recursos          |
| **TM-10** | Média      | O plano tratava a passagem obrigatória pelo upload-guard como se fosse garantida pela rede | Declarado explicitamente que **NetworkPolicy não impede POST direto ao file-service**; a propriedade é de roteamento L7 do Traefik e passa a ter invariantes de CI sobre o manifest **renderizado** e testes E2E                                | §Fronteira de confiança L7 · §NetworkPolicies · §CI · §Testes |

### O que a Rev 2 invalida da Rev 1

Estas afirmações da Rev 1 estavam **erradas ou eram hipóteses** e foram
removidas. Não devem ser reintroduzidas:

| Afirmação da Rev 1                                        | Estado na Rev 2                                                                                                    |
| --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| "UID/GID do ClamAV é 100/100"                             | **Errado.** Medido: `uid=100(clamav) gid=101(clamav)`                                                              |
| "Filer do SeaweedFS _provavelmente_ não está rodando"     | **Confirmado parado.** `connection refused` em `127.0.0.1:8888`                                                    |
| "Migration 000005 não aplicada"                           | **Errado.** Está aplicada, `dirty=false`, `in_progress=false`                                                      |
| "Backfill vai revogar aprovações existentes no nchat-dev" | **Sem impacto operacional.** `files.attachments` tem 0 linhas                                                      |
| "Resources do ClamAV a definir após medição"              | **Medido.** Proposta numérica na §Recursos                                                                         |
| "ClamAV entra em `base/kustomization.yaml`"               | **Rejeitado.** Só o overlay nchat-dev renderiza o workload                                                         |
| "Aumentar a ResourceQuota"                                | **Desnecessário.** A proposta cabe na quota atual                                                                  |
| "Remover as 3 policies manuais após o deploy"             | **Errado para uma delas.** `nchat-allow-upload-guard-file-ingress` é adotada por convergência de nome, não apagada |
| "`clamdcheck.sh` serve como probe"                        | **Rejeitado.** O script reporta `unhealthy` com o daemon comprovadamente saudável                                  |
| "Benchmark: 120 s para 512 MiB"                           | **Não representativo.** Caminho errado (sem `--stream`). O caminho real levou **4.258 s**                          |

---

## Entendimento

### Arquitetura atual do fluxo de upload

```
Browser
  → Traefik (ns ingress-system)
      ├─ Ingress/nchat-dev  → file-service:8083   (download, listagem, preview, health)
      └─ IngressRoute/nchat-dev-uploads (POST /api/files/{channels|dm}/{id}/attachments, prio 200)
             → Middleware upload-inflight (16) + strip-files-prefix
             → upload-guard:8080  (nginx, cap streaming 536879104 B)
                  → file-service:8083
                        ├─ PostgreSQL (metadados, admission control por advisory lock)
                        ├─ SeaweedFS filer :8888 (ciphertext)
                        ├─ Valkey :6379 (broadcast de status, opcional)
                        └─ clamd :3310 (worker assíncrono RF-22, INSTREAM/TCP)
```

### Causa do 503

O 503 `attachment uploads are disabled` **não é um bug** — é o comportamento
correto e documentado de `FILE_UPLOADS_ENABLED=false`
(`services/file-service/internal/config/config.go:184`, default `false`).
Nenhum ConfigMap versionado do NChat define essa variável em lugar nenhum:
`infra/k8s/base/configmap.yaml` e
`infra/k8s/overlays/nchat-dev-server/configmap-patch.yaml` não contêm nenhuma
chave `FILE_*`.

Mas a causa-raiz real é maior do que uma variável ausente: **o stack de
attachments nunca foi provisionado em Kubernetes**. O código, as migrations, o
Compose e a documentação existem; os manifests que ligariam isso ao nchat-dev
não. Ligar `FILE_UPLOADS_ENABLED=true` hoje faz o file-service **falhar no
start-up** em `Config.Validate()`, e — mesmo que passasse — ficaria
permanentemente `unready`, porque o Filer do SeaweedFS está comprovadamente
parado.

---

## Estado atual vs. estado necessário

| Item                                    | Estado atual (comprovado)                                                                                                                  | Estado necessário                                                 |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------- |
| `FILE_UPLOADS_ENABLED`                  | ausente → `false`                                                                                                                          | `"true"` no overlay nchat-dev                                     |
| `DATABASE_URL` no file-service          | **ausente** — o patch do overlay substitui `envFrom` inteiro por só `nchat-config`                                                         | `secretKeyRef` → `nchat-postgres-runtime`                         |
| `AUTH_JWT_HMAC_SECRET` no file-service  | **ausente** — mesmo motivo                                                                                                                 | `secretKeyRef` → `nchat-secrets`                                  |
| `VALKEY_URL` no file-service            | **ausente** — mesmo motivo                                                                                                                 | `secretKeyRef` → `nchat-secrets`                                  |
| `nchat-file-encryption`                 | template existe, SealedSecret **não existe** no repo nem no cluster; e o `secretRef` da base é descartado pelo patch                       | SealedSecret selado + `secretKeyRef` explícito                    |
| `SEAWEEDFS_FILER_URL`                   | overlay define **`null`** (remove a chave); base aponta para `seaweedfs-filer`, que não existe em nchat-dev (Service chama-se `seaweedfs`) | `http://seaweedfs:8888`                                           |
| Filer do SeaweedFS                      | **parado** — `connection refused` em `:8888`; StatefulSet roda `weed server` sem `-filer`                                                  | `-filer=true` no StatefulSet **existente**                        |
| ClamAV                                  | inexistente em K8s                                                                                                                         | Deployment + Service ClusterIP :3310, **só no overlay nchat-dev** |
| `FILE_MALWARE_SCANNER_ADDRESS`          | ausente → worker não inicia, nada aprovado                                                                                                 | `clamav:3310`                                                     |
| Migration 000005                        | **já aplicada**, `dirty=false`, `in_progress=false`                                                                                        | nenhuma ação                                                      |
| `files.attachments`                     | **0 linhas**                                                                                                                               | nenhuma ação; backfill sem impacto                                |
| NetworkPolicy Traefik→upload-guard      | `nchat-allow-traefik-http` não lista o componente `upload-guard`                                                                           | incluir                                                           |
| NetworkPolicy DNS                       | `nchat-allow-dns-egress` não lista `file` nem `upload-guard`                                                                               | incluir                                                           |
| upload-guard→file-service               | policy manual não versionada                                                                                                               | versionar com o **mesmo nome** para adoção                        |
| file-service→PG/Valkey/SeaweedFS/ClamAV | nenhuma policy em nenhuma direção                                                                                                          | criar                                                             |
| Policies criadas à mão no cluster       | 3, não versionadas → **drift**                                                                                                             | 1 adotada por nome, 2 removidas nominalmente                      |
| ResourceQuota                           | folga suficiente para o ClamAV proposto                                                                                                    | **não alterar**                                                   |
| CI `k8s-manifests-check.sh`             | contém invariantes que proíbem parte do que é necessário                                                                                   | substituir por invariantes positivos mais precisos                |

---

## Achados

### A1 — `FILE_UPLOADS_ENABLED` não existe em nenhum ConfigMap versionado

- **Evidência:** `infra/k8s/base/configmap.yaml` (35 linhas, nenhuma chave
  `FILE_*`); `infra/k8s/overlays/nchat-dev-server/configmap-patch.yaml`
  (18 linhas, idem). `config.go:184` —
  `configuredBool("FILE_UPLOADS_ENABLED", false)`.
- **Impacto:** 503 determinístico em `POST /attachments`. É o sintoma reportado.
- **Arquivos:** os dois acima.

### A2 — O patch do overlay descarta _todos_ os `secretRef` do file-service

- **Evidência:** `infra/k8s/base/services/file-service/deployment.yaml:50-64`
  declara `envFrom` com `nchat-config` + `secretRef: nchat-secrets` +
  `secretRef: nchat-file-encryption`.
  `infra/k8s/overlays/nchat-dev-server/patches/file-service.yaml:11-14` declara
  `envFrom` apenas com `configMapRef: nchat-config`. `envFrom` é uma lista sem
  `patchMergeKey`, portanto o strategic merge **substitui** a lista inteira.
- **Confirmação independente:** `scripts/ci/k8s-manifests-check.sh` →
  `if grep -q 'secretRef:' "$application" ...; then return 1; fi`. O CI
  **proíbe** `secretRef` no overlay renderizado; o padrão do nchat-dev é `env` +
  `secretKeyRef` por chave (ver `patches/auth-service.yaml`,
  `chat-service.yaml`, `media-service.yaml`).
- **Impacto:** em nchat-dev o file-service **não tem** `DATABASE_URL`,
  `AUTH_JWT_HMAC_SECRET`, `VALKEY_URL` nem nenhuma chave de encryption. Com
  `FILE_UPLOADS_ENABLED=true` o `Validate()` falha em `config.go:310`
  (`database URL is required`) antes de qualquer outra coisa.
- **Arquivos:** `patches/file-service.yaml`, `scripts/ci/k8s-manifests-check.sh`.

### A3 — `SEAWEEDFS_FILER_URL` é removido no overlay e o valor da base está errado para nchat-dev

- **Evidência:** `configmap-patch.yaml:17-18` — `SEAWEEDFS_FILER_URL: null` /
  `SEAWEEDFS_S3_ENDPOINT: null` (kustomize remove a chave).
  `base/configmap.yaml:32` aponta para `http://seaweedfs-filer:8888`, mas o
  Service em nchat-dev chama-se **`seaweedfs`**
  (`data/resources.yaml:206-218`, portas `master:9333` e `filer:8888`). O CI
  ainda **proíbe** a string:
  `if grep -Eq 'SEAWEEDFS_(FILER_URL|S3_ENDPOINT)' "$application"; then return 1; fi`.
- **Impacto:** com uploads ligados, `validateUploadDependencies` falha em
  `config.go:334-336` (`SEAWEEDFS_FILER_URL must be a valid HTTP or HTTPS URL`).
  E o invariante de CI precisa ser reescrito — não basta reintroduzir a chave.
- **Arquivos:** `configmap-patch.yaml`, `base/configmap.yaml`,
  `scripts/ci/k8s-manifests-check.sh`.

### A4 — O Filer do SeaweedFS está CONFIRMADAMENTE parado _(atualizado na Rev 2)_

- **Evidência estática:** `data/resources.yaml:250-254` →
  `args: [server, -dir=/data, -ip=seaweedfs, -ip.bind=0.0.0.0]`. Não há
  `-filer`. A documentação do SeaweedFS: `weed server -filer=true` é o que
  inicia o Filer junto do master/volume; sem a flag ele não sobe. O
  `readinessProbe` (linhas 260-263) checa apenas `/cluster/status` na porta
  `master`, então a ausência do Filer nunca apareceu. O Compose, em contraste,
  roda o Filer como processo próprio (`compose.dev.yml:142`).
- **Evidência operacional (Rev 2):**

  ```
  kubectl -n nchat-dev exec sts/seaweedfs -- wget -qO- http://127.0.0.1:8888/
  → connection refused
  ```

- **Impacto:** **bloqueante e confirmado.** `SeaweedFSStore.Ping` faz
  `GET {filer}/` (`seaweedfs.go:211-224`) e é um check _crítico_ de `/readyz`
  quando uploads estão ligados (`handlers.go:58-63`). Sem Filer, o file-service
  nunca fica `ready`, `wait_for_rollouts` falha e o deploy inteiro aborta.
- **Deixa de ser hipótese.** O Bloco 1 do plano é **obrigatório**, não
  condicional.
- **Arquivos:** `infra/k8s/overlays/nchat-dev-server/data/resources.yaml`.

### A5 — As policies permissivas da base são deletadas no nchat-dev, e a substituta não cobre upload-guard nem file

- **Evidência:**
  `overlays/nchat-dev-server/delete-permissive-network-policy.yaml` remove
  `nchat-allow-same-namespace-ingress` e `nchat-allow-traefik-ingress` da base.
  A substituta `network-policies.yaml:45-72` (`nchat-allow-traefik-http`)
  seleciona `web, auth, chat, file, notification, admin, search, media` — **sem
  `upload-guard`**. `nchat-allow-dns-egress:14-40` seleciona
  `auth, chat, notification, migrations, postgres-bootstrap, seaweedfs, livekit, coturn, media`
  — **sem `file` e sem `upload-guard`**. `nchat-default-deny-ingress` vem da
  base (`base/network-policy.yaml:1-14`), `nchat-default-deny-egress` do
  overlay.
- **Impacto:** explica exatamente os bloqueios 1 e 2 do diagnóstico
  operacional. Explica também por que os bloqueios 3 e 4 existem: não há
  nenhuma policy versionada autorizando `upload-guard → file-service` em
  qualquer direção.
- **Arquivos:** `network-policies.yaml`,
  `delete-permissive-network-policy.yaml`, `base/network-policy.yaml`.

### A6 — O file-service não tem _nenhuma_ policy de egress ou de acesso às suas dependências

- **Evidência:** em `network-policies.yaml`, `nchat-allow-postgres:86-96`
  autoriza ingress de
  `auth, chat, notification, migrations, postgres-bootstrap, media` — sem
  `file`. `nchat-allow-valkey:112-115` autoriza apenas `chat`. Não existe policy
  de ingress para o componente `seaweedfs` nem egress para o componente `file`.
- **Impacto:** o diagnóstico operacional parou no 503 antes de o file-service
  tentar sair para qualquer dependência, então esses quatro bloqueios adicionais
  ainda **não apareceram**. Eles aparecerão no primeiro `/readyz` com uploads
  ligados.
- **Arquivos:** `network-policies.yaml`.

### A7 — Drift confirmado: as 3 policies criadas à mão não existem no repositório

- **Evidência:**
  `grep -rn "nchat-allow-traefik-upload-guard-ingress\|nchat-allow-upload-guard-egress\|nchat-allow-upload-guard-file-ingress" infra/`
  → nenhum resultado.
- **Impacto:** o próximo `kubectl apply -f application.yaml` do pipeline **não**
  as remove (o deploy usa `apply`, não `prune`). Uma delas colide em nome com
  uma policy versionada e será **adotada**; as outras duas precisam de remoção
  nominal. Ver §R13.

### A8 — A imagem de migrations tem as migrations _embutidas_ _(reclassificado na Rev 2)_

- **Evidência:** `Dockerfile.migrations:8` → `COPY migrations /app/migrations`.
  `scripts/db/migrate.sh:371-374` → `collect_up_files` faz
  `find "$MIGRATIONS_DIR" -name "*.up.sql" | sort` **em runtime dentro do
  container**.
- **Estado na Rev 2:** a 000005 **já está aplicada** (A9), logo a imagem
  implantada já a continha. Este achado **deixa de ser causa** do problema e
  permanece apenas como invariante operacional a preservar:
  - toda migration nova exige rebuild da imagem `migrations` — o que o pipeline
    faz naturalmente;
  - `migrate.sh` confere o **SHA-256** de cada migration já aplicada contra
    `public.schema_migrations` e **aborta** se divergir. Portanto
    `migrations/files/000005_attachment_malware_scan_jobs.up.sql`
    **não pode ser editado** por esta issue nem por nenhuma outra: qualquer
    alteração de um byte quebra todo deploy futuro do nchat-dev.
- **Arquivos:** `Dockerfile.migrations`, `scripts/db/migrate.sh`,
  `scripts/deploy/nchat-dev/deploy.sh`.

### A9 — A verificação operacional inicial da 000005 procurou o objeto errado _(resolvido na Rev 2)_

- **Evidência estática:** `migrations/files/000005_attachment_malware_scan_jobs.up.sql:48-53`
  **não cria tabela nenhuma**. Ela adiciona
  `scan_attempts SMALLINT NOT NULL DEFAULT 0` e
  `scan_next_attempt_at TIMESTAMPTZ` em `files.attachments`, mais o CHECK
  `attachments_scan_attempts_check`, o backfill das linhas legadas (linhas
  127-132) e o índice parcial `idx_attachments_scan_pending` (linhas 163-165). O
  nome do arquivo descreve o _conceito_ de fila, não uma tabela. Procurar por
  `files.attachment_malware_scan_jobs` nunca poderia encontrar nada.
- **Evidência operacional (Rev 2):** com as consultas corretas,

  ```
  public.schema_migrations: domain=files,
    filename=000005_attachment_malware_scan_jobs,
    dirty=false, in_progress=false
  colunas presentes: scan_attempts, scan_next_attempt_at
  índice presente:   idx_attachments_scan_pending
  ```

- **Conclusão:** **a migration 000005 está aplicada.** Não há correção de
  migration pendente no nchat-dev. Não reaplicar manualmente. O pipeline mantém
  apenas seus invariantes normais.

### A10 — Backfill da 000005 não tem impacto operacional no nchat-dev _(atualizado na Rev 2)_

- **Evidência estática:** linhas 127-132 da 000005 —
  `UPDATE files.attachments SET status='pending_scan', scan_attempts=0, scan_next_attempt_at=now() WHERE status IN ('pending_scan','clean') AND deleted_at IS NULL`.
  Comentário nas linhas 62-90 declara isso deliberado.
- **Evidência operacional (Rev 2):**
  `SELECT status, count(*) FROM files.attachments GROUP BY status;` → **0 rows**.
- **Conclusão:** o backfill já rodou contra uma tabela vazia. **Nenhum anexo
  existente foi ou será revogado neste ambiente**, e não há comunicação de
  indisponibilidade a fazer. O comportamento documentado continua valendo para
  qualquer ambiente que já possua anexos — isso é propriedade da migration, não
  desta issue.

### A11 — O CI tem invariantes fechados que qualquer mudança quebra

- **Evidência:** `scripts/ci/k8s-manifests-check.sh`:
  - `network_policy_names_by_type "$application" Egress` comparado por igualdade
    exata com 8 nomes;
  - `validate_workload_hardening` exige `automountServiceAccountToken:false`,
    `runAsNonRoot:true`, `RuntimeDefault`, `allowPrivilegeEscalation:false`,
    **`readOnlyRootFilesystem:true`**, `drop: [ALL]`, `requests` e `limits` em
    todo Deployment/StatefulSet/Job renderizado — inclusive no ClamAV;
  - `external_image_refs` tem regex fechada
    (`postgres|valkey/valkey|chrislusf/seaweedfs|livekit/livekit-server|coturn/coturn`)
    - contagem `-eq 6`, e exige digest `@sha256:` em todas;
  - `grep -q 'secretRef:'` e `grep -Eq 'SEAWEEDFS_(FILER_URL|S3_ENDPOINT)'`
    proíbem construções necessárias.
- **Impacto:** o CI precisa ser atualizado **no mesmo commit** que introduz as
  policies e o workload. Nenhum gate deve ser relaxado genericamente: cada
  proibição cega vira um invariante positivo mais preciso (§CI).

### A12 — Padrão estabelecido para arquivo de config compartilhado entre Compose e K8s

- **Evidência:** `compose.dev.yml:222` monta
  `../k8s/base/services/upload-guard/nginx.conf.template`;
  `base/services/upload-guard/kustomization.yaml` gera o ConfigMap a partir do
  **mesmo arquivo**; o README (linhas 44-48) explica que o arquivo vive junto
  dos manifests porque kustomize não lê fora do próprio diretório. Reforço:
  `prepare_deploy_tree` copia só `infra/k8s`, então um caminho relativo saindo
  dessa árvore quebraria no deploy.
- **Impacto:** o `clamd.conf` deve **mudar de lugar** para
  `infra/k8s/base/services/clamav/clamd.conf`, com o Compose passando a
  montá-lo de lá. Duplicar o arquivo criaria exatamente o drift que esse padrão
  existe para evitar. Ver §R7.

### A13 — Existem template e runbook de encryption; falta apenas o SealedSecret

- **Evidência:**
  `infra/k8s/secrets/templates/nchat-dev-file-encryption.template.yaml`
  (namespace `nchat-dev`, `REPLACE_ME`),
  `docs/runbooks/file-service-envelope-encryption.md` (provisionamento
  §Provisioning, rotação §Rotation), `docs/security/secrets-owners.md` (owner
  Dev B / Infra já registrado).
  `infra/k8s/secrets/sealed/nchat-dev/kustomization.yaml` lista 5 secrets,
  **sem** `nchat-file-encryption`.
- **Impacto:** o mecanismo está pronto; falta executar e versionar o output
  selado, em PR separado.

### A14 — ClamAV não entra em liveness/readiness do file-service, por design

- **Evidência:** `docs/api/file-attachments.md` §"Health e readiness";
  `handlers.go:53-65` — só `postgres` e `object-storage` são checks.
- **Impacto:** indisponibilidade do ClamAV **não** derruba o file-service nem o
  rollout. É a propriedade que torna seguro implantar o ClamAV sem acoplá-lo ao
  deploy — e a razão de o ClamAV não precisar entrar em `wait_for_rollouts`.

### A15 — O healthcheck da imagem ClamAV reporta `unhealthy` com o daemon saudável _(novo na Rev 2)_

- **Evidência (benchmark):** no mesmo instante em que o healthcheck do container
  reportava `Status: unhealthy` com `ERROR: Unable to contact server`:
  - `printf 'PING\n' | nc -w 5 127.0.0.1 3310` → **`PONG`**;
  - `clamd` estava ouvindo em `0.0.0.0:3310`;
  - `clamdscan --stream` de 512 MiB completou com `OK`, `SCAN_RC=0`.
- **Causa provável:** o `clamdcheck.sh` da imagem espera um mecanismo de
  conexão diferente do que a configuração do NChat usa — o `clamd.conf` do NChat
  define **apenas `TCPSocket 3310`**, sem `LocalSocket`.
- **Impacto:** **o `clamdcheck.sh` não pode ser reaproveitado como
  `startupProbe`, `readinessProbe` nem `livenessProbe`.** Usá-lo produziria um
  pod em `CrashLoopBackOff` ou permanentemente `NotReady` com o scanner
  funcionando perfeitamente. Ver §Probes.

### A16 — O benchmark de 120 s não representa o caminho do NChat _(novo na Rev 2)_

- **Evidência:** `clamdscan` **sem** `--stream` sobre o arquivo de 512 MiB levou
  `120.015 s` e retornou `OK`. O mesmo arquivo por `clamdscan --stream` — o
  caminho que o file-service usa — levou `4.258 s` (`4.364 s` real), `SCAN_RC=0`.
- **Impacto:** o número de 120 s **não deve ser usado para dimensionar
  performance, timeouts ou recursos**. O caminho real é
  `file-service → TCP/3310 → INSTREAM`.
- **Observação registrada para o Threat Model, não para esta issue:** os logs do
  clamd mostram um limite interno de `120000 ms`, que é o default de
  `MaxScanTime` e coincide de forma suspeita com os `120.015 s` observados. A
  interação entre `MaxScanTime` (clamd) e `FILE_MALWARE_SCAN_TIMEOUT_SECONDS=300`
  (file-service) precisa ser analisada **com evidência de protocolo/código**
  antes de qualquer alteração: se um scan abortado por tempo puder responder
  `OK`, isso seria uma aprovação sem inspeção completa. Não há evidência
  suficiente hoje para afirmar isso, e o caminho INSTREAM real não chegou perto
  do limite. **Esta issue não altera `MaxScanTime`.**

---

## Evidências operacionais e de benchmark (Rev 2)

Todas as verificações abaixo são **read-only**. Nada foi alterado no cluster.

### Cluster nchat-dev

| Verificação        | Comando                                                                                                            | Resultado                                         |
| ------------------ | ------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------- |
| Filer do SeaweedFS | `kubectl -n nchat-dev exec sts/seaweedfs -- wget -qO- http://127.0.0.1:8888/`                                      | **`connection refused`**                          |
| Migration 000005   | `SELECT ... FROM public.schema_migrations WHERE domain='files' AND filename='000005_attachment_malware_scan_jobs'` | presente, `dirty=false`, `in_progress=false`      |
| Colunas da 000005  | `information_schema.columns`                                                                                       | `scan_attempts`, `scan_next_attempt_at` presentes |
| Índice da 000005   | `pg_indexes`                                                                                                       | `idx_attachments_scan_pending` presente           |
| Anexos existentes  | `SELECT status, count(*) FROM files.attachments GROUP BY status`                                                   | **0 rows**                                        |

### Capacidade e ResourceQuota

| Recurso           | Em uso | Cota | Folga  |
| ----------------- | ------ | ---- | ------ |
| `requests.cpu`    | 625m   | 4    | 3375m  |
| `requests.memory` | 1536Mi | 6Gi  | 4608Mi |
| `limits.cpu`      | 6250m  | 8    | 1750m  |
| `limits.memory`   | 6400Mi | 12Gi | 5888Mi |
| `pods`            | 14     | 30   | 16     |
| `services`        | 13     | 20   | 7      |

Nó `srv-apps-01`: 8 CPUs e ~50 GiB RAM allocatable; uso observado ~747m (9%) de
CPU e ~14844Mi (29%) de RAM.

**Conclusão:** não há pressão global de CPU nem de memória. A ResourceQuota
continua sendo o limite administrativo a respeitar, e a proposta desta issue
cabe dentro dela — **nenhum aumento de quota é solicitado**.

### Imagem ClamAV (`clamav/clamav:1.4`)

| Item                     | Valor medido                                                                                                         |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------- |
| Usuário                  | `clamav:x:100:101::/var/lib/clamav:/bin/false`                                                                       |
| Grupo                    | `clamav:x:101:clamav`                                                                                                |
| **UID / GID**            | **100 / 101** — não 100/100                                                                                          |
| Entrypoints              | `/init` e `/init-unprivileged` (existe e é executável)                                                               |
| `clamd.conf` efetivo     | `TCPSocket 3310`, `TCPAddr 0.0.0.0`, `StreamMaxLength 512M`, `MaxFileSize 512M`, `MaxScanSize 1024M`, `MaxThreads 4` |
| Diretiva `User`          | **ausente** na configuração consultada                                                                               |
| Healthcheck do container | `unhealthy` / `ERROR: Unable to contact server` — ver A15                                                            |

A ausência de `User` no `clamd.conf` é uma propriedade a **preservar**: com
`capabilities: drop: [ALL]` o clamd não teria como executar uma troca de usuário
solicitada por essa diretiva. O UID correto é imposto pelo `securityContext` do
pod, não pela configuração do daemon.

### Benchmark INSTREAM — o caminho real do file-service

Arquivo sintético seguro de 512 MiB (`536870912` bytes), via
`clamdscan --stream`:

| Métrica              | Valor                                       |
| -------------------- | ------------------------------------------- |
| Resultado            | `OK`, `SCAN_RC=0`                           |
| Tempo (clamdscan)    | **4.258 s**                                 |
| Tempo (real)         | 4.364 s                                     |
| RSS idle             | ~945 MiB                                    |
| RSS durante INSTREAM | ~959 MiB, **pico ~1000 MiB**                |
| CPU                  | **176.92%** e 136.55% (usa mais de um core) |

**Leituras corretas deste benchmark:**

- o clamd custa ~1 GiB de RAM **em repouso** — a base de assinaturas domina o
  consumo, não o arquivo;
- a memória **não** cresce proporcionalmente ao tamanho do arquivo, porque
  INSTREAM é streaming;
- **CPU é o recurso dominante** durante o scan, e o workload é _burst_;
- o arquivo é de zeros e **não representa o pior caso** de parsing de archives
  ou documentos complexos — por isso a margem no `limit` de memória.

---

## Arquitetura proposta

```
                        ns: ingress-system
                        ┌──────────┐
                        │ Traefik  │
                        └────┬─────┘
             ┌───────────────┴────────────────┐
   [NP: traefik-http, port http]   [NP: traefik-http + upload-guard]
             │                                │
             ▼                                ▼
     ┌───────────────┐                ┌────────────────┐
     │ file-service  │◄───────────────│  upload-guard  │
     │    :8083      │  [NP dedicada] │     :8080      │
     │  component:   │   8083         │ component:     │
     │    file       │                │  upload-guard  │
     └───┬───┬───┬───┘                └────────────────┘
         │   │   │  └──────────────┐         │
   5432  │   │   │ 6379            │ 3310    │ DNS/53
         ▼   ▼   ▼                 ▼         ▼
   postgres valkey seaweedfs    clamav    kube-system
              (master 9333 +    :3310     (CoreDNS)
               filer 8888,        ▲
               UM processo)       │
                          [NP: ingress só de component=file, TCP/3310]
                          sem Ingress, sem NodePort, sem LB, sem DNS egress
```

**Fronteiras de confiança (TM-10).** A seta `Traefik → upload-guard` para os dois
caminhos de POST é uma decisão de **roteamento L7**, não de rede: a NetworkPolicy
permite `Traefik → file-service` porque todas as outras rotas de `/api/files`
precisam disso, e ela não distingue método HTTP. Ver §Fronteira de confiança L7.

Fluxo de estados após o upload:

```
POST → 201 (status=pending_scan, scan_next_attempt_at=now())
   │
   ├─ GET download/preview/range → 403 file_not_scanned   (gate fail-closed)
   │
   └─ worker (poll 10 s, claim 1 linha, lease = timeout+30 s)
         ├─ clamd OK          → clean    → download/preview liberados
         ├─ clamd FOUND       → rejected → permanentemente bloqueado
         └─ clamd indisponível→ pending_scan (nada gravado), backoff lease×min(n,8)
```

---

## Plano de implementação

Ordem lógica. **Nada disso foi executado.**

### Bloco 1 — SeaweedFS Filer _(obrigatório; o Filer está parado)_

1. `infra/k8s/overlays/nchat-dev-server/data/resources.yaml`, StatefulSet
   `seaweedfs`:
   - adicionar `- -filer=true` aos args (linha ~254). **Um único processo
     `weed server` passa a servir master, volume e filer — nenhum StatefulSet,
     Deployment ou Service novo é criado.**
   - `startupProbe`: `httpGet` em `/cluster/status` na porta `master` — o master
     tem de subir antes de o filer poder servir;
   - `readinessProbe`: `httpGet` em `/` na porta `filer`. É **exatamente o
     endpoint que `SeaweedFSStore.Ping` usa** (`seaweedfs.go:211-224`) e o mesmo
     que o healthcheck do Compose já usa (`compose.dev.yml:153`). Aplicar aqui a
     mesma lição do A15: **o probe testa o endpoint real do consumidor**, não um
     endpoint conveniente;
   - `livenessProbe`: manter `tcpSocket` na porta `master`.
   - O Service `seaweedfs` já expõe `filer: 8888` — nenhuma alteração.

### Bloco 2 — Segredo de envelope encryption _(PR separado, ver §R12)_

2. Gerar os valores fora do repositório seguindo
   `docs/runbooks/file-service-envelope-encryption.md` §Provisioning, a partir de
   `infra/k8s/secrets/templates/nchat-dev-file-encryption.template.yaml`, em
   `infra/k8s/secrets/unsealed/` (git-ignored).
3. Selar com `scripts/secrets/sealed-secrets-seal.sh` (escopo `strict`).
4. **Novo:** `infra/k8s/secrets/sealed/nchat-dev/nchat-file-encryption.yaml`.
5. `infra/k8s/secrets/sealed/nchat-dev/kustomization.yaml` — adicionar o
   recurso.

### Bloco 3 — ClamAV _(renderizado somente pelo overlay nchat-dev)_

6. **Mover:** `infra/compose/clamav/clamd.conf` →
   `infra/k8s/base/services/clamav/clamd.conf`. Os limites existentes,
   derivados de `uploadpolicy.MaxMaxUploadBytes`, **não mudam**, e o arquivo
   continua **sem diretiva `User`** e **sem `ForceToDisk`**.
   6a. **Acrescentar ao mesmo `clamd.conf`** o bloco de política exigido pelo
   Threat Model: `AlertExceedsMax yes`, `MaxScanTime 420000` e os limites de
   parser explicitados (TM-01 e TM-06). Conteúdo exato em
   §Configuração do clamd, item C. Este é o único ponto do plano em que o
   conteúdo do arquivo muda em relação à Rev 2, e a mudança vale igualmente
   para o Compose, que monta o mesmo arquivo.
7. `infra/compose/compose.dev.yml` — ajustar o mount para
   `../k8s/base/services/clamav/clamd.conf:/etc/clamav/clamd.conf:ro`.
8. **Novo:** `infra/k8s/base/services/clamav/{deployment.yaml, service.yaml, kustomization.yaml, README.md}`.
   O `kustomization.yaml` gera o ConfigMap a partir do `clamd.conf`, no mesmo
   padrão de `base/services/upload-guard/kustomization.yaml`. O
   `deployment.yaml` carrega o initContainer validado, os dois `emptyDir` com
   `sizeLimit` e o `ephemeral-storage` explícito.
9. **`infra/k8s/base/kustomization.yaml` NÃO é alterado.** O diretório existe sob
   `base/` apenas para hospedar o arquivo compartilhado com o Compose e os
   manifests reutilizáveis; ele **não** é um recurso da base.
10. `infra/k8s/overlays/nchat-dev-server/kustomization.yaml` — adicionar
    `../../base/services/clamav` em `resources`. É o único lugar que o
    renderiza. (Alternativa equivalente, se o Design Review preferir a semântica
    explícita: transformar o diretório em um Kustomize _Component_ e referenciá-lo
    por `components:` — kustomize v5.7.1, já fixado em
    `scripts/deploy/nchat-dev/kustomize.env`, suporta ambos.)
11. **Nenhuma alteração em `resource-quota.yaml` nem em `quota-patch.yaml`** —
    ver §Recursos.

### Bloco 4 — Configuração do file-service

12. `infra/k8s/overlays/nchat-dev-server/configmap-patch.yaml` — adicionar:
    `FILE_UPLOADS_ENABLED: "true"`, `FILE_MALWARE_SCAN_REQUIRED: "true"`,
    `FILE_MALWARE_SCANNER_ADDRESS: clamav:3310`,
    `FILE_MALWARE_SCAN_TIMEOUT_SECONDS: "300"`,
    `SEAWEEDFS_FILER_URL: http://seaweedfs:8888` (substituindo o `null`).
    Manter `SEAWEEDFS_S3_ENDPOINT: null`.
13. `infra/k8s/overlays/nchat-dev-server/patches/file-service.yaml` — adicionar
    bloco `env` com `secretKeyRef`, no formato de `patches/chat-service.yaml`.
    Ver §Fontes de configuração do file-service.

### Bloco 5 — NetworkPolicies

14. `infra/k8s/overlays/nchat-dev-server/network-policies.yaml` — ver
    §NetworkPolicies.

### Bloco 6 — Invariantes de CI _(mesmo commit que o Bloco 5)_

15. `scripts/ci/k8s-manifests-check.sh` — substituições, invariantes novos e
    invariantes derivados do Threat Model (TM-01 e TM-06). Ver §CI.
    15a. `scripts/ci/gateway-config-check.sh` — **estender ao manifest renderizado**
    e acrescentar prioridade, `strip-files-prefix` e a asserção negativa de
    backend (TM-10, §CI 20-24). Nenhuma asserção existente é removida.

### Bloco 7 — Testes derivados do Threat Model

16. Teste de unidade da **matriz de veredito do cliente clamd** (TM-01), contra
    daemon falso. Ver §Testes.
17. Teste de **limite interno** provando que limite excedido não vira `clean`
    (TM-01), com fixture sintética segura — nunca malware real nem archive bomb.
18. Testes **E2E de roteamento** pelo host público (TM-10), E1 a E6.

### Bloco 8 — Documentação

19. `docs/api/file-attachments.md` — configuração em Kubernetes; a semântica de
    `AlertExceedsMax` e da ordem de timeouts; a declaração de que a passagem
    pelo guard é propriedade de roteamento L7.
20. `docs/runbooks/nchat-dev-server.md` — provisionamento do
    `nchat-file-encryption`, ClamAV, `-filer=true`, convergência e remoção das
    policies manuais.
21. `docs/runbooks/file-service-envelope-encryption.md` — referenciar o
    SealedSecret nchat-dev.
22. `infra/k8s/README.md` — ClamAV em §Workloads, com a nota de que ele é
    exclusivo do overlay nchat-dev.

**Fora de escopo, deliberadamente:** freshclam, HA do ClamAV, ClamAV em
k3s-dev/k3s-staging, alteração do upload-guard, do cap de 536879104 B, da CSP,
do gate `FILE_MALWARE_SCAN_REQUIRED` ou do valor de
`FILE_MALWARE_SCAN_TIMEOUT_SECONDS`, e qualquer edição em
`migrations/files/000005_*.sql` (A8).

**Entrou em escopo pelo Threat Model:** `MaxScanTime` e `AlertExceedsMax` no
`clamd.conf` — a Rev 2 os havia diferido, o TM-01 os tornou obrigatórios.

---

## ClamAV — workload

### Identidade e hardening

```yaml
securityContext:            # pod
  runAsNonRoot: true
  runAsUser: 100            # clamav — medido na imagem
  runAsGroup: 101           # clamav — medido na imagem
  fsGroup: 101
  seccompProfile:
    type: RuntimeDefault

securityContext:            # container (e initContainer)
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop:
      - ALL
```

Mais `automountServiceAccountToken: false` e `enableServiceLinks: false`.
Entrypoint: `/init-unprivileged` — **condicionado** à verificação de que ele
funciona com a config customizada, os emptyDirs e o UID/GID acima
(§Decisões do segundo Design Review, item 3).

### Configuração do clamd — política explícita (TM-01, TM-06)

O Threat Model reprovou a Rev 2 porque o desenho dependia de **defaults ocultos
do engine** para uma decisão de segurança. Esta seção transforma cada limite
relevante em política versionada, e separa deliberadamente os dois problemas que
a Rev 2 tratava como um só.

#### A — Limite de TEMPO: quem tem de vencer a corrida

Estado observado no benchmark: `Global time limit set to 120000 milliseconds`
(default de `MaxScanTime`) e `AlertExceedsMax heuristic detection disabled`
(default). Com `FILE_MALWARE_SCAN_TIMEOUT_SECONDS=300`, **o limite interno do
engine venceria a corrida** — e um limite de tempo atingido pode produzir um
resultado terminal ambíguo que o cliente leria como `OK`. É exatamente o TM-01.

`AlertExceedsMax` **não resolve isto**: essa diretiva promove limites de
_conteúdo_ (tamanho, recursão, número de arquivos), não o limite de _tempo_. Os
dois problemas precisam de mitigações diferentes.

**Decisão: o deadline externo do file-service é a autoridade fail-closed, e o
limite interno do clamd é apenas um backstop que nunca deve ser alcançado.**

Ordem obrigatória, do primeiro ao último a disparar:

| #   | Prazo                                       | Valor                  | Origem                                                                                                                               | Papel                                                                                                                                            |
| --- | ------------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | Deadline de socket e de job do file-service | **300 s**              | `FILE_MALWARE_SCAN_TIMEOUT_SECONDS` (`config.go:216`); `conn.SetDeadline` em `clamd.go:139`; `ctx` fecha a conexão em `clamd.go:136` | **Autoridade externa fail-closed.** Ao expirar, a conexão é fechada, o scanner devolve erro, nada é gravado e o anexo continua em `pending_scan` |
| 2   | Lease do claim                              | **330 s**              | `newScanLease(jobTimeout) = jobTimeout + scanLeaseMargin(30 s)` (`malware_scan_service.go:35-67`)                                    | A linha volta a ficar _due_ e outro worker pode reivindicá-la                                                                                    |
| 3   | `MaxScanTime` do clamd                      | **420 000 ms = 420 s** | `clamd.conf` (novo)                                                                                                                  | Backstop interno. Nunca decide o resultado no caminho normal                                                                                     |

`MaxScanTime 420000` — justificativa concreta, e por que **não** 301000 nem
330000:

- **120 s de margem absoluta (40%) sobre os 300 s.** Não é um valor colado no
  deadline externo: é folga suficiente para jitter de agendamento, throttling de
  CPU no `cpu.limit: 1250m`, latência de TCP e a diferença entre o relógio do
  cliente e o do engine;
- **estritamente acima do lease de 330 s.** Se `MaxScanTime` fosse 330000 ele
  coincidiria exatamente com o instante em que a linha volta a ser reivindicável
  — dois eventos independentes no mesmo milissegundo é a definição de corrida
  não determinística. 420 s mantém o backstop fora dessa janela;
- **estruturalmente já favorecido:** o relógio do clamd começa quando ele começa
  a _escanear_, ou seja, **depois** de receber o stream, enquanto os 300 s do
  cliente contam desde o dial. O engine tem, portanto, menos tempo de parede
  disponível do que o número sugere — a margem de 120 s é sobre uma corrida que
  já está inclinada a favor do cliente;
- **o teto efetivo continua sendo 300 s**, porque o cliente fecha o socket
  primeiro e o clamd aborta o scan de um peer que sumiu. `MaxScanTime` não
  afrouxa nenhuma proteção de DoS: ele só existe para o caso em que o deadline
  externo falhe.

**Proibido:** `MaxScanTime` menor ou igual ao timeout externo. Se
`FILE_MALWARE_SCAN_TIMEOUT_SECONDS` mudar, `MaxScanTime` tem de ser recalculado
na mesma mudança — e o CI passa a provar a desigualdade (§CI, invariante 15).

#### B — Limites de CONTEÚDO: um limite excedido não pode parecer scan íntegro

**Decisão: `AlertExceedsMax yes`.**

Com o default (`no`), ultrapassar `MaxFileSize`, `MaxScanSize`, `MaxFiles` ou
`MaxRecursion` faz o clamd **parar de inspecionar e responder `OK`** — uma
aprovação sobre um arquivo parcialmente examinado, que o cliente registra como
`clean` e o gate de entrega passa a liberar. Com `yes`, o mesmo evento vira
`stream: Heuristics.Limits.Exceeded FOUND`.

Consequências, aceitas explicitamente:

- o cliente Go já classifica qualquer sufixo `" FOUND"` como `VerdictInfected`
  sem erro (`clamd.go:323-328`), o que persiste `rejected`. **Nenhuma alteração
  em Go é necessária para esta mitigação**;
- **falsos positivos por `Heuristics.Limits.Exceeded` são aceitáveis.** A
  política desta feature prefere `rejected` ou erro a aprovação parcial, e
  SECURITY.md §"Regras para uploads" já estabelece que indisponibilidade,
  timeout ou resposta inválida nunca produzem aprovação. Um arquivo legítimo
  recusado é um incidente de suporte; um arquivo não inspecionado aprovado é uma
  falha do controle;
- `FILE_MALWARE_SCAN_REQUIRED=true` **não muda**. `AlertExceedsMax` endurece o
  veredito; não substitui o gate.

**Proibido como "solução" para falso positivo:** `AlertExceedsMax no`. O
tratamento correto é ajustar o limite específico que está sendo excedido, com
justificativa, ou recusar o arquivo.

#### C — `clamd.conf` esperado

Bloco de política a acrescentar ao arquivo já existente. Os valores da primeira
tabela **já estão no `clamd.conf` versionado** e não mudam; os demais tornam
explícitos os efetivos observados na imagem, para que deixem de ser defaults
ocultos (TM-06).

Já presentes, preservados (derivados de `uploadpolicy.MaxMaxUploadBytes`):

```conf
StreamMaxLength 512M
MaxFileSize     512M
MaxScanSize     1024M
MaxRecursion    12
MaxFiles        10000
MaxThreads      4
```

Novos — semântica de veredito (TM-01):

```conf
AlertExceedsMax yes
MaxScanTime     420000
```

Novos — limites de parser tornados explícitos (TM-06), com os **valores
efetivos já observados na imagem**, não valores inventados:

```conf
MaxEmbeddedPE       40M
MaxHTMLNormalize    40M
MaxHTMLNoTags       8M
MaxScriptNormalize  20M
MaxZipTypeRcg       1M
MaxPartitions       50
MaxIconsPE          100
MaxRecHWP3          16
PCREMatchLimit      100000
PCRERecMatchLimit   2000
PCREMaxFileSize     100M
```

Regras que o arquivo mantém:

- **nenhuma diretiva `User`** — o UID é imposto pelo `securityContext`; com
  `capabilities: drop: [ALL]` o clamd não teria como executar a troca de usuário
  que essa diretiva pede;
- **`ForceToDisk` permanece ausente**, isto é, no default `no`. Verificado: o
  `clamd.conf` versionado não contém `ForceToDisk`, `TemporaryDirectory`,
  `LeaveTemporaryFiles`, `AlertExceedsMax` nem `MaxScanTime`. Manter `ForceToDisk`
  desligado é o que evita forçar a disco cada arquivo embutido de um formato
  composto — o scratch de `/tmp` é dimensionado para o caso que resta
  (§Writable paths);
- se a sintaxe exata de alguma diretiva divergir no ClamAV 1.4, vale **a sintaxe
  aceita pela versão fixada**, nunca um valor diferente do efetivo. Qualquer
  divergência de valor exige justificativa registrada.

#### D — Semântica do cliente clamd: preservada, não redesenhada

Regra que continua valendo: **somente uma resposta terminal completa e válida
`<target>: OK\0` pode produzir `VerdictClean`.**

A inspeção de `services/file-service/internal/scanner/clamd.go` mostra que o
comportamento atual **já satisfaz** o exigido pelo TM-01, e por construção, não
por acaso:

| Situação                                            | Trecho                                                                                             | Resultado                                | Vira `clean`?        |
| --------------------------------------------------- | -------------------------------------------------------------------------------------------------- | ---------------------------------------- | -------------------- |
| `... OK\0`                                          | `clamd.go:321-322`                                                                                 | `VerdictClean`                           | **sim — único caso** |
| `... FOUND\0` (inclui `Heuristics.Limits.Exceeded`) | `clamd.go:323-328`                                                                                 | `VerdictInfected`, sem erro → `rejected` | não                  |
| `... ERROR`                                         | `clamd.go:331-332`                                                                                 | `ErrProtocol`                            | não                  |
| `size limit exceeded`                               | `clamd.go:329-330`                                                                                 | `ErrStreamTooLarge`                      | não                  |
| resposta desconhecida / vazia                       | `clamd.go:333-337` (`default`)                                                                     | `ErrProtocol`                            | não                  |
| **NUL ausente** / resposta truncada                 | `readBounded`, `clamd.go:348-358` — o terminador é **obrigatório**; EOF vira `io.ErrUnexpectedEOF` | erro                                     | não                  |
| EOF, reset, falha de socket                         | `clamd.go:316`, `errRedacted`                                                                      | erro                                     | não                  |
| timeout do deadline                                 | `clamd.go:139`, `errRedacted` → `DeadlineExceeded`                                                 | erro                                     | não                  |
| cancelamento de contexto                            | `clamd.go:136` fecha o socket sob a I/O                                                            | erro                                     | não                  |
| falha da fonte de conteúdo (storage/decrypt)        | `sourceError`, `clamd.go:200-203`                                                                  | erro                                     | não                  |

Três propriedades sustentam isso e não podem ser perdidas numa refatoração
futura: o zero value de `Verdict` é `VerdictClean`, e por isso **todo retorno
nomeia o veredito explicitamente** em vez de depender do default; não existe
ramo `"se não disser FOUND então está limpo"` — a classificação é por sufixo
contra um conjunto fechado; e a leitura do reply acontece **em paralelo** ao
envio, para que uma resposta seguida de `close` pelo daemon não se perca num
reset (`clamd.go:143-165`).

**Conclusão: nenhuma correção em Go é necessária nesta etapa.** O que falta é
travar o comportamento com testes, para que a propriedade não se degrade — ver
§Testes, "Matriz de veredito do cliente clamd (TM-01)".

Um ponto residual, deliberado e a cobrir por teste: `Scan` honra a resposta do
daemon mesmo quando o envio falhou (`clamd.go:206-208`), porque o daemon pode
ter respondido antes de fechar. O teste tem de provar que esse caminho **não**
produz `clean` a partir de um stream incompleto — só a partir de uma resposta
terminal íntegra.

### Signatures — decisão fechada (R2)

```
initContainer (mesma imagem)
   └─ copia /var/lib/clamav da camada da imagem  →  emptyDir
container clamd
   └─ monta o mesmo emptyDir em /var/lib/clamav
```

Por que assim, e não de outro jeito:

- **`emptyDir` puro em `/var/lib/clamav` não funciona.** Diferente do volume
  nomeado do Docker Compose, um `emptyDir` do Kubernetes **não** herda o
  conteúdo da imagem: ele mascararia a base de assinaturas e o clamd subiria sem
  base;
- **não montar nada** deixaria a base legível, mas dependeria de o entrypoint
  jamais escrever naquele caminho — uma aposta contra `readOnlyRootFilesystem`;
- **PVC está descartado** para o nchat-dev: exigiria PV estático novo em
  `srv-apps-01`, consumiria cota de `persistentvolumeclaims`/`requests.storage`
  e mudaria o runbook, sem nenhum benefício (as assinaturas vêm da imagem, não
  são geradas em runtime).

Propriedades obtidas: startup determinístico, sem PVC, sem freshclam, sem egress
externo, compatível com root filesystem read-only.

#### Validações obrigatórias do initContainer (TM-06)

A cópia é um ponto de confiança: é ela que decide com qual base de assinaturas o
gate de entrega vai operar. O desenho exige, e o CI verifica onde for estático:

1. **Mesma imagem, mesmo digest.** initContainer e container principal usam
   `clamav/clamav:1.4@sha256:<digest>` — literalmente a mesma referência. Um
   initContainer com digest diferente copiaria assinaturas de um build que não é
   o que vai escanear;
2. **falhar se a origem estiver vazia.** Se `/var/lib/clamav` da camada da
   imagem não contiver a base esperada, o initContainer sai com código ≠ 0;
3. **falhar se o destino ficar vazio** após a cópia — cópia silenciosamente
   parcial é pior que cópia ausente;
4. **copiar apenas o conteúdo esperado** da base (`*.cvd` / `*.cld` /
   `*.cdb` e afins), sem varrer caminhos arbitrários;
5. **não seguir symlinks** para fora do diretório de origem sem validação;
6. **ownership final compatível com UID 100 / GID 101**, para que o clamd
   não-root consiga ler a base;
7. **o container principal não inicia se o initContainer falhar** — é o
   comportamento nativo do Kubernetes e é a propriedade desejada: sem base, sem
   scanner, e portanto **nada é aprovado**. Falha fechada;
8. **o readiness do clamd confirma que a base foi realmente carregada**, não
   apenas que a porta abriu (§Probes).

Nenhum freshclam é criado. As assinaturas continuam vindo exclusivamente da
imagem fixada por digest.

### Writable paths e limites de scratch (TM-06)

| Caminho           | Volume                                  | `sizeLimit` | Motivo              |
| ----------------- | --------------------------------------- | ----------- | ------------------- |
| `/tmp`            | `emptyDir`                              | **2Gi**     | scratch de scan     |
| `/var/lib/clamav` | `emptyDir`, populado pelo initContainer | **1Gi**     | base de assinaturas |

**`/tmp` = 2Gi — justificativa.** O pior caso plausível de um único scan é a
soma de duas coisas limitadas pela própria política do clamd:

- o arquivo de stream que o INSTREAM materializa, teto `StreamMaxLength 512M`;
- o scratch de descompactação de formatos compostos, teto `MaxScanSize 1024M`.

Isso dá ~1,5 GiB de pior caso para um scan. A concorrência real é de **um scan
por vez**: `scanBatchSize = 1` (`malware_scan_service.go:22-30`) e o
file-service roda com uma réplica, então `MaxThreads 4` não multiplica esse
número no fluxo do NChat. 2Gi cobre o pior caso com margem sem ser ilimitado, e
é ~4% do disco do nó — um `emptyDir` sem teto é exatamente o caminho para
exaustão de disco do nó a partir de um arquivo enviado por um usuário.

**`/var/lib/clamav` = 1Gi — justificativa.** O volume contém apenas a base
copiada da imagem (`main`, `daily`, `bytecode`), na ordem de algumas centenas de
MiB comprimidos em disco — bem abaixo do ~945 MiB de RSS, que é a base já
expandida em memória. 1Gi dá margem para crescimento da base entre versões da
imagem sem virar cota arbitrária. Se um bump futuro de imagem estourar esse
teto, o initContainer falha, o pod não sobe e **nada é aprovado** — falha
fechada e visível, que é o comportamento correto.

**`ephemeral-storage` explícito.** O uso de `emptyDir` conta contra o
`ephemeral-storage` do container, então deixá-lo sem limite reintroduziria por
outra porta o risco que os `sizeLimit` fecham. Proposta, alinhada aos dois
volumes acima mais logs:

```yaml
resources:
  requests:
    ephemeral-storage: 512Mi
  limits:
    ephemeral-storage: 4Gi
```

Sem impacto na ResourceQuota: ela restringe `requests.cpu`, `requests.memory`,
`limits.cpu`, `limits.memory`, `pods`, `services`, `persistentvolumeclaims` e
`requests.storage` — esta última aplicável a PVC, que este desenho não usa.

`/run/clamav` **não** é montado. O `clamd.conf` do NChat não define `LocalSocket`
nem `PidFile`, então não há evidência de que seja necessário. Se a verificação de
implementação provar o contrário, adicionar **com a evidência registrada** —
nunca preventivamente.

### Probes — decisão corrigida (A15)

**`clamdcheck.sh` está descartado como probe.** O benchmark mostrou o script
reportando `unhealthy` enquanto o daemon respondia `PONG` em TCP/3310 e
completava scans reais. Um probe assim produziria `CrashLoopBackOff` ou
`NotReady` permanente com o scanner íntegro.

O probe deve testar **o mesmo endpoint que o file-service usa** — TCP/3310 — e,
preferencialmente, validar semanticamente `PING`/`PONG`: aceitar a conexão TCP
não prova que o engine terminou de carregar a base de assinaturas, e é
exatamente durante esse carregamento que o pod não deve receber trabalho.

**Candidato primário:** `exec: ["clamdscan", "--ping", "1"]`.

- `clamdscan` está na imagem; `--ping` existe desde ClamAV 0.104 e retorna 0
  somente com `PONG`;
- lê `/etc/clamav/clamd.conf` — o nosso, montado do ConfigMap — que define
  apenas `TCPSocket 3310`, logo conecta por TCP e **não** depende de
  `LocalSocket`;
- não exige shell, nem capabilities extras, nem alteração do hardening.

**A verificar na implementação (§Novo Design Review, item 3):** que
`clamdscan --ping` na 1.4 realmente use `TCPSocket` na ausência de `LocalSocket`
e retorne código diferente de zero com o daemon parado.

**Fallback, apenas se o candidato primário for refutado por evidência:**
`livenessProbe` com `tcpSocket: 3310` e `readinessProbe`/`startupProbe` com um
comando `exec` cuja ferramenta esteja **comprovadamente** presente na imagem,
com o comando justificado no manifest. Não usar `tcpSocket` sozinho para
readiness se a validação de `PONG` for viável.

**Cadência:** `startupProbe` generoso — o carregamento de ~1 GiB de assinaturas
leva dezenas de segundos — seguido de `readiness`/`liveness` folgados. O
`startupProbe` é o que impede um `livenessProbe` de matar o pod durante a carga
inicial.

### Recursos — medidos e aprovados (R5)

Valores **finais**, ratificados no segundo Design Review com um ajuste: o
`cpu.limit` proposto de 1500m foi reduzido para **1250m**.

```yaml
resources:
  requests:
    cpu: 500m
    memory: 1536Mi
  limits:
    cpu: 1250m
    memory: 3Gi
```

| Valor                    | Justificativa                                                                                                                                                                                                                                                                                                                                                                                                           |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `memory.request: 1536Mi` | idle medido ~945 MiB; pico INSTREAM ~1 GiB. 1536Mi dá margem relevante acima do steady-state. Request é garantia de scheduling, não teto de uso.                                                                                                                                                                                                                                                                        |
| `memory.limit: 3Gi`      | ~3× o pico observado. O benchmark usou um arquivo de zeros e **não** cobre o pior caso de parser/archives; a margem é para isso. Com freshclam desligado não há recarga concorrente da base.                                                                                                                                                                                                                            |
| `cpu.request: 500m`      | idle é praticamente zero e os scans são _burst_. Reservar um core inteiro continuamente seria desperdício em dev; 500m garante scheduling sem sobre-reservar.                                                                                                                                                                                                                                                           |
| `cpu.limit: 1250m`       | **decisão do segundo Design Review.** O benchmark observou pico de ~177%, então 1250m pode alongar a fase CPU-bound do scan por throttling — mas o scan de 512 MiB levou 4.258 s contra um orçamento operacional de 300 s, folga larga o bastante para absorver isso com sobra. Em troca, a cota de `limits.cpu` do namespace fica em 7500m de 8000m, preservando **500m** de folga em vez dos 250m que 1500m deixaria. |

**Cabe na ResourceQuota atual, sem alteração:**

| Recurso           | Atual  | + ClamAV  | Cota    | Folga final |
| ----------------- | ------ | --------- | ------- | ----------- |
| `requests.cpu`    | 625m   | 1125m     | 4000m   | 2875m       |
| `requests.memory` | 1536Mi | 3072Mi    | 6144Mi  | 3072Mi      |
| `limits.cpu`      | 6250m  | **7500m** | 8000m   | **500m**    |
| `limits.memory`   | 6400Mi | 9472Mi    | 12288Mi | 2816Mi      |
| `pods`            | 14     | 15        | 30      | 15          |
| `services`        | 13     | 14        | 20      | 6           |

`limits.cpu` fecha com 500m de folga. **A ResourceQuota não é alterada** — foi
o `cpu.limit` do ClamAV que cedeu, e não a cota, exatamente como a alavanca
identificada na Rev 2 previa.

**Nota sobre o initContainer:** as requests efetivas de um pod são
`max(soma dos containers, maior initContainer)`. As requests do initContainer
devem ficar **abaixo** das do container principal para não elevar o consumo de
cota do pod. O cálculo acima assume isso.

**Nota sobre `MaxThreads 4`:** o valor permanece, mas não é fonte de
concorrência no fluxo do NChat — `scanBatchSize = 1` e uma réplica de
file-service significam um scan em voo por vez. `MaxThreads` só limitaria um
segundo cliente do daemon, e **qual pod pode abrir uma conexão TCP/3310** é
precisamente o que `nchat-allow-clamav` restringe — uma pergunta L3/L4, dentro
do que uma NetworkPolicy consegue responder, ao contrário da do TM-10.

**Estes números foram ratificados no segundo Design Review**, com o ajuste de
`cpu.limit` para 1250m. O acréscimo de `ephemeral-storage` (§Writable paths) é
derivado do TM-06 e precisa de ratificação no Threat Model seguinte.

### Estratégia, disponibilidade e exaustão de recursos (TM-06)

- `replicas: 1`, `strategy: Recreate` — réplica única de ~1 GiB; um
  `RollingUpdate` reservaria o dobro de memória durante o rollout sem nenhum
  ganho de disponibilidade;
- o ClamAV **não** entra em `wait_for_rollouts` (a lista em
  `scripts/deploy/nchat-dev/deploy.sh` é fixa) e **não** entra no readiness do
  file-service (A14). Um ClamAV lento ou fora do ar não atrasa nem quebra o
  deploy: os anexos apenas ficam em `pending_scan`, não baixáveis.

**Nenhum evento de exaustão pode promover `clean`.** Registro explícito de cada
caminho e do que ele produz:

| Evento                                      | Efeito imediato                         | Estado do anexo                                                   |
| ------------------------------------------- | --------------------------------------- | ----------------------------------------------------------------- |
| Throttling de CPU no `cpu.limit`            | scan mais lento, fila cresce            | `pending_scan` até um veredito real                               |
| Estouro do `memory.limit` → OOMKill         | container reinicia; o scan em voo morre | `pending_scan`; lease expira em 330 s e a linha volta a ser _due_ |
| Estouro de `/tmp` ou de `ephemeral-storage` | pod despejado pelo kubelet              | `pending_scan`; retomado quando o pod voltar                      |
| `MaxScanTime` atingido (backstop)           | resposta não-terminal ou erro           | `pending_scan` — nunca `clean` (§A do clamd)                      |
| Limite de conteúdo atingido                 | `Heuristics.Limits.Exceeded FOUND`      | `rejected` (§B do clamd)                                          |
| ClamAV indisponível                         | worker registra `result=retry`          | `pending_scan`, com backoff `lease × min(n, 8)`                   |

**Backlog é risco de disponibilidade, não justificativa para bypass.** Uma fila
crescendo significa que anexos demoram a ficar baixáveis — nunca que o gate deva
ser afrouxado. As alavancas legítimas são `cpu.limit`, réplicas de file-service
e o tamanho dos arquivos aceitos; **não** são `FILE_MALWARE_SCAN_REQUIRED`,
`AlertExceedsMax` nem `MaxScanTime`.

#### Observabilidade mínima exigida

O file-service já expõe as categorias de resultado e de erro como conjuntos
fechados decididos no código (`malware_scan_service.go:69-137`). O desenho exige
que estas fiquem observáveis no nchat-dev:

| Sinal                                                                                                                                               | Origem                                                        | Para quê                                 |
| --------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------- | ---------------------------------------- |
| scans por resultado (`clean`, `infected`, `retry`, `too_large`, `superseded`)                                                                       | rótulo de resultado do worker                                 | distinguir veredito de falha operacional |
| erros por categoria (`storage_read`, `decrypt`, `clamd_unavailable`, `clamd_protocol`, `clamd_stream_too_large`, `timeout`, `canceled`, `database`) | rótulo de erro do worker                                      | separar "clamd caiu" de "chave não abre" |
| duração do scan                                                                                                                                     | worker                                                        | detectar aproximação dos 300 s           |
| profundidade da fila `pending_scan`                                                                                                                 | gauge sobre o índice parcial `idx_attachments_scan_pending`   | backlog                                  |
| limite excedido                                                                                                                                     | resultado `too_large` + veredito `Heuristics.Limits.Exceeded` | visibilidade do TM-01/TM-06 em produção  |
| scanner indisponível                                                                                                                                | erro `clamd_unavailable`                                      | alarme operacional                       |

**Proibido como rótulo de métrica:** nome de arquivo, id de anexo, id de
usuário, texto de erro do daemon, nome de assinatura — qualquer valor
influenciado pelo conteúdo enviado. É a mesma regra que SECURITY.md
§Observability já impõe e que o código já segue ao usar vocabulário fechado.

---

## NetworkPolicies

Fluxos mínimos. Nenhum egress genérico, nenhum `0.0.0.0/0`, nenhum
`namespaceSelector: {}`, `default-deny` de ingress **e** de egress preservados.

### Alterações em policies existentes

| Policy                     | Mudança                    | Fluxo liberado                                                               |
| -------------------------- | -------------------------- | ---------------------------------------------------------------------------- |
| `nchat-allow-traefik-http` | `+ upload-guard`           | Traefik → upload-guard:8080 (porta nomeada `http`)                           |
| `nchat-allow-dns-egress`   | `+ file`, `+ upload-guard` | resolução DNS de `file-service`, `postgres`, `valkey`, `seaweedfs`, `clamav` |
| `nchat-allow-postgres`     | `+ file` na origem         | file-service → PostgreSQL:5432 (ingress)                                     |
| `nchat-allow-valkey`       | `+ file` na origem         | file-service → Valkey:6379 (ingress)                                         |

**O ClamAV não é adicionado à policy de DNS.** Com freshclam desligado ele não
resolve nada; ele apenas _aceita_ conexões. Adicionar só mediante evidência
concreta de necessidade.

### Policies novas

| Nome                                    | Tipo    | Selector                  | Regra                                                              |
| --------------------------------------- | ------- | ------------------------- | ------------------------------------------------------------------ |
| `nchat-allow-upload-guard-file-egress`  | Egress  | `component: upload-guard` | → `component: file`, TCP/8083                                      |
| `nchat-allow-upload-guard-file-ingress` | Ingress | `component: file`         | ← `component: upload-guard`, TCP/8083                              |
| `nchat-allow-file-data-egress`          | Egress  | `component: file`         | → `postgres` TCP/5432; → `valkey` TCP/6379; → `seaweedfs` TCP/8888 |
| `nchat-allow-seaweedfs`                 | Ingress | `component: seaweedfs`    | ← `component: file`, TCP/8888 apenas (**não** 9333, **não** 8333)  |
| `nchat-allow-file-clamav-egress`        | Egress  | `component: file`         | → `component: clamav`, TCP/3310                                    |
| `nchat-allow-clamav`                    | Ingress | `component: clamav`       | ← `component: file`, TCP/3310                                      |

### Notas de correção

- **DNS do upload-guard é obrigatório:** `nginx.conf.template:59` usa
  `upstream file_service { server file-service:8083; }`, resolvido no
  _carregamento da config_. Sem DNS o nginx não sobe — não é uma falha só na
  primeira requisição.
- **`nchat-allow-file-clamav-egress` é separada de `nchat-allow-file-data-egress`**
  para que remover o ClamAV seja apagar um arquivo lógico e não editar uma regra
  compartilhada.
- **Proibido no manifest e no CI:** `0.0.0.0/0`, `namespaceSelector: {}`, egress
  genérico, qualquer `Ingress`/`IngressRoute` apontando para o ClamAV,
  `NodePort`, `LoadBalancer`, e qualquer referência à porta 3310 fora da policy
  e do Service ClusterIP.
- **O que estas policies NÃO garantem (TM-10):** elas **não** obrigam um POST de
  upload a passar pelo upload-guard. Ver §Fronteira de confiança L7 abaixo.

### Convergência do drift (R13) — **corrigido na Rev 2**

Existem hoje, criadas manualmente e não versionadas:

1. `nchat-allow-traefik-upload-guard-ingress`
2. `nchat-allow-upload-guard-egress`
3. `nchat-allow-upload-guard-file-ingress`

A policy versionada **`nchat-allow-upload-guard-file-ingress` tem exatamente o
mesmo nome da (3)**. Portanto:

- **(3) é ADOTADA por convergência.** O `kubectl apply` do pipeline assume a
  posse do objeto existente e o alinha ao manifest. **Ela NÃO deve ser apagada,
  nem antes nem depois do deploy.**
- Como o objeto foi criado fora do `apply` versionado, ele pode não ter a
  anotação `last-applied-configuration`. Nesse caso o merge de três vias
  **preserva** campos que existam no objeto vivo e não no manifest. Por isso a
  convergência tem de ser **verificada**, comparando o `spec` renderizado com o
  vivo. Se sobrar campo residual, a remediação é um `kubectl replace` nominal
  desse único objeto — **nunca** um delete.
- **(1) e (2) são removidas nominalmente**, e só _após_ a validação de que os
  fluxos equivalentes estão cobertos por `nchat-allow-traefik-http` (com
  `upload-guard`), `nchat-allow-dns-egress` (com `upload-guard`) e
  `nchat-allow-upload-guard-file-egress`:

  ```
  kubectl delete networkpolicy nchat-allow-traefik-upload-guard-ingress -n nchat-dev
  kubectl delete networkpolicy nchat-allow-upload-guard-egress          -n nchat-dev
  ```

- **Proibido:** `--prune`, curingas, `delete` genérico, ou apagar qualquer
  policy antes da validação.

---

## Fronteira de confiança L7 — upload-guard (TM-10)

### Declaração explícita do limite

**NetworkPolicy não impede um POST de upload de alcançar o file-service
diretamente.** A Rev 2 tratava a passagem obrigatória pelo guard como se fosse
uma propriedade de rede; não é. Correção registrada:

- o Traefik **precisa** alcançar `file-service:8083` para todas as outras rotas
  de `/api/files` — download, HTTP Range, preview, listagem, metadados, health.
  A policy `nchat-allow-traefik-http` existe justamente para permitir isso;
- **NetworkPolicy opera em L3/L4.** Ela não enxerga método HTTP, caminho nem
  corpo. Não existe regra de rede que autorize `GET /api/files/...` e recuse
  `POST /api/files/.../attachments` pelo mesmo caminho de rede;
- portanto **a propriedade "todo POST de upload passa pelo guard" é uma
  propriedade do roteamento L7 do Traefik**, e só pode ser garantida e
  verificada lá.

O que cada camada realmente entrega:

| Camada               | Garante                                                                                                                  | Não garante                                       |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------- |
| NetworkPolicy        | quem pode falar com quem, em que porta                                                                                   | qual método/rota                                  |
| Roteamento Traefik   | que `POST` nos dois caminhos de upload vá ao guard                                                                       | nada sobre outros clientes já dentro do namespace |
| upload-guard (nginx) | teto de corpo **enquanto transmite** (536879104 B)                                                                       | autenticação, autorização, limite de política     |
| file-service         | autenticação, autorização, **limite autoritativo por bytes lidos**, admission control, envelope encryption, gate de scan | —                                                 |

**Esta correção não enfraquece nada.** O upload-guard nunca foi o controle de
segurança do _tamanho de política_: SECURITY.md §"Regras para uploads" já declara
que **"o file-service é a única fronteira de tamanho"** e que contornar gateway
ou frontend não amplia o limite. O guard é defesa em profundidade contra
exaustão de disco do gateway **antes** da autenticação. O que muda aqui é apenas
onde a propriedade é afirmada e provada: em L7, com invariantes de CI e testes
E2E, e não em uma policy de rede que nunca poderia expressá-la.

### Estado atual dos invariantes de roteamento

`scripts/ci/gateway-config-check.sh` **já** verifica, tanto no gateway local
quanto nos três overlays (`assert_upload_route`, linhas 176-222):

- existe uma rota de upload;
- ela é restrita a `PathRegexp(^/api/files/(channels|dm)/[^/]+/attachments$)`;
- ela é restrita a `Method(POST)`;
- o serviço de destino é `upload-guard` — casando a referência real, não a
  palavra, para que um rótulo de componente não satisfaça a asserção;
- ela carrega o middleware `upload-inflight`;
- os routers `/api/files` **não**-upload continuam indo direto ao file-service.

**Lacunas que o TM-10 obriga a fechar** — o que hoje _não_ é verificado:

1. as asserções rodam sobre os **arquivos-fonte**, não sobre o manifest
   **renderizado** pelo kustomize, que é o que efetivamente vai ao cluster;
2. a `priority: 200` da IngressRoute **não** é verificada. Sem ela, a regra
   genérica `PathPrefix(/api/files)` do `Ingress/nchat-dev` pode disputar o
   mesmo POST, e a resolução passa a depender do comprimento da regra —
   frágil e invisível;
3. o middleware `strip-files-prefix` **não** é verificado, apesar de ser o que
   entrega ao guard o caminho que o file-service registra;
4. não existe asserção negativa provando que **nenhuma** rota pública capaz de
   casar um POST de upload aponta para `file-service`.

Os quatro viram invariantes de CI (§CI, invariantes 16-20) e testes E2E
(§Testes).

| Origem                                    | Chaves                                                                                                                                                      |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| ConfigMap `nchat-config`                  | `APP_ENV`, `FILE_UPLOADS_ENABLED`, `FILE_MALWARE_SCAN_REQUIRED`, `FILE_MALWARE_SCANNER_ADDRESS`, `FILE_MALWARE_SCAN_TIMEOUT_SECONDS`, `SEAWEEDFS_FILER_URL` |
| `secretKeyRef` → `nchat-postgres-runtime` | `DATABASE_URL`                                                                                                                                              |
| `secretKeyRef` → `nchat-secrets`          | `AUTH_JWT_HMAC_SECRET`, `VALKEY_URL`                                                                                                                        |
| `secretKeyRef` → `nchat-file-encryption`  | `FILE_ENCRYPTION_MASTER_KEY`, `FILE_ENCRYPTION_MASTER_KEY_ID`, `FILE_ENCRYPTION_PREVIOUS_KEYS`                                                              |
| `env` do Deployment (base)                | `PORT`, `SERVICE_NAME`                                                                                                                                      |
| **Ausente por decisão**                   | `SEAWEEDFS_S3_ENDPOINT`                                                                                                                                     |

---

## Secrets e encryption

**Nenhum valor gerado ou exposto aqui.**

### Formato exato

Fonte: `crypto.ValidateKeyring` via `config.go:326-333`, template e runbook.

| Variável                        | Formato                                                                                              | Obrigatória              |
| ------------------------------- | ---------------------------------------------------------------------------------------------------- | ------------------------ |
| `FILE_ENCRYPTION_MASTER_KEY`    | base64 **padrão** de exatamente **32 bytes** (`openssl rand -base64 32`). Sem default, sem fallback. | sim, com uploads ligados |
| `FILE_ENCRYPTION_MASTER_KEY_ID` | rótulo **não secreto**, `[a-z0-9][a-z0-9._-]{0,63}`; convenção datada, ex.: `kek-2026-08`            | sim                      |
| `FILE_ENCRYPTION_PREVIOUS_KEYS` | pares `id:chave_base64` separados por vírgula; vazio fora de rotação                                 | não                      |

Recusados no start-up: base64 inválido, comprimento ≠ 32 bytes, id malformado,
id duplicado, entrada anterior que sombreie o id ativo. Nenhum valor aparece em
erro, log ou `/readyz`.

### Mecanismo oficial

1. `infra/k8s/secrets/templates/nchat-dev-file-encryption.template.yaml` →
   preencher em `infra/k8s/secrets/unsealed/` (git-ignored).
2. `scripts/secrets/sealed-secrets-seal.sh`, escopo **`strict`** (padrão exigido
   pela SECURITY.md; `namespace-wide` exigiria justificativa no PR,
   `cluster-wide` é proibido).
3. **Sim, deve ser SealedSecret.** SealedSecret é atado a nome **e** namespace,
   por isso `nchat` e `nchat-dev` precisam de manifests distintos — o template
   `nchat-dev-*` existe exatamente por isso.
4. Output selado versionado em
   `infra/k8s/secrets/sealed/nchat-dev/nchat-file-encryption.yaml`, registrado no
   `kustomization.yaml` do diretório.
5. **PR separado**, em branch operacional distinta de `develop`
   (`docs/runbooks/nchat-dev-server.md` §10), **integrado e aplicado antes** do
   PR que liga `FILE_UPLOADS_ENABLED=true`.

### Por que não em `nchat-secrets`

Todo serviço monta `nchat-secrets` com `envFrom`. Colocar a KEK lá a entregaria
a sete serviços que jamais devem vê-la. O `deployment.yaml` da base já documenta
isso nas linhas 56-61, e o `secrets-owners.md` registra a decisão. **Não
adicionar ao secret compartilhado.**

### Rotação

`docs/runbooks/file-service-envelope-encryption.md` §Rotation: gerar novo par,
mover o par atual para `FILE_ENCRYPTION_PREVIOUS_KEYS`, re-selar, aplicar,
reiniciar; depois _rewrap_ das linhas com o `kek_key_id` antigo; só remover a
chave anterior quando
`SELECT count(*) ... WHERE kek_key_id='<antigo>' AND deleted_at IS NULL`
retornar zero. **Limitação registrada pelo próprio runbook:** o job de rewrap
**não está implementado**. Rotação não é automatizada e não tem cadência — é
evento de incidente/comprometimento.

---

## Migrations

### Estado: nada a fazer

A **000005 já está aplicada** no nchat-dev, com `dirty=false` e
`in_progress=false`, colunas e índice presentes, e `files.attachments` vazia
(A9, A10). **Não reaplicar. Não rodar SQL manual. Não alterar o arquivo da
migration** — o checksum SHA-256 registrado em `public.schema_migrations` faria
todo deploy futuro abortar (A8).

O pipeline mantém seus invariantes normais: `run_migrations` executa o Job, que
percorre `find migrations -name "*.up.sql" | sort`, pula o que já está aplicado
após conferir o checksum, e reaplica `grant-runtime.sql` — o que cobre as colunas
da 000005 automaticamente.

### Consultas de verificação corretas

A busca por uma tabela `files.attachment_malware_scan_jobs` **nunca** encontraria
nada: a migration não cria tabela (A9). As consultas válidas são:

```sql
-- 1. registro da migration
SELECT domain, filename, dirty, in_progress, applied_at
  FROM public.schema_migrations
 WHERE domain='files' AND filename='000005_attachment_malware_scan_jobs';

-- 2. colunas
SELECT column_name FROM information_schema.columns
 WHERE table_schema='files' AND table_name='attachments'
   AND column_name IN ('scan_attempts','scan_next_attempt_at');

-- 3. índice parcial
SELECT indexname FROM pg_indexes
 WHERE schemaname='files' AND indexname='idx_attachments_scan_pending';
```

### Comportamento em falha (inalterado)

`backoffLimit: 0` + `restartPolicy: Never` + `activeDeadlineSeconds: 300`: uma
falha **não** é reexecutada. `deploy.sh` faz
`kubectl wait --for=condition=complete --timeout=330s`; falhando, o trap
`on_error` coleta `describe`/`logs` do Job e o deploy aborta **antes** de
`apply_application`. Uma migration parcial fica marcada `dirty`, e
`assert_no_dirty_migrations` bloqueia toda execução seguinte até reparo manual.

---

## Rollout

O `deploy.sh` aplica um **único** `application.yaml` renderizado (ConfigMap,
policies, ClamAV, Deployments). Não é possível intercalar `kubectl apply`
manuais dentro do pipeline sem sair do mecanismo oficial — a ordenação é obtida
**fatiando em dois PRs**, não sequenciando comandos.

### PR 1 — SealedSecret

1. `nchat-file-encryption` mergeado e aplicado pelo mecanismo oficial.
   Verificar apenas a existência: `kubectl get secret nchat-file-encryption -n nchat-dev`.
   **Nunca** inspecionar o conteúdo.

### PR 2 — Infraestrutura (#483)

2. Merge → pipeline normal.
3. `start_data_services` — StatefulSet SeaweedFS com `-filer=true`.
   **Gate:** o Filer tem de responder em `:8888` antes de seguir; é o
   `readinessProbe` novo que garante isso, e `wait_for_rollouts` já espera
   `statefulset/seaweedfs`.
4. `run_migrations` — Job roda e reporta a 000005 como `[SKIP]` (já aplicada).
5. `apply_application` — ConfigMap com `FILE_UPLOADS_ENABLED=true`,
   NetworkPolicies, ClamAV, patch do file-service.
6. `wait_for_rollouts` — file-service com `maxUnavailable: 0`: se `/readyz`
   falhar (PostgreSQL, Filer ou policy), **o pod antigo continua servindo** e o
   deploy aborta sem outage. É a rede de segurança que torna o apply único
   aceitável.
7. `run_smoke_tests` — `/healthz` interno de todos os deployments +
   `GET https://$HOST/`.
8. **Pós-deploy, manual e nominal:** validar a convergência de
   `nchat-allow-upload-guard-file-ingress` (§R13) e então remover **somente**
   `nchat-allow-traefik-upload-guard-ingress` e `nchat-allow-upload-guard-egress`.
9. Upload real → `201`, `status=pending_scan`.
10. `pending_scan → clean` dentro de ~1 poll (10 s) + duração do scan.
11. Download/preview/Range bloqueados antes, liberados depois.
12. EICAR → `rejected`, download `403 file_not_scanned`, permanente. Fixture
    padrão EICAR apenas, conforme SECURITY.md — nenhuma fixture maliciosa nova.

### Gate de "stack funcional" (atualizado pelo Threat Model)

O stack só é considerado funcional quando **todos** estes forem verdadeiros:

1. **ClamAV `Ready`** — com o probe que valida `PONG`, não apenas TCP aberto;
2. **Filer `Ready`** — respondendo em `:8888`;
3. **file-service `Ready`** — `postgres` e `object-storage` passando;
4. **invariantes de roteamento validados no CI** (TM-10, §CI 20-24) sobre o
   manifest renderizado;
5. **semântica de limite de scan validada** (TM-01) — `AlertExceedsMax yes`,
   `MaxScanTime` acima do timeout externo, e o teste de limite interno provando
   que limite excedido não vira `clean`.

`FILE_UPLOADS_ENABLED=true` só entra quando **todas** as dependências estão
presentes: Filer ativo, Secret aplicado, policies versionadas, ClamAV implantado
e os cinco itens acima satisfeitos.

Depois do deploy, o comportamento não muda: **indisponibilidade do ClamAV não
derruba o file-service**; os uploads apenas se acumulam em `pending_scan`, não
baixáveis.

---

## Rollback

O rollback seguro é **de configuração, não de schema**.

### Rollback primário: desligar uploads

`FILE_UPLOADS_ENABLED=false` no `configmap-patch.yaml` + rollout. Rotas voltam a
`503`, o worker não inicia, o serviço fica health-only. **Nenhum anexo é
perdido**, nenhum `pending_scan`/`rejected` fica acessível, nenhuma capacidade
de decrypt é perdida (o Secret permanece), nenhum bypass é criado.

### Rollback de imagem

`kubectl rollout undo deployment/file-service -n nchat-dev`. A 000005 é
_forward-compatible_ (colunas nullable/com DEFAULT — linhas 40-44 da migration),
então o binário anterior funciona com o schema novo: apenas nunca agenda scans.

### Proibições explícitas

- **Não rodar `migrate.sh down`.** A `000005.down.sql` remove as colunas de
  agendamento. Pior: a **000002** tem guarda que a torna irreversível com
  `files.attachments` não-vazia
  (`docs/runbooks/file-service-envelope-encryption.md` §Rollback), e o `down` do
  runner rola migrations em ordem `applied_at DESC` — um `--steps` errado
  alcança a 000002.
- **Não `UPDATE ... SET status='clean'`** para "destravar" anexos. É bypass de
  malware scan e viola a SECURITY.md.
- **Não `FILE_MALWARE_SCAN_REQUIRED=false`.** `APP_ENV=nchat-dev` está no
  allowlist (`config.go:421`), então o serviço **aceitaria** — o que torna a
  proibição uma disciplina, e é por isso que ela vira invariante de CI (§R14).
- **Não remover o SealedSecret `nchat-file-encryption`.** É a única cópia da
  KEK; perdê-la é perda de dados irreversível de todo anexo cifrado sob ela
  (`secrets-owners.md` §"Losing the attachment key ring").
- **Não remover as policies de default-deny, não abrir a rede, não expor o
  ClamAV.**
- **Não remover o ClamAV como forma de "destravar" uploads.** Sem scanner o
  worker não inicia e tudo fica em `pending_scan` — comportamento correto, mas
  não é rollback.
- **Não `AlertExceedsMax no`** para "resolver" falso positivo por
  `Heuristics.Limits.Exceeded` (TM-01). Isso restaura exatamente a condição que
  reprovou a Rev 2: limite excedido voltando a responder `OK`. O tratamento
  correto é ajustar o limite específico, com justificativa, ou recusar o
  arquivo.
- **Não reduzir `MaxScanTime` para igual ou abaixo do timeout externo**
  (TM-01). Isso inverte a corrida de timeouts e devolve ao engine a decisão que
  tem de ser do file-service. Se o timeout externo mudar, `MaxScanTime` muda
  junto, na mesma alteração, e o CI prova a desigualdade.
- **Não remover `emptyDir.sizeLimit` nem o `ephemeral-storage`** para "dar mais
  espaço ao scanner" (TM-06). O teto finito é o que impede um arquivo enviado
  por um usuário de exaurir o disco do nó.
- **Não alegar que NetworkPolicy resolve o bypass do upload-guard** (TM-10), nem
  remover a rota dedicada de POST confiando na rede.

### Se apenas o ClamAV precisar sair

Limpar `FILE_MALWARE_SCANNER_ADDRESS` e remover o Deployment. Anexos existentes
em `clean` continuam baixáveis; novos ficam em `pending_scan` indefinidamente.
Estado degradado seguro, não um rollback.

---

## CI

Nenhum gate é relaxado genericamente. Cada proibição cega vira um invariante
**positivo** e mais preciso em `scripts/ci/k8s-manifests-check.sh`.

### Substituições

| Gate atual                                              | Substituto                                                                                                                         |
| ------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `grep -Eq 'SEAWEEDFS_(FILER_URL\|S3_ENDPOINT)'` → falha | `SEAWEEDFS_FILER_URL` presente **exatamente uma vez** com valor exato `http://seaweedfs:8888`; `SEAWEEDFS_S3_ENDPOINT` **ausente** |
| lista fechada de policies de Egress (8 nomes)           | lista fechada estendida com as 3 novas policies de egress                                                                          |
| `external_image_refs` regex + `-eq 6`                   | regex e contagem estendidas para `clamav/clamav`, mantendo a exigência de digest `@sha256:`                                        |

### Invariantes novos

1. `FILE_MALWARE_SCAN_REQUIRED` presente no render do nchat-dev e **igual a
   `"true"`** — falha se ausente, se `false` ou se qualquer outro valor;
2. `FILE_MALWARE_SCANNER_ADDRESS` igual a `clamav:3310`, casando
   `^[a-z0-9.-]+:[0-9]{1,5}$` (sem esquema, path ou credenciais);
3. `FILE_UPLOADS_ENABLED` presente e explícito;
4. ClamAV presente no render de **`overlays/nchat-dev-server`** e **ausente** nos
   renders de `base`, `overlays/k3s-dev` e `overlays/k3s-staging`;
5. `base/kustomization.yaml` **não** referencia `services/clamav`;
6. Service do ClamAV é `ClusterIP`; nenhum `NodePort`/`LoadBalancer`; nenhuma
   `Ingress`/`IngressRoute` referencia `clamav` ou `3310`;
7. hardening do ClamAV: `runAsUser: 100`, `runAsGroup: 101`, `fsGroup: 101`,
   `runAsNonRoot: true`, `readOnlyRootFilesystem: true`,
   `allowPrivilegeEscalation: false`, `drop: [ALL]`, `RuntimeDefault`,
   `automountServiceAccountToken: false` — no container **e** no initContainer;
8. `nchat-allow-clamav` com origem **única** `component: file` e porta **única**
   TCP/3310;
9. nenhum probe do ClamAV referencia `clamdcheck.sh` nem `LocalSocket`;
10. o `clamd.conf` renderizado no ConfigMap contém `StreamMaxLength 512M` e
    `MaxFileSize 512M` (amarrados a `uploadpolicy.MaxMaxUploadBytes`) e **não**
    contém diretiva `User`;
11. existe **exatamente um** `clamd.conf` no repositório, e é o mesmo arquivo que
    o Compose monta — nenhuma segunda cópia;
12. o StatefulSet `seaweedfs` declara `-filer=true` e um probe que cobre a porta
    `filer`.

### Invariantes derivados do Threat Model

**TM-01 — semântica de veredito** (sobre o `clamd.conf` renderizado no ConfigMap):

13. `AlertExceedsMax yes` presente **exatamente uma vez**; falha se ausente ou se
    igual a `no`;
14. `MaxScanTime` presente e explícito — falha se ausente (default oculto);
15. **desigualdade de timeouts provada:**
    `MaxScanTime` (ms) `>` `FILE_MALWARE_SCAN_TIMEOUT_SECONDS × 1000`, lida do
    mesmo render. É o invariante que impede que uma mudança futura em qualquer
    um dos dois lados inverta a ordem silenciosamente. Recomendado exigir também
    margem mínima — `MaxScanTime ≥ (timeout + 60) × 1000` — para que a
    desigualdade não seja satisfeita por 1 ms;

**TM-06 — limites e scratch:**

16. todas as diretivas de limite listadas em §C do clamd presentes com os valores
    versionados (`StreamMaxLength`, `MaxFileSize`, `MaxScanSize`, `MaxRecursion`,
    `MaxFiles`, `MaxThreads`, `MaxEmbeddedPE`, `MaxHTMLNormalize`,
    `MaxHTMLNoTags`, `MaxScriptNormalize`, `MaxZipTypeRcg`, `MaxPartitions`,
    `MaxIconsPE`, `MaxRecHWP3`, `PCREMatchLimit`, `PCRERecMatchLimit`,
    `PCREMaxFileSize`);
17. `emptyDir.sizeLimit` presente e finito em **todos** os volumes do pod ClamAV
    — falha se algum `emptyDir` do workload não tiver teto;
18. `ephemeral-storage` declarado em `requests` e `limits` do container ClamAV, e
    `limits.ephemeral-storage` ≥ soma dos `sizeLimit` dos `emptyDir`;
19. initContainer e container principal referenciam **a mesma imagem e o mesmo
    digest**;

**TM-10 — roteamento como fronteira L7** (sobre o manifest **renderizado** do
overlay nchat-dev, e não apenas sobre o arquivo-fonte):

20. existe uma rota com `Method(POST)` **e** o `PathRegexp` de upload, cujo
    backend é `upload-guard` na porta `8080`;
21. essa rota declara `priority` e o valor é **estritamente maior** que a
    prioridade efetiva da rota genérica `/api/files` — na prática, asserção de
    que `priority` está presente e acima do comprimento da regra genérica, que é
    como o Traefik calcula a prioridade default;
22. os middlewares `strip-files-prefix` **e** `upload-inflight` estão na rota;
23. **asserção negativa:** nenhuma rota pública que possa casar
    `POST /api/files/(channels|dm)/{id}/attachments` tem `file-service` como
    backend;
24. as asserções de 20 a 23 falham se a rota dedicada desaparecer, se o backend
    mudar, se a prioridade cair, ou se qualquer dos dois middlewares sumir.

O `gateway-config-check.sh` já cobre parte disso sobre os arquivos-fonte
(`assert_upload_route`); o trabalho é **estender ao render** e acrescentar
prioridade, `strip-files-prefix` e a asserção negativa. Nenhuma asserção
existente é removida ou afrouxada.

**Proibido no plano e na documentação:** afirmar que NetworkPolicy bloqueia o
bypass do upload-guard por método HTTP.

### Checks existentes que devem continuar passando

`gateway-config-check.sh` (upload-guard e o cap de 536879104 B, **sem
regressão**), `migrations-check.sh`, `sealed-secrets-policy-check.sh`,
`dev-env-config-check.sh` (após mover o `clamd.conf`),
`nchat-dev-deployment-check.sh`, `kustomize build` sem warnings,
`kubeconform -strict`, `docker compose ... config`, `trivy config .`,
`gitleaks detect --source .`, e a DoD do CONTRIBUTING.md
(`go test/vet ./...`, `govulncheck ./...`).

---

## Testes de aceite em nchat-dev (pós-deploy)

| #   | Critério                      | Como verificar                                                                                                                                           |
| --- | ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Filer do SeaweedFS ativo      | `kubectl -n nchat-dev exec sts/seaweedfs -- wget -qO- http://127.0.0.1:8888/` responde                                                                   |
| 2   | Migration 000005 íntegra      | as 3 consultas da §Migrations; Job reporta `[SKIP]`                                                                                                      |
| 3   | file-service inicia           | `kubectl rollout status`, sem CrashLoop                                                                                                                  |
| 4   | `/healthz` 200                | probe interna via API proxy                                                                                                                              |
| 5   | `/readyz` ready               | checks `postgres` **e** `object-storage` passando                                                                                                        |
| 6   | ClamAV `Ready`                | probe TCP/3310 com validação de `PONG`                                                                                                                   |
| 7   | Upload válido                 | `201`, `status=pending_scan`                                                                                                                             |
| 8   | Download antes do scan        | `403 file_not_scanned`                                                                                                                                   |
| 9   | Preview antes do scan         | `403` — mesmo gate                                                                                                                                       |
| 10  | HTTP Range antes do scan      | `403` — o gate cobre toda entrega de bytes derivados                                                                                                     |
| 11  | Worker processando            | log `worker=malware_scan`, `result=clean`                                                                                                                |
| 12  | Transição para `clean`        | ~1 poll (10 s) + duração do scan                                                                                                                         |
| 13  | Realtime                      | evento de status chega por WebSocket sem reload (via `VALKEY_URL`)                                                                                       |
| 14  | Preview após `clean`          | 200                                                                                                                                                      |
| 15  | EICAR → `rejected`            | download `403`, permanente, sem reprocessamento                                                                                                          |
| 16  | Anexo grande                  | arquivo próximo de 512 MiB é aceito e escaneado; comparar o tempo real com os 4.258 s do benchmark                                                       |
| 17  | ClamAV indisponível           | `kubectl scale deploy/clamav --replicas=0`: upload novo fica em `pending_scan`, log `result=retry`, **nada** vira `clean`; file-service continua `ready` |
| 18  | Restart do ClamAV             | `--replicas=1`: o backlog drena no próximo claim                                                                                                         |
| 19  | Restart do file-service       | linhas com lease expirado voltam a ficar due; nenhuma fica presa                                                                                         |
| 20  | Porta 3310 não exposta        | `kubectl get svc,ingress,ingressroute -n nchat-dev` sem referência a 3310; varredura externa não vê a porta                                              |
| 21  | Encryption at rest            | ler o objeto direto no Filer e confirmar que não é o plaintext                                                                                           |
| 22  | Sem regressão no upload-guard | upload > 536879104 B rejeitado pelo nginx enquanto transmite                                                                                             |
| 23  | Sem regressão no limite       | limite administrativo do workspace continua aplicado pelo file-service                                                                                   |
| 24  | Sem regressão no default-deny | ambas as default-deny presentes; um pod arbitrário não alcança o ClamAV                                                                                  |
| 25  | Drift convergido              | `nchat-allow-upload-guard-file-ingress` existe e bate com o manifest; as outras duas policies manuais não existem mais                                   |
| 26  | Quota respeitada              | `kubectl describe resourcequota -n nchat-dev` dentro dos limites, sem alteração de cota                                                                  |

### Matriz de veredito do cliente clamd (TM-01) — teste obrigatório

Teste de unidade contra um daemon falso, travando a propriedade "só `OK\0`
íntegro produz `clean`". A análise em §D mostra que o código atual satisfaz
todos os casos; o teste existe para que uma refatoração futura não os perca.

| Resposta / evento do daemon                                    | Resultado exigido                           |
| -------------------------------------------------------------- | ------------------------------------------- |
| `stream: OK\0`                                                 | `clean`                                     |
| `stream: Eicar-Signature FOUND\0`                              | `rejected`                                  |
| `stream: Heuristics.Limits.Exceeded FOUND\0`                   | `rejected` — **nunca** `clean`              |
| `... ERROR\0`                                                  | retry / `pending_scan`                      |
| `INSTREAM size limit exceeded\0`                               | retry / `pending_scan` (`too_large`)        |
| EOF antes do terminador                                        | retry / `pending_scan`                      |
| resposta **sem NUL** final                                     | retry / `pending_scan`                      |
| connection reset                                               | retry / `pending_scan`                      |
| timeout do deadline                                            | retry / `pending_scan`                      |
| cancelamento de contexto                                       | retry / `pending_scan`                      |
| resposta vazia                                                 | retry / `pending_scan`                      |
| resposta desconhecida                                          | retry / `pending_scan`                      |
| resposta terminal recebida **após** falha da fonte de conteúdo | nunca `clean` a partir de stream incompleto |

Se algum desses casos puder virar `clean` na implementação, **isso é uma
correção Go obrigatória** antes de habilitar uploads — não um ajuste opcional. A
análise atual indica que nenhum pode.

### Teste de limite interno (TM-01) — obrigatório e seguro

Objetivo: **provar que um limite interno do engine não resulta em `clean`.**

Restrições: **não** criar malware real nem archive bomb. Usar fixture sintética
inofensiva ou configuração controlada — por exemplo, uma cópia do `clamd.conf`
com um limite deliberadamente baixo aplicada a um ambiente de teste, ou um
arquivo composto legítimo que exceda um limite reduzido.

Basta comprovar **um** dos dois caminhos:

```
limite de conteúdo excedido → Heuristics.Limits.Exceeded FOUND → rejected
                    ou
limite / timeout            → erro ou protocolo não-clean    → pending_scan/retry
```

**Nunca aceitável:** `limite excedido → OK → clean`. Esse é o resultado que
reprovou a Rev 2, e é a condição de aprovação deste teste.

### Testes E2E do roteamento (TM-10) — obrigatórios

Todos executados contra o **host público**, porque a propriedade sob teste é de
roteamento L7. Enviar requisição direta ao ClusterIP de dentro do cluster **não**
prova nada sobre o roteamento público e não substitui nenhum destes.

| #   | Cenário                                          | Resultado exigido                                                      |
| --- | ------------------------------------------------ | ---------------------------------------------------------------------- |
| E1  | `POST` de anexo, multipart normal                | passa pelo upload-guard (confirmável no log/contador do guard)         |
| E2  | `POST` de anexo com `Transfer-Encoding: chunked` | passa pelo upload-guard                                                |
| E3  | `POST` de anexo com `Content-Length` válido      | passa pelo upload-guard                                                |
| E4  | payload acima de 536879104 bytes                 | recusado **pelo guard, durante o streaming** — não após bufferizar     |
| E5  | `GET` de download / preview / Range              | alcança o file-service normalmente, sem o hop extra                    |
| E6  | inventário de rotas públicas                     | **nenhuma** rota pública de `POST` de anexo aponta para `file-service` |

---

## Git/GitHub

**Nada executado.** Recomendações apenas.

### Issue

**#483 — `fix(infra): enable secure attachment uploads in nchat-dev`** já existe
e está aberta. Não criar issue nova. A branch atual
`fix/files-483-nchat-dev-attachment-infra` já segue
`fix/<epic>-<id>-<descricao>` do CONTRIBUTING.md.

### PR separado do segredo

`chore/files-483-nchat-dev-file-encryption-sealed-secret`.

### Divisão de commits sugerida

```text
1. fix(infra): enable the SeaweedFS filer in nchat-dev
2. feat(infra): add ClamAV workload for attachment scanning
3. fix(infra): wire file-service secrets and storage endpoint in nchat-dev
4. feat(infra): allow the attachment upload path through NetworkPolicies
5. feat(infra): enable attachment uploads in nchat-dev
6. ci(infra): assert the attachment stack invariants in manifest checks
7. docs(files): document the nchat-dev attachment deployment
```

No PR separado:

```text
chore(secrets): add the nchat-dev attachment encryption SealedSecret
```

Os commits 4 e 6 provavelmente precisam ser um só: o CI tem lista fechada de
policies de Egress (A11) e um commit intermediário ficaria vermelho. O commit 5
é deliberadamente o último dos funcionais — é o que "liga a chave", depois que
tudo de que ele depende já está no lugar. Isso é correção de
integração/infraestrutura, **não** reimplementação da RF-31.

Acréscimos derivados do Threat Model, a encaixar na mesma sequência:

```text
2a. security(files): make a clamd limit a verdict, not a silent pass   (TM-01/TM-06)
6a. ci(infra): prove upload POST is routed through the guard           (TM-10)
6b. test(files): lock the clamd verdict matrix and the limit semantics (TM-01)
```

O commit `2a` tem escopo `security` porque altera semântica de veredito, o que o
CONTRIBUTING.md contempla como tipo próprio. `6a` acompanha o commit 6 pelo mesmo
motivo dos commits 4 e 6: a asserção e o manifest que ela verifica têm de entrar
juntos.

---

## Decisões do segundo Design Review

**Veredito: APROVADO. Risco geral: Médio.** Todas as decisões abaixo foram
ratificadas como propostas, com uma única exceção — o item 1, ajustado pelo
reviewer. As verificações de implementação nomeadas nos itens 3 e 4 continuam
obrigatórias: elas confirmam premissas técnicas na hora de escrever os
manifests, não reabrem decisão arquitetural.

| #     | Decisão                                                                                                                                             | Contexto                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **1** | **Recursos do ClamAV — AJUSTADO:** `requests 500m / 1536Mi`, `limits` **`1250m`** ` / 3Gi` (proposta original: 1500m)                               | Medido: idle ~945 MiB, pico INSTREAM ~1 GiB, CPU pico ~177%, 512 MiB em 4.258 s. O reviewer aplicou a alavanca prevista na própria Rev 2: reduzir o `cpu.limit` do ClamAV em vez de aumentar a cota. Com 1250m o throttling pode alongar a fase CPU-bound, mas 4.258 s contra 300 s de orçamento absorve isso com folga larga. `limits.cpu` do namespace passa de 6250m para **7500m de 8000m**, preservando **500m** de folga. **A ResourceQuota não é alterada.** |
| **2** | **Forma de referência do ClamAV pelo overlay:** `resources: ../../base/services/clamav` (simples) _vs._ Kustomize _Component_ (semântica explícita) | Ambos suportados pelo kustomize v5.7.1 já fixado. O resultado renderizado é o mesmo; a diferença é de expressividade. Em qualquer das duas, `base/kustomization.yaml` **não** referencia o diretório.                                                                                                                                                                                                                                                               |
| **3** | **Probe do ClamAV:** `exec: ["clamdscan", "--ping", "1"]` como candidato primário                                                                   | Precisa ser **verificado na implementação**: que a 1.4 use `TCPSocket` na ausência de `LocalSocket` e retorne código ≠ 0 com o daemon parado. Ratificar também o fallback (`tcpSocket` para liveness + exec comprovado para readiness). Ratificar ainda que `/init-unprivileged` funciona com a config customizada, os emptyDirs e UID/GID 100/101 — se não funcionar, o entrypoint precisa ser reavaliado no mesmo Design Review.                                  |
| **4** | **`/run/clamav` não é montado**                                                                                                                     | Baseado na ausência de `LocalSocket` e `PidFile` no `clamd.conf` do NChat. Se a verificação de implementação provar necessidade, adicionar **com evidência registrada**.                                                                                                                                                                                                                                                                                            |
| **5** | **Reescrita dos gates de CI**                                                                                                                       | Nenhum gate é afrouxado: proibições cegas viram invariantes positivos mais estritos. Ratificar que a troca do `grep` de `SEAWEEDFS_*` por asserção de valor exato é aceitável.                                                                                                                                                                                                                                                                                      |
| **6** | **Convergência de `nchat-allow-upload-guard-file-ingress`**                                                                                         | Adoção por igualdade de nome via `kubectl apply`, com verificação de spec e `kubectl replace` nominal como contingência. Ratificar que o time aceita adotar um objeto criado fora do versionamento em vez de recriá-lo.                                                                                                                                                                                                                                             |
| **7** | **Modelo de um único `kubectl apply`**                                                                                                              | Mitigado por dois PRs + `maxUnavailable: 0` (o pod antigo sobrevive a um `/readyz` que falhe). Ratificar que isso substitui um deploy manual em etapas.                                                                                                                                                                                                                                                                                                             |

### Estado dos itens que a Rev 2 levou ao Threat Model

- **`MaxScanTime` × `FILE_MALWARE_SCAN_TIMEOUT_SECONDS` — RESOLVIDO como
  TM-01.** A Rev 2 registrou a suspeita; o Threat Model confirmou que ela é
  real, classificou como **Alta** e reprovou o plano. Deixou de ser item
  diferido: virou decisão de desenho nesta Rev 3 — `AlertExceedsMax yes`,
  `MaxScanTime 420000`, ordem de timeouts declarada, invariante de CI e teste
  obrigatório. **Esta issue passa a tocar `MaxScanTime`**, com justificativa.
- **Idade das assinaturas — mantido como decisão futura.** Com freshclam
  desligado, as assinaturas têm a idade da imagem fixada por digest. Atualizar =
  bumpar o digest de forma controlada. A política para staging/produção continua
  sendo decisão separada, não desta issue.
- **Cobertura do benchmark — mantido, e agora com mitigação.** O arquivo de
  zeros não exercita parsers de archive nem documentos complexos, então o pior
  caso real de CPU e memória permanece não medido. É a razão da margem no
  `limits.memory` (3Gi ≈ 3× o pico observado) e, agora, também do
  dimensionamento de `/tmp` pelo pior caso _da política_ (`StreamMaxLength` +
  `MaxScanSize`) em vez de pelo caso medido.

### Pontos que o novo Threat Model precisa ratificar

Derivados exclusivamente dos três findings; nada de escopo novo.

| #      | Decisão a ratificar                                                                  | Por quê                                                                                                                         |
| ------ | ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| **T1** | `MaxScanTime 420000` (420 s) como backstop                                           | 120 s de margem sobre os 300 s e estritamente acima do lease de 330 s. Ratificar o valor e a margem mínima que o CI vai exigir. |
| **T2** | `AlertExceedsMax yes` e a aceitação de falsos positivos `Heuristics.Limits.Exceeded` | Trade-off explícito: recusar arquivo legítimo é preferível a aprovar arquivo não inspecionado.                                  |
| **T3** | `/tmp` `sizeLimit: 2Gi` e `/var/lib/clamav` `sizeLimit: 1Gi`                         | Dimensionados pelo pior caso da política, não pelo benchmark. Ratificar os tetos.                                               |
| **T4** | `ephemeral-storage` `requests 512Mi` / `limits 4Gi`                                  | Acréscimo aos recursos já aprovados; sem impacto na ResourceQuota.                                                              |
| **T5** | Versionar os limites de parser com os valores **efetivos observados**                | Nenhum valor foi inventado; ratificar que congelar o efetivo é a política desejada.                                             |
| **T6** | Invariantes de roteamento sobre o manifest **renderizado**, incluindo prioridade     | É a única camada que pode garantir a propriedade do TM-10.                                                                      |

---

## Conclusão

**PLANO CORRIGIDO PELO THREAT MODEL — PRONTO PARA REVALIDAÇÃO**

O Design Review aprovou a Rev 2 com risco Médio; o Threat Model a reprovou com
risco **Alto** por três findings. Esta Rev 3 corrige o **desenho** dos três:

- **TM-01** — `clean` passa a significar scan completo. O limite de _tempo_ do
  engine fica atrás do deadline externo de 300 s, que é declarado a autoridade
  fail-closed (`MaxScanTime 420000` > lease 330 s > timeout 300 s); o limite de
  _conteúdo_ deixa de responder `OK` e passa a responder
  `Heuristics.Limits.Exceeded FOUND` (`AlertExceedsMax yes`). A análise de
  `clamd.go` mostra que o cliente Go **já** satisfaz a semântica exigida em
  todos os casos enumerados — nenhuma correção em Go é necessária, apenas testes
  que travem a propriedade;
- **TM-06** — todo limite relevante do ClamAV vira política versionada e
  verificada, nenhum default oculto; `/tmp` e `/var/lib/clamav` ganham
  `sizeLimit` finito dimensionado pelo pior caso da política; `ephemeral-storage`
  passa a ser explícito; o initContainer ganha validações de origem, destino,
  digest e ownership;
- **TM-10** — o plano deixa de tratar a passagem pelo upload-guard como
  propriedade de rede. Fica registrado que **NetworkPolicy não distingue método
  HTTP** e que a garantia é de roteamento L7, com invariantes de CI sobre o
  manifest renderizado (rota, backend, prioridade, middlewares, asserção
  negativa) e seis testes E2E pelo host público.

Nenhuma decisão aprovada no Design Review foi revertida: recursos permanecem
`500m / 1536Mi` e `1250m / 3Gi`, a ResourceQuota não é alterada, e R2 a R14
seguem fechados. Seis pontos (T1-T6) aguardam ratificação no novo Threat Model.

Nenhum arquivo de manifest ou de código foi modificado, nenhum recurso do
cluster foi tocado, nenhuma migration executada, nenhum Secret criado, nenhuma
chave gerada, nenhum commit feito.
