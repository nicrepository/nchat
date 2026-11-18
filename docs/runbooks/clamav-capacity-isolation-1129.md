# ClamAV #1129 — Entrega E: isolamento e capacidade

Estado: implementação candidata local; **não ativada, não homologada no cluster**.
Nenhuma alteração de produção, cutover B/C ou instalação de controller é autorizada
por este documento. Complementa [B](clamav-signatures-1129.md),
[C](scan-queue-fencing-1129.md) e [D](../observability/clamav-1129-delivery-d.md).

## Preflight e design review

Base local em 2026-11-18: `be68f7f79f3e93c19752fa46a44db09d1d05662e`, igual a
`origin/develop`, branch `feature/files-1129-clamav-capacity-isolation`.
Dois untracked preexistentes preservados. Sem merge/rebase pendente.
Issues relacionadas consultadas: #1128/#481/#483 fechadas; #948/#1100 abertas.
#1128 define formatos/autorizações, #948 rege coexistência e rollback e #1100
não autoriza mudar composer/UI nesta entrega.

Anteriormente `wireAttachments` iniciava API, scan, preview, cleanup e expiração
no mesmo processo. Não havia boot mode por responsabilidade nem limite global
entre réplicas; C fornece claim unitário, lease, UUID e fencing de persistência.
Migrations são Jobs externos; nenhum papel da aplicação as executa.

| Alternativa                           | Avaliação / decisão                                                                                                                                                                                                                      |
| ------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A: processo compartilhado com limites | Compatibilidade mantida em `combined`, mas não isola CPU/memória nem crash do renderer.                                                                                                                                                  |
| B: API, scan e preview separados      | Escolhida, mesma imagem e contratos; limites, identidade e rede distintos.                                                                                                                                                               |
| C: pools por custo                    | Adiada: faltam classificação e medições; não antecipar Entrega F.                                                                                                                                                                        |
| D: capacidade fixa configurável       | Escolhida, 1 réplica/slot inicial e limite global PostgreSQL=1.                                                                                                                                                                          |
| E: autoscaling por fila               | Não ativado: não há KEDA, adapter custom/external metrics ou pipeline Kubernetes validado no checkout; não há contexto kubectl local para confirmar controllers instalados. Prometheus Compose não prova sua disponibilidade no cluster. |

Não há nova plataforma, imagem, dependência Go, estado de domínio ou alteração
no CD global. O candidato não integra o overlay ativo; exige revisão e promoção
explícitas. A configuração ativa atual aponta `clamav:3310` bruto sem idade
aprovada, enquanto o cliente B exige Guardian e max-age positivo. Isso já impede
processamento efetivo nessa configuração. Não converter essa lacuna em fallback
para scanner sem evidência. SEC-B-001/002/007 continuam conforme a documentação B;
esta entrega não resolve confiança de recibos nem aprova o digest do engine.

## Papéis e boundaries

| FILE_BOOT_MODE       | Componentes                                   | Dependências / responsabilidade                                                                                         |
| -------------------- | --------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| ausente / `combined` | Comportamento legado                          | Compatibilidade de rollout; não usar junto a pools dedicados sem orçamento global.                                      |
| `api`                | HTTP, cleanup, drafts e URL safety existentes | JWT, PG, storage cifrado, conversor de áudio síncrono, integrações de URL; nenhum scan/preview de anexos em background. |
| `scan-worker`        | Somente consumidor AV                         | PG, keyring, storage, Guardian e publisher opcional; sem JWT, Cloudflare, conversor ou rotas públicas de anexos.        |
| `preview-worker`     | Somente consumidor preview                    | PG, keyring, storage e conversor; somente anexos elegíveis pelo contrato clean existente, sem AV/limpeza/URL consumers. |

Modos desconhecidos são recusados. Workers exigem uploads/scan obrigatório,
URL previews/safety desligados; configuração do conversor de preview é validada
localmente em `Config.Validate()` antes da abertura do pool PG, reutilizando as
regras do construtor existente, sem alterar defaults ou combined/API.
Scan-worker exige endpoint e recusa startup se o
scanner não puder ser construído. Credenciais JWT não são exigidas dos workers.
`combined` conserva tolerância anterior de API disponível com scanner ausente;
nenhum arquivo pendente torna-se acessível por essa tolerância.

Workers expõem apenas health, readiness, version e metrics internos. Nenhum
Service/Ingress público seleciona seus labels. SAs distintas não possuem RBAC e
não montam token. Secrets são selecionados por chave. O keyring e a role runtime
PG `nchat_app` continuam compartilhados porque os contratos criptográficos e as funções de
persistência atuais os exigem: **não há isolamento criptográfico nem autorização
SQL por papel**. Comprometimento de autoridade runtime continua risco residual
preexistente, particularmente SEC-B-002; não afirmar least privilege de tabelas.

Containers non-root, sem capabilities/privilege escalation, seccomp RuntimeDefault,
rootfs read-only, /tmp privado limitado. Policies por papel permitem DNS, PG,
storage, monitoramento; scan alcança somente Guardian e Valkey opcional além
das dependências comuns; preview alcança conversor, sem scanner/Valkey/Internet.
API conserva conversão de áudio existente. ClamAV mantém egress fechado e updater
isolado. O candidato usa a base versionada/atômica B, não acrescenta réplicas clamd.

## Orçamento e backpressure

Concorrência local `FILE_MALWARE_SCAN_CONCURRENCY`: 1 por padrão, máximo 4, com
reserva de pool validada existente. Preview mantém um consumidor por processo.
Candidato: uma réplica de cada papel, mínimo operacional 1, sem scale-to-zero,
sem HPA e sem teto de réplicas automático. Réplicas adicionais não são autorização
para elevar capacidade global; recursos e limite de conexões PG devem ser
reavaliados antes de qualquer mudança versionada.

Migration `000022_scan_global_capacity` instala política `max_scans=1`, intervalo
aceito 1..4, modificável somente pelo migrator. Claim e reserva são atômicos;
um lock curto serializa admissão entre todas as réplicas. O UPDATE da policy
renova sua versão MVCC: snapshots antigos em REPEATABLE READ falham com 40001,
em vez de admitir com contagem stale. Slots locais sem
capacidade global não reivindicam anexos e voltam ao polling C. O job e o objeto
continuam sob a autoridade do PG; nenhuma fila paralela é introduzida.

MaxThreads=4 é teto de threads do daemon, **não capacidade medida**. Mantidos
StreamMaxLength/MaxFileSize=512M, MaxScanSize=1024M, MaxRecursion=12,
MaxFiles=10000, AlertExceedsMax=yes e MaxScanTime=420s. Mantido timeout=300s,
lease=330s no candidato. Não se altera o orçamento do engine para elevar throughput.

Reserva conserva capacidade após retry/cancelamento/crash/remoção do anexo:
`reserved_until = scan_lease_until + 450s` (420s de engine + 30s de margem).
Com lease padrão, até 780s desde claim sem resultado terminal. Só um resultado
terminal fenced remove antecipadamente a reserva correspondente. Isso cobre
trabalho que possa permanecer no daemon depois que o cliente sumiu; recuperar
lease sozinho não prova que clamd parou. É uma escolha conservadora: mesmo erro
antes de INSTREAM retém slot até expirar. Não apagar reservas para acelerar fila.
Não aumentar MaxScanTime sem rever essa margem e os testes. A reserva pressupõe
que não existem consumidores externos e que o daemon respeita seu timeout; um
processo travado além desse limite exige contenção/restart operacional. Não é
prova de limite absoluto de execução do processo clamd.

Instalação sobre C já ativada impõe pausa conservadora de 4080s (lease máximo
3630s + 450s) antes de novos claims. Instalação em preparação não impõe pausa.
A expansão não ativa C. Durante pausa, fila cresce em quarentena e APIs de arquivos
já aprovados continuam funcionando. Planejar janela; não prometê-la transparente.

Recursos dos novos Pods são baseline conservadora, não medição: requests/limits
estão no candidato; pools dedicados aumentam conexões e reserva total do nó.
Limites de memória e ephemeral storage contêm blast radius, não provam que todo
arquivo até 512MiB completará. Medir OOM/throttling, I/O e formatos caros antes de
elevar max_scans; manter 1 até então. Não criar pools por extensão/MIME.

### Risco residual Low — starvation entre workspaces

`max_scans=1` é configuração inicial conservadora, não capacidade validada.
Não há garantia de fairness por workspace: uploads custosos que provoquem falhas
podem reter a única reserva por até 780s e atrasar outros workspaces. Admissão e
quotas existentes não eliminam essa interferência entre tenants. A retenção é
intencional para conter trabalho órfão no daemon; não reduzir TTL, apagar reservas
nem elevar capacidade automaticamente para contornar a espera.

Avaliar mitigação em evolução separada, após benchmark de capacidade e teste
multi-tenant com misturas de carga/falha e latência por workspace. Qualquer ajuste
de teto/agendamento exige capacidade medida e preservação de fencing/fail-closed.
Esta consolidação não implementa fairness, round-robin ou pools adicionais.

## Métricas e escala manual

Reutilizar D. Histogramas/contadores locais: somar taxas entre instâncias;
gauges de fila são snapshots globais: usar `max` entre snapshots frescos, nunca
somar. Slots configured/active são locais; somá-los mede consumidores, não a
capacidade global reservada nem a saúde do Guardian. API/preview exportam registro
sem consumidor AV: capacity=0 e snapshot não coletado; filtrar capacity>0 e
snapshot_success/freshness. Ausência de worker não equivale a fila vazia.

Consultar `files.scan_capacity_policy` e contar reservas com
`reserved_until > clock_timestamp()` em transação READ ONLY para conhecer teto
global e ocupação conservadora. Não exportar attachment_id/claim_token.
Métrica `eligible` de D é elegibilidade de agenda/chave/lease; não desconta
reservas conservadoras de E. Portanto não é sinal autossuficiente para autoscaler.
Backoff não deve causar escala; scans em voo e reservas ainda válidas já ocupam
capacidade. Demanda é fila autoritativa, não CPU baixa.

Para avaliar escala: snapshot PG fresco, policy C ativa, assinatura saudável,
backlog elegível sustentado, taxa de conclusões, retries/timeouts, oldest age,
CPU/RSS/throttling e I/O. Aumentar apenas um orçamento por experimento. Daemon
fora/assinaturas inválidas: não escalar; manter mínimo ativo e backoff/quarentena.
Backend de métricas fora: capacidade fixa permanece, nenhuma escala cega.
Sem autoscaler não há flapping automático; mudança manual exige estabilização e
comparação de janelas. Só introduzir autoscaling após coleta/discovery Kubernetes,
capacidade global observável, limites e fault injection validados.

## Shutdown e transições

SIGTERM cancela todos os loops imediatamente pelo hook httpserver existente,
antes dos 5s de propagação e drain HTTP. Shutdown espera consumidores antes de
fechar PG/publisher; usa orçamento compartilhado 45s. Grace de worker=60s conserva
margem do helper, não aguarda scan de 300s: I/O recebe cancelamento, lease e reserva
recuperam interrupções. Falha ao confirmar encerramento é reportada como `worker_shutdown_incomplete`,
com `pool_closed=false`, independentemente do papel; nunca clean.

| Transição / estado                                  | Resultado esperado / evidência necessária                                               |
| --------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Vazia → burst/backlog                               | Claim unitário por slot disponível, demais pendentes; scheduler/PG tests.               |
| Backlog → mais réplicas → saturação                 | Limite global serializa claims; reserves/PG multi-session test.                         |
| Scanner saudável → indisponível/assinatura inválida | Retry/backoff, nenhum download/preview; testes D e B existentes.                        |
| Scanner recuperado                                  | Nova tentativa após backoff e reserva; novo token e evidência.                          |
| Worker iniciando/Ready/ocupado → terminating        | Ready verifica dependências, não eficácia; cancelamento encerra aquisição.              |
| Terminado → lease expirado/substituído              | Sem aprovação; reserva evita sobreposição prematura no daemon.                          |
| Writer antigo → commit tardio                       | Fencing C recusa token vencido/substituído; E não muda persistência.                    |
| DB/storage lento ou indisponível                    | Falha fechada; sem claim confirmado não iniciar scan; leitura/decrypt falha não aprova. |
| Scale-down com backlog                              | Trabalho pode repetir após recuperação; veredito stale não pode substituir atual.       |
| N/N+1 → rollback                                    | Binários C usam mesma função com limite E; rollback não remove proteção do banco.       |

Falha após commit e antes de realtime: estado PG já autoritativo, cliente refaz
leitura; não transformar publicação perdida em novo veredito. Arquivos clean
preservam byte a byte original; preview é derivado cifrado independente.

## Threat model

Assets: anexos cifrados, chaves/credenciais, integridade dos vereditos/leases,
PostgreSQL, assinaturas, disponibilidade e autorização multi-tenant.
Boundaries: API/worker→PG, worker→storage/Guardian, updater→assinaturas,
monitoramento→metrics, Pod→SA. Não há autoscaler/controller novo com acesso K8s.

| Abuso / impacto                                  | Mitigação                                                      | Validação                                                 |
| ------------------------------------------------ | -------------------------------------------------------------- | --------------------------------------------------------- |
| Upload massivo / indisponibilidade               | Admissão/quotas existentes, limite global e recursos por papel | Testes C, PG capacidade, benchmark operacional pendente   |
| Arquivo compactado custoso / OOM                 | Limites engine preservados, pool/Pod preview separado          | Testes negativos existentes; corpus de custo pendente/F   |
| Scale-out excessivo / daemon saturado            | Reserva PG global, teto=1 inicial                              | Multi-session PG16/17                                     |
| Writer perdeu lease / aprovação inconsistente    | Token e checks C inalterados                                   | Integração fencing/expiração                              |
| Pod comprometido / movimento lateral             | Labels/policies específicos, sem JWT/Cloudflare nos workers    | Testes manifests; enforcement CNI pendente                |
| SA excessiva / acesso API cluster                | SAs distintas, sem RBAC/token                                  | Config tests e Trivy                                      |
| Daemon fora / backlog                            | Fail-closed, retry/cooldown, reserva conservadora              | Falhas scanner existentes, recuperação PG                 |
| Métrica ausente/manipulada / escala insegura     | Sem autoscaler; PG autoritativo                                | Config sem HPA/KEDA, coleta operacional pendente          |
| Rollout termina consumidores / perda de trabalho | Cancelamento, lease/reserva, sem scale-zero                    | App/worker tests e PG; rolling real pendente              |
| Base inválida em réplica / falsa aprovação       | Guardian/policy B; não escalar clamd                           | B/D locais; SEC-B bloqueia ativação                       |
| Volume/credencial compartilhados indevidamente   | /tmp privado, keyring só papéis necessários                    | Config tests; SQL/keyring por papel é limitação explícita |
| Logs/metrics com PII                             | Labels/categorias fechados D; sem IDs em métricas novas        | Suites existentes e inspeção do delta                     |

## Rollout e rollback futuros — exigem autorização

1. Revisões independentes e autorização operacional. Confirmar SEC-B resolvidos,
   digest aprovado da mesma imagem para os três papéis, estado B/C, relógios,
   CNI, quotas, pools PG e reserva de recursos no nó. Não promover candidato com
   placeholders nem modo sintético B para ambiente compartilhado.
2. Renderizar candidato conforme instruções junto aos manifests. Validar schemas,
   testes de policies, digest único, Secrets por chave, métricas e selectors.
3. Aplicar expansão 000022 pelo migrator oficial, após verificar jobs em voo.
   Se C ativa, observar a pausa admit_after; não limpar reservas nem ativar C/B.
4. Preparar SAs/policies. Parar consumidores combinados antes de confiar nos
   dedicados; confirmar término de processos antigos e fim de trabalho residual.
   Pode haver janela sem consumidor, durante a qual anexos ficam pendentes.
5. Inicializar API em `api`, scan/preview dedicados com 1 réplica/slot, mesma
   imagem, Guardian B aprovado e policy C já autorizada. C em preparação permanece
   standby NC006; não confundir com eficácia. Confirmar que não há combined ativo.
6. Validar health/dependências, scrape de cada Pod, soma slots locais, teto/reservas
   PG, assinaturas e conclusão real, não apenas readiness.
7. Corpus sintético benigno/EICAR: clean/download íntegro; rejected/download,
   Range e preview negados; scanner fora não libera; recuperar e conferir fila.
8. Exercitar burst, múltiplas réplicas, SIGTERM durante storage/INSTREAM/commit,
   falha PG/storage e rollback. Medir throughput, p50/p95/p99, fila e recursos.
   Sem corpus de clientes e sem alteração operacional implícita por este runbook.

Rollback funcional: manter 000022, B/C e reservas. Parar dedicados, esperar término,
restaurar manifest versionado anterior em combined **com binário compatível C/B**;
a função pública claim_scans é a mesma e conserva teto global inclusive em N.
Verificar evidência/Guardian e fila após voltar. Não remover E após C ativada:
down é recusado nesse caso. Em preparação nunca ativada, down só com todos os
consumidores parados, preservando B e a ordem oficial de migrations. Sem imagem
compatível, conter uploads conforme procedimento existente e manter quarentena.
Não fazer downgrade para writer pré-C, nem reset de políticas para obter throughput.

## Benchmark e troubleshooting

Comparar com D somente no mesmo hardware, limites, snapshot de assinaturas,
policy, corpus e número de slots. D mede eficácia em containers locais, não prova
capacidade desta topologia. Rodar nominal/burst/saturação, pequenos/grandes e
compactados, daemon fora/recuperação e 2+ workers. Registrar manifest SHA do corpus,
aquecimento e três janelas por cenário; throughput, p50/p95/p99, queue wait,
scan/upload-to-clean, CPU/RSS/throttling, retries/timeouts e backlog inicial/final.
Metas p95 1s/5s da issue permanecem propostas, não resultados E.

- Fila parada: verificar C standby, idade/Guardian B, snapshot fresco e admit_after.
- Eligible>0 e active=0: inspecionar reservas vivas; não são lease nem backoff D.
- Pod reinicia em startup: conferir modo, flags de URL desligadas, keyring,
  endpoint Guardian, idade aprovada e conversor no preview.
- Ready mas sem throughput: Ready não testa corpus/assinaturas nem ativação C;
  verificar métricas D e policies/Secret keys, sem imprimir segredos.
- Crescimento após falha: reserva até 780s é intencional; não apagar por latência.
- Aumento de réplicas sem ganho: limite global/daemon/storage/PG pode ser gargalo;
  não elevar threads ou reduzir inspeção por intuição.

Validação operacional, enforcement CNI, capacidade/SLO e rollback real permanecem
pendentes por ausência de contexto Kubernetes e autorização de rollout.
