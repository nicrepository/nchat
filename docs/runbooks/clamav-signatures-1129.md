# Assinaturas ClamAV #1129 B — operação futura em nchat-dev

**Não executar deploy com esta entrega.** Artefatos locais, candidatos suspensos,
idade máxima sem aprovação, StorageClass/mirror/capacidade desconhecidos.
[Arquitetura, trust boundaries e limitações](../architecture/clamav-signatures-1129.md).
[Remediação e homologação distribuída](../architecture/clamav-signatures-1129-remediation.md)
definem os controles adicionais de clock, privilégios, confiança e testes em dois nós.
Nenhum comando deste documento se destina a main/nchat-prod. Mudança operacional
passa pelo fluxo GitOps revisado, não por edição manual de recursos Kubernetes.

## Validação local reproduzível

Somente fixtures sintéticas; imagens já presentes, sem pull automático:

```bash
go test ./services/file-service/...
go vet ./services/file-service/...
(cd services/file-service && golangci-lint run --config ../../.golangci.yml ./...)
go test -race ./services/file-service/internal/{scanner,signatures,signatureguard,service,storage,config,app}
bash scripts/testing/clamav-signatures-isolated.sh
python3 scripts/testing/test_clamav_signature_policy.py -v
CLAMAV_POLICY_TEST_POSTGRES=16 python3 scripts/testing/test_clamav_signature_policy.py -v
bash scripts/testing/build-clamav-signatures-local.sh
make migrations-check
K8S_OVERLAY=infra/k8s/overlays/nchat-dev-server bash scripts/ci/k8s-manifests-check.sh
make format-check
git diff --check
```

O script ClamAV compila testes, usa a imagem oficial fixada, rede none,
UID100/GID101, capabilities removidas, no-new-privileges, rootfs read-only,
3GiB, 2 CPUs, pids-limit64 e tmpfs limitada. Só binários/config sintéticos e
saída de cobertura são montados. Não monta anexos, credenciais ou docker socket.
HTTPS de teste usa loopback, CA efêmera confiada explicitamente, CVDs oficiais
da imagem, e simula 429. Não toca o CDN. EICAR só ocorre nesse container isolado.

PostgreSQL 16/17 usa containers próprios descartáveis, sem portas, sem rede,
sem credenciais do host, com tmpfs, CPU/memória/PIDs limitados. Schema projetado
exercita a migration real, não demonstra o plano completo de migrations do NChat.
SIGKILL do runner pode deixar container; remover somente o nome desta execução
com prefixo nchat-signature-policy-test-, sem comandos amplos de limpeza.

A cadeia integrada adicional usa PostgreSQL 17 em outro container sem rede,
alcançado somente por volume de Unix socket privado: reader cifrado real do
serviço, cliente guardião, clamd, store PGX e triggers reais. Storage de objeto e
claim usam doubles em memória, sem SeaweedFS nem anexos reais; não é baseline
nem teste de ownership da Entrega C.

O job Tests / Go Coverage executa esse runner após obter as duas imagens pinadas;
CLAMAV_SIGNATURE_COVERAGE_PROFILE retém o perfil somente depois do sucesso da
cadeia completa. O gate oficial mescla esse perfil no módulo file-service,
conservando threshold90 e o agregador CI / Required. Sonar consome os mesmos
artifacts; nenhum teste foi removido, pulado para aprovação ou executado contra
scanner compartilhado.

Build local produz apenas `nchat-clamav-signatures:local-test`, binário instalador
em scratch/UID100. Não publica registry nem aprova um digest. FreshClam e clamd
executam na imagem oficial pinada; init instala a ferramenta em volume separado,
que os runtimes montam read-only. CI de imagem/SBOM e digest de registry ainda
precisam de revisão e promoção autorizada.

Renderer de candidato exige valores explícitos. A referência de aprovação deve
cobrir **idade e sintéticos em ambiente isolado**. Não preencher com valores
fictícios para implantação. Ele imprime manifests, não aplica e não muda Git:

```text
python3 scripts/testing/render-clamav-signatures.py
  --helper-image <registry/repo@sha256:digest-aprovado>
  --storage-class <classe-RWX-validada>
  --approval <referencia-humana>
  --mirror <https://mirror-privado-aprovado>
  --mirror-cidr <IP-aprovado/32-ou-/128>
  --max-age <segundos-aprovados>
```

O resultado conserva `suspend: true` e `replicas: 0`. É apenas a unidade do
scanner, sem namespace/DB/file-service completos; integração no overlay deve
substituir a referência legada e os checks oficiais devem ganhar asserções do
protocolo novo antes da promoção. Não desabilitar/remover o gate atual de probes
legadas para fazer esse candidato passar.

## Sequência de rollout — depende de autorização futura

1. Coletar baseline real pelo [runbook A](clamav-baseline-1129.md), sem inventar
   medições; registrar imagens ativas, backlog, clocks UTC e recursos disponíveis.
2. Plataforma valida StorageClass RWX, capacidade e quota: 8Gi de PVC propostos,
   cache/gerações/staging, snapshot+scratch, fsGroup/subPath, attach após reboot,
   atomic rename e fsync no mesmo filesystem, locks entre nós e resize. Falhar
   fechado em teste real de ENOSPC/interrupção. Sem isso, não prosseguir.
3. Confirmar CNI e todas as NetworkPolicies aditivas. Testar DNS real e mirror
   único /32 ou /128 em443; comprovar recusa de outro destino, ausência de egress
   do consumidor e acesso3310 somente do componente file. Não usar FQDN como
   garantia de NetworkPolicy padrão. Confirmar podPidsLimit do kubelet aprovado.
4. Mirror aprovado serve exclusivamente CVDs oficiais, com HTTPS/CA confiável,
   sem CLD/custom signatures, credenciais de app, redirects para fora da allowlist
   ou acesso do scanner à Internet. O abastecimento do mirror é responsabilidade
   da plataforma e deve respeitar FreshClam/CDN; validar disponibilidade e limites.
5. Aprovar engine suportado e digests: imagem ClamAV exata, installer e file-service
   revisados. Vulnerabilidades HIGH/CRITICAL sem mitigação bloqueiam. Updater não
   pode mudar major/minor do engine. Mudança de engine requer código/CI/review.
6. Conferir UID/GID100:101, fsGroup101, rootfs/tools/PVC publicados read-only no
   consumidor, ack RW apenas no subPath, nenhum SA token/app Secret/anexo montado.
7. Humano aprova idade máxima e referência de decisão. Proposta48h não é aprovação.
   Alinhar segundos no guardião, cliente e files.signature_policy; confirmar clocks.
8. Confirmar a decisão arquitetural SEC-B-002: HTTP atual não autentica o Guardian
   e a role runtime não prova autenticidade do recibo no banco. Não ativar sem
   controle de confiança aprovado/implementado e nova revisão independente.
   Confirmar que nchat_app não é owner/superuser, não herda privilégios que
   reabram escrita de policy/evidência e só executa a função de aprovação nova.
   Ensaiar rollback com geração anterior ainda fresca, perda de update, boot sem
   PVC, erro de carga, queda do child e expiração durante scan/commit. Sem nenhuma
   base fresca, resultado correto é pendente, nunca fallback ao legado.
9. Via GitOps revisado: aplicar migration aditiva com policy disabled; preparar
   SA/NP/PVC e bootstrap updater controlado, sem habilitar autorização nova. Criar
   ack/ antes do consumidor. Confirmar assinatura e geração publicadas. Criar
   consumidor isolado com EICAR autorizado e aguardar prova real de carga.
10. Drenar workers antigos, habilitar policy DB aprovada e configurar clientes
    com FILE_MALWARE_SIGNATURE_MAX_AGE_SECONDS. Manter FILE_MALWARE_SCAN_REQUIRED=true
    e timeout300s. Policy enabled recusa writers antigos; cliente novo com daemon
    antigo e cliente antigo com guardian falham fechado. Promover unidade inteira
    sem interpretar essa indisponibilidade esperada como motivo para bypass.
11. Somente após evidência da cadeia, autorizar uma réplica e CronJob não suspensa
    no GitOps; manter Recreate. Verificar benigno/EICAR isolados, /readyz,/metrics,
    versões de disco/carregada/evidência no banco e preservação de geração anterior.
12. Testar update/mirror/DNS/offline/429 e expiração sem afetar usuários. Anexos
    sintéticos pendentes não podem ser entregues por download/Range/preview.
    Confirmar hashes e decrypt dos originais existentes antes/depois; não enviar
    arquivos de usuários para fora nem usá-los como corpus.
13. Observar no mínimo uma atualização e uma transição completas, capturando
    recursos, restart/drain, freshness/failed Jobs e manutenção de quarentena.
    Medir latência/capacidade separadamente da baseline sem atribuir resultados
    das fixtures ao ambiente real.
14. Abortar ao primeiro clean sem prova fresca, mismatch de engine/geração,
    assinatura inválida, egress indevido, permissão ampla, perda de original,
    lock/rename não coerente, OOM/ENOSPC ou esgotamento de quota. Fechar admissão,
    preservar pending e originais e executar rollback revisado. Não afrouxar idade.

## Rollback e recuperação

Atualização falha antes da publicação: state ativa/anterior permanece. Cache pode
conter parcial; FreshClam recupera/rebaixa download corrompido, staging nunca é
consumido. Próximo Job respeita cooldown persistente, não roda busy-loop.

Falha de ativação: nenhum /scan fica disponível antes de VERSION/PING/EICAR/benigno.
Não há alternância automática. Via Job GitOps explicitamente autorizado, executar
na identidade do updater a ferramenta instalada com `--mode=rollback`, store e
**a mesma idade aprovada**. Não expor endpoint de administração para isso.
O comando toma o mesmo lock, revalida assinatura/engine/flevel/idade da anterior,
grava marcador rejected da candidata, troca active/previous em uma transação de
state e limpa previous para impedir oscilação. Depois, consumidor reinicia,
revalida snapshot e prova carga/detecção. Geração rejeitada não é repromovida pelo
updater; aguardar um novo CVD válido. Marcadores preservam data para auditoria.

Atualizações sucessivas antes de ativar não podem apagar a última base carregada:
ack informa retenção sob lock compartilhado de ativação, e previous conserva a referência mesmo se ack se perder.
GC após promoção retém active, previous e ack; startup concorrente cujo diretório
foi coletado falha/reinicia. Interrupção depois de rename e antes de state usa
comparação do conjunto para retomar; diretório existente divergente fecha o
processo e requer investigação/restauração autorizada do PVC. Não apagar a única
base íntegra. Download/locks/gerações ficam limitados pelo PVC; disco cheio pode
impedir novos updates e exige recuperação de capacidade antes da expiração.

Nó/PVC indisponível: pod não fica pronto, banco recusa ausência de prova, originais
permanecem. Updater morto libera flock pelo kernel; Forbid e lock impedem dois
writers ativos sob a semântica de filesystem que precisa ser validada.

Nenhuma anterior fresca: manter gate DB habilitado e anexos pending_scan.
**Rollback não permite base vencida, retorno automático ao protocolo legado ou
FILE_MALWARE_SCAN_REQUIRED=false.** Rollback de código deve conservar implementação
que honre a policy/evidência. Down da migration é recusada com policy enabled;
só avaliar remoção do gate após parar todos os writers e aprovar uma proteção
substituta equivalente. Não é procedimento normal de rollback de assinaturas.

## Alertas candidatos (não instalados)

Coleta proposta15s, avaliação15s; Platform/SRE confirma labels reais e target dev.
Métricas ausentes nunca são tratadas como base saudável. Exemplos a adaptar:

| Condição                         | Expressão/sinal                                                                                 | Ação                                                              |
| -------------------------------- | ----------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| Base vencida                     | signature*age_seconds >= signature_max_age_seconds; names com prefixo nchat_clamav*             | Fechar admissões, recuperar update sem ampliar limite             |
| Próxima de vencer                | age/max_age >=0.75 por10min                                                                     | Investigar mirror/DNS/cache/quota                                 |
| Atualização repetidamente falha  | Failed Jobs de clamav-signature-updater + timestamp de sucesso sem avanço por duas janelas de2h | Inspecionar categorias/Kubernetes; respeitar cooldown             |
| Geração/carga incompatível       | signatures_ready=0 ou target ausente por10min                                                   | Verificar VERSION/manifest, flevel, sintéticos e child            |
| Ativação falhou                  | última terminação Error/OOMKilled, target/ready ausente                                         | Rollback revisado; reinício Completed por geração nova é esperado |
| Publicada diferente da carregada | state.active != ack.generation em coleta autorizada por mais do que janela de drain+load        | Investigar consumidor; não tratar download como reload            |
| Target ausente                   | up=0/absent no job específico dev                                                               | Sem aprovação de novos anexos; recuperar scanner                  |

Não há reload_errors exportado: ativação usa restart. Status dos Jobs, state,
ack e timestamps fornecem a distinção entre disponível/verificado/carregado/
apto. Integração real de scrape, alert rules e on-call depende da plataforma;
nenhuma porta nova ou scrape de prometheus foi liberado nesta implementação.

## Evidências e pendências

Resultados reais dos checks e limites estão no relatório de
[validação](../architecture/clamav-signatures-1129-validation.md). Não há
SonarQube disponível a este executor; não existe aprovação de Quality/Security
Review independente. A entrega não está concluída para implantação enquanto
política, infraestrutura, gates faltantes e revisões não forem resolvidos.

## Recuperação de clock e sinais de política

`policy_blocked` no contador nchat_file_malware_scans_total é erro operacional,
nunca superseded. Consultar error_type categórico do log, conferir migração/grants,
idade e aprovação; corrigir pelo fluxo revisado. Sem retry manual agressivo:
a fila retenta no lease/backoff existente, até44min por anexo. Não resetar claims.

Em clock rollback, os requests em voo são interrompidos; um resultado inteiramente
concluído antes da falha continua sujeito ao gate temporal do PostgreSQL. O ciclo
termina e espera a fonte temporal alcançar o watermark persistente. A perda do
HTTP durante essa espera é indisponibilidade conservadora, com thresholds/backoff
das probes existentes. Não remover /ack/clock.json nem loaded.json, restaurar
watermark antigo ou aumentar idade para obter readiness. Recuperar clock confiável,
verificar banco/nós e aguardar nova carga/sintéticos. Expiração durante recuperação
exige nova base fresca. Corrupção/perda do watermark requer investigação/recuperação
administrativa autorizada, sem bootstrap automático após ativação anterior.

Proposta de imagem corrigida, digests e evidências locais estão na remediação.
Nenhum digest foi promovido nos manifests nesta rodada; a imagem atual continua
bloqueando implantação, mesmo que a proposta passe nos testes isolados.
