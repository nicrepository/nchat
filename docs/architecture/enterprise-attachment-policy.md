# Anexos empresariais: design, ameaças e rollout (#1128)

## Contratos observados e decisão

O checkout, na branch `feature/files-1128-enterprise-formats`, já usa PostgreSQL
para autorização/quota/admission, SeaweedFS para objetos NCF1 cifrados, AES-GCM
por chunk e DEK vinculada a objeto/workspace/tamanho. Uploads começam como
`pending_upload`; a finalização agenda scan e preview na mesma statement.
ClamAV é assíncrono; somente `clean` é entregável. Claims usam SKIP LOCKED/lease;
rejeição e preview compartilham a fence existente. Não há deduplicação/resume nem
sweep geral de uploads órfãos; a compensação existente preserva o ponteiro quando
o delete falha.

Problemas confirmados: o gate antigo admitia ZIP/GZIP/CFBF só por cabeçalho; não
validava Office nem a totalidade de JSON/XML. O download de áudio podia converter
silenciosamente para MP3, alterando bytes e extensão do suposto original.
Preview já tinha objeto/DEK próprios. Não foi identificada necessidade de mudar
chat-service, barramento, storage ou formato criptográfico.

Fluxo final:

```text
JWT + sessão + destino → quota/admission → streaming limitado
→ sinal de cabeçalho → pending_upload + objeto original cifrado
→ classificação estrutural limitada do objeto autenticado
→ política versionada + nova autorização do destino
→ pending_scan → revalidação de política no worker → ClamAV completo
→ transição atômica clean/rejected → download original autorizado
                                  → preview derivado independente
```

O pacote `filepolicy` contém catálogo, decisões e parsers coesos de texto, e-mail,
ZIP e Office. Não conhece SQL, objetos, chaves ou scanner. `AttachmentService`
coordena admissão/persistência/compensação. `MalwareScanService` revalida o objeto
persistido e exige EOF autenticado antes de aceitar um `OK` exato do daemon.
Resultado desconhecido, timeout, protocolo inválido ou indisponibilidade não
produzem aprovação. Política futura desconhecida permanece pendente; recusa
definitiva de política produz `rejected`, distinguida de malware na telemetria.

Alternativas descartadas: allowlist ampliada de extensões; aceitar contêiner
genérico como Office; spool de plaintext; buffer integral de objetos grandes;
classificar só no worker antigo; parser próprio CFBF; motor de regras/DSL; nova
fila AV; novo framework de flags. A inspeção usa acesso aleatório autenticado do
envelope existente, com buffers limitados. Sua leitura adicional no upload é o
custo explícito de identificar estrutura antes de finalizar; o worker relê para
revalidar a política no momento da aprovação.

O frontend não mantém allowlist de formatos: o seletor permite seleção e o
backend decide. A blacklist existente é somente UX; por isso não foi criada uma
API de capabilities nova. Não houve mudança visual nem reescrita do composer.
Erros normalizados diferenciam tipo não suportado, bloqueio de política,
conteúdo inválido, limite de inspeção e indisponibilidade. Status assíncronos
existentes continuam distinguindo pendente/rejeitado/clean.

## Threat model de implementação

Assets: bytes corporativos, permissões/sessões, metadados, chaves, banco,
disponibilidade e storage. Fronteiras: browser/gateway/backend; parser/objeto;
backend/ClamAV; worker/conversor; tenants/workspaces. Nenhum nome vira chave de
storage ou argumento de processo.

| Ameaça / caminho                        | Controle aplicado                                                                                                           | Evidência automatizada                                                                                     |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| CWE-434, spoofing e extensão composta   | Catálogo fechado, MIME não autoriza, estrutura completa quando declarada, desconhecido não liberado                         | Matriz de classificação; PDF/PE; ZIP arbitrário como XLSX; corpus nativo                                   |
| Macros, OLE, senha, referência externa  | Perfis passivos, CFBF/macros opacos recusados; XML/relações/manifest inspecionados                                          | Casos OOXML/ODF ativos, EML multipart/HTML, password flags                                                 |
| CWE-22 / Zip Slip / parser differential | Nomes seguros, duplicados/case, tipos de entrada, diretório/header/offset/CRC e consumo DEFLATE concordantes; sem extração  | Guardas ZIP, corrupção, symlink, offset e metadados adversariais                                           |
| CWE-400 / decompression bomb            | Budgets fixos por entrada, total, ratio, número, nesting, nomes e tempo; streaming                                          | Fronteiras; expansão; cancelamento; fuzz seed corpus                                                       |
| CWE-79 / conteúdo ativo e fórmulas      | Original só attachment/nosniff; XML/HTML/SVG ativos recusados; preview neutro; fórmulas de planilha recusadas               | XML ativo, CSV/TSV, fórmulas e browser download sem preview                                                |
| CWE-918 / XXE                           | Sem resolver XML externo, DTD/entidades/XInclude/PI recusados; relações externas bloqueadas; classificadores não usam rede  | XML/Office com referências externas                                                                        |
| CWE-862/863 / cross-tenant              | Autorizador existente deriva workspace; sessão/membership relidas na finalização e em cada leitura                          | Revogação após autorização; round-trip HTTP/SQL; Range após revogação; suites existentes de autorização    |
| CWE-367 / retry e resultado atrasado    | Claims/leases existentes, status condicionado; rejeição não reabre; constraint de versão impede aprovação por worker antigo | Testes existentes de concorrência, remoção/retry/idempotência; aprovação SQL antiga recusada em integração |
| Scanner parcial/offline/inconclusivo    | Resposta limpa exata e consumo até EOF autenticado; demais resultados não aprovam                                           | Scanner TCP, resposta antecipada/valor desconhecido, outage e EICAR reais                                  |
| Substituição/corrupção de objeto        | DEK vinculada à identidade/workspace/tamanho; chunks autenticados e objeto original nunca reescrito                         | Tampering, truncamento, tamanho/binding inválido; SHA-256 e Range reais                                    |
| Preview como bypass de quarentena       | Mesmo gate de autorização/scan; DEK/ID/objeto separados; original nunca usa preview                                         | Todos os endpoints pendentes; regeneração real de oito previews seguida de download íntegro                |
| CWE-502                                 | Não há desserialização de objetos executáveis; JSON/XML são dados, YAML não é liberado                                      | JSON/XML estruturais e tipos opacos recusados                                                              |

Limitações reais: assinatura de mídia e guardas lexicais PDF não são validação
semântica integral nem detecção universal de polyglots. AV negativo não prova
segurança. Egress/isolamento dos processos de conversão permanecem os existentes;
nenhum novo parser CAD ou CFBF é executado. O perfil deliberadamente recusa muitos
documentos com recursos ativos legítimos. A [matriz](../api/supported-file-types.md)
é a descrição exata das garantias, não uma promessa de compatibilidade SAP total.

## Integridade e transporte

Não foi adicionado hash de plaintext ao banco. O envelope já autentica tamanho,
identidade e sequência de chunks, e a chave protegida impede editar o tamanho
sem falhar antes da resposta. Hash persistido duplicaria esse controle e criaria
um identificador correlacionável de documentos. Os testes calculam SHA-256 apenas
do corpus sintético e comparam bytes/tamanho no HTTP real após cifrar e escanear.

`/attachments/{id}/content` devolve sempre o original nativo, inclusive áudio.
`/attachments/{id}/audio.mp3` é um derivado explicitamente solicitado, usando o
conversor existente, com nova checagem de autorização/scan após conversão. Nenhuma
ação apresentada como download original usa esse endpoint ou `/preview`. O nome
seguro é preservado, inclusive caixa, Unicode, espaços e extensão. Controles e
formatos Unicode invisíveis são removidos da apresentação; nomes inválidos são
recusados/normalizados pelo contrato existente, sem tocar no conteúdo.

Range mantém o contrato de intervalo único de net/http. Os segmentos são bytes
do original, inclusive em fronteiras de chunks. Um range não pode verificar a
cauda que não leu. Se a integridade falha após headers, o stream termina antes do
Content-Length; não é possível substituir o status HTTP já enviado. Não há
fallback para preview, conteúdo parcial "corrigido" nem buffer irrestrito.

## Persistência e N/N+1

Migration expansiva `files/000019` adiciona `format_policy_version`, `format_id`
e `format_scan_version`, com defaults zero/vazio para escritores antigos.
Linhas novas registram a versão desde `pending_upload`. A finalização grava
formato/versão junto com a chave e o tamanho. `clean` exige versão de scan pelo
menos igual à de admissão. O worker atual só aprova versões que conhece.

| Superfície         | N → N+1                                                                       | N+1 → N / rollback                                                                                   |
| ------------------ | ----------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Banco              | Defaults aceitam escritor antigo; versão zero identifica legado               | Colunas aditivas; leitores antigos continuam lendo metadados conhecidos                              |
| Worker             | N+1 revalida pendências legadas, sem ampliar formatos por extensão            | N não consegue aprovar upload v1 mudando apenas status; permanece indisponível até worker compatível |
| Original           | Envelope, chave, object key e tamanho não mudam                               | Office/dados/ZIP usam o mesmo formato de storage; original não depende de novo enum                  |
| Preview            | Mesmo armazenamento/fence; adaptador de MIME canônico para renderer existente | Estados e eventos não mudam; consumidores desconhecendo preview usam fallback existente              |
| Auth/eventos/cache | Contratos existentes                                                          | Sem novo evento, enum, lock distribuído ou formato de cache                                          |
| Flags              | Novas chaves opcionais, false por padrão                                      | N ignora as chaves, mas o banco mantém a fence de aprovação                                          |

**Limitação importante de rollback de áudio:** o binário anterior converte
implicitamente áudio no endpoint original. Nenhuma migration pode corrigir seu
código. Para exigir o novo contrato de original em todos os nós, primeiro atualizar
todos os leitores para este baseline com perfis desligados; só depois habilitar
novas famílias. Um rollback para o binário anterior não oferece a nova garantia
de original de áudio. Não foi simulado um rehearsal completo de dois releases.

## Ativação restrita e rollback

1. Aplicar a expansão de schema; manter os três perfis false.
2. Atualizar leitores e workers no ambiente autorizado `nchat-dev` com os perfis
   desligados e drenar todas as instâncias anteriores à #1128. Confirmar que nenhum
   worker antigo é usado como caminho de aprovação v1 antes de ativar perfis.
3. Executar o corpus sintético e verificar métricas de admissão, inspeção, scan
   e backlog. Habilitar Data, Office e Archives separadamente, após as revisões
   independentes e validação operacional. Este trabalho local não ativa flags.
4. Para suspender novas admissões, desligar o perfil em **todas** as réplicas e
   drenar workers com configuração anterior. Pendências são revalidadas; versões
   antigas não podem aprovar v1 pela constraint. Desligar flag não revoga originais
   legados já clean; revogação de segurança usa a transição de rejeição existente.
5. Rollback de aplicação mantém o schema expandido e a fence. A migration down
   adquire lock e recusa remover metadados enquanto existir qualquer linha v1.
   Não executar down para fazer rollback de aplicação.

Não houve commit, push, PR, merge, deploy, alteração de `main` ou acesso
operacional a `nchat-prod`. O alvo operacional autorizado é somente `nchat-dev`.

## Reprodução local da integração

Usar PostgreSQL/SeaweedFS/ClamAV descartáveis, migrations completas em banco
`*_test` e o conversor do repositório. A suite exige explicitamente as variáveis
abaixo; ausência ou serviço indisponível é skip, nunca evidência de validação.

```bash
cd services/file-service
FILE_TEST_DATABASE_URL='postgresql://nchat@127.0.0.1:55128/nchat_1128_test?sslmode=disable' \
FILE_TEST_SEAWEEDFS_URL=http://127.0.0.1:58130 \
FILE_TEST_CLAMD_ADDRESS=127.0.0.1:53130 \
FILE_TEST_CONVERTER_URL=http://127.0.0.1:58129 \
go test ./internal/service -run '^TestIntegrationEnterprise' -v -count=1 -timeout=180s
```

O teste usa sessão/JWT e autorização PostgreSQL reais, admission control real,
cliente SeaweedFS real, envelope real, worker e ClamAV reais. O corpus em
`internal/filepolicy/testdata` contém somente dados sintéticos. EICAR é criado
durante o teste, não armazenado como malware real. A suite limpa apenas seus
UUIDs e objetos no ambiente descartável.

## Correções de segurança S-001 a S-005

`libs/go/platform/documentguard` concentra o parsing limitado de XML, relações
OPC e resolução de referências de pacote usado por admissão, preview e conversor.
Não contém catálogo de formatos, SQL ou lógica de aprovação antimalware. Os dois
serviços já dependiam de platform; não há dependência runtime ou schema novo.
O worker AV continua revalidando pela mesma política de admissão. O conversor
inspeciona independentemente o pacote antes de chamar LibreOffice.

Todas as variantes de caixa de `.rels` recebem o parser específico, com namespace,
atributos OPC não qualificados, tipo de relação e destino. O Type deve ser URI
HTTP(S) canônica, sem escaping, whitespace ou sufixos de query/fragmento; tipos
personalizados fora dessa política não são liberados. Destinos internos são
resolvidos relativamente à parte de origem e precisam existir com nome exato.
Referências remotas, absolutas, opacas, queries, escapes do pacote, barras invertidas,
percentuais ambíguos e `xml:base` são recusados. XML é UTF-8, sem DTD/resolver externo,
com profundidade 64 e 64 atributos. Os consumidores mantêm budgets de 2.048 entradas,
8 MiB por entrada, 32 MiB expandido e razão 100; os budgets menores da admissão v1
continuam vigentes. Referências xlink usam a URI do namespace, com qualquer prefixo.
Fragmentos locais e recursos internos existentes são permitidos; XML standalone só
admite fragmentos. Worksheets da política v1 exigem namespace Transitional e recusam
fórmulas, inclusive em destinos fora de `xl/worksheets/`. Strict permanece recusado.
Previews legados de planilhas apenas leem valores cacheados, sem avaliação.

Áudio derivado usa admissão não bloqueante antes de decriptar/ler buffers. Uma
operação por instância file-service é conservadora para 512 MiB RAM/64 MiB scratch;
o slot permanece reservado até fechar o reader do download. O conversor, com
500m CPU/768 MiB RAM/256 MiB work, admite um áudio por handler e protege o runner
com o mesmo limite finito de processos, inclusive chamadas internas. ffmpeg usa
uma thread de codec/filtro, limite de saída em disco e o timeout existente de
40 s no executável. Cancelamento mata o grupo de processos; stdout/stderr são
descartados, temporários removidos. Não há espera ou fila não limitada. Cada
réplica concede seu próprio slot; todas compartilham a proteção do conversor
que recebe a chamada, sem prometer uma quota global distribuída. MP3 pronto
não precisa transcodificar. Range do derivado continua exigindo geração integral;
rate limit e admissão limitam custo, sem cache persistente ou singleflight.

Regeneração após falha usa cooldown de 24 horas no CAS existente, baseado em
`updated_at`, atualizado por todas as transições terminais. Isso limita novas
rodadas de três tentativas por attachment, inclusive após reinício; alterações
conservadoras de metadata podem alongar a espera. Pedidos concorrentes não reiniciam
uma rodada pendente. Expiração normal (30 dias) conserva regeneração imediata.
Não há mudança de estados/colunas/migration 000019. Binaries antigos não aplicam os
novos guardas: atualizar e drenar todos os consumidores antes de habilitar famílias.

Evidências e limitações estão em
[correções de segurança](../testing/enterprise-attachment-security-corrections.md).
