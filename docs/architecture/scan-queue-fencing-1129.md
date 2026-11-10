# Entrega C — fila antimalware com fencing (#1129)

Implementacao local sobre `9567adf7471fd427630df749bef40ddd2e39920a` (PR #1167),
branch `feature/files-1129-scan-queue-fencing`. Nenhuma ativacao operacional.
[Runbook](../runbooks/scan-queue-fencing-1129.md) e
[validacao/handoff](scan-queue-fencing-1129-validation.md).

## Diagnostico da base N

`MarkUploaded` finaliza o upload cifrado e grava `pending_scan` e agenda no
mesmo UPDATE. O worker interno chama `ProcessDue` por tick de 10s; batch=1,
scan timeout=300s, escrita desacoplada=10s, margem=30s. O claim usa
`FOR UPDATE SKIP LOCKED`, incrementa SMALLINT saturado em 32000 e empurra
`scan_next_attempt_at` por `lease*min(tentativa,8)`. Agenda e lease sao a mesma
coluna. Falha deixa pending ate esse prazo, chegando a 2640s no default.
Crash recupera sem janitor, mas o banco nao identifica qual tentativa decidiu.

Aprovacao legada aceita pending/clean; rejeicao aceita pending/clean/rejected.
Assim, A pode aprovar pending depois de B assumir ou rejeitar clean depois de
B concluir. Isso e uma falha de autoridade mesmo quando a direcao bloqueia
acesso. Evidence valida nao determina o dono da tentativa. A fixture reproduz
o comportamento legado antes de ativar C e prova sua recusa depois.

Leitura continua streaming: objeto cifrado, binding, decrypt autenticado,
inspecao de formato, INSTREAM completo, EOF/tamanho exatos, Guardian/Evidence
quando configurado. O resultado e persistido antes do publisher; este revalida
os destinatarios por workspace/canal/conversa. Download, Range e preview usam
o status persistido e autorizacao; C nao abre caminhos novos de conteudo.

PGX tem default 10 conexoes: 4 uploads + 4 de headroom + 2. O preview usa
advisory fence entre render e invalidacao. O ClamAV compartilhado declara
MaxThreads=4 e MaxScanTime=420s; isso nao mede capacidade nem autoriza 4 scans
por replica. Timeout configurado continua derivando lease=timeout+30s.

O CD `cd-develop.yml` chama `deploy-nchat-dev.yml` e `deploy.sh`, que aplica o
Job de migrations antes do rollout da aplicacao. A expansao precisa, portanto,
funcionar com N em execucao. Pelo overlay versionado, file-service e clamav
legado permanecem ativos; updater/Guardian/PVC candidatos B estao fora dos
overlays, suspensos/zero replicas. Estado real de pods, recursos, drift,
assinaturas e conexoes de nchat-dev **nao foi consultado**: nao houve autorizacao
operacional. Nao confundir YAML com observacao do ambiente.

## Contrato N+1

O PostgreSQL continua sendo a fila e a autoridade. A migration 000021 adiciona:

| Campo                               | Funcao                                           |
| ----------------------------------- | ------------------------------------------------ |
| `scan_claim_token uuid`             | Identidade nova por claim; UUID gerado no banco  |
| `scan_lease_until`                  | Prazo para tomar a decisao sob lock              |
| `scan_next_attempt_at`              | Elegibilidade, independente do lease             |
| `scan_attempts`                     | Diagnostico saturado, nunca fencing              |
| `scan_last_error`, `scan_failed_at` | Ultima falha operacional, categoria fechada      |
| `scan_queue_policy`                 | Preparacao/cutover aprovado e latch irreversivel |

`claim_scans(1, lease)` exige policy C ativa e retorna uma linha pronta para
execucao, sem prefetch. O UPDATE atomico troca UUID e lease e incrementa
contador sob SKIP LOCKED. Nenhuma conexao fica retida durante scan/storage.
Claims abandonados expiram; cada reclaim troca o token mesmo no contador
saturado. Retry limpa token/lease, tornando o resultado anterior incapaz de
escrever ate antes da proxima tentativa.

SEC-C-001 continua aberto como LOW: uma transacao que obteve autoridade dentro
do lease pode manter o lock e confirmar o commit depois do deadline absoluto.
O contrato exige validade na checagem apos row lock; nao e um prazo de commit.
Um teste cruza o deadline real do PostgreSQL antes do commit e comprova que
outro claim e pulado enquanto a autoridade esta travada. Uma checagem extra no
UPDATE so estreitaria a janela; nao impediria atraso posterior do commit.
statement_timeout e trigger deferred tambem nao equivalem a uma garantia exata
de instante de commit. Nao foi introduzida nova logica temporal/deadlock.

O token acompanha ScanJob → ScanApproval/ScanRejection → `persist_scan_v2`;
RetryScan tambem recebe o token. `lock_scan_attempt` bloqueia a linha e verifica
id, workspace, deleted_at, token nao nulo/igual e deadline usando o relogio PG.
Esse e o ponto de linearizacao. O lock continua ate commit/rollback: mesmo se
o tempo passar depois da verificacao, nenhum reclaimer pode adquirir outra
autoridade antes de a transacao terminar. Uma tentativa que chega depois do
prazo ja e recusada, mesmo sem sucessor. Sem timestamp local como autoridade.

Aprovacao/rejeicao so mudam pending. Replay do mesmo resultado no mesmo token
vigente retorna already_recorded, sem evento ou escrita; apos expiracao pode
retornar superseded, tambem sem efeito. Resultado contrario em estado terminal
nao muda nada. Rescan precisa colocar o arquivo em quarentena e obter token
novo. Exclusao serializa no mesmo row lock; resultado atrasado nao ressuscita
arquivo nem modifica Evidence/preview. Identidade aleatoria nao reutiliza
intencionalmente valores; a garantia assume a unicidade usual de UUID v4.

Rejeicao continua dentro do advisory fence do render, incluindo cleanup de
preview ready. Nao ha lock de longa duracao durante scan. Nao ha evento antes
de commit. ACK perdido nao repete evento: novo pedido e idempotente. Falha do
publisher nao muda o veredito duravel; clientes reconciliam por leitura. A
entrega do evento continua best-effort, sem nova outbox/exactly-once.

## Persistencia B e privilegios

A 000020 permanece byte a byte intacta. A nova migration copia sua implementacao
de aprovacao para `persist_proven_scan_internal`, privada. O nome/assinatura
legados permanecem como wrapper: so funciona antes do cutover. A API v2 checa
fencing antes de chamar a implementacao B. Seus triggers imediatos e diferidos,
checks de engine/geracao/tempos/frescor e restricoes de Evidence permanecem.
Com B ativa, v2 exige Evidence nova mesmo se a linha de um rescan ainda carrega
Evidence anterior fresca. B inativa permite scanner legado, sempre com C ativa
e token; nao habilita B nem escolhe idade maxima.

Funcoes definer usam owner de migrations e search_path controlado com pg_temp
por ultimo; catalogo de owner e qualificado em pg_catalog. Funcoes internas nao
sao executaveis por PUBLIC/nchat_app. Runtime tem apenas SELECT na policy e
EXECUTE nas APIs estreitas. O trigger e invoker: reconhece a identidade do owner
na chamada definer, sem GUC que o chamador possa forjar. Leitura travada da
policy ocorre em helper definer, porque SELECT FOR SHARE exige privilegio de
UPDATE que o runtime nao possui na policy. Regrants oficiais preservam tudo.

Apos cutover, triggers barram vereditos/claims/retries SQL antigos. Tambem
recusam sair de pending_scan por SQL direto (inclusive para failed), reabrir
estados terminais, trocar id/workspace ou restaurar deleted_at. So a finalizacao
de pending_upload para pending_scan/failed permanece direta. CancelDraft,
ExpireDrafts e remocao conservam status/identidade/claim e podem fazer soft-delete
e manter ou limpar a agenda; isso nao concede autoridade para um veredito.
INSERT nao pode inicializar token, lease ou metadados de tentativa artificiais.
O trigger UPDATE cobre tambem id, workspace_id e deleted_at. O wrapper B
sem token tambem falha, apesar de executar como owner. Essa combinacao evita
que o caminho privilegiado legado contorne o gate. Owner/superuser continua
sendo identidade administrativa confiavel; C nao autentica resultados de um
runtime malicioso nem resolve SEC-B-002.

## Retries, draining e capacidade

Backoff usa equal jitter deterministico pelo UUID e FNV32 (nao criptografico):
limite superior `min(20s*2^(min(attempt,10)-1),2640s)`, delay entre metade desse
limite e o limite, em milissegundos. Primeira falha espera 10–20s, crescendo ate
1320–2640s; piso acompanha o fallback de 10s, teto conserva o maximo anterior
com timeout default. Nao ha limite de tentativas que abandone quarentena.

Falhas de scanner/Guardian, storage, decrypt, timeout e policy conservam pending.
Retry e transacional e fenced. Falha de persistencia/ACK ambiguo nunca autoriza
novo clean; retry em linha ja concluida nao altera status. Se banco nao aceita
retry, o lease ainda garante recuperacao. Um cooldown compartilhado de 10s
limita tambem jobs novos durante indisponibilidade. Slots ja ativos podem
concluir: a rajada maxima adicional e limitada pelos slots. Nao e breaker global;
muitas replicas ainda exigem dimensionamento conjunto.

Cada consumidor sincronico toma um job por vez. `FILE_MALWARE_SCAN_CONCURRENCY`
default=1, intervalo valido 1–4, parsing estrito; headroom requerido para N slots:
`DBMaxConnections >= UploadMaxConcurrent+4+N-1`, alem das validacoes anteriores.
Maximo 4 e um teto de seguranca de configuracao, nao capacidade demonstrada.
Nenhum manifesto/config operacional foi ajustado para aumentar concorrencia.

Cada rodada drena ate 32 jobs ou 1s entre conclusoes, depois cede 100ms. Um job
pode ocupar seu timeout inteiro; o limite de drain nao interrompe trabalho no
meio. Fila vazia, cooldown ou erro de claim espera 10s, sem queries em loop.
Primeiro despertar tambem espera 10s. Preparacao deliberada (enabled=false,
activated=false) retorna NC006/ErrScanQueueStandby no claim: ProcessDue devolve
erro tipado, o consumidor espera sem WARN repetitivo e retoma apos ativacao sem
restart. NC004, policy ausente/incoerente e falhas de conexao continuam visiveis. Sem LISTEN/NOTIFY: o fallback e todo o
mecanismo de despertar; notificacao perdida nao pode perder trabalho. A cedencia
limita monopolizacao e nao segura conexao PG. Contagem do backlog e limitada a
uma consulta a cada 10s por processo, compartilhada pelos slots.

Cancelamento e verificado antes de claim e depois de scan; um scanner que
retorna clean apos cancelamento nao aprova. Consumidores propagam contexto e
Start aguarda todos com WaitGroup. Escritas desacopladas continuam limitadas a
10s. Shutdown do App conserva seu budget e recusa fechar pool sob worker ainda
ativo. Recuperacao apos morte abrupta continua pelo lease. Interfaces de I/O
precisam honrar contexto; nao se inventou preempcao de goroutine bloqueada em
implementacao que viola esse contrato.

## Ordem e plano SQL

Ordem e indice permanecem `scan_next_attempt_at NULLS FIRST, created_at` e
`idx_attachments_scan_pending`. Novos uploads normais recebem now(), portanto
nao passam a frente de um prazo anterior. NULL e recuperacao de agenda ausente;
um conjunto finito e drenado primeiro. Retry recebe prazo futuro e nao ocupa
a cabeca continuamente. Nao foi introduzida fila/quota por tenant: nao se promete
latencia ou divisao igual sob sobrecarga ilimitada. Muitos claims vivos na cabeca
podem aumentar linhas filtradas; medir antes de novo indice/fairness complexo.

EXPLAIN ANALYZE em 10.000 linhas sinteticas no PG16/17 usou Index Scan no indice
existente, LockRows e Limit, sem Sort. Tempos locais de uma captura: PG16 0,034ms,
PG17 0,031ms; isso verifica forma do plano, nao capacidade de producao. O predicado
de lease e filtro adicional. A fixture reproduzivel esta em `scripts/testing/scan-queue-plan.sql` e e executada
pelo runner somente em banco descartavel.

## Observabilidade e limites

Sem labels de identificadores. Prefixo `nchat_file_malware_scan_`:
queue_depth (pending total), eligible, leased (snapshots persistentes), active e
capacity (locais), claims_total, duration_seconds, wait_seconds e backoff_seconds{reason}. reason e a categoria fechada persistida no retry:
policy_blocked usa signature_policy; falha de banco usa database. O histograma
antes sem labels ganhou somente esse label de cardinalidade limitada nesta
remediacao; nao usar texto de erro como reason.
`wait` usa uploaded_at; fallback created_at para legados. Histogramas de duracao
incluem persistencia e publicacao. `scans_total{result}` preserva resultados
fechados e inclui superseded, already_recorded, policy_blocked e retry_write_failed.
Cancelamentos usam result retry/error_kind canceled. Logs existentes sao
categoricos, sem conteudo/filename/chaves/prova. active/capacity mostra saturacao;
claims_total por rate mostra admissao. Snapshots de fila repetidos por replica
nao devem ser somados (usar max); slots/claims locais podem ser agregados.

Nao foram adicionados dashboards, HPA, mensageria externa ou liberacao automatica.
Frescor, clock watermark e trust boundary da B nao foram relaxados.

## Conflito de formato e remediacao final

Quando format_policy_version avanca enquanto uma aprovacao aguarda o row lock,
a API retorna outcome=conflict em ambos os ramos (com/sem Evidence), mapeado
para ErrScanConflict. O ramo sem Evidence antes retornava zero linhas e gerava
pgx.ErrNoRows no cliente. Nenhum clean e persistido; nao houve relaxamento da
politica de formato. Resultado de dominio nao gera SQLSTATE de erro novo; NC003,
40001 e 40P01 mantem o mapeamento anterior. A reconciliacao dos pareceres e as
novas evidencias estao na secao de remediacao do documento de validacao.
