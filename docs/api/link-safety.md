# Bloqueio de links maliciosos (RF-21)

Antes de mostrar a alguem uma mensagem que carrega um link, e antes de buscar a
pagina de um preview, o backend decide a **URL completa** -- scheme, host, path e
query -- no Cloudflare URL Scanner. URL reportada como maliciosa: a operacao e
recusada.

O provedor e submit-then-poll (`POST .../urlscanner/v2/scan` devolve um UUID;
`GET .../urlscanner/v2/result/{id}` responde 404 enquanto o scan roda, e a
Cloudflare recomenda polling a cada 10-30 s), entao o veredito de uma URL nova
nao existe dentro de um request interativo. RF-21 e portanto **assincrono**: uma
mensagem com URL ainda sem veredito e aceita e **retida** ate o scan liberar.

O backend e a autoridade. Bloquear o card no frontend nao satisfaz o requisito,
entao a recusa acontece no mesmo caminho server-side que persiste a mensagem --
chamar a API diretamente, pular o preview ou modificar o frontend nao muda nada.

## Onde o check roda

| Fluxo                                   | Servico      | Efeito                       |
| --------------------------------------- | ------------ | ---------------------------- |
| `POST /api/chat/channels/{id}/messages` | chat-service | malicious: nao criada; sem veredito: retida |
| `POST /api/chat/dm/{id}/messages`       | chat-service | malicious: nao criada; sem veredito: retida |
| `PATCH /api/chat/messages/{id}`         | chat-service | malicious: recusada; sem veredito: 409 `link_check_pending` |
| `POST .../messages/forward`             | chat-service | malicious: nao criada; sem veredito: retida |
| `POST /api/files/link-preview`          | file-service | Open Graph **nao** e buscado; sem veredito: 202 `link_check_pending` |

### Os tres resultados

- **cleared** -- toda URL ja tem veredito `safe` valido no cache: publica na
  hora, exatamente como antes de RF-21 existir;
- **malicious** -- qualquer URL condenada: nada e persistido, nada e publicado,
  HTTP 403 `malicious_url`;
- **pending** -- qualquer URL sem veredito: a mensagem e persistida com status
  `pending_link_scan`, que **todo** caminho de leitura ja exclui (`status =
  'active'`), e ninguem alem do remetente sabe que ela existe. O worker submete o
  scan, e quando todas as URLs sao decididas promove (uma unica vez) ou bloqueia.

A edicao e a unica excecao ao pending: reter uma *edicao* significaria mostrar a
todos um corpo nao verificado, ou manter o antigo enquanto se diz ao autor que
salvou. Ambos sao piores que pedir para tentar de novo -- a versao publicada fica
intacta e o scan que a classificacao acabou de enfileirar faz o retry funcionar.

Edicao e encaminhamento estao na lista de proposito. Um check so na criacao
seria contornado de duas formas triviais: enviar uma mensagem limpa e editar o
link depois, ou encaminhar uma mensagem antiga -- escrita antes do check existir,
ou enquanto ele estava desligado -- para um canal onde ela nunca passou por um.
Encaminhar cria uma mensagem **nova**, entao passa pelo mesmo gate.

### Encaminhamento sem TOCTOU

O forward acontece nessa ordem:

0. resolver a idempotency key contra o que ja existe
   (`LookupForwardReplay`) -- se a mensagem ja foi criada, ela e devolvida aqui,
   sem snapshot, sem escrita e **sem consultar o provedor**;
1. ler o snapshot do conteudo de origem (`SnapshotForwardableMessage`) -- leitura
   simples, sem transacao, sem `FOR SHARE`, sem conexao reservada;
2. verificar esse snapshot;
3. persistir **exatamente** esse snapshot.

O passo 0 vem antes de qualquer chamada externa porque um retry legitimo pede a
mensagem que ja existe, nao a criacao de outra: um veredito que mudou para
malicious depois do envio original, ou um provedor fora do ar, transformaria o
replay de uma mensagem ja persistida em recusa. Nada novo e publicado ali, entao
nada novo precisa ser verificado. A key reusada para outra origem continua
gerando `ErrConflict` -- a mesma regra do statement de forward, aplicada antes do
upsert em vez de depois. A chave e escopada por workspace, sender e canal, entao
ela nao devolve a mensagem de outra pessoa.

Duas primeiras tentativas concorrentes podem ambas errar o passo 0 e ambas
verificar. Isso e aceito: o indice unico continua sendo a unica autoridade, uma
so mensagem e criada, e a outra recebe replay. Um lock distribuido para evitar
uma consulta duplicada em corrida rara nao se paga.

Duas propriedades vem dessa ordem. A chamada externa ao provedor nunca acontece
com lock de linha ou transacao aberta. E o INSERT recebe os bytes ja verificados
em vez de reler `source.body_text`, entao uma edicao concorrente da origem nao
consegue trocar o conteudo entre a verificacao e a escrita -- no maximo o forward
leva a versao (verificada) que foi lida.

A autorizacao nao se separa da escrita: o snapshot aplica os mesmos predicados
de origem que o statement de forward aplica, e a autorizacao de destino continua
inteira dentro do statement atomico. O cliente segue enviando apenas o ID de
origem; corpo e formato nunca vem do payload.

O mecanismo e um so, em `libs/go/platform/urlsafety`, importado pelos dois
servicos. Nao ha RPC novo entre eles: o padrao aqui e o mesmo de
`libs/go/platform/uploadpolicy` -- uma regra decidida uma vez e aplicada em dois
lugares que nao podem discordar.

## Veredito

Tres estados, nunca dois:

| Veredito    | Origem                                                                         | Resultado |
| ----------- | ------------------------------------------------------------------------------ | --------- |
| `safe`      | o provedor respondeu e nao reportou risco                                      | permite   |
| `malicious` | o provedor reportou pelo menos uma categoria de risco                          | bloqueia  |
| `unknown`   | timeout, status inesperado, corpo invalido, `success:false`, credencial errada | bloqueia  |

**Erro tecnico nunca vira `safe`.** Um booleano tornaria "o provedor nao
respondeu" indistinguivel de "o provedor disse que esta tudo bem", e uma queda
da Cloudflare viraria sinal verde silencioso. O tipo tem `unknown` como valor
zero exatamente para que um veredito nunca atribuido nao leia como liberado.

### Fail-closed

Falha, timeout, scan ainda processando, `success:false`, JSON invalido, JSON com
lixo depois do documento, veredito desconhecido: **nada disso vira safe**. O scan
permanece pendente, a mensagem permanece retida, e o worker tenta de novo com
backoff. RF-21 e Must Have: um controle que se desliga justamente quando seu
provedor esta fora e um controle que o atacante pode agendar.

### Granularidade

O veredito e **por URL canonica**, nao por host. Estas tem chaves distintas:

```
https://example.com/
https://example.com/login
https://example.com/redirect?to=https://evil.example
```

Estas compartilham chave (o fragmento nunca chega ao servidor de origem):

```
https://example.com/file#um
https://example.com/file#dois
```

A canonicalizacao (`urlsafety.CanonicalizeURL`) e conservadora de proposito:
lowercase de scheme e host, punycode, porta default removida, path vazio vira
`/`, fragmento removido. Path e query passam **byte a byte** -- nao sao
ordenados, decodificados nem recodificados, porque qualquer uma dessas coisas faz
duas URLs que o servidor de origem trata de forma diferente compartilharem um
veredito.

Domain Intelligence (`/intel/domain`) nao participa deste caminho. Ele responde
sobre um *dominio*, que e exatamente a granularidade em que RF-21 nao pode
decidir. Um fast deny por host esta na issue #526.

### Privacidade da submissao

Toda submissao pede `visibility: "Unlisted"`. Uma URL canonica carrega path e
query, que e onde vivem identificadores internos e nomes de recursos, e o default
da API (`Public`) listaria o scan publicamente. O corpo enviado tem exatamente
dois campos, `url` e `visibility`: nao ha campo que pudesse carregar cookie do
usuario, o `Authorization` do NChat ou qualquer header interno.

### Hosts sem reputacao consultavel

Endereco IP literal (`http://93.184.216.34/...`), nome sem ponto, nome acima do
teto de DNS: nao ha dominio a consultar. Sao **bloqueados**, com o mesmo erro
permanente de um dominio condenado. Ignora-los faria de "hospede na porta 80 de
um IP" o caminho obvio para passar pelo check.

### IDN

O host e convertido para punycode antes da consulta, porque e esse o nome que
existe no DNS. Perguntar ao provedor pela grafia Unicode retornaria vazio para
um dominio que ele conhece perfeitamente pelo A-label -- um homografo se
liberaria sozinho.

## Contrato de erro

Codigos estaveis; o frontend reconhece por `code`, nunca por texto.

| Servico      | Status | Codigo                   | Quando                                            |
| ------------ | ------ | ------------------------ | ------------------------------------------------- |
| ambos        | `403`  | `malicious_url`          | link condenado, ou host sem reputacao consultavel |
| ambos        | `503`  | `link_check_unavailable` | veredito nao pode ser obtido -- **retentavel**    |
| chat-service | `400`  | `bad_request`            | mais de 10 dominios distintos na mesma mensagem   |

`403` e `503` sao deliberadamente diferentes: um e permanente e o outro pede
nova tentativa. Dizer "tente de novo" para um link bloqueado e tao errado quanto
dizer "seu link e malicioso" para uma queda temporaria.

A resposta nunca diz **qual** link foi recusado, **qual** categoria o provedor
reportou, nem repete corpo, status ou endereco do provedor: a diferenca entre
"recusado" e "recusado porque a categoria X" e um oraculo gratuito para conferir
se um dominio ja esta queimado.

Mensagem ao usuario (definida no frontend, em portugues):

- `malicious_url` -> "Este link foi bloqueado por seguranca."
- `link_check_unavailable` -> "Nao foi possivel verificar a seguranca do link.
  Tente novamente em instantes."

O composer preserva o rascunho: `sendMessage` rejeita, e o editor so limpa em um
`sent` confirmado.

## Multiplas URLs

- todas sao verificadas;
- **um** veredito malicioso recusa a operacao inteira, e o loop para ali -- a
  mensagem ja esta recusada e as consultas restantes so gastariam quota;
- a deduplicacao e por host, entao vinte links para o mesmo site sao uma
  consulta;
- **10 dominios distintos** por mensagem e o teto. Acima disso a mensagem e
  recusada como entrada invalida, e nao truncada: verificar os dez primeiros e
  ignorar o resto faria de "coloque um decimo primeiro link" um bypass
  documentado. O teto existe porque o corpo vai a 40.000 runes e um corpo grande
  nao pode virar fan-out de chamadas ao provedor.

### Extracao

O corpo e lido como o leitor vai ve-lo. As gramaticas v2/v3 armazenam escapes de
barra invertida, entao `my\-site.example` renderiza `my-site.example` -- varrer
a string crua simplesmente nao veria o link, e "escape um hifen" seria um bypass
trivial. O corpo e desescapado pela mesma regra do cliente
(`apps/web/src/chat/richTextMarkers.ts`) antes da varredura. Uma barra invertida
que a gramatica nao escapa permanece, entao desescapar nunca fabrica um link que
nenhum leitor veria.

Pontuacao final (`.,;:!?)]}'"<>`) volta para a frase: "veja
https://example.com." aponta para o site, nao para um host com ponto no fim.

## Cache

Em processo, com TTL finito e numero maximo de entradas, chaveado pelo **host
normalizado inteiro** -- sem truncar, sem hash, sem casar sufixo. Isso e o que
torna impossivel um host herdar o veredito de outro.

| Entrada              | TTL         |
| -------------------- | ----------- |
| `safe` e `malicious` | 15 minutos  |
| falha (`unknown`)    | 30 segundos |

O TTL de `safe` e finito porque reputacao muda: um dominio comprometido depois
de liberado nao pode ficar congelado. Falha e cacheada **como falha** -- a
entrada continua recusando; ela existe para que uma queda do provedor custe uma
consulta por host a cada meio minuto em vez de uma por mensagem.

Cancelamento do chamador nao e cacheado nem contado: nao e uma resposta sobre o
host.

### O TTL do veredito manda sobre o cache de preview

No link preview a reputacao e consultada **antes** do cache de Open Graph, nao
depois. Se fosse depois, as duas vidas compunham na ordem errada: o preview e
reutilizado por ate 24 h e o veredito so vale 15 min, entao um cache hit
continuaria servindo o card de uma URL cuja reputacao ja tinha virado. Perguntar
primeiro faz a vida mais curta ser a autoridade -- expirado o veredito, a
proxima requisicao reconsulta, e uma URL que virou maliciosa e bloqueada mesmo
com a entrada Open Graph ainda valida.

Isso nao vira uma chamada a Cloudflare por request: quem responde e o cache de
veredito acima. Requisicao morna = uma consulta a um mapa.

Nenhum dos dois valores e configuravel, e isso e proposital: sao a semantica de
um veredito compartilhado por dois servicos, e um knob por servico deixaria
chat-service e file-service discordarem sobre por quanto tempo um host esta
liberado. O endpoint tambem nao e configuravel -- um endpoint fornecido pelo
operador seria uma forma de apontar um controle de seguranca para algo que
sempre responde "safe".

Duas limitacoes conhecidas, ambas aceitas e ja documentadas para o cache de
preview: o cache e por processo (N replicas consultam ate N vezes), e nao ha
coalescencia de chamadas em voo. A segunda e gentileza com a quota do provedor,
nao um controle de seguranca.

## Relacao com a politica SSRF

**Sao controles distintos e nenhum e a permissao do outro.**

- a politica SSRF do link preview responde _se este backend pode abrir conexao
  para aquele destino_ -- julgada pelo IP que a conexao vai usar, revalidada em
  cada redirect;
- o Safe Browsing responde _qual a reputacao daquele dominio_.

Um host pode ter otima reputacao e ainda ser um endereco privado que este
servico jamais deve alcancar. Nada em RF-21 relaxa qualquer regra descrita em
[link-preview.md](./link-preview.md): esquemas, portas, credenciais na URL,
validacao de DNS, limite de redirects, allowlist de Content-Type, tetos de corpo
e timeouts continuam identicos, e um destino recusado pela politica e recusado
sem o provedor ser consultado.

A ordem tambem importa: o check de reputacao roda **antes** do fetch Open Graph.
Buscar primeiro e perguntar depois seria renderizar a pagina de phishing.

## Configuracao

Tres variaveis por servico, todas em `.env.example`:

| file-service                             | chat-service                             |
| ---------------------------------------- | ---------------------------------------- |
| `FILE_LINK_SAFETY_ENABLED`               | `CHAT_LINK_SAFETY_ENABLED`               |
| `FILE_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID` | `CHAT_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID` |
| `FILE_LINK_SAFETY_CLOUDFLARE_API_TOKEN`  | `CHAT_LINK_SAFETY_CLOUDFLARE_API_TOKEN`  |

Desligado por default nos dois. O token e exclusivamente server-side: nunca vai
ao frontend, nunca aparece em log, erro ou resposta, e nunca entra na query
string (vai no header `Authorization`).

Flag ligada e credencial ausente **falha no start-up nos dois servicos**. So
existem tres estados:

- flag `false`: o check esta desligado de proposito, o checker pode ser `nil`, e
  o fluxo de mensagem/preview nao consulta ninguem;
- flag `true` com credenciais validas: o checker existe e e obrigatorio;
- flag `true` sem credencial, ou com configuracao que impede construir o
  checker: erro de configuracao, o servico nao sobe.

Nao ha quarto estado. `checker == nil` significa uma unica coisa -- a flag esta
desligada -- entao nao existe deployment que rode acreditando que os links sao
checados enquanto nada e checado. No chat-service isso e garantido duas vezes:
`Config.Validate` recusa a configuracao, e `wireLinkSafety` devolve erro (nao
loga e segue) se a construcao do checker falhar por qualquer motivo futuro.

Isso e diferente de **falha em runtime**. Provedor fora do ar depois que o
servico subiu nao e erro de configuracao: o checker existe, `Service.Check`
devolve erro, e a politica fail-closed continua valendo -- a mensagem e recusada
com `link_check_unavailable`.

### Kubernetes

As flags nao sao secret e ficam no `ConfigMap` do overlay onde RF-21 deve estar
ativo (`infra/k8s/overlays/nchat-dev-server`, ambiente-alvo do MVP), com
`CHAT_LINK_SAFETY_ENABLED` e `FILE_LINK_SAFETY_ENABLED` em `"true"` e assercao
em CI para que nao voltem ao default.

As credenciais ficam no Secret `nchat-link-safety`, referenciado apenas pelos
Deployments de chat-service e file-service -- fora de `nchat-secrets`, que todo
servico monta com `envFrom`. Template em
`infra/k8s/secrets/templates/nchat-link-safety.template.yaml`; selagem em
[sealed-secrets-rotation.md](../runbooks/sealed-secrets-rotation.md).

A chamada ao provedor sai do cluster, e `nchat-default-deny-egress` cobre todos
os pods, entao a NetworkPolicy `nchat-allow-link-safety-egress` libera TCP/443
para a internet publica -- excluindo faixas privadas, loopback, link-local e
reservadas -- apenas para esses dois componentes.

Valor **inelegivel** na flag e outra coisa e falha nos dois servicos.
`CHAT_LINK_SAFETY_ENABLED=enabled` nao vira `false` silenciosamente: `Load` marca
a configuracao como invalida e `Config.Validate` -- chamada no inicio de
`app.New`, antes de qualquer fiacao -- devolve
`CHAT_LINK_SAFETY_ENABLED must be a valid boolean`. Um controle de seguranca que
se desliga por causa de um typo nao tem sintoma nenhum alem de nunca bloquear
nada. Ausente aplica o default documentado; `true`/`false` (e `1`/`0`) funcionam
normalmente. A mensagem nomeia a variavel e nunca o valor.

## Observabilidade

`nchat_url_safety_checks_total{result}`, conjunto fechado: `hit`, `safe`,
`malicious`, `error`, `not_checkable`. Registrada no file-service.

No chat-service as recusas aparecem como
`nchat_http_requests_total{route,status}` nas duas rotas de mensagem -- o
registry servido pertence ao router, construido depois da fiacao do servico, e
atravessa-lo ate o bootstrap so para uma segunda copia do mesmo contador nao se
paga. Ha um comentario `ponytail:` em `wireLinkSafety` indicando a mudanca caso
o breakdown por veredito seja necessario ali.

**Nunca sao label**: URL, hostname, user ID, token, query string ou resposta do
provedor. O chamador escolhe o valor consultado, entao uma label derivada dele
deixaria um cliente hostil criar uma serie por request.

Nenhum log carrega a URL bruta, o host, o token ou o corpo do provedor.
