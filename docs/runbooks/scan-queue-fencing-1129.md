# Entrega C — preparacao, cutover e recuperacao (nao executados)

Procedimento futuro sujeito a autorizacao operacional especifica. Esta entrega
nao autoriza nchat-dev/nchat-prod, mudanca de manifests, replicas ou configuracao.
[Arquitetura](../architecture/scan-queue-fencing-1129.md) e
[evidencias locais](../architecture/scan-queue-fencing-1129-validation.md).

## Matriz de compatibilidade

N = baseline #1167; N+1 = C; B = policy de assinaturas da 000020.

| Banco / worker                      | C                  | B desligada                                              | B ligada                                              |
| ----------------------------------- | ------------------ | -------------------------------------------------------- | ----------------------------------------------------- |
| N / N                               | inexistente        | Comportamento anterior; sem fencing                      | Evidence B obrigatoria; ainda sem fencing             |
| N / N+1                             | inexistente        | Incompativel: API SQL ausente, fail-closed               | Igual; aplicar expansao primeiro                      |
| N+1 / N                             | off, preparacao    | Legado continua sem interrupcao pela expansao            | Funcao B legada preservada com Evidence               |
| N+1 / N+1                           | off, preparacao    | Novo worker em standby NC006; N deve continuar atendendo | Novo em espera; nao faz fallback                      |
| N+1 / N e N+1                       | off                | N atende; N+1 espera; garantia C ainda nao ativa         | Mesmo arranjo, B continua obrigatoria para N          |
| N+1 / N+1                           | on, cutover        | Token obrigatorio com scanner legado completo            | Token + Evidence B obrigatorios                       |
| N+1 / N e N+1                       | on                 | N recusado; N+1 atende                                   | N recusado inclusive API privilegiada B               |
| N+1 / rollback para N               | on                 | Scans de N param fail-closed; operacionalmente proibido  | Mesmo bloqueio; sem bypass B                          |
| Rollback DB 000021 antes de cutover | off, nunca ativada | Down permitido com novos workers parados                 | B continua intacta                                    |
| Rollback DB depois de cutover       | on/activated       | Down recusado                                            | Down recusado; remover B tambem nao e rollback valido |

Mistura depois do cutover conserva seguranca porque N e recusado, mas perde
capacidade se N ainda for necessario. **Barreira operacional:** manter N durante
preparacao; drenar/parar N em todos os slots/replicas antes de ativar e confiar
somente em N+1 depois. Troca de selector nao muda esse contrato de banco.

## Ordem de rollout futuro

1. Revisoes independentes, gates e aprovacao da operacao. Confirmar capacidade,
   clocks, pools, replicas, scanner compartilhado e backpressure. Nao aumentar
   default=1 sem benchmark. B tem barreiras proprias, inclusive SEC-B-002; C nao
   autoriza sua ativacao nem altera max_age_seconds.
2. Aplicar somente expansao 000021 e regrants oficiais como migrator. O CD de dev
   aplica migrations automaticamente antes do rollout: nunca incluir enabled=true
   na migration ou tratar merge como aprovacao de cutover.
3. Verificar policy C enabled=false/activated=false; testar N em corpus sintetico
   e N+1 em espera. A garantia de fencing ainda nao existe nesse periodo.
4. Preparar N+1 com FILE_MALWARE_SCAN_REQUIRED=true, timeout existente e concorrencia
   1. Se todos os N forem substituidos antes de ativar, a fila fica em quarentena;
      planejar essa janela explicitamente. Nao habilitar candidatos B.
5. Parar admissao de scans N, cancelar/drenar tentativas, aguardar fim dos processos
   antigos em todos os slots e conferir ausencia de claims legados em andamento.
   Nao liberar pelo simples timeout local sem verificar os writers.
6. Apenas com aprovacao concreta, migrator altera a unica linha da policy em
   transacao, com enabled=true e approval_reference da mudanca. O trigger marca
   activated=true; a decisao e irreversivel pelo rollback comum. Row lock da policy
   serializa o corte com operacoes antigas. Nenhum comando de ativacao foi executado.
7. Observar claims com UUID novo, lease independente e recusas NC004/NC005; testar
   benigno/sintetico negativo, eventos e download/Range/preview sob policy B atual.
   Falha nao permite reduzir controles ou FILE_MALWARE_SCAN_REQUIRED=false.

## Rollback e incidentes

Antes do cutover: parar N+1 e voltar N; se necessario, down 000021 exige policy
nunca ativada. Sem novos claims C, nao existe autoridade C a perder. B e sua
idade/policy continuam independentes. Conservar backup e ordem oficial das migrations.

Depois do cutover: manter DB e C ativados. Voltar apenas para binario que implemente
v2. Se so houver N disponivel, conter admissao e manter pending ate recuperar N+1;
N sera bloqueado, nao e rollback funcional da fila. Down e desativacao da policy
sao recusados inclusive para migrator; uma remocao futura exigiria entrega
contract-phase revisada com protecao substituta, nao bypass do latch.

Scanner/Guardian fora: pending, backoff por job e cooldown local. Recuperacao
normal dispensa reset massivo. DB fora: se retry falhar, recuperar por lease;
ACK ambiguo: consultar estado, nunca reemitir evento por suposicao. Expiracao
segura recusa o resultado tardio e exige nova inspecao. Publisher fora: status ja
duravel, UI reconcilia por leitura. Nao criar aprovacao manual.

NC006/ErrScanQueueStandby: preparacao deliberada com enabled=false e
activated=false. N+1 espera o poll sem WARN repetitivo, conservando o erro tipado;
quando a policy for ativada, retoma sem restart.

NC004: policy C ausente/incoerente ou writer legado bloqueado; conferir ordem de
rollout e role sem desativar o contrato. Conexao recusada nao e standby. NC005/superseded: token vencido,
substituido, retry liberado ou attachment removido; nao repetir o veredito com
token novo. Retry write failed: verificar PG; lease recupera. Muitas replicas
contra daemon fora ainda podem gerar N falhas por janela: nao escalar durante
incidente. Saturacao active/capacity e fila crescente exigem medir capacidade.

Rescan futuro e operacao administrativa autorizada: primeiro retirar o arquivo
de clean para pending_scan e invalidar lease/claim em transacao do migrator,
preservando a Evidence forense; depois o worker precisa obter UUID novo. Nao
reutilizar token nem Evidence como substituto de nova inspecao. Rejeitado nao e
reaberto por callback/worker antigo. Rotina administrativa automatizada fica
fora do escopo; nao ha endpoint de rescan nesta entrega.

## Validacao local e homologacao futura

- `bash scripts/testing/scan-queue-postgres.sh`: cria/remove somente containers
  proprios, network=none, PG16/17 pinados; aplica migrations com migrator e testa
  com nchat_app nao-superuser. Nao aceita DSN do host. SCAN_QUEUE_COVERAGE_DIR
  opcional guarda perfis para o gate oficial. Nunca apontar testes antigos para
  banco compartilhado; varias suites recriam schemas/bancos.
- `go test -race ./services/file-service/internal/...` e gate oficial completo
  `bash scripts/ci/go-test.sh -race`.
- `go test ./services/file-service/internal/worker -run '^$' -bench BenchmarkScanScheduler -benchmem -count 3`:
  overhead do scheduler, nao capacidade ClamAV.
- Gate oficial de cobertura precisa das fixtures PG e do perfil ClamAV real,
  alem dos dois perfis C. Sem eles, skips nao sao evidencias de falha de codigo.

Homologacao posterior: dataset exclusivamente sintetico, com manifest SHA-256,
faixas de tamanho/formato, mistura de pequenos/grandes e negativos autorizados.
Executar baseline e C em checkouts isolados no mesmo hardware, scanner, snapshot
de assinaturas, DB/pool, storage, quotas e numero de replicas, com concorrencia 1.
Repetir aquecimento e ao menos tres janelas fixas, registrando jobs/min,
finalizado→claim e finalizado→veredito p50/p95/p99, backlog inicial/final,
CPU/RSS/OOM, conexoes PG ativas/maximas, slots maximos, retries, superseded e
fencing. Depois variar um limite por vez. Injetar scanner offline/lento, DB fora,
shutdown/crash, exclusao e ACK perdido usando barreiras; nao medir throughput
com testes de erro como se fossem scans bem-sucedidos. Nao usar anexos reais nem
produzir numeros de homologacao a partir de ns/op do scheduler.

## Limite temporal e categorias depois da remediacao

SEC-C-001 permanece LOW aberto. O lease deve estar valido na aquisicao da
autoridade sob row lock. A transacao pode conservar esse lock e confirmar depois
do deadline; ninguem pode obter claim concorrente nesse intervalo. Os timeouts
existentes limitam o caminho normal da aplicacao, mas nao provam um prazo absoluto
de commit no PostgreSQL. Nao tratar statement_timeout ou trigger deferred como
uma garantia dessa natureza.

policy_blocked e reprogramado como signature_policy, enquanto falha de banco usa
database, inclusive no label reason do histograma de backoff. Conflito de versao
de formato e ErrScanConflict/outcome=conflict, nao pgx.ErrNoRows ou indisponibilidade.
Nunca atualizar versao de formato artificialmente para forcar aprovacao.

Os casos adicionais do runner verificam cancelamento/expiracao de drafts com
agenda limpa e status preservado, exclusao por workspace, recusa de ressurreicao,
SET ROLE/SESSION AUTHORIZATION, GUC forjada, funcoes internas/PUBLIC e dono real.
A aprovacao desses casos nao substitui nova reavaliacao independente direcionada.
