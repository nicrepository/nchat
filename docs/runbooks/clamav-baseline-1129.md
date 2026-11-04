# Baseline ClamAV #1129 — coleta segura

Escopo: Entrega A, diagnóstico passivo de **nchat-dev**, execução única por janela.
[Relatório e matriz A–F](../architecture/clamav-baseline-1129.md).
Nenhum comando desta seção aplica manifests, reinicia pods, toma claims ou envia
corpus ao scanner. A coleta cria somente arquivos locais, com acesso restrito.
Não usar kubeconfig/credenciais de produção, `-A`, dumps de ambiente, Secrets,
logs brutos ou `EXPLAIN ANALYZE`. Falta de acesso é evidência pendente, nunca zero.

## Pré-requisitos e inventário passivo

Ferramentas existentes: kubectl, jq, psql; identidade com get/list somente nos
recursos necessários de nchat-dev, SELECT nas colunas usadas do banco e acesso
read-only ao Prometheus. Não conceder privilégios durante o diagnóstico.
Contexto e serviço libpq são escolhidos explicitamente pelo operador, após
confirmar que apontam para dev. Não imprimir kubeconfig, DSN ou `.pgpass`.

```bash
kubectl config get-contexts -o name
# Em bash, preencher apenas com contexto já autorizado de desenvolvimento:
DIAG_CONTEXT='<contexto-dev-autorizado>'
KDEV=(kubectl --context="$DIAG_CONTEXT" --namespace=nchat-dev --request-timeout=10s)
umask 077

"${KDEV[@]}" get deployment file-service clamav \
  -o jsonpath='{range .items[*]}{.metadata.name}{" replicas="}{.spec.replicas}{" ready="}{.status.readyReplicas}{" SA="}{.spec.template.spec.serviceAccountName}{" token="}{.spec.template.spec.automountServiceAccountToken}{"\n"}{range .spec.template.spec.initContainers[*]}{"init "}{.name}{" "}{.image}{" "}{.resources}{"\n"}{end}{range .spec.template.spec.containers[*]}{.name}{" "}{.image}{" "}{.resources}{"\n"}{end}{end}'

"${KDEV[@]}" get pods -l 'app.kubernetes.io/component in (file,clamav)' \
  -o jsonpath='{range .items[*]}{.metadata.labels.app\.kubernetes\.io/component}{" "}{.status.phase}{"\n"}{range .status.initContainerStatuses[*]}{"init "}{.name}{" "}{.imageID}{" ready="}{.ready}{" restarts="}{.restartCount}{" last="}{.lastState.terminated.reason}{"\n"}{end}{range .status.containerStatuses[*]}{.name}{" "}{.imageID}{" ready="}{.ready}{" restarts="}{.restartCount}{" last="}{.lastState.terminated.reason}{"\n"}{end}{end}'

"${KDEV[@]}" get service clamav file-service \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.type}{" "}{.spec.ports}{"\n"}{end}'
"${KDEV[@]}" get endpointslices -l 'kubernetes.io/service-name in (clamav,file-service)' \
  -o json | jq '[.items[] | {service: .metadata.labels["kubernetes.io/service-name"], ports: .ports, ready: ([.endpoints[]? | select(.conditions.ready == true)] | length), other: ([.endpoints[]? | select(.conditions.ready != true)] | length)}]'

# Somente contadores de conformidade: nenhum valor arbitrário de ConfigMap sai.
"${KDEV[@]}" get configmap nchat-config -o json | jq '{app_env_matches: (.data.APP_ENV == "nchat-dev"), uploads_enabled_matches: (.data.FILE_UPLOADS_ENABLED == "true"), scan_required_matches: (.data.FILE_MALWARE_SCAN_REQUIRED == "true"), timeout_300s_matches: (.data.FILE_MALWARE_SCAN_TIMEOUT_SECONDS == "300"), scanner_address_matches: (.data.FILE_MALWARE_SCANNER_ADDRESS == "clamav:3310"), metrics_enabled_matches: (.data.PROMETHEUS_METRICS_ENABLED == "true")}'

# Regras e selectors são configuração operacional, sem annotations/managedFields.
"${KDEV[@]}" get networkpolicies -o json | jq '[.items[] | {name: .metadata.name, spec: .spec}]'
"${KDEV[@]}" top pods -l 'app.kubernetes.io/component in (file,clamav)' --containers
```

Saída de `top` é consumo pontual de CPU/memória, **não** CPU throttling nem RSS.
Terminated reason cobre apenas o último estado; ausência de OOMKilled não prova
que nunca houve OOM. Deployment/ConfigMap são configuração declarada no cluster,
não ambiente efetivo de processo. Para drift, verificar overrides de `env`,
`envFrom`, args, referências e resourceVersion sem exportar valores de Secret.
Sem isso, declarar flags efetivas pendentes. Se o clamd não declarar SA, vale
default; confirmar automount=false e nenhuma credencial montada.

Verificar probes/volumes/securityContext dos dois Deployments por projeção:

```bash
"${KDEV[@]}" get deployment clamav file-service -o json | jq '[.items[] | {name: .metadata.name, strategy: .spec.strategy.type, pod_security: .spec.template.spec.securityContext, volumes: [.spec.template.spec.volumes[]? | {name, emptyDir, configMap: (.configMap.name // null)}], containers: [.spec.template.spec.containers[] | {name, securityContext, startupProbe, readinessProbe, livenessProbe, volumeMounts}]}]'
```

Config efetiva do clamd: confrontar o volume e mount com a ConfigMap referida,
sem dump da ConfigMap compartilhada do aplicativo. Registrar apenas os valores
de MaxThreads, StreamMaxLength, MaxFileSize, MaxScanSize, MaxFiles, MaxRecursion,
AlertExceedsMax e MaxScanTime. Com subPath, ConfigMap atualizada não comprova
arquivo recarregado no pod. Se não houver mecanismo aprovado de leitura do
arquivo montado, essa evidência permanece pendente.

## Engine e assinaturas: quatro verificações distintas

1. Service/endpoints/Ready só mostram descoberta e prontidão declarada.
2. PING/PONG e VERSION ativos demonstram resposta ao protocolo, não eficácia.
3. Base ativa deve ter versão/data/origem/integridade e idade validadas pela
   política operacional aprovada. Essa política ainda está pendente.
4. Detecção de corpus benigno/EICAR exige ensaio autorizado isolado, fora da
   coleta passiva. Não rodar testes de integração contra serviços compartilhados.

Reutilizar o diagnóstico ClamAV existente do Admin Health Center quando houver
permissão apropriada (runbook `task-admin-secure-integrations.md`): já fala
PING/VERSION com timeout. Não copiar credenciais para comandos/documentos nem
confundir o diagnóstico de uma configuração salva com o endereço efetivamente
usado pelo worker. Se esse caminho não estiver disponível, registrar VERSION
pendente; não abrir portas, criar pod temporário ou fazer port-forward como
atalho nesta coleta. A versão do pacote/imagem ou mtime de CVD não prova a base
carregada. O último update bem-sucedido e o reload precisam de evidência própria.

## PostgreSQL somente leitura, com orçamento

`scripts/diagnostics/clamav-baseline.sql` usa snapshot REPEATABLE READ READ ONLY,
timeout de statement 2 s, lock 250 ms, idle-in-transaction 5 s, sem paralelismo,
work_mem 4 MB. Não recebe identificadores, nomes, filtros SQL ou DSN por variável.
Produz JSONL agregado e termina com marcador `section=complete` antes de ROLLBACK.
É necessário **exit code zero e marcador final**; descarte saída parcial.

Antes da consulta, DBA confirma schema/índice de `migrations/files/000005` e
estima o plano da seleção limitada sem executar o claim:

```sql
BEGIN READ ONLY;
SET LOCAL statement_timeout = '2s';
SET LOCAL lock_timeout = '250ms';
SET LOCAL idle_in_transaction_session_timeout = '5s';
EXPLAIN (COSTS true)
SELECT scan_attempts, scan_next_attempt_at, created_at, uploaded_at
FROM files.attachments
WHERE status = 'pending_scan' AND deleted_at IS NULL
ORDER BY scan_next_attempt_at ASC NULLS FIRST, created_at ASC
LIMIT 10001;
ROLLBACK;
```

O preflight é uma dependência **humana**, não uma aprovação automática do SQL.
Inspecionar toda a subárvore abaixo do LIMIT: para a fila, esperar leitura por
`idx_attachments_scan_pending` (Index Scan ou Index Only Scan) sem Sort/Bitmap
sobre o backlog. A amostra física de `table_sample` pode usar Seq Scan sob LIMIT,
pois não filtra/ordena a tabela; isso não autoriza Seq Scan integral + Sort da fila.
Se o plano do SQL completo diferir desta seleção, o DBA deve avaliar também suas
CTEs com EXPLAIN **sem ANALYZE**, mantendo o mesmo orçamento read-only.

| Classificação      | Evidência / ação                                                                                                                                                               |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Eficiente estimado | Índice parcial esperado sob LIMIT, caminho sem operador que exija materializar todo o backlog; registrar versão, estatísticas disponíveis e plano antes de considerar a coleta |
| Ineficiente        | Sort/Bitmap Heap Scan/varredura integral prévia pode ler/ordenar muito mais que 10.001 registros; não prosseguir com a coleta e encaminhar ao DBA                              |
| Indeterminado      | Índice ausente/inválido, estatísticas desconhecidas, operadores ou custo sem validação suficiente; não prosseguir e registrar evidência pendente                               |

Um `Limit → Sort → Bitmap Heap Scan` pode retornar 10.001 linhas e ainda ler
20.000 antes de ordená-las. **Linhas retornadas não são trabalho realizado.**
Índice válido não obriga o planner a escolhê-lo. Estatísticas ausentes/defasadas,
distribuição, seletividade, versão e configuração podem mudar o plano; não há
garantia de um plano específico após ANALYZE.

EXPLAIN sem execução contém **estimativas**, não comprova custo efetivo sob carga.
LIMIT não limita rigidamente páginas físicas, tuplas mortas ou I/O; filtros sob
uma leitura por índice também podem descartar muitas linhas. O timeout reduz
o risco de execução longa, mas não impõe orçamento rígido de linhas físicas ou
I/O já consumido. Não declarar segurança operacional para qualquer volume.
Não alterar planner, configuração/índice nem executar ANALYZE, VACUUM ou CREATE
INDEX no dev durante a coleta; manutenção exige avaliação/autorização separada.
Não aumentar timeouts automaticamente após falha. ANALYZE e EXPLAIN ANALYZE dos
testes ficam exclusivamente no PostgreSQL descartável, nunca em nchat-dev.

Configurar previamente um serviço libpq `nchat_dev_baseline` em arquivo privado,
com endpoint/database dev e role somente leitura das colunas consultadas.
Credenciais ficam no mecanismo privado já utilizado pelo operador. Não usar
DATABASE_URL do runtime, `PGPASSWORD` em linha de comando, sudo ou superuser.
A role precisa USAGE em files e SELECT somente em **status, deleted_at,
scan_attempts, scan_next_attempt_at, created_at e uploaded_at**, além da leitura
normal de pg_catalog e USAGE da linguagem confiável plpgsql (padrão do PostgreSQL)
para a guarda anônima, sem SECURITY DEFINER. Não conceder SELECT na tabela inteira,
wrapped_dek ou kek_key_id. Não fazer GRANT/REVOKE durante o diagnóstico.

Antes da coleta, verificar privilégios **efetivos** na própria conexão de diagnóstico:

```sql
SELECT has_schema_privilege(current_user, 'files', 'USAGE') AS schema_usage,
       has_table_privilege(current_user, 'files.attachments', 'SELECT') AS broad_table_select,
       has_column_privilege(current_user, 'files.attachments', 'wrapped_dek', 'SELECT') AS wrapped_dek_select,
       has_column_privilege(current_user, 'files.attachments', 'kek_key_id', 'SELECT') AS kek_key_id_select;
```

Esperado: schema_usage=true e os três SELECTs acima=false. As funções verificam
permissões efetivas, incluindo grants amplos, PUBLIC e roles herdadas; um grant
restrito adicional não remove acesso anterior. Não usar superuser, owner da tabela
ou role que possa assumir identidade mais privilegiada. Se houver acesso amplo,
interromper e solicitar provisionamento separado ao responsável; a consulta acima
não retorna nenhum valor das colunas criptográficas.

```bash
# Coleta privada SR-001: executar da raiz do repositório, em bash.
(
  umask 077
  baseline_dir=''
  baseline_output=''
  cleanup_baseline() {
    if [ -n "$baseline_dir" ]; then
      rm -f -- "$baseline_output" "$baseline_dir/psql.stderr"
      rmdir -- "$baseline_dir"
    fi
  }
  trap cleanup_baseline EXIT
  trap 'exit 1' HUP INT TERM
  if ! baseline_dir="$(mktemp -d /tmp/nchat-clamav-baseline.XXXXXXXX)"; then
    printf 'Falha ao criar diretório privado da coleta.\n' >&2
    exit 1
  fi
  if ! baseline_output="$(mktemp "$baseline_dir/result.XXXXXXXX.jsonl")"; then
    printf 'Falha ao criar arquivo privado da coleta.\n' >&2
    exit 1
  fi
  if ! PGCONNECT_TIMEOUT=5 psql -X -qAt -v ON_ERROR_STOP=1 \
      'service=nchat_dev_baseline' -f scripts/diagnostics/clamav-baseline.sql \
      > "$baseline_output" 2> "$baseline_dir/psql.stderr"; then
    printf 'Coleta falhou; resultado parcial descartado.\n' >&2
    exit 1
  fi
  if ! python3 - "$baseline_output" <<'PY'
import json
import sys

try:
    sections = []
    last = {}
    with open(sys.argv[1], encoding="utf-8") as stream:
        for line in stream:
            last = json.loads(line)
            sections.append(last["section"])
    valid = sections == ["snapshot", "catalog", "table_sample", "queue_sample", "complete"]
    valid = valid and last == {"section": "complete", "read_only": "on"}
except (OSError, ValueError, KeyError, TypeError):
    valid = False
sys.exit(0 if valid else 1)
PY
  then
    printf 'Coleta incompleta ou inválida; resultado descartado.\n' >&2
    exit 1
  fi
  if ! rm -- "$baseline_dir/psql.stderr"; then
    printf 'Falha ao finalizar arquivo privado; coleta descartada.\n' >&2
    exit 1
  fi
  trap - EXIT
  printf 'baseline_output=%s\n' "$baseline_output"
)
```

mktemp cria diretório exclusivo 0700 e arquivo exclusivo 0600, fora do repositório
e sem reutilizar nomes previsíveis. Existência, sucesso do psql e integridade do
JSONL/marcador são verificações distintas. Falhas e sinais encerram com erro e
removem o resultado parcial; stderr não é publicado. Sucesso preserva somente o
JSONL no diretório privado e imprime seu caminho, nunca seu conteúdo. A validação
confirma estrutura/conclusão; não prova frescor nem autenticidade da evidência.

Após análise, excluir somente esse arquivo e seu diretório nominal. Manter pelo
menor prazo necessário segundo a política de evidências, sem anexar conteúdo
bruto a logs, issues, artifacts públicos ou Git. Unlink não garante apagamento
físico em SSD, snapshots ou backups; usar somente host com controles de acesso e
proteção de armazenamento aprovados. Se houver SIGKILL/queda do host, o cleanup
pode não executar: localizar apenas o diretório privado criado nesta coleta,
validar propriedade/permissões e excluir nominalmente; não usar curingas de /tmp.

Interpretação:

- `catalog.estimated_table_rows` vem de `pg_class.reltuples`: estimativa de
  estatísticas, nunca contagem exata de anexos. Valor negativo (por exemplo -1)
  indica estimativa indisponível; **não significa tabela vazia**. Pode estar
  defasado mesmo quando positivo.
- `catalog.indexes` lista somente os índices conhecidos pelo instrumento.
  Localizar `idx_attachments_scan_pending` pelo nome, verificar presença,
  `valid=true`, definição e predicado. A validade é necessária, mas não prova uso
  pelo planner nem substitui o preflight. Ausência exige interromper/registrar
  pendência, não criar ou reconstruir índice durante o diagnóstico.
- `table_sample` é prefixo físico limitado, **não amostra representativa**.
  Rejected/clean/pending são contagens dentro dessa amostra. Sem truncamento,
  representam a tabela no snapshot; com truncamento, não extrapolar.
- `queue_sample` é prefixo da fila viva na ordem de agenda. Truncado: números
  são parciais; idade máxima é apenas a máxima da amostra, não da fila global.
  A amostra de tabela e a de fila são diferentes e não devem ser somadas.
- `queue_sample.key_completeness_basis=validated_schema_constraint` identifica
  **inferência**, não leitura física das chaves. Para pending_scan, a constraint
  CHECK validada da migration 000002 garante as duas colunas preenchidas; nenhuma
  migration posterior inspecionada remove essa invariante. `has_keys` interno foi
  removido. `eligible` conta prazo nulo/vencido; `deferred_lease_or_backoff` conta
  prazo futuro; `retried_or_abandoned_due` mantém prazo vencido e tentativa >0.
  Esses contadores conservam o significado do claim somente sob essa invariante.
  `blocked_missing_keys=0` é uma conclusão da constraint validada, **não** ausência
  de medição apresentada silenciosamente como zero. Deferred reúne lease/backoff:
  o schema ainda não os distingue.
- Antes de queue_sample, a guarda confere OID da tabela qualificada, nome/tipo
  CHECK, convalidated, origem local, tipos text/bytea/text e definição canônica
  emitida pelo PostgreSQL com search_path=pg_catalog. Nenhum valor das chaves é
  acessado. A comparação estrita usa a forma decompilada pelo servidor, testada
  em PG16/17, sem parser genérico; versões/formas equivalentes não reconhecidas
  são recusadas conservadoramente e exigem revisão, nunca aprovação por nome
  apenas. Ausente, NOT VALID, predicado/tipo incompatível: SQLSTATE 55000, sem
  queue_sample nem complete. O AccessShareLock da amostra anterior impede DROP/
  ALTER concorrente desse contrato até o ROLLBACK. Schema antigo/ausente também
  interrompe a coleta. Isso confia na integridade do catálogo e na aplicação das
  constraints pelo PostgreSQL, não detecta corrupção do banco ou adulteração por
  administrador. Campos anteriores permanecem, com esta base de inferência nova.
- `retried_or_abandoned_due` é tentativa anterior agora elegível. Não demonstra
  abandono/crash nem autoriza requeue. Contador saturado subestima tentativas.
- Idades usam uploaded_at/created_at; NULL é indisponível, futuro é anomalia de
  relógio/dado. `oldest_created_age_seconds` ignora created_at futuro; fila vazia
  ou somente timestamps futuros retorna NULL, não zero. Para uploads, as faixas
  existentes são [0,60] s, (60,600] s, (600,3600] s e >3600 s; NULL/futuro têm
  contadores próprios. Nunca usar updated_at como veredito ou inferir latência.
- Categorias de falha não são persistidas por scan. `failure_code` não é fonte
  confiável da falha de AV; não agrupar texto livre nem exportar esse campo.

Se já existir coleta agregada validada do DBA, preferi-la e documentar orçamento,
predicados e janela; não executar leituras duplicadas. Não executar requeue,
UPDATE/DELETE, grants, migrations ou testes contra o banco operacional.

## Prometheus, profiling e carga posterior

Usar o acesso existente ao Prometheus/Grafana, sempre filtrando namespace
nchat-dev e containers file-service/clamav. Primeiro confirmar labels dos targets
reais; não presumir que as métricas de cAdvisor estejam instaladas. Medir uma
janela passiva de 15 min com início/fim UTC, scrape interval, resets e lacunas.
Não adicionar exporters, profiling endpoints ou RBAC nesta entrega.

| Medida                | Consulta/modelo (ajustar labels comprovados)                                                                                | Limite de interpretação                                                             |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Backlog               | `max(nchat_file_malware_scan_queue_depth{namespace="nchat-dev"})`                                                           | Gauge pré-passe pode estar stale; não somar réplicas                                |
| Resultados/tentativas | `sum by (result) (rate(nchat_file_malware_scans_total{namespace="nchat-dev"}[5m]))`                                         | Clean repetido não é throughput único; ausência de série não é zero                 |
| CPU                   | `sum by (container) (rate(container_cpu_usage_seconds_total{namespace="nchat-dev",container=~"file-service\|clamav"}[5m]))` | Uso em cores, não throttling                                                        |
| Throttling            | rate throttled_periods / rate periods, com denominador >0                                                                   | Fração de períodos, não tempo perdido; métricas podem faltar                        |
| RSS / scratch         | `container_memory_rss`; container_fs_usage_bytes; volume stats quando disponíveis                                           | RSS ≠ working set; emptyDir pode não ter volume stats e exige evidência alternativa |

Não exportar labels de pod/arquivo/tenant para relatórios compartilhados.
Buckets de AV/tempos por etapa ainda não existem: não produzir percentis usando
histogramas HTTP, preview ou inspeção estrutural como substitutos. Para custo
de fila, medir claim/contagem em ambiente isolado com EXPLAIN/BUFFERS autorizado;
para storage/decrypt, perfil CPU/I/O do processo sintético; para rede/clamd,
bytes/conexões/tempo, sem payload capture. Separar concorrência de réplicas,
MaxThreads e slots reais, bem como CPU throttling, read amplification e DB I/O.

Plano de ensaio **não autorizado no serviço compartilhado e não executado**:

| Cenário                      | Dataset/carga fixa inicial                                                                                | Evidência exigida                                                                                           |
| ---------------------------- | --------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| Vazio e pequenos sequenciais | 30 benignos de 64 KiB, concorrência 1                                                                     | Espera por tick, p50/p95; p99 com 30 é instável, ampliar amostra e declarar N                               |
| Rajada e médios              | 100 benignos, 1/10 MiB, concorrência 1/2/4                                                                | Backlog, throughput único, erros, recursos e duração total                                                  |
| Grandes e formatos           | 50/250/512 MiB somente onde política aceitar; formatos sintéticos aprovados e compactados limitados       | Limites, duração, RSS, CPU, I/O; separar famílias e tamanhos                                                |
| Lento/offline/backlog        | Daemon de teste com atraso/falha controlada, backlog conhecido                                            | Quarentena, backoff, consultas, tempo de recuperação sem retry storm                                        |
| Workers/recovery             | 1/2 réplicas, crash e lease expirado apenas em fixtures isoladas                                          | Jobs recuperados, ausência de veredito tardio inconsistente, shutdown                                       |
| Eficácia e entrega           | Benigno + EICAR oficial sintético no fluxo aceito; fault injection de protocolo/decrypt/storage/DB/evento | Clean/rejected corretos, download/Range/preview/derivados bloqueados, membership atual, SHA-256 do original |

Benchmark puro de scan usa mesma sequência de bytes no scanner de teste;
benchmark upload→download inclui autenticação, formato, cifragem, storage, fila,
veredito e GET autorizado/refetch. Não misturar as duas distribuições. Registrar
hash/seed do corpus sintético, N, bytes/formato, warm-up, concorrência, janela,
versões/digests/assinaturas, hardware/nodes/limites, réplicas, scrape interval e
erros/timeouts. Não excluir erros para melhorar percentis; reportá-los à parte.
Comparar antes/depois com o mesmo dataset/hardware/parâmetros. Sem timestamps
confiáveis por etapa, declarar intervalo não medido. Testes reais existentes:
`services/file-service/internal/service/enterprise_integration_test.go`; exigem
dependências descartáveis, podem mutar seu banco e não são coleta passiva.

## Teste do instrumento e rollback

```bash
# Local e MUTANTE somente em container descartável criado pelo próprio teste.
# Sem rede, sem porta publicada, sem bind mount, sem credenciais do host.
# Requer Docker e esta imagem já disponível; não faz pull automático.
docker image inspect postgres@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f >/dev/null
python3 scripts/diagnostics/test_clamav_baseline.py -v
# Opcional: mesma suite com PostgreSQL 16, usado pela stack do projeto.
# Requer a imagem 16 já disponível; nenhuma imagem é baixada automaticamente.
CLAMAV_BASELINE_TEST_POSTGRES=16 python3 scripts/diagnostics/test_clamav_baseline.py -v
git diff --check
make format-check
```

Os testes cobrem fila vazia, todas as faixas e fronteiras temporais, particionamento
dos estados, contador saturado, truncamento em
10.001/fronteira exata de 10.000, schema ausente, SQLSTATE 42501 por SELECT
insuficiente, timeout e escrita recusada em READ ONLY mesmo como superuser.
Contadores/idades ausentes não viram zero. Testes de fronteira substituem apenas
`now()` por um timestamp PostgreSQL fixo; a idade criada é comparada ao relógio
retornado no snapshot, com tolerância de 1 ms para representação JSON.

A fixture reduzida reproduz das migrations 000001/000002/000005 os CHECKs de
status, tentativas não negativas, tamanho do kek_key_id e vínculo de chaves.
Jobs normais/de volume possuem bytes e identificador de chave **sintéticos**,
sem criptografia real. Cópias `anomalous.attachments` testam constraint ausente,
NOT VALID, tipo/predicado incorreto e tipos de coluna incompatíveis; sem CHECK,
as quatro combinações de nulidade existem, mas o diagnóstico é interrompido,
sem publicar elegibilidade ou zero falso. As cópias são removidas no cleanup.
Nenhuma constraint operacional é alterada. A suite não valida a
cadeia completa de migrations nem o scanner.

Após inserir 20.000 jobs válidos, o teste executa ANALYZE na fixture e percorre
recursivamente a CTE bounded de cada EXPLAIN ANALYZE. Exige limite efetivo e
leitura de 10.001 linhas, incluindo Actual Loops e Rows Removed by Filter/Index
Recheck; para queue_sample exige o índice parcial correto. Não impõe a mesma
contagem a todos os operadores. Sort/Bitmap são rejeitados; caminho desconhecido
ou dados de execução insuficientes são indeterminados, nunca eficientes. Isso
é uma verificação específica da fixture, não um estimador genérico de custo:
não mede páginas físicas, e contagens por loop do EXPLAIN podem ser arredondadas.
Fixtures de plano negativas reproduzem Limit=10.001 sobre leitura de 20.000,
além de loops/filtros excessivos, para detectar a regressão do próprio teste.
ANALYZE não força uma escolha: se o planner deixar de produzir caminho limitado,
o teste falha para investigação, sem desligar opções do planner para aprová-lo.

O relatório roda como role com SELECT nas seis colunas operacionais, sem acesso a
wrapped_dek, kek_key_id, nome ou failure_code; ambas as leituras criptográficas
são negadas com 42501 e verificadas via has_column_privilege. Isso também detecta
regressão que reintroduza leitura das chaves no SQL completo. A role
incompleta alcança catalog/table_sample e falha especificamente ao acessar
created_at em queue_sample, sem marcador complete. O container é removido inclusive em
falha de setup via cleanup; interrupção SIGKILL pode exigir remover somente o
container local identificado pelo prefixo `nchat-clamav-baseline-test-`.

Os testes SR-001 extraem e executam o próprio bloco bash deste runbook. mktemp
real é redirecionado para diretório privado do teste; psql é um double local,
sem conexão. Verificam 0600/0700, nomes exclusivos, arquivo legado preservado,
falhas de ambos os mktemp, psql com erro mesmo emitindo marcador, JSON inválido,
resultado parcial e marcador isolado. Nenhum conteúdo parcial chega ao stdout/
stderr compartilhado; falhas não preservam resultado. Não há corrida contra
arquivos ou contas de outros usuários.

Rollback de A: parar coleta, descartar evidência incompleta, remover os arquivos
locais restritos conforme política de retenção; nenhuma ação em banco/cluster.
Não há migração, configuração ou funcionalidade de produção para reverter.
