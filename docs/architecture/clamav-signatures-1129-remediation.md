# #1129 B — remediação consolidada local

Estado: remediação local implementada/testada, pronta para re-review direcionado;
sem aprovação independente ou rollout. SEC-B-002 continua bloqueante de ativação.
Branch `security/files-1129-clamav-signatures`, HEAD/base/origin/develop/merge-base
`1e7c91ec35ce638696fe9e68809771929153a679`. Entrega A presente.

## Identificação do snapshot e reconciliação

O [inventário anterior à remediação](clamav-signatures-1129-before-remediation.sha256)
foi calculado **antes de editar**, sobre os 15 arquivos modificados e 29 novos
existentes. Formato sha256sum, caminhos relativos ao repositório, nenhum conteúdo
ou segredo. Para comparar: recalcular SHA-256 dos mesmos caminhos e comparar os
registros, preservando esse inventário original; `sha256sum -c` naturalmente
reportará diferenças nos arquivos remediados. HEAD igual não identifica worktree.
Arquivos criados nesta rodada não existiam naquele inventário.

`scripts/db/grant-runtime.sql` estava limpo no status inicial; sua origem adicional
é reproduzível com `git show HEAD:scripts/db/grant-runtime.sql | sha256sum`.
Sua alteração é necessária: o grant geral pós-migration reabria a escrita da
política e das evidências se a restrição existisse somente na migration.

Status informados pelo solicitante: Code Quality APPROVED WITH NON-BLOCKING
FINDINGS; Security CHANGES REQUESTED/HIGH. Não são aprovações do delta remediado.
Os pareceres completos não foram fornecidos como arquivos nesta sessão. A tabela
reconcilia as afirmações transcritas no pedido com o snapshot inventariado, sem
inventar a origem dos relatórios ou concluir que analisaram o mesmo worktree.

| Tema           | Afirmação CQ transcrita         | Afirmação AppSec transcrita               | Código antes da remediação / evidência                                                            |
| -------------- | ------------------------------- | ----------------------------------------- | ------------------------------------------------------------------------------------------------- |
| Geração        | `gen-` + hash abreviado         | 64 caracteres hex                         | AppSec corresponde: `Manifest.ID`, `ValidGeneration`; sem prefixo                                 |
| Frescor        | EffectiveTime = menor BuildTime | DatabaseTime de daily                     | AppSec corresponde: `readManifest` só atribui `DatabaseTime` para daily; EffectiveTime não existe |
| Guardian       | HTTP8080                        | HTTP3310                                  | AppSec corresponde: flag listen `:3310` e candidato                                               |
| clamd          | TCP127.0.0.1:3310               | Unix privado                              | AppSec corresponde: privateConfig remove TCP e cria clamd.sock                                    |
| Evidence       | Estrutura diferente descrita    | geração/engine/daily/data/load/completion | Código tem esses seis campos JSON em signatures/policy.go                                         |
| Persistência   | Detalhes não comprovados        | deferred + clock_timestamp                | AppSec corresponde à migration000020; FOR SHARE, BEFORE e constraint trigger AFTER ROW deferred   |
| Política off   | Zero rows → superseded          | SEC-B-004                                 | Confirmado no snapshot: WHERE EXISTS e conversão pgx.ErrNoRows; corrigido nesta rodada            |
| Clock rollback | Indisponibilidade permanente    | SEC-B-005                                 | Confirmado: closed em memória, health200 e sem sinal ao supervisor; corrigido nesta rodada        |

As descrições CQ divergentes estão desatualizadas, incorretas para este snapshot
ou vieram de outra versão local; não há evidência para escolher qual hipótese.
A implementação não foi convertida para corresponder a essas descrições.

## SEC-B-004 — política e persistência

O novo writer chama `files.persist_proven_scan` em uma operação transacional.
A função usa a tabela de política autoritativa, com lock FOR SHARE, e trava o
anexo antes de classificar o resultado. Não há cache de política ou consulta
adicional do cliente por scan. SQL fixo, SECURITY DEFINER, search_path fixo,
objetos qualificados e EXECUTE revogado de PUBLIC.

| Situação                                                                               | Resultado                                                   |
| -------------------------------------------------------------------------------------- | ----------------------------------------------------------- |
| Política inativa                                                                       | SQLSTATE NC001 → ErrSignaturePolicyInactive                 |
| Política ausente/inválida                                                              | NC002 → ErrSignaturePolicyInvalid                           |
| Anexo inexistente/deletado/no outro workspace                                          | ErrNotFound, sem enumerar tenant                            |
| Estado não permite aprovação                                                           | ErrScanSuperseded                                           |
| Prova inteira exatamente já gravada no mesmo anexo                                     | ErrScanAlreadyRecorded; nenhum UPDATE/evento/aprovação nova |
| Mesmo completed_at com prova parcialmente diferente, ou falha de serialização/deadlock | ErrScanConflict; NC003/40001/40P01 quando aplicável         |
| Erro de banco, inclusive evidência inválida/vencida                                    | Causa preservada, retry, nunca clean                        |

Política produz `nchat_file_malware_scans_total{result="policy_blocked"}` e log
estruturado `error_type=signature_policy_inactive|signature_policy_invalid`.
Sem texto SQL, endpoint, base, conteúdo, chaves ou credenciais nos logs. A métrica
usa vocabulário limitado; os logs mantêm a correlação de anexo já existente.

Não há retry imediato novo. A claim já agenda a próxima tentativa: lease de330s
vezes min(attempts,8), sem teto de tentativas, limite44min. Portanto o problema
anterior não provava abandono permanente; ocultava falha operacional e atrasava
o diagnóstico. Após ativação, o pending volta à fila no prazo do lease existente.
Uma linha fora do estado pending não é reclamada. Nenhum tuning de fila/fencing.

## SEC-B-005 — relógio, supervisão e recuperação

Antes de admissão e conclusão, o guardião persiste o maior horário observado em
`/ack/clock.json`, com rename/fsync. O polling de saúde mantém o watermark também
quando a geração expirou: um relógio que retorna à janela antiga não a reabre.
Qualquer retrocesso observado, inclusive pequeno, fecha definitivamente o ciclo.
Não há tolerância permissiva nova. Readiness e health tornam-se503; o supervisor
recebe o sinal, fecha o HTTP para cancelar requests, encerra/junta o child e faz
cleanup. O handler nunca chama os.Exit.

O processo aguarda o relógio alcançar o watermark persistido, com polling de1s
e cancelamento, antes de criar um novo ciclo/snapshot/engine/sintéticos. Não faz
busy-loop de cargas. Restart do container/pod lê o mesmo watermark do PVC; a
perda/corrupção do arquivo depois de uma ativação bloqueia bootstrap. Geração nova
não zera o clock. Após recuperação, a política é reavaliada: base vencida continua
fechada. Probes Kubernetes podem reiniciar o container conforme seus thresholds e
backoff nativo; isso não remove o watermark. Não se alega disponibilidade durante
relógio não confiável ou falha do armazenamento de controle.

Limites: único consumidor Recreate; não é coordenação para múltiplos guardiões.
Não é fonte externa de tempo nem proteção contra comprometimento do nó/PVC/UID
ou restauração administrativa de watermark antigo. Bootstrap exige relógio UTC
confiável e PostgreSQL confiável e sincronizado. O banco verifica novamente tempo
no commit; comprometimento simultâneo das fontes temporais continua fora dessa
prova. Esses controles precisam de homologação operacional, sem remover watermark
para contornar a indisponibilidade.

## SEC-B-003 e SEC-B-009 — evidência e repetição

BEFORE cobre status e todas as seis colunas de evidência. Validação deferred
continua no fim da transação. Mudança isolada de geração/versão/datas não pode
escapar pela ausência de status. Evidência não pode mudar junto a uma rejeição.
Metadados não relacionados continuam atualizáveis, inclusive após expiração.
Um novo scan exige completed_at estritamente posterior e todos os campos válidos.

Para nchat_app, INSERT/UPDATE diretos nas seis colunas são revogados. Como grants
de tabela se sobrepõem aos de coluna, INSERT/UPDATE de tabela são revogados e
reconcedidos somente nas demais colunas, preservando writers legados. A operação
com prova é a função fixa autorizada, que escreve status e evidência juntos.
O hook oficial de grants repete essas restrições após seu grant geral. A política
é SELECT-only para runtime; alterações são da identidade de migration/operação.
O down exige política explicitamente desabilitada/válida, recusa ausência,
trava a tabela e restaura os grants legados somente após remover o gate desativado.
Owners/superusers, grants herdados e roles reais devem ser auditados antes do
rollout; não há alegação de proteção contra o próprio administrador.

Repetição exata é **recusada como nova transição**, com categoria already_recorded,
sem duplicação de clean/eventos. Um novo scan completo de anexo já clean grava
nova evidência, retorna rescanned e não publica outro evento de status; a flag é
interna e não serializada no contrato público. Não é promessa de retry idempotente autorizado:
a arquitetura não autentica criptograficamente um recibo de scan. Uma geração
diferente com novo scan completo passa por toda a validação; reuso/forja por um
ator que controla a autoridade de escrita continua no bloqueio SEC-B-002 abaixo.
Não foi liberado UPDATE genérico para satisfazer idempotência.
O cliente também recusa completed_at anterior ao início desta requisição,
inclusive um JSON válido reaproveitado para outro corpo. Isso exige relógios
sincronizados; não autentica recibo nem impede forja por MITM/autoridade maliciosa.

## SEC-B-006 — monotonicidade normal

A promoção normal compara versões de main/daily/bytecode dos CVDs verificados
com a geração ativa, sob lock EX. Rejeita redução de qualquer versão e versão
igual com hash diferente. Manifesto persistido e hashes da ativa precisam ser
íntegros. A identidade existente da geração não mudou. Atualização idêntica é
idempotente; manifestos de verificação não giram o ID por timestamp de download.
Rollback separado continua revalidando assinatura, engine/flevel e frescor,
registrando rejeição e impedindo oscilação; exige operação humana GitOps futura.
Não há flag de downgrade no caminho normal do CronJob.

## SEC-B-008 — erros internos

HTTP413 representa excesso de tamanho, 504 deadline, 408 cancelamento,
502 protocolo inválido e503 indisponibilidade/admissão fechada. Causas locais
são preservadas por wrapping/errors.Is. Deadline/cancelamento têm prioridade
na categoria operacional mesmo quando acompanhados de wrapper de protocolo. A resposta de erro não contém texto
interno. Não é mudança do contrato público de anexos. Readiness não substitui
nenhuma verificação na aprovação.

## SEC-B-002 — fronteira de confiança ainda bloqueante

Fatos: worker e guardian falam HTTP plaintext, sem autenticação mútua e sem
assinatura de recibo. Proxy e redirects são desabilitados; isso não autentica o
servidor. A evidência identifica snapshot/sessão no processo honesto, mas não é
recibo criptográfico vinculado a anexo ou desafio imprevisível. A trigger valida
formato e tempo; não certifica que o hash é uma geração autorizada.

O overlay dev usa o componente least-privilege, que remove as permissões amplas
de namespace da base e permite3310 somente de pods com label component=file no
mesmo namespace. Agora o candidato isolado também contém essa ingress policy,
além de egress negado. Não foi aplicado. Policies são aditivas; CNI, regras
adicionais, labels, autorização de criação de pods e identidades efetivas ainda
não foram verificados operacionalmente. Token de SA não é montado no file-service.

| Ator/capacidade                                   | Efeito demonstrável / pré-requisito                                                                      |
| ------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| Pod que apenas alcança /scan                      | Pode solicitar scan/consumir capacidade; não escolhe o veredito nem ganha escrita no banco               |
| Pod fora da origem file sob CNI/policies corretos | Conexão deve ser negada; não comprovado em cluster                                                       |
| Controlador de DNS/endpoints/CNI/nó capaz de MITM | HTTP não autentica origem: pode substituir resposta; isolamento apenas não prova resistência a esse ator |
| Ator com credencial runtime nchat_app             | Banco aceita campos bem formados/frescos pela função; não comprova que vieram do scanner                 |
| Owner/migrator/superuser                          | Pode alterar a autoridade/política; é fronteira administrativa, não um controle da trigger               |

Os manifests de file/chat montam o Secret comum nchat-secrets, e o script de grants
usa nchat_app compartilhado. Os valores reais de credenciais e grants herdados
não foram acessados. O caminho SQL de prova sintética nos testes demonstra o
limite: conhecer campos com formato válido não equivale a autenticação do scanner.
Comprometer a autoridade runtime ou interceptar sua resposta ultrapassa uma
fronteira que o mecanismo atual não autentica. **SEC-B-002 é bloqueio arquitetural
de ativação**, não só melhoria opcional ou achado considerado resolvido.

Opções para decisão: identidade/transportes de serviço já provisionados e
rotacionáveis (mTLS se adotado), separação de autoridade de banco por serviço,
ou verificação de recibo/registro de gerações emitidos por identidade confiável.
Só registrar hashes enviados pelo mesmo worker não resolve autenticidade. Token
em HTTP também não protege contra MITM. O repositório não fornece PKI/mTLS pronta
para este caminho; não foram criadas credenciais/Secrets/PKI ad hoc. É necessário
aprovar provisionamento, rotação, autorização e rollback antes de implementar e
ativar esse controle. A limitação de replay entre anexos por autoridade maliciosa
fica formalmente nessa decisão; o código não afirma tê-la resolvido.

## SEC-B-007 — homologação distribuída

flock não é universalmente local nem universalmente distribuído: depende de FS,
CSI, servidor, cliente e mount options; Linux/NFS pode emular via locking remoto.
Exigimos que EX do publisher exclua publishers/rollback/GC e SH do consumidor
em todos os nós; SH protege seleção/cópia/verificação/carga/ack. Scans já usam
snapshot privado e não mantêm esse lock. Crash deve liberar o lock; nunca remover
o inode .publisher.lock. Estado e gerações exigem rename/fsync/visibilidade coerentes.

Teste preparado: `scripts/testing/qualify-clamav-rwx.py`, somente opt-in, raiz de
homologação aprovada e run ID hexadecimal32. Escreve apenas em diretório novo
`.nchat-rwx-<run>`, sem limpeza recursiva ou descoberta/acesso a cluster. Self-test
local está em `test_qualify_clamav_rwx.py`; PASS local **não homologa RWX**.

No futuro, em dois pods de teste em **nós distintos**, mesmo PVC e UID/GID e
versão/hash do script, definir QUALIFY_ROOT para o mount e QUALIFY_RUN para o
mesmo ID único. Fora dos diretórios de assinaturas/anexos. Registrar backend,
versões kernel/CSI/servidor, opções de montagem, nós, clocks e resultados.

1. NóA: `--action init`; nenhum recurso compartilhado existente é alterado.
2. NóA: `--action hold --seconds 60` (EX). Esperar evento lock_acquired.
3. NóB: `--action probe` e depois `--action probe --shared`: ambos exit2/conflict.
4. Após saída do holder, nóB EX deve passar. Repetir matando **somente o holder**
   via SIGKILL; confirmar liberação com prazo de recuperação aprovado do backend.
5. NóA: `--action hold --shared --seconds 60`. NóB SH passa, EX retorna2.
6. NóA: `--action publish --sequence 1`; depois2. NóB snapshot SH precisa validar
   o conjunto inteiro publicado e enxergar state após os fsyncs concluídos.
7. NóB: `--action snapshot --shared --seconds 60`, aguardar snapshot_selected.
   NóA prune e publish3 precisam retornar2 enquanto SH está retido. Depois, ambos
   passam; nova leitura verifica conjunto íntegro. Repetir alternando nós.
8. Desconectar/reconectar somente o volume de teste pelo procedimento aprovado do
   CSI (não pelo script). Repetir lock/crash/rename/visibilidade/prune; timeout,
   mixed generation ou dois locks incompatíveis adquiridos são FAIL e abortam.
9. Parar todos os holders antes de remover manualmente **somente a fixture desta
   execução**, por fluxo autorizado. O script não remove mounts, PVCs ou diretórios
   do serviço. Não simula reconexão nem durabilidade física de fsync em teste local.

Qualquer divergência bloqueia este backend. Não foi introduzido Kubernetes Lease,
RBAC/API ou alegação de fencing distribuído. RWX real permanece desconhecido.

## FreshClam e frescor

Configuração de produção candidata agora explicita ScriptedUpdates no, além de
PrivateMirror HTTPS e TestDatabases yes. O mirror aprovado deve fornecer somente
CVD completos oficiais; CLD no cache continua recusado, sem relaxamento de crypto.
PrivateMirror pode consultar CLD antes de CVD; a ausência deve ser404. Não se usa
CDIFF incremental no modelo escolhido. A configuração foi confrontada com o
sample oficial1.4.6 e testada no binário oficial, não inferida da primeira carga.

Ensaios incluem bootstrap, repetição sem mudança, duas gerações oficiais de
imagens pinadas por mirror HTTPS local, mudança real de daily, rollback,
repromoção rejeitada, CLD corrompido/sem CVD e429. Não acessam CDN. Download integral
é custo do mirror privado; abastecer esse mirror continua responsabilidade da
plataforma com políticas oficiais e cache/cooldown persistente. Não se promete
incremental eficiente neste design CVD-only.

Frescor é exclusivamente data autenticada de daily.cvd. Main e bytecode são
verificados independentemente para autenticidade/hash/compatibilidade/monotonicidade,
sem exigência de publicação diária. Última consulta bem-sucedida do mirror, data
de publicação e geração carregada são sinais distintos. A proposta48h não foi
aprovada e nenhum default permissivo foi adicionado.

## Imagem e bloqueios

Digest atual preservado: d97f5ef70fbe9f1f8e5490278082067f16f5824eca2eb4176f4df3748a45a4c2.
Trivy reproduziu oito achados, 1CRITICAL/7HIGH, com os mesmos pacotes/CVEs e
correções do [relatório anterior](clamav-signatures-1129-validation.md).

Consulta ao registry oficial identificou proposta **mesmo engine1.4.6**:
`clamav/clamav:1.4.6@sha256:8ad6197396974726c35d017e50aaaaf4b91745726a04faec8480c203edd98052`,
publicada em2026-11-02, Alpine3.24.3. Pull local verificou esse digest. Trivy
reportou zero HIGH/CRITICAL na proposta. Isso não equivale a aprovação de origem,
SBOM, proveniência de build, review ou rollout; os manifests/engine selecionado
não foram trocados. Digest d97f continua bloqueante até promoção revisada. A plataforma validada
localmente é amd64. O wrapper /init-unprivileged executou clamd --version sem
root, sem rede e com rootfs read-only em ambos os digests: atual daily28085
(2026-08-07), proposta daily28171 (2026-11-01). O seed da proposta também não
satisfaria a proposta48h na data do teste: corrigir a imagem não substitui o mirror
nem comprova frescor operacional. As fixtures usam365 dias somente para testes.

Permanecem: idade e referência humana, baseline A, mirror/CA/IP, controle SEC-B-002,
RWX/CNI/grants reais/relógios/PIDs/capacidade/quotas/scratch/observabilidade,
installer promovido e revisões independentes. Nenhuma política ou recurso foi
ativado, nenhum overlay ativo/prod alterado, nenhum commit/add/push/PR/deploy.

## Fontes oficiais

- [PostgreSQL17 CREATE TRIGGER](https://www.postgresql.org/docs/17/sql-createtrigger.html): UPDATE OF e constraint triggers.
- [freshclam.conf1.4.6](https://github.com/Cisco-Talos/clamav/blob/clamav-1.4.6/etc/freshclam.conf.sample): PrivateMirror/ScriptedUpdates/TestDatabases.
- [ClamAV Docker](https://docs.clamav.net/manual/Installing/Docker.html): variantes, digests e atualizações de imagem independentes de assinaturas.
- [Linux flock](https://man7.org/linux/man-pages/man2/flock.2.html): locks, NFS e dependência de backend.

## Evidências finais locais

| Comando / escopo                                                                                                    | Resultado                                                                                                                                                      |
| ------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make test-go`                                                                                                      | PASS, todos os módulos; fixtures opt-in executadas separadamente abaixo                                                                                        |
| `make ci-static-go`                                                                                                 | PASS; gofmt/vet/golangci-lint em todos os módulos                                                                                                              |
| `go test -race ./services/file-service/internal/{scanner,signatures,signatureguard,storage,service,config,app}`     | PASS; testes scanner/service repetidos após refinamento de testes                                                                                              |
| `python3 scripts/testing/test_clamav_signature_policy.py -v`, versões16 e17                                         | PASS, **16 testes em cada versão**, incluindo privileges/functions/down/N/N+1                                                                                  |
| `bash scripts/testing/clamav-signatures-isolated.sh`, digest atual + CLAMAV_SIGNATURE_NEXT_IMAGE pinada na proposta | PASS; duas bases oficiais, HTTPS local, EICAR/benigno, negativos e cadeia até PGX/triggers                                                                     |
| Mesmo runner com CLAMAV_SIGNATURE_TEST_IMAGE pinada na proposta                                                     | PASS; engine1.4.6, assinatura, daemon e cadeia real compatíveis isoladamente                                                                                   |
| Perfil integral atual e `make go-coverage-check` com fixture PostgreSQL16 descartável                               | PASS: **770/844 = 91,2322%** nos três pacotes centrais; file-service **90,7%**, nove módulos acima de90%                                                       |
| `make ci-infra-migrations ci-infra-kubernetes ci-infra-config` (alvos individualmente)                              | PASS; sem apply, migrations operacionais ou acesso ao cluster                                                                                                  |
| `make ci-static-repository` / CI architecture                                                                       | PASS após formatar o relatório novo; Required e thresholds preservados                                                                                         |
| Renderer com valores fictícios explícitos + Trivy config do resultado                                               | PASS, zero HIGH/CRITICAL; suspend=true, replicas0, ingress file3310, egress restrito                                                                           |
| `python3 scripts/testing/test_qualify_clamav_rwx.py -v`                                                             | PASS, três testes em filesystem local: locks/crash, snapshot/prune/rename e recusa de symlink                                                                  |
| `make security-trivy-config security-trivy-fs` (alvos individualmente)                                              | PASS                                                                                                                                                           |
| `make security-govulncheck`                                                                                         | PASS em todos os módulos, v1.1.4 recompilada com Go1.26.10 temporariamente                                                                                     |
| Trivy image dos dois digests oficiais                                                                               | Atual FAIL8 (1CRITICAL/7HIGH); proposta PASS0 HIGH/CRITICAL, sem promover manifests                                                                            |
| `make security-secrets`                                                                                             | FAIL, mesmas quatro detecções históricas; sem ignore/allowlist nova                                                                                            |
| Gitleaks sobre cópia privada dos arquivos do delta                                                                  | PASS, zero detecções no delta; não substitui o gate de histórico                                                                                               |
| Semgrep OSS preventivo, três regras locais, metrics off                                                             | PASS, zero achados em15 arquivos; sete testes ignorados pelos padrões padrão, não é revisão independente                                                       |
| Build local helper e sintaxe Bash/Python                                                                            | PASS; imagem nchat-clamav-signatures:local-test, sem registry push                                                                                             |
| Gocyclo/Gocognit                                                                                                    | PASS: 213 funções novas/alteradas contra HEAD; máximos ciclomático10 e cognitivo10, incluindo testes; 190 scores cognitivos não zero, sem diagnóstico de tipos |
| SonarQube / CI remoto / homologação distribuída                                                                     | Não executados: ferramenta indisponível; sem push/PR; sem cluster                                                                                              |

Cobertura por módulo: platform94,4%; admin91,3%; auth90,8%; chat91,0%;
converter92,7%; file90,7%; media92,4%; notification96,0%; search98,1%.
O job existente Tests/Go Coverage inclui agora PostgreSQL16/17, self-test local
RWX e segunda fixture CVD pinada; mantém o mesmo owner/threshold/agregador.

Limitações/falhas preliminares registradas: ajuste de fixture após revogar escrita
direta, UUID de fixture substituído indevidamente no singleton policy (corrigido),
parêntese em asserção de teste (corrigido), grant de schema ausente na fixture
do novo teste de rescan (corrigido aplicando o hook oficial), alerta gosec em abertura de arquivo de
teste (corrigido usando OpenRoot), formatação de documento novo (corrigida), e
complexidade cognitiva12 em um teste novo (separação de asserção reutilizável).
Ferramenta Gocognit instalada com Go1.25 emitia erros de tipos: a mesma v1.3.0 foi
recompilada com Go1.26.10 e a análise final não teve diagnóstico. O filtro inicial
do resumo não reconheceu caminhos absolutos; os dados exatos foram reaproveitados
com normalização de caminhos. Nada disso foi tratado como PASS antes de corrigir.

Não há medição de baseline/capacidade nem garantia de detecção desconhecida.
Não foram alterados engine major/minor, thresholds, claims, concorrência, contratos
públicos, overlays ativos, main/prod ou recursos operacionais. Os bloqueios de
confiança, imagem atualmente selecionada, política e infraestrutura permanecem.

## Handoff

SEC-B-004 e SEC-B-005 têm implementação e testes negativos locais. SEC-B-003,
SEC-B-006 e SEC-B-008 receberam os controles descritos. SEC-B-009 recusa repetição
como nova aprovação e não duplica eventos em rescan; não promete autenticidade
ou proteção contra autoridade de escrita comprometida. SEC-B-002 permanece decisão
arquitetural bloqueante, SEC-B-007 depende de homologação distribuída, e SEC-B-001
continua bloqueando o digest selecionado. Nenhuma aprovação independente foi
emitida por este executor.

Pronto para Code Quality Re-Review e Security Re-Review direcionados aos arquivos
inventariados, com esses limites explícitos. Preparação posterior de commit/PR
exige nova instrução; não está pronto para merge/ativação enquanto os bloqueios
permanecerem. O [inventário final](clamav-signatures-1129-after-remediation.sha256)
identifica o delta completo (exclui apenas a si próprio). Não contém logs,
segredos, perfis de cobertura, binários ou artefatos temporários. O inventário
original foi preservado. Fixtures Docker/volumes próprios foram removidos;
imagens locais de teste ficaram no cache, sem push ou limpeza ampla.
