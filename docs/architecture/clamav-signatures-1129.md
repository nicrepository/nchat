# ClamAV #1129 — Entrega B: gerações e autorização de scan

Estado: **IMPLEMENTED / TESTED localmente; PENDING OPERATIONAL VALIDATION;
BLOCKED BY POLICY DECISION e BLOCKED BY INFRASTRUCTURE**. Não é entrega pronta
para implantação. Não encerra a #1129. Nenhum deploy, commit, push ou PR.
A [Entrega A](clamav-baseline-1129.md) continua com baseline operacional pendente.
[Operação futura e rollback](../runbooks/clamav-signatures-1129.md).
[Remediação consolidada, reconciliação e bloqueio de confiança SEC-B-002](clamav-signatures-1129-remediation.md)
complementam este design; descrevem a revisão local mais recente.

## Escopo e diagnóstico confirmado

Base/HEAD inicial: `1e7c91ec35ce638696fe9e68809771929153a679`, igual a
origin/develop e merge-base; branch `security/files-1129-clamav-signatures`,
working tree inicialmente limpa. #1166 presente. Prompt mestre v4 lido de
`/home/alvaroneto/Documents/nchat/PROMPT_MESTRE_NCHAT_v4.md`.

O overlay ativo nchat-dev-server continua usando Deployment/clamav, Recreate,
uma réplica, imagem oficial `clamav/clamav:1.4` fixada em
`sha256:d97f5ef70fbe9f1f8e5490278082067f16f5824eca2eb4176f4df3748a45a4c2`.
Inspeção **local da imagem**, sem rede, identificou engine 1.4.6, daily 28085,
CVD publicado em 2026-08-07. Isso não mede o ambiente compartilhado.

O init seed copia CVD/CLD/CUD da imagem para emptyDir. Reinício do container
preserva emptyDir; recriação do pod o perde e repete a cópia. Não há atualização
automática, política de idade ou prova de geração carregada no caminho legado.
O mount original de assinaturas no deployment não declara readOnly; rootfs é
read-only. Esta observação do YAML não altera retroativamente o relatório A.
Sem SelfCheck explícito, clamd herda a verificação periódica; RELOAD existe no
protocolo, mas não há publicador/reloader versionado. PING nos probes confirma
resposta, não frescor, autenticidade, compatibilidade ou eficácia.

UID 100/GID 101, capabilities removidas, seccomp RuntimeDefault, sem token e sem
service links. ClusterIP 3310 recebe somente componente file. Default-deny
egress e ausência do componente clamav na permissão DNS impedem saída, desde
que o CNI aplique essas policies. Nenhum acesso ao cluster foi feito.
Métricas atuais do file-service contam resultados e backlog, não assinaturas.

O caminho existente autentica o conteúdo cifrado, revalida formato, exige EOF e
quantidade exata de bytes, envia INSTREAM completo e persiste somente OK exato.
Erro de protocolo, storage, decrypt, timeout e indisponibilidade deixam pendente.
A deficiência desta entrega é a falta de proveniência/frescor na aprovação.
Claims e ownership não foram alterados: o risco conhecido permanece na Entrega C.

## Design Review

| Modelo                                   | Consistência / disponibilidade                                                                                                   | Operação e custo                                                                                    | Decisão                                                |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| A: updater + PVC mutável e staging       | Troca dos arquivos não comprova reload; concurrent reload pode duplicar memória; RWO exige co-scheduling, RWX exige backend real | Menos cópias, mais coordenação de reload e sessões                                                  | Não escolhido                                          |
| B: gerações + snapshot privado + restart | Conjunto imutável por processo/sessão; indisponibilidade curta durante carga; scans em voo drenados; geração anterior retida     | Cache + ativa + anterior + staging e eventual snapshot em ativação; RWX entre pods/nós              | **Escolhido**                                          |
| C: imagem de assinaturas via pipeline    | Boa identidade/rollback por digest; ativação ainda exige prova e gate na persistência                                            | Novos build/promote/GitOps frequentes e armazenamento de registry; independente da imagem do engine | Viável, mas mais integração nova para este repositório |

A implementação B tem um publicador, um guardião por daemon e um gate no banco.
Não há reload online nem sobreposição de dois engines no consumidor. Mantém
Recreate, MaxThreads=4 e **300s < 330s < 420s**, sem tuning de fila.

Os candidatos de Kubernetes ficam em `infra/k8s/candidates/clamav-signatures/`,
fora de todo overlay ativo. O renderer exige digest, StorageClass, mirror/IP,
idade e referência de aprovação; sempre gera CronJob suspenso e zero réplicas.
Render com valores fictícios é teste estático, nunca aprovação desses valores.

## Cadeia implementada

1. CronJob isolado executa FreshClam no cache persistente `download/`, sob flock.
   O lock não é removido; Forbid complementa, sem substituir o lock de filesystem.
2. Mirror privado HTTPS fornece **somente CVDs oficiais assinados**. CLI não
   aceita origem oficial CDN para este modelo de downloads integrais. TLS é
   validado; CA privada pode ser montada e indicada por CURL_CA_BUNDLE, sem
   desabilitar validação. Não há proxy herdado, scripts de hook ou curl improvisado.
3. O conjunto permitido é main.cvd, daily.cvd, bytecode.cvd. CLD é recusado.
   FreshClam normal suporta CDIFF e produz CLD incremental; PrivateMirror muda a
   descoberta, tenta CLD e depois CVD, sem DNS TXT upstream. Exigir CVD-only no
   mirror evita inventar autenticidade independente de um CLD reconstruído.
   É um trade-off explícito de banda **dentro da rede privada**, não autorização
   para downloads integrais repetidos no CDN. Rate limiting/cooldown permanecem
   no cache e uma falha não altera a geração ativa.
4. Cópia limitada e allowlist em `.candidate-*`; sigtool --info valida CVD com os
   mecanismos oficiais, header confrontado pela libclamav com .info do payload assinado determina versão/data/flevel; engine
   deve ser exatamente 1.4.6 e flevel <=216. clamscan carrega o conjunto completo
   com OfficialDatabaseOnly e analisa um benigno fixo. Nenhum upgrade de engine.
5. Manifest SHA-256 identifica o conjunto inteiro. SHA não substitui assinatura.
   Arquivos e diretórios são sincronizados. Rename do diretório ocorre no mesmo
   filesystem; estado ativo/anterior é **um único JSON** promovido por rename.
   Cross-filesystem falha; não há fallback para copiar publicação parcialmente.
6. Crash entre diretório publicado e state.json é recuperável verificando os
   hashes de todos os arquivos da geração já existente. Staging interrompido é
   removido sob lock. Não se sobrescreve uma geração existente divergente.
7. Consumidor monta o store publicado read-only; copia uma geração para snapshot
   privado, revalida assinatura, hashes, compatibilidade e idade após copiar.
   Lstat, IDs hex estritos, nomes fixos, O_EXCL e os.OpenRoot/OpenInRoot limitam
   symlinks/traversal; nada oriundo do upload nomeia arquivo ou comando.
8. Guardião inicia clamd somente em Unix socket privado, com SelfCheck=0,
   OfficialDatabaseOnly=yes e sem TCP bruto. Config deriva do clamd.conf comum,
   conservando limites. VERSION deve corresponder ao daily e engine esperados;
   PING, EICAR e benigno precisam passar. Não há HTTP /scan antes disso.
9. Acknowledgement em mount separado `/ack` registra geração realmente carregada
   após os testes. É somente evidência de retenção: o publicador preserva a
   última geração carregada, mesmo após várias candidatas falhas. `/ack` é o
   único subdiretório do PVC gravável pelo consumidor. Não aprova outro daemon.
10. Mudança de estado é observada a cada 30s. Guardião fecha admissão/readiness,
    drena HTTP (até 310s), encerra child e sai; kubelet reinicia o container com
    novo snapshot. Perder o child fecha o servidor; falha de carga não expõe scan.
    Não existe RELOAD parcialmente concluído no caminho de autorização.
11. Cada HTTP /scan transmite bytes ao INSTREAM local; erro ou ausência de EOF
    não emite evidência. Resposta contém clean/infected explícito, geração,
    engine, versão daily, data assinada, loaded_at e completed_at. Não contém
    assinatura casada, nome, storage key ou IDs. Transporte interno herda a
    fronteira de confiança da NetworkPolicy do scanner.
12. File-service exige cliente com evidência e idade explícita; sem idade, não inicia o worker e não aprova novos arquivos.
    Cada aprovação operacional leva a prova até o UPDATE; versão de formato, original,
    criptografia, Range, previews, status públicos e eventos continuam iguais.

O consumidor mantém flock compartilhado, aberto read-only, durante seleção,
cópia, verificação, carga e acknowledgement. Publicação/GC exigem lock exclusivo.
Isso impede coletar a geração entre copiar e confirmar carga; o lock é solto
antes de atender scans. Um backend que não ofereça essa serialização bloqueia
rollout. Atualizador não promove acknowledgement cujo diretório já desapareceu.
A configuração montada também precisa conservar todos os limites críticos,
sem duplicatas; configuração ausente, truncada ou com AlertExceedsMax/MaxScanTime
inseguros é recusada antes de iniciar clamd.

## Política, TOCTOU e N/N+1

**Nenhuma idade máxima foi aprovada.** A proposta herdada de A é **48h**, para
avaliação humana: daily costuma ser publicado uma ou duas vezes por dia; 48h
acomoda uma interrupção sem tornar ausência de update ilimitada. Atualização
proposta a cada 2h, alerta antes da expiração. Esses números não são defaults
operacionais. Ensaios da imagem usam 365 dias exclusivamente para a fixture
antiga, e ensaios SQL usam 7200s/1s exclusivamente como fronteiras de teste.

A idade é `agora - timestamp Unix autenticado do daily.cvd`, e não download/mtime.
Main/bytecode podem ser estáticos por mais tempo; autenticidade e compatibilidade
são verificadas para os três. Timestamp ausente, futuro, geração desconhecida,
engine/flevel não reconhecido, carga/sintéticos incompletos ou max-age ausente
fecham o guardião. Exatamente no vencimento é inválido. Relógio que retrocede
no guardião encerra o ciclo: watermark persistente em /ack, health/readiness503,
supervisão e espera de recuperação antes de novo snapshot/engine. Restart não
zera o watermark (ver remediação). Sincronismo UTC confiável entre nós/file-service/
PostgreSQL é pré-requisito operacional, não demonstrado aqui.

`FILE_MALWARE_SIGNATURE_MAX_AGE_SECONDS` não tem idade permissiva default.
Ausência mantém **novas aprovações fechadas**, sem iniciar o worker;
valor configurado inválido impede startup. A API continua aceitando uploads em
pending_scan, sem declarar B habilitada. Não há fallback automático a clamd bruto. Para B é obrigatório habilitar também
`files.signature_policy` com idade e referência humana não vazia. Zero/desconhecido
não é política aprovada. O cliente com prova só escreve se a policy DB está on.

A migration 000020 é aditiva e nasce **disabled**, preservando N/N+1 durante a
preparação. Quando enabled, trigger BEFORE e privilégios de coluna exigem prova nova para cada escrita
explícita de clean e rejeita writers antigos sem prova, inclusive INSERT clean.
Trigger deferred revalida expiração no fim da transação antes do commit. Leitura
da política usa FOR SHARE; o relógio é lido **depois** de esperar esse lock.
Alterar política e aprovar se serializam. Não se usa now() de início de transação
para decidir expiração. Metadata da geração é gravada no mesmo UPDATE do verdict.
Os testes usam a migration real em PG16/17, incluindo espera após UPDATE.
O writer com evidência chama a função transacional persist_proven_scan; política
inativa/ausente/inválida tem SQLSTATE/categoria própria. O runtime não altera a
política nem as seis colunas de evidência diretamente. O hook de grants preserva
essas restrições, mantendo as demais colunas acessíveis aos writers legados.

Semântica: uma sessão pertence ao snapshot imutável do daemon que a executou.
Troca de geração depois de uma resposta completa não invalida retroativamente
esse scan: sua prova pode persistir se ainda fresca segundo o banco no commit.
Se a base expirar durante streaming, na resposta ou antes do commit, não aprova.
Rollback não revalida automaticamente arquivos históricos; rescan/ownership e
telemetria da fila continuam fora do escopo B. Perda da conexão após commit pode
perder a confirmação/evento; banco e refetch continuam autoritativos.

Não há autorização distribuída baseada apenas em readiness, VERSION antes/depois
ou arquivo no PVC. A prova depende do snapshot verificado e processo imutável,
e do gate de banco. Configurar idade somente no cliente ou publicar apenas o
CronJob **não habilita a cadeia inteira**.

## Threat model e riscos

| Asset / fronteira                  | Abuso plausível                                                             | Controle / limite                                                                                                                                          |
| ---------------------------------- | --------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Bases e publisher → origem externa | MITM, mirror substituído, download parcial, replay antigo                   | HTTPS/CA, CVD assinado oficial, TestDatabases, staging, flevel, idade assinada; mirror pode causar DoS/replay dentro da janela, nunca clean após expiração |
| PVC → consumidor                   | Corrupção, symlink, mistura de arquivos, crash, dois publishers             | Allowlist, snapshots revalidados, hash de conjunto, lock, state atômico e fsync; depende de semântica real do backend                                      |
| Daemon → guardião                  | PONG sem detecção, versão divergente, morte ou transição                    | Unix privado, VERSION, PING, sintéticos, geração fixa, admissão fechada antes da troca                                                                     |
| Guardião → file-service → banco    | OK precoce, resposta truncada/ausente, expiry/upgrade em voo, writer antigo | EOF em ambas as fronteiras, conjunto fechado de resultado, evidência por sessão, triggers e política transacional                                          |
| Updater comprometido               | Credenciais/anexos/cluster exfiltrados                                      | Sem volumes de usuários, app secrets, token ou endpoint admin; NP somente DNS e IP do mirror em 443; scanner sem saída                                     |
| Disponibilidade                    | PVC cheio, OOM, mirror/DNS/429, nó perdido                                  | Recursos/PVC limitados, ativa preservada, retry externo CronJob, scanner continua enquanto snapshot fresco; após expirar mantém quarentena                 |

Risco residual: guardião e clamd são processos do mesmo container/UID. O PVC
publicado é efetivamente read-only; o snapshot privado tem arquivos 0400, mas
não é uma sandbox contra um clamd já comprometido, que compartilha UID e /tmp.
Não se alega isolamento entre esses dois processos nem proteção contra um engine
comprometido que fabrique respostas. Imagem/engine precisam de patches revisados;
nenhuma assinatura substitui atualização do engine. EICAR prova detecção sintética,
não cobertura de malware desconhecido.

## Storage, segurança Kubernetes e disponibilidade

Candidato exige RWX entre updater e consumidor em nós distintos. RWO limita a um
nó e exige scheduling comprovado; ReadWriteOncePod impede esses dois pods e não
serve ao modelo escolhido. StorageClass não foi inventada. Validar fsGroup,
subPath ack preexistente, suporte a locks POSIX entre nós, atomic rename, fsync,
crash recovery, latência de visibilidade, quota, capacidade, expansão e backup.
Aumento/resize não é automático. `/ack` é criado no bootstrap pelo updater antes
de criar o consumidor. Não usar NFS/RWX só por existir, sem testar essas garantias.

8Gi de PVC é proposta limitada: cache, ativa, última carregada, staging e possível
geração em ativação; cada CVD tem teto de 512MiB. Coleta remove somente gerações
obsoletas após publicação bem-sucedida; uma candidata falha não remove ativa/
anterior. Os snapshots privados usam emptyDir de 2Gi, junto ao scratch de scan:
a capacidade conjunta **não foi validada** para o máximo de arquivo+archive;
essa disputa é bloqueio de capacidade a medir antes de rollout, sem aumentar
limites especulativamente. Crash cleanup, corrupção e storage cheio falham fechado.

Mesmo requests/limits do daemon (500m/1250m,1536Mi/3Gi) e estratégia Recreate.
Updater separado propõe request100m/256Mi e limit500m/3Gi; init é pequeno.
Esses limites exigem baseline/capacidade; não são comprovação de agendamento.
Kubernetes não tem resources.limits.pids padrão: exigir podPidsLimit do kubelet
com valor aprovado e verificar runtime. O teste Docker usa pids-limit=64.

Policies candidatas: updater sem ingress, DNS somente kube-system/kube-dns e
TCP443 para um único /32 ou /128 aprovado; nenhum 0.0.0.0/0. DNS/CNI/NodeLocalDNS
reais precisam ser confirmados. NetworkPolicy padrão **não filtra FQDN**;
o hostname do mirror é validado por TLS, e seu IP deve casar com a regra de rede.
Policies são aditivas: auditar também regras abrangentes existentes no namespace.
TCP3310 conserva o mesmo Service/selector e origem componente file; não há
Ingress/LoadBalancer/NodePort/hostPort. O protocolo candidato é HTTP interno no
mesmo endpoint; clientes incompatíveis falham fechado, sem fallback automático.

## Observabilidade preparada e implementada

Guardião expõe /metrics no mesmo endpoint interno, somente após carga verificada.
Coleta proposta 15s, relógio UTC do processo; owner Plataforma. Não há job scrape
novo ativo nem ampliação de ingress: integrar coletor autorizado exige revisão
da identidade de observabilidade e NP, sem abrir 3310 indiscriminadamente.

| Métrica                                               | Tipo / unidade / origem                                                                |
| ----------------------------------------------------- | -------------------------------------------------------------------------------------- |
| nchat_clamav_signature_version                        | Gauge, versão numérica daily efetivamente carregada                                    |
| nchat_clamav_signature_age_seconds                    | Gauge, segundos desde data assinada do daily carregado                                 |
| nchat_clamav_signature_max_age_seconds                | Gauge, segundos da política configurada no guardião                                    |
| nchat_clamav_last_successful_update_timestamp_seconds | Gauge Unix UTC, último update verificado no state, atualizado pelo guardião a cada 30s |
| nchat_clamav_loaded_timestamp_seconds                 | Gauge Unix UTC, última ativação concluída deste processo                               |
| nchat_clamav_engine_info{version}                     | Gauge=1; único label, versão do engine de allowlist fixa                               |
| nchat_clamav_signatures_ready                         | Gauge 0/1, admissão/frescor local; não substitui o gate de banco                       |

Ausência de série é desconhecido/indisponível, não zero; versões no disco não são
exportadas como carregadas. Atualização sem mudança não reinicia consumidor; o timestamp é relido do state
a cada 30s. Para falha repetida combinar idade desse timestamp com status/failed
Jobs da CronJob e disponibilidade do alvo. Não existe
métrica fictícia reload_errors: este modelo não solicita RELOAD; erros de
ativação aparecem como reinícios, ausência de target/ready e falhas de carga.
Alertas candidatos estão no runbook. Não foram ligados ao Prometheus ativo.
Sem attachment/user IDs, hashes de geração como labels, filenames ou URLs.

## Fontes oficiais consultadas em 2026-11-04

- [FreshClam e gestão de assinaturas](https://docs.clamav.net/manual/Usage/SignatureManagement.html): aquisição, incrementais e recuperação de corrupção.
- [CVD e frequência de publicação](https://docs.clamav.net/faq/faq-cvd.html): assinatura/base e limites do teste de eficácia.
- [Imagem Docker e reload](https://docs.clamav.net/manual/Installing/Docker.html): memória, imagens com/sem seed e consumidores compartilhados.
- [freshclam.conf da série 1.4](https://github.com/Cisco-Talos/clamav/blob/clamav-1.4.6/etc/freshclam.conf.sample): PrivateMirror, TestDatabases, CLD/CVD e hooks.
- [sigtool 1.4.6](https://github.com/Cisco-Talos/clamav/blob/clamav-1.4.6/sigtool/sigtool.c): verificação oficial CVD; --info/Verification OK confirmados no binário pinado, sem --unsigned ou verificador próprio.
- [Functionality level 216](https://github.com/Cisco-Talos/clamav/blob/clamav-1.4.6/libclamav/others.h) e [clamd.conf 1.4.6](https://github.com/Cisco-Talos/clamav/blob/clamav-1.4.6/etc/clamd.conf.sample): opções confirmadas para o engine utilizado.
- [Política de suporte](https://docs.clamav.net/faq/faq-eol.html): matriz consultada lista 1.4.6 na série 1.4 LTS com EOL previsto em 2027-08-15. Revalidar patches/CVEs antes de implantar; updater não promove engine automaticamente.
- [FreshClam e CA de TLS](https://docs.clamav.net/faq/faq-freshclam.html): CURL_CA_BUNDLE configura confiança, não desliga verificação.

O teste negativo de replay altera somente o timestamp do header externo, mantendo
o payload comprimido intacto, e confirma rejeição pela cadeia oficial. Não se
presume que ler o header isolado valide sua data: [cl_cvdload confronta versão,
flevel e stime com a .info do payload verificado](https://github.com/Cisco-Talos/clamav/blob/clamav-1.4.6/libclamav/cvd.c#L652).
