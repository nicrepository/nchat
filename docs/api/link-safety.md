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

| Fluxo                                   | Servico      | Efeito                                                               |
| --------------------------------------- | ------------ | -------------------------------------------------------------------- |
| `POST /api/chat/channels/{id}/messages` | chat-service | malicious: nao criada; sem veredito: retida                          |
| `POST /api/chat/dm/{id}/messages`       | chat-service | malicious: nao criada; sem veredito: retida                          |
| `PATCH /api/chat/messages/{id}`         | chat-service | malicious: recusada; sem veredito: 409 `link_check_pending`          |
| `POST .../messages/forward`             | chat-service | malicious: nao criada; sem veredito: retida                          |
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

**Editar uma mensagem `pending_link_scan` e proibido.** A regra de elegibilidade
e uma allowlist (`active` e o unico estado editavel), nao um "tudo menos
deleted": aplicar a edicao publicaria `message.updated` com o novo corpo para
todos os inscritos no destino, que e exatamente a entrega que a retencao existe
para impedir. Erro: `403 edit_forbidden`, sem revelar detalhe de Link Safety.

**Malicious e inconclusive tem evento terminal.** A promocao e o bloqueio
escrevem em `chat.message_publish_outbox` na mesma transacao que muda o
estado:

- safe → `message.created`, destinado ao canal/DM;
- malicious → `message.blocked`, destinado **somente ao remetente**
  (`target_type = 'sender'`), com `reason = malicious_link` e nada mais — sem
  corpo, sem URL, sem scan UUID, sem resposta do provedor;
- inconclusive → o mesmo `message.blocked`, mesma audiencia, com
  `reason = link_check_inconclusive`. Ve "Inconclusivo" abaixo para a origem
  desse estado.

Sem isso o autor ficava para sempre em "verificando", porque o backend levava a
mensagem a estado terminal e nao contava a ninguem.

**A promocao e vinculada ao conteudo.** `chat.messages.link_safety_fingerprint`
e um SHA-256 de (versao, corpo verificado, conjunto ordenado de URLs canonicas),
carimbado tambem em cada associacao. A promocao e um compare-and-set que exige
mensagem ainda pending **e** fingerprint igual, entao um veredito obtido para um
conteudo nunca publica outro. Defesa em profundidade: a edicao de pending ja e
proibida, mas essa regra e uma linha que uma mudanca futura poderia relaxar.

**Freshness vale tambem no worker.** Uma URL so conta como safe se
`decided_at + VerdictTTL > now()`. Um veredito vencido nao promove e tambem nao
prende: `ReopenExpiredVerdicts` devolve a URL para a fila (apenas as que alguma
mensagem retida aguarda), e a mensagem promove quando chegar veredito fresco.
**Escopado a `safe`/`malicious` apenas** -- ve "Inconclusivo" logo abaixo para
o porque de `inconclusive` ficar de fora dessa reabertura.

### Inconclusivo

O incidente que este estado corrige: a Cloudflare responde HTTP 200,
`task.status=finished`, `task.success=false`, `hasVerdicts=false` -- o scan
**terminou**, mas nao produziu nenhum veredito utilizavel. Antes disso ser um
estado proprio, essa resposta caia em `ErrUnavailable`, que e retentavel por
design: o worker fazia poll do mesmo scan ja finalizado a cada passagem, para
sempre, e toda mensagem com aquela URL ficava presa em `pending_link_scan`
indefinidamente.

`urlsafety.ErrScanInconclusive` e o que a camada do provedor reporta para essa
resposta especifica, distinto de `ErrUnavailable` de proposito: um e "pergunte
de novo", o outro e "este scan especifico esta decidido, e a decisao e que ele
nao decide nada". O worker so alcanca esse erro a partir de um poll -- nunca de
um submit -- e so depois de confirmar que a resposta pertence ao scan que ele
esperava.

**Duas perguntas diferentes, e a separacao entre elas e a politica (issue
#135).** A primeira versao deste estado bloqueava a mensagem: `deleted` mais um
`message.blocked` com `reason = link_check_inconclusive`. Isso e fail-closed nas
duas perguntas ao mesmo tempo, e a segunda resposta estava errada. A resposta
real da Cloudflare em producao foi

```
"Refusing to scan: hostname was recently scanned or too many scans to hostname
 in the last days."
```

uma recusa **operacional** do provedor, que nao diz absolutamente nada sobre o
link. Recusar a mensagem de um usuario legitimo por causa disso e um bug de
produto.

Entao as duas perguntas ficam separadas:

| Estado         | Mensagem  | Clique no link | Preview server-side |
| -------------- | --------- | -------------- | ------------------- |
| `pending`      | retida    | n/a            | **NAO**             |
| `safe`         | publicada | sim            | **SIM**             |
| `malicious`    | bloqueada | nao            | **NAO**             |
| `inconclusive` | publicada | sim            | **NAO**             |

A linha `inconclusive` e assimetrica de proposito, e a assimetria e a feature:

```
                     conteudo da mensagem
inconclusive  -----------------------------> pode ser publicado

                     fetch server-side da URL
inconclusive  -----------------------------> PROIBIDO
```

`inconclusive` significa **"este deployment nao tem liberacao para executar essa
URL automaticamente"** -- uma afirmacao sobre a nossa propria autoridade, nao
sobre o link. O navegador do leitor nao e o nosso servidor.

Nada no codebase pode inferir permissao de fetch a partir de "a mensagem foi
publicada". `domain.MessageLinkSafety.AllowsServerFetch()` e um allowlist de um
unico valor (`safe`), e o file-service reavalia o veredito na sua propria base a
cada request de preview -- contra a linha, nunca contra o que um payload de chat
disse.

**Clicabilidade do link.** Uma mensagem publicada renderiza suas URLs http(s)
como ancoras normais. Isso e navegacao do navegador do leitor, iniciada por um
clique -- nao e preview, nao e fetch, e nao ha nada no cliente que consulte o
endereco para decidir como desenha-lo.

| Estado         | Ancora no cliente   | Aviso acima da mensagem                                    |
| -------------- | ------------------- | ---------------------------------------------------------- |
| `pending`      | nao (nao publicada) | "Verificando seguranca dos links..." (so o autor)          |
| `safe`         | **sim**             | nenhum                                                     |
| `inconclusive` | **sim**             | "Nao foi possivel verificar este link agora..."            |
| `malicious`    | **nao**             | "Este link foi bloqueado apos a verificacao de seguranca." |

A autorizacao e `linkSafetyState`, **nunca** `status` sozinho, e é um
**allowlist** (CQ-004):

```
linksClickable = status === "active" && !isRemoved &&
                 (linkSafetyState === "safe" || linkSafetyState === "inconclusive")
```

Um allowlist e não "ativa e não maliciosa", porque o estado vazio existe: a
migration 000027 deu `link_safety_state = ''` a toda mensagem pré-existente, e um
deployment com o scan desligado não produz outra coisa. Essas mensagens nunca
foram verificadas por nada. Tratar "não sabidamente ruim" como "boa o bastante
para virar link" transformaria o histórico inteiro em âncoras sem evidência
nenhuma. Um valor desconhecido para este cliente também cai no lado literal.

| `link_safety_state` | Âncora              |
| ------------------- | ------------------- |
| `safe`              | sim                 |
| `inconclusive`      | sim, com aviso      |
| `malicious`         | não, corpo retirado |
| `""` (legado)       | não, texto literal  |
| desconhecido        | não, texto literal  |

**Delimitadores balanceados.** Um fechamento é devolvido à frase apenas quando
não é balanceado dentro do candidato. `https://ex.test/wiki/Função_(x)` mantém o
`)`; `(https://ex.test/foo)` não. A contagem é textual, então `%29` não participa
— não é um parêntese para o leitor. Heurística de fronteira, não parser de
Markdown.

**A mesma regra roda nos dois lados, e isso é obrigatório.** `trimTrailingDelimiters`
existe idêntica em `autolink.ts` e em `scanURLCandidates`
(services/chat-service/internal/service/link_safety.go), porque uma decide o que
vira ancora e a outra decide o que é **escaneado**. Enquanto elas divergiam,

```
texto:    https://example.test/wiki/Function_(mathematics)
escaneado: https://example.test/wiki/Function_(mathematics    <- backend
ancora:    https://example.test/wiki/Function_(mathematics)   <- cliente
```

ou seja, um link clicável para um endereço que o Link Safety nunca viu.

**Invariante, verificada por um corpus compartilhado.** Para toda ancora `H` que
o cliente renderiza, `H` tem que constar entre os candidatos que o backend extrai
do mesmo texto:

```
frontendHrefs(text) ⊆ backendCandidates(text)
```

`libs/testdata/link-safety/autolink-corpus.json` é a fonte única das
expectativas. O teste Go assere `scanURLCandidates(input) == backendCandidates`,
o teste TS assere `findAutolinks(input) == frontendHrefs`, e **ambos** assere a
inclusão acima. Duas listas mantidas separadamente voltariam a divergir — foi
assim que o bug entrou.

**Under-link é aceitável; over-link não.** O cliente pode desenhar menos links do
que o backend escaneia, e faz isso de propósito onde os dois parsers podem
discordar. Ele recusa a ancora quando o candidato contém:

| Caso                          | Por quê                                                                                                                                                    |
| ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| barra invertida               | WHATWG normaliza `\` para `/` em esquema especial, então `https://good.test\@evil.test` tem host `evil.test` no navegador e `good.test` no `net/url` do Go |
| escape percent inválido       | `url.Parse` do Go recusa; WHATWG aceita literalmente                                                                                                       |
| bidi / zero-width             | o rótulo renderizado deixaria de ser o endereço resolvido                                                                                                  |
| credenciais                   | seção de autoridade que se lê como hostname é disfarce clássico                                                                                            |
| esquema dentro de outro token | `shttp://`, `blob:https://` — não é um link que o remetente escreveu                                                                                       |

Nesses casos o backend continua extraindo e escaneando (direção conservadora
para um controle de segurança) e o cliente simplesmente não oferece o clique.

Nada disso e derivado de uma consulta a URL. `autolink.ts` reconhece os prefixos
literais `http://` e `https://` e mais nada -- `javascript:`, `data:`, `file:`,
`blob:` e o resto nunca sao _casados_, em vez de serem filtrados por uma
denylist que alguem teria que manter completa. `new URL()` reconfere o protocolo,
o hostname tem que existir, credenciais na URL sao recusadas, e o `href` e o
texto exato que o leitor ve -- uma ancora que aponta para lugar diferente do
endereco impresso ao lado dela e um primitivo de phishing por si so. Ancoras
abrem em nova aba com `rel="noopener noreferrer"`.

Um `RichTextRenderer` so linkifica quando recebe `linksClickable`, cujo default e
`false`: um novo ponto de uso (preview de citacao, card de referencia, historico
de edicao) renderiza URLs como texto ate alguem decidir o contrario, em vez de
herdar uma permissao que ninguem pensou.

**Granularidade: agregado, nao por URL.** O estado e da mensagem, nao de cada
link. A consequencia esta escolhida na direcao segura: se **qualquer** URL da
mensagem for maliciosa, o agregado e `malicious` e **nenhuma** ancora e
renderizada -- o corpo inteiro e retirado. Uma URL maliciosa nunca fica clicavel
porque outra da mesma mensagem e inconclusive; o preco e que uma URL safe na
mesma mensagem tambem deixa de ser clicavel, que e o lado conservador do
trade-off.

Um modelo por URL exigiria transportar o par (URL canonica, estado) por mensagem
e o cliente casar o texto renderizado com a URL canonica -- o que significa
reproduzir `CanonicalizeURL` em TypeScript, incluindo punycode de IDN. Uma
divergencia ali torna links legitimos nao-clicaveis (fail-closed, mas quebrado
para dominios IDN de forma rotineira). O ganho seria apenas manter uma URL safe
clicavel numa mensagem que ja contem uma maliciosa, e isso nao paga o risco de
divergencia. Se um dia pagar, o lugar da mudanca e o payload da mensagem mais
`RichTextRenderer`, e o default fail-closed ja esta no lugar certo.

**Onde o estado mora.** `chat.messages.link_safety_state` (migration 000027):
`''`, `safe`, `inconclusive` ou `malicious`. E um eixo separado de `status`, que
continua significando `pending_link_scan` / `active` / `deleted` -- juntar os
dois foi exatamente o que produziu o bug. Uma mensagem inconclusive e `active`,
por isso todo destinatario a recebe.

Deliberadamente uma coluna e nao um valor derivado em tempo de leitura:
`EnsureLinkScans` legitimamente devolve uma linha vencida para `pending` quando
alguem reenvia a mesma URL, e nessa janela uma mensagem **publicada** cujo link
ja tinha sido provado malicious derivaria como irrestrita.

O que continua igual ao que era: `inconclusive` nunca vira uma liberacao.
`LoadVerdict`/`LoadLinkVerdicts` continuam devolvendo apenas `safe`/`malicious`,
e todo fetch server-side continua exigindo `safe` explicito e fresco.

**Mencoes funcionam normalmente.** Uma mensagem inconclusive foi publicada,
entao suas mencoes sao tao reais quanto as de qualquer outra e sao liberadas na
mesma transacao da promocao. Uma notificacao nomeia uma mensagem, nao busca uma
URL -- nenhum pipeline downstream ganha por isso uma autoridade que a propria
mensagem nao tem.

**Aplica-se apenas para frente.** As mensagens que a versao anterior transformou
em `deleted` por um link inconclusive continuam deletadas. Nao ha backfill de
proposito: republica-las faria mensagens historicas aparecerem, sem aviso e fora
de ordem, para destinatarios que nunca as viram.

**Mencoes de mensagem retida sao adiadas, nao perdidas.** Elas ficam em
`chat.message_pending_mentions` e sao movidas para `chat.notification_outbox` na
mesma transacao da promocao. Mensagem bloqueada nunca gera notificacao.

A edicao e a unica excecao ao pending: reter uma _edicao_ significaria mostrar a
todos um corpo nao verificado, ou manter o antigo enquanto se diz ao autor que
salvou. Ambos sao piores que pedir para tentar de novo -- a versao publicada fica
intacta e o scan que a classificacao acabou de enfileirar faz o retry funcionar.

Edicao e encaminhamento estao na lista de proposito. Um check so na criacao
seria contornado de duas formas triviais: enviar uma mensagem limpa e editar o
link depois, ou encaminhar uma mensagem antiga -- escrita antes do check existir,
ou enquanto ele estava desligado -- para um canal onde ela nunca passou por um.
Encaminhar cria uma mensagem **nova**, entao passa pelo mesmo gate.

### Maquina de estados

```
sem URL no corpo                      -> active
URL com veredito safe valido          -> active
URL com veredito malicious            -> bloqueada (403 malicious_url)
URL sem veredito                      -> pending_link_scan

worker: qualquer URL malicious            -> deleted, message.blocked (reason=malicious_link),
                                            link_safety_state=malicious
worker: todas terminais, alguma inconclusive -> active + message.created,
                                            link_safety_state=inconclusive
worker: todas terminais e safe            -> active + message.created,
                                            link_safety_state=safe
worker: alguma URL ainda indecidida       -> permanece pending_link_scan
worker: provedor indisponivel/scan rodando -> permanece pending_link_scan, retry com backoff
```

**Agregacao sobre multiplas URLs**, explicitamente:

```
enquanto QUALQUER URL ainda puder ser condenada:
    mensagem continua pending

quando todas forem terminais:
    QUALQUER malicious    -> bloqueada
    senao QUALQUER inconclusive -> publicada com aviso
    senao                 -> publicada normalmente

precedencia: pending  >  malicious > inconclusive > safe
```

Uma URL e _terminal_ quando e uma liberacao fresca, uma condenacao fresca, ou
inconclusive. Ela **nao** e terminal enquanto esta `pending`, nem quando seu
veredito `safe`/`malicious` ja passou do `VerdictTTL`.

Uma mensagem com uma URL inconclusive e outra ainda em scan **nao e publicada**:
a pendente ainda pode voltar malicious. Antes de #135 isso nao importava --
inconclusive significava "recusar", entao decidir cedo era inofensivo. E
justamente publicar em cima de inconclusive que torna obrigatorio esperar todas.

Malicious continua podendo decidir a mensagem sozinho, antes das outras
terminarem. Nao e o mesmo risco ao contrario: o resultado e uma recusa, e nenhum
veredito posterior torna uma recusa errada.

`pending_link_scan` nao e visivel: toda leitura de mensagem exclui esse estado,
com uma unica excecao escopada ao proprio remetente (`sender_id = requester`),
para que o cliente dele reconstrua "verificando" apos um reload. As projecoes
laterais -- sidebar, last activity, favoritos -- sao `active`-only, sem excecao:
uma mensagem retida nao reordena a lista de ninguem.

A promocao e o evento sao **uma transacao**. Antes eram um COMMIT seguido de um
publish best-effort, entao um crash entre os dois deixava uma mensagem ativa que
ninguem foi informado que existia. Agora `chat.message_publish_outbox` recebe a
linha no mesmo statement, e um dispatcher entrega depois. A entrega e
**at-least-once**: o mesmo `message.created` pode chegar duas vezes, e o cliente
deduplica por message id. Exactly-once de rede nao existe e nao e afirmado aqui.

### Garantias, ditas com precisao

Quatro afirmacoes distintas, porque confundi-las e como se afirma entrega
confiavel sobre um transporte que nao a oferece:

| Camada                     | Garante                                                                    | Nao garante                   |
| -------------------------- | -------------------------------------------------------------------------- | ----------------------------- |
| Banco                      | e a fonte de verdade do estado da mensagem                                 | nada sobre entrega            |
| `message_publish_outbox`   | que o evento e **processado** no backend, sobrevivendo a crash e a restart | que algum cliente o recebeu   |
| WebSocket                  | entrega em tempo real, best-effort                                         | entrega, ordem ou recebimento |
| Reconciliacao no reconnect | **convergencia do cliente** para o estado autoritativo                     | latencia                      |

O outbox nao e transformado em ACK de browser. Guardar o evento ate um navegador
confirmar traria sessao, multi-device, cleanup e contabilidade de entrega para
dentro de uma tabela cujo trabalho e outro. O cliente se recupera perguntando.

### Garantia de submissao ao provedor

Cloudflare URL Scanner **nao oferece idempotency token**. Nao existe chave de
requisicao que faca um POST repetido devolver o scan original, e nada aqui
inventa uma. Isso importa porque submeter sao dois passos que nao podem ser um:

```
POST ao provedor   -> provedor aceita e devolve uuid
UPDATE link_scans  -> o uuid passa a ser nosso
```

Um crash entre os dois deixava uma URL que **foi** submetida e uma linha que nao
consegue provar isso. Antes, `scan_uuid IS NULL` significava as duas coisas, e a
recuperacao submetia de novo -- um scan logico virava dois scans cobrados, a cada
restart que caisse na janela.

A correcao registra a **intencao antes** da chamada, o que separa os tres casos:

| Linha                                                        | Significado              | O que o worker faz            |
| ------------------------------------------------------------ | ------------------------ | ----------------------------- |
| `scan_uuid IS NULL`, `submit_attempt_started_at IS NULL`     | nunca submetida          | submete                       |
| `scan_uuid IS NULL`, `submit_attempt_started_at IS NOT NULL` | **outcome desconhecido** | reconcilia, **nunca** submete |
| `scan_uuid IS NOT NULL`                                      | submetida e confirmada   | faz poll                      |

Erro do provedor cai no estado do meio de proposito: o client nao distingue
"Cloudflare recusou" de "Cloudflare aceitou e a resposta se perdeu" -- as duas
aparecem como troca falha. Tratar isso como "nada aconteceu" e exatamente como se
compra o duplicado.

Sequencia completa:

```
reservar capacidade compartilhada de submit
-> BeginSubmit (intencao duravel, generation++)
-> POST Cloudflare
-> RecordSubmission com CAS em (status, scan_uuid IS NULL, generation)
   |
   +- falhou o write: retry local curto e limitado (3x), lease ainda em maos
   +- esgotou: linha fica uncertain. Isso NAO e resubmit.
```

Maquina de estados final:

```
submit_pending --(BeginSubmit)--> submitting --(uuid persistido)--> polling
                                      |
                                      +--(outcome desconhecido)--> submit_uncertain
                                                                        |
                                              +-- Search encontra scan --+
                                              |        (CAS)             |
                                              v                          |
                                           polling                       |
                                                                         |
                                              nada encontrado / erro ----+
                                                        (continua uncertain)

polling --> safe | malicious
```

**`submit_uncertain -> submit_pending` nao existe.** Nao ha aresta, nao ha
metodo de store, nao ha branch no worker e nao ha configuracao. E estrutural, nao
um default.

**Garantias, ditas com precisao:**

- **exactly-once local: sim** -- mensagem, job duravel, transicao de estado e
  evento de outbox. O CAS e o indice unico decidem, nao a ausencia de erro;
- **exactly-once no provedor: nao afirmado**. Nao existe mecanismo que prove isso
  sem suporte do provedor, e o Cloudflare nao oferece idempotency token;
- o que se afirma, e isso e verificavel: **para uma generation, o NChat executa
  no maximo um Submit automatico**. Falha de persistencia nao gera outro.
  Restart nao gera outro. Retry do worker nao gera outro. Search nao gera outro.
  Passagem do horizonte nao gera outro. Nenhuma configuracao gera outro.

Isso e diferente de dizer "o Cloudflare recebeu exatamente uma requisicao de
rede": um timeout de transporte ambiguo pode ter entregue bytes que a aplicacao
nunca soube que chegaram. O que se garante e sobre o comportamento do NChat, nao
sobre o que aconteceu no cabo.

Nao escreva "duplicate scan impossible" em lugar nenhum. Nao e verdade.

### Reconciliacao da submissao incerta

Antes de qualquer nova submissao, o provedor e perguntado se o scan que ele pode
ter aceitado ja existe:

```
GET /accounts/{id}/urlscanner/v2/search?q=task.url:"<url>"&size=10
```

- **escaping**: a URL e valor escolhido por quem enviou e entra numa sintaxe de
  filtro. Aspas e barra invertida sao escapadas; caractere de controle e recusado
  em vez de codificado, porque uma URL canonica nao pode conter um e newline e a
  forma classica de anexar clausula. Testado com aspas, backslash, percent
  encoding, Unicode e tentativa explicita de injetar `OR task.url:`;
- **escopo**: o path e da propria conta, entao nao existe resultado de outra
  conta para filtrar;
- **identidade**: so e adotado um scan cuja `task.url` canonicaliza para a mesma
  URL, cujo `visibility` e o que este client submete (`Unlisted`), com uuid
  presente e timestamp nao anterior a tentativa (com tolerancia de clock);
- **multiplos matches**: o provedor nao promete unicidade. Havendo varios
  elegiveis da mesma URL, o mais recente e adotado deterministicamente e a
  ambiguidade e contada em metrica -- normalmente significa que um bounded
  resubmit anterior realmente criou um duplicado;
- **not found**: nao e "nao foi aceito". Indexacao remota nao e sincrona, entao a
  linha continua incerta e pergunta de novo;
- **erro / 429 / 5xx / timeout**: tambem nao e ausencia. Continua incerta,
  backoff, **sem submit**;
- **adocao**: CAS em (status, `scan_uuid IS NULL`, generation). A busca recupera
  um **id**; o veredito continua vindo do endpoint de result, que e onde mora a
  rigidez que decide uma liberacao. Reconciliacao nunca e um segundo caminho
  para um veredito;
- **cadencia**: a propria claim ja aplica backoff exponencial limitado
  (`lease x min(attempts, steps)`), entao a reconciliacao nao vira busy loop e
  uma linha que ninguem consegue resolver assenta em uma tentativa a cada poucos
  minutos;
- **horizonte**: `SUBMIT_UNCERTAIN_TIMEOUT` **nao libera acao nenhuma**. Passado
  ele, a unica coisa que muda e a metrica ganhar `result=stale`. A reconciliacao
  continua no mesmo ritmo e nenhuma submissao e feita, porque nenhum tempo
  decorrido transforma "o provedor pode ja ter esse scan" em "e seguro mandar
  outro".

A busca nao registra em log a query, a URL nem o uuid.

### O tradeoff, explicito

Esta escolha prioriza **idempotencia externa conservadora** sobre **progresso
automatico em submissao ambigua**.

A consequencia e real e nao esta escondida: se o Cloudflare aceitou o scan e a
aplicacao nao consegue recuperar o uuid pela Search API, aquela URL permanece
indecidida. As mensagens que dependem dela seguem retidas e o preview segue
respondendo "verificando". Isso pode durar indefinidamente.

A alternativa era comprar um segundo scan por uma submissao que provavelmente ja
existe, num provedor sem idempotency token -- e deixar essa decisao numa variavel
de ambiente que um operador pode aumentar. A metrica e o runbook sao como esse
caso e resolvido; reiniciar worker explicitamente nao e, porque restart nao
resubmete.

### Reconciliacao de veredito (inconclusive -> safe/malicious)

O unico caminho de saida do estado `inconclusive`, e ele nunca submete.

**Search nao e autoridade de veredito.** A busca por scans da propria conta serve
para descobrir um **scan id** e nada mais. A resposta da busca carrega um campo
de veredito resumido; ele nunca e lido -- `urlsafety.ScanRecord` nao tem campo
onde ele caberia, entao "usar o veredito da busca" nao e um bug possivel, e um
tipo que nao existe. Encontrado um candidato, o relatorio completo e lido pelo
endpoint de resultado normal, que aplica exatamente as mesmas verificacoes
estritas de um poll comum: identidade (`task.uuid` == o id pedido),
`task.success`, `verdicts.overall.hasVerdicts`, presenca de `malicious`.

```
inconclusive
    |
    v
Search (escopado a conta, URL canonica EXATA)
    |
    +-- nenhum candidato        -> continua inconclusive
    |
    +-- candidato valido
            |
            v
       GET result/{uuid}   (mesmas validacoes fortes de um poll)
            |
            +-- safe valido      -> safe
            +-- malicious valido -> malicious
            +-- sem veredito     -> continua inconclusive
```

**Escopo da busca.** O path do provedor e `/accounts/{account_id}/...`, entao a
resposta ja e confinada aos scans que as credenciais deste deployment criaram --
nao existe resultado de outra conta para filtrar. Em cima disso, um candidato so
e adotado se:

- a URL relatada **canonicaliza exatamente** para a URL armazenada. Nao ha
  correspondencia por hostname: `https://youtube.com/` nunca herda o resultado de
  `https://youtube.com/watch?v=...`;
- a visibilidade e a que este cliente submete (`unlisted`) -- um scan publico nao
  e nosso, porque nada aqui submete publico;
- o scan nao e mais antigo que a janela de lookback;
- o uuid existe e nao e vazio.

**Por que nao ha `apikey:me`.** O filtro mais estreito seria restringir a busca
aos scans criados por _este token_. Ele nao esta implementado, e a decisao e
deliberada:

- o path ja e `/accounts/{account_id}/...`, entao a resposta so contem scans da
  conta deste deployment -- nao existe resultado de outra conta para filtrar. O
  que `apikey:me` acrescentaria e distinguir tokens _dentro_ da mesma conta, e
  adotar um scan criado por outro token da propria conta NChat, da URL canonica
  exata, lido pelo relatorio completo com todas as validacoes estritas, nao e um
  cenario de ameaca -- e o mesmo scan que teriamos criado;
- a identidade real e estabelecida pela correspondencia **exata** da URL
  canonica, nao pelo escopo da busca. O escopo reduz o conjunto; a igualdade
  decide;
- o filtro de visibilidade (`unlisted`) ja exclui scans publicos, que este
  cliente nunca cria;
- e o custo do erro e assimetrico: a v2 search nao documenta `apikey` entre seus
  filtros de forma que se possa confirmar sem exercitar a API de producao, e uma
  query invalida vira HTTP 400, que este cliente trata como `ErrUnavailable`. O
  resultado seria uma reconciliacao que nunca funciona -- fail-closed, mas
  silenciosamente inutil.

Se um dia houver confirmacao de suporte, o lugar da mudanca e uma linha em
`scanSearchQuery`, e os testes de escaping ja cobrem a forma do termo.

**Escaping da query.** A URL e input do usuario e vai para dentro de um termo
entre aspas na sintaxe de filtro do provedor. Ela e escapada para aquele destino
(aspas e barra invertida), e caracteres de controle sao **recusados** em vez de
codificados -- uma URL canonica nao pode conter um, entao a presenca de um
significa que o valor nao veio de onde deveria. Testado em
`cloudflare_search_test.go`.

**Janela de lookback vs. frescor do veredito.** São duas coisas diferentes, e
confundi-las era o finding CQ-001.

A busca olha 24h para trás, deliberadamente: a URL sendo reconciliada é
justamente uma que o provedor já se recusou a escanear de novo, então o único
candidato que pode existir é mais antigo. Isso decide **o que é elegível para ser
lido**.

O frescor do veredito adotado é decidido separadamente, pelo **timestamp do
próprio provedor**:

```
providerVerdictTime = task.timeEnd, senão task.time, senão SubmittedAt da busca
expiresAt           = providerVerdictTime + VerdictTTL
```

`task.timeEnd` é preferido porque um veredito se forma quando o scan termina, não
quando entrou na fila; `task.time` é o fallback documentado; e a hora de
submissão do candidato é o último recurso — um scan não pode ter terminado antes
de começar, então substituí-la só pode fazer a evidência parecer **mais velha**,
nunca mais nova. Se nenhum dos três existir, não há frescor honesto a atribuir e
nada é liberado.

**O horário da adoção nunca é autoridade de frescor.** Numericamente, com
`VerdictTTL = 15min`:

```
scan SAFE concluído  T0
Search o encontra    T0 + 23h
adoção               T0 + 23h
expiresAt            = T0 + 15min   -> já passou -> NENHUMA liberação
```

`ErrEvidenceExpired` é o resultado, o row continua `inconclusive`, e o servidor
continua proibido de buscar. Com evidência de 5min, a liberação vale os 10min
restantes — nunca 15 de novo. Uma segunda adoção minutos depois descreve o mesmo
instante e portanto tem menos vida, não mais.

Timestamp do provedor no futuro é **capado em `now()`**: um relógio adiantado não
pode cunhar uma liberação mais longa que `VerdictTTL`.

**MALICIOUS é retido a partir da adoção, e isso é deliberado.** Uma restrição não
é uma permissão: retê-la além da idade da evidência não concede nada e só mantém
uma URL comprovadamente ruim recusada por mais tempo. Datá-la para trás faria o
row envelhecer imediatamente para fora da janela de frescor de todo leitor, o que
**descartaria** a descoberta — o oposto de conservador. E a proibição de fetch em
si não depende desse timestamp: ela vive na denylist compartilhada, que é
permanente.

**Nunca um novo Submit.** Estrutural, nao por disciplina: a interface que a
reconciliacao recebe (`service.LinkVerdictReconciler`) tem **um** metodo, e ele
nao e `Submit`. Nada em `link_reconcile.go` (chat) ou no passe de recuperacao do
file-service limpa `scan_uuid`, zera `attempts`, escreve `pending` ou toca
`submit_generation`. `submit_uncertain` tambem continua sem resubmissao
automatica, exatamente como antes.

#### Onde a reconciliacao roda

Dois caminhos, com orcamentos separados:

| Caminho               | Disparo             | Limite                                                 |
| --------------------- | ------------------- | ------------------------------------------------------ |
| Background            | worker, a cada 1min | 4 tentativas por URL, backoff 5min / 30min / 2h / 6h   |
| "Verificar novamente" | leitor clica        | 6 req/min por usuario **e** 1 busca por URL por minuto |

**O bloqueio no backend e imediato e independente do fan-out.** Quando
`ReconcileLinkVerdict` commita, `chat.link_scans` ja carrega o novo veredito --
e e essa linha, nao a atualizacao das mensagens, que `LoadLinkVerdicts` le no
caminho de envio e que todo gate fail-closed consulta. Uma URL provada maliciosa
passa a recusar mensagens novas naquele instante, antes de qualquer linha de
mensagem ser tocada, e nenhum lote ou teto pode atrasar isso.

O que vem depois -- recalcular `link_safety_state` das mensagens ja entregues e
avisar os clientes -- e convergencia de _renderizacao_. Ela e drenada em lotes
ate nao restar nada (`RefreshMessageLinkSafety` so devolve as linhas que
realmente mudou, entao o laco termina sozinho). Nao existe teto fixo de passes:
cancelamento do contexto e deteccao de lote repetido/sem progresso sao os
backstops de seguranca. Uma URL carregada por milhares de mensagens converge dentro da mesma
reconciliacao, e o pior caso residual e um aviso ainda nao removido ou um
bloqueio ainda nao desenhado numa mensagem cujos links o file-service ja recusa
buscar.

O background so olha URLs que estao **agora** fazendo uma mensagem publicada
exibir o aviso -- uma URL que ninguem aguarda nao e reconciliada, porque a
resposta seria correta, inutil e paga no provedor.

O limite por usuário usa o mesmo limitador Valkey compartilhado que as demais
ações do chat (`nchat:chat:action:link_safety_reconcile:<hash do user id>`,
INCR+EXPIRE atômico em Lua). Um limitador em memória estaria errado aqui de um
jeito que escala com o deployment: daria a cada réplica o orçamento inteiro, ou
seja N pods admitiriam N × a taxa pretendida na única rota disparada por usuário
que chega a um terceiro pago. O user id é hasheado dentro do limitador, então
nenhum identificador chega ao Valkey, a um log ou a uma métrica. Um limitador que
não responde é **recusa**, não passe livre.

O contador de tentativas do background e consumido pelo claim **haja ou nao
resposta do provedor**, e nada o reabastece. E isso que faz o passe terminar: nao
existe configuracao de falhas que o transforme num loop infinito de Search. O
clique de um leitor deliberadamente **nao** gasta esse orcamento -- uma pessoa
perguntando e evidencia que a resposta e desejada, coisa que um timer nao e --
mas divide com ele o cooldown por URL, entao um canal inteiro clicando num aviso
custa uma busca.

#### `POST /api/chat/messages/{messageID}/link-safety/reconcile`

Autenticado. Corpo vazio.

O cliente nomeia **uma mensagem que ja pode ler**, e mais nada. Ele nao envia
URL e nao envia scan uuid: ambos sao lidos do banco, a partir das associacoes
gravadas quando a mensagem foi criada. Um cliente que pudesse nomear uma URL
teria transformado isso num proxy de URL Scanner utilizavel por qualquer um,
pago por este deployment. Nao existe variante escopada por URL e nao existe
"Forcar scan".

Autorizacao: a mesma leitura de mensagem de sempre -- workspace ativo,
membership ativa, e visibilidade de canal ou participacao no DM. Uma mensagem
que o chamador nao pode ler, uma que nao existe e uma que nao tem link
inconclusive respondem **todas** 404: distingui-las tornaria o endpoint um
oraculo de ids.

```json
{ "data": { "link_safety_state": "inconclusive", "retry_after_seconds": 60 } }
```

Sem URL, sem uuid, sem texto do provedor, sem contagem de links. Nada na resposta
diz que um scan foi iniciado, porque nenhum e.

#### `message.link_safety_changed`

```json
{
  "type": "message.link_safety_changed",
  "target_type": "channel",
  "target_id": "...",
  "message_id": "...",
  "link_safety": { "message_id": "...", "state": "safe" }
}
```

Enderecado a **conversa**, nao ao autor -- diferente de `message.blocked` --
porque a mensagem foi entregue e todo mundo que a tem precisa convergir.

Nao e um segundo `message.created`: reemitir a mensagem a duplicaria na timeline
de cada cliente e dispararia suas mencoes de novo. Este evento muta **um campo**
de uma mensagem que o cliente ja possui, o que o torna idempotente sob entrega
at-least-once -- uma correcao repetida, ou uma que concorda com o que ja esta
desenhado, e um no-op.

Ao chegar pelo bus, o `state` e revalidado contra o conjunto fechado
(`safe`/`inconclusive`/`malicious`) e o `message_id` do bloco tem que ser o do
envelope. Uma instancia remota nao pode injetar um estado que esta versao nao
entende.

#### Cooldown compartilhado de reconciliação: `files.link_reconcile_leases`

chat-service e file-service guardam rows `inconclusive` separados para o mesmo
endereço, com chaves diferentes (chat pela URL canônica, files pelo SHA-256
dela), e reconciliam em agendas separadas. Os cooldowns de cada um eram
invisíveis para o outro, então isto era alcançável dentro de um mesmo minuto:

```
T0  a passada do chat reivindica X, chama Search e depois GET
T0  a passada do files reivindica o mesmo X, chama Search e depois GET
```

Dois Search e dois GET, na mesma conta, para uma pergunta só — e a segunda
resposta é necessariamente cópia da primeira, porque reconciliação lê um scan que
**já terminou**.

**A lease.** Uma linha por `url_digest` em `files.link_reconcile_leases`, tomada
por quem chegar primeiro, válida por `urlsafety.ReconcileLeaseTTL` (1 minuto).
Ela autoriza **gastar uma tentativa no provedor** e nada mais: não é clearance de
fetch, não é veredito, e um row vencido é simplesmente disponível.

**Por que não na denylist.** A denylist é insert-only de propósito: uma linha lá é
condenação permanente. Um cooldown é o oposto — temporário, expira, e não concede
nada. Misturar os dois colocaria um row que expira exatamente na tabela cuja
garantia é que nada expira para permissão.

**Onde o gate fica.** Dentro do statement de claim, não depois dele. Em ambos os
serviços é o claim que gasta o orçamento (`reconcile_attempts + 1`), então checar
a lease em Go depois seria tarde: o orçamento já teria ido embora numa chamada que
nunca aconteceu. `urlsafety.AcquireReconcileLeaseSQL` é composta como CTE no
próprio claim, e uma URL que o outro serviço está lendo simplesmente não é
retornada. **Um worker bloqueado não consome tentativa.**

**Por que upsert com `WHERE` e não SELECT seguido de INSERT.** `ON CONFLICT ... DO
UPDATE ... WHERE leased_until <= now()` relê a versão mais recente do row sob o
lock que o conflito já tomou — é compare-and-set, não read-then-write. Dois
serviços tentando o mesmo digest no mesmo instante produzem um vencedor e um
ausente do `RETURNING`.

`TestCrossServiceReconcileLeasePostgreSQL` corre os dois claims **reais** em
conexões separadas, liberadas juntas, e falha se a soma dos claims não for
exatamente 1 ou se o perdedor tiver gasto tentativa.

#### Autoridade global de fetch: `files.link_fetch_denylist`

`chat.link_scans` e `files.link_scans` são autoridades independentes com TTLs
independentes, e isso tornava esta sequência alcançável:

```
T0  files.link_scans tem SAFE para X, com TTL restante
T1  chat prova X MALICIOUS
T2  um preview lê files.link_scans, ainda vê SAFE, e faz fetch de X
```

Consistência eventual não resolve: a requisição que o veredito existe para
impedir é a próxima.

**Invariante global.** Depois que qualquer componente NChat persiste um veredito
MALICIOUS para uma URL canônica, nenhum fetch server-side posterior dessa URL
pode ser autorizado — por nenhum componente, em nenhum momento, independente do
que o row daquele componente diga.

**Como.** Uma condenação escreve três coisas **no mesmo statement** que a
registra:

1. o veredito no store de origem;
2. uma linha em `files.link_fetch_denylist` — a autoridade durável. O gate novo a
   consulta antes de autorizar qualquer fetch, então nem um re-scan posterior que
   volte SAFE reabre a URL;
3. a expiração do row `files.link_scans`, se houver. É isso que alcança um pod
   **antigo** durante um rolling deployment: o gate dele é `state='done' AND
verdict_expires_at > now()`, e um row expirado lê como ausente — ausente é
   recusa.

Se o statement falhar, nada é registrado e o row continua onde estava
(`inconclusive` ou `pending`), o que continua recusando todo fetch. Fail-closed
em qualquer ordem.

**Por que só a metade restritiva é compartilhada.** SAFE é uma liberação
por-serviço que expira e está bem onde está; MALICIOUS é um fato sobre o mundo
que precisa dominar em todo lugar e nunca pode expirar virando permissão.
Compartilhar só a restrição é a menor mudança que torna a resposta única, e é
**monotônica** — a tabela é insert-only, então não existe ordenação em que um
SAFE posterior sobrescreva um MALICIOUS anterior. Levantar uma negação é ação
deliberada de operador (um DELETE, após a revalidação que julgar suficiente), e
ser incômodo é o ponto.

**Backfill no upgrade.** A migration não cria a tabela vazia. Ela carrega toda
condenação já conhecida — `chat.link_scans` com `status='malicious'` e
`files.link_scans` com `done/malicious` — e, na mesma transação, expira qualquer
`files.link_scans` `done/safe` conflitante. Sem isso a invariante só valeria para
URLs condenadas _depois_ do deploy, e o buraco continuaria aberto na manhã da
subida.

As duas tabelas chaveiam diferente (chat por `canonical_url`, files por
`url_digest`), então as linhas do chat são hasheadas na entrada. Que os digests
coincidam não é assumido: `sha256(canonical_url::bytea)` do PostgreSQL é
exatamente o que `urlsafety.URLDigest` calcula, e
`TestLinkFetchDenylistBackfillPostgreSQL` fixa isso contra a função Go para URLs
ASCII e não-ASCII. Nenhuma canonicalização é reimplementada em SQL — as duas
tabelas já guardam URLs canônicas.

**Worker legado não pode recriar clearance.** Código novo consultar a denylist não
protege um pod antigo, então a proteção é um trigger `BEFORE INSERT OR UPDATE` em
`files.link_scans`: qualquer linha que ficaria `state='done'`, `verdict='safe'` e
`verdict_expires_at > now()` para uma URL negada é recusada (`RETURN NULL`, zero
linhas). Isso é exatamente a forma que o gate do leitor legado procura, então uma
linha que nunca pode assumi-la nunca pode conceder clearance a nenhuma versão.

Permanecem permitidos: `done/malicious`, expirar/retirar uma clearance existente
(é o que o backfill e `InvalidateFetchAuthoritySQL` fazem), e tudo para URLs não
negadas. `RETURN NULL` em vez de `RAISE` porque zero linhas é o que o
compare-and-set do próprio worker antigo já trata como "outro worker chegou
primeiro" — derrubar o pod seria pior. O custo é que tal worker pode re-pollar o
mesmo scan até ser substituído: desperdício pela duração do rollout, nunca
insegurança.

**Onde mora e por quê.** No schema `files`, porque file-service é o único
componente que faz fetch server-side — a autoridade fica ao lado do que ela
autoriza. Ambos os runtimes já têm SELECT/INSERT/UPDATE/DELETE em todas as
tabelas de `auth`, `chat` e `files`
([grant-runtime.sql](../../scripts/db/grant-runtime.sql)), então **nenhum grant
novo, nenhum schema novo e nenhuma role nova** — o que também significa que
nenhum passo de rollout pode ser esquecido e deixar um serviço sem conseguir ler
a própria proteção.

**Digest único.** A chave é `urlsafety.URLDigest`, uma definição só, usada pelos
dois serviços. Duas definições de "a chave desta URL" seriam um veto que
silenciosamente nunca casa — a linha escrita sob um digest e procurada sob outro,
e a falha pareceria "não há tal URL" em vez de um bug.

#### Sequência de migrations desta mudança

| Migration      | O quê                                                                           |
| -------------- | ------------------------------------------------------------------------------- |
| `chat/000027`  | coluna `link_safety_state` + agendamento de reconciliação + CHECK **NOT VALID** |
| `chat/000028`  | `VALIDATE CONSTRAINT` do CHECK acima                                            |
| `files/000009` | abre `inconclusive -> done`                                                     |
| `files/000010` | denylist compartilhada + backfill + trigger anti-clearance                      |
| `files/000011` | allowlist por coluna na transição de reconciliação                              |
| `files/000012` | lease de reconciliação compartilhado entre serviços                             |

`chat/000027` e `chat/000028` são separadas de propósito. `ADD CONSTRAINT ...
CHECK` num único statement toma ACCESS EXCLUSIVE em `chat.messages` e o segura
por uma varredura sequencial completa: toda leitura e escrita de toda mensagem
fica na fila enquanto isso, por um tempo que cresce com o histórico. `NOT VALID`
toma o mesmo lock mas não varre — é escrita de catálogo e volta na hora — e a
varredura acontece na 000028 sob SHARE UPDATE EXCLUSIVE, que não conflita com
SELECT/INSERT/UPDATE/DELETE.

Precisa ser em **dois arquivos** e não dois statements: cada arquivo roda numa
transação e um lock é segurado até o commit, então um `VALIDATE` ao lado do `ADD`
ainda estaria varrendo sob o ACCESS EXCLUSIVE que o `ADD` tomou.
`TestLinkSafetyCheckValidationDoesNotBlockWritersPostgreSQL` mantém uma transação
escritora aberta durante o `VALIDATE`, lê o lock realmente tomado de `pg_locks` e
falha se for ACCESS EXCLUSIVE.

A coluna em si é adicionada com `NOT NULL DEFAULT ''`, que desde o PostgreSQL 11 é
metadata-only — sem backfill linha a linha.

#### A porta no banco (file-service, migrations 000008 e 000009)

A migration 000008 tornou uma linha `inconclusive` **imutavel por UPDATE**, via
trigger. Isso era certo para o rolling deployment e errado para a recuperacao: um
worker de `origin/develop` cuja predicate de claim e `state <> 'done'` selecionaria
uma linha inconclusive, a resubmeteria e destruiria o scan uuid -- mas a mesma
imutabilidade impedia tambem a transicao legitima.

A 000009 substitui a funcao do trigger (a 000008 aplicada nao e editada) por uma
que permite exatamente uma saida:

```
inconclusive -> done             PERMITIDO, com uuid inalterado, verdict presente
                                 e attempts nao decrescente
inconclusive -> submit_pending   RECUSADO
inconclusive -> submitting       RECUSADO
inconclusive -> submit_uncertain RECUSADO
inconclusive -> polling          RECUSADO
inconclusive -> inconclusive     PERMITIDO **apenas** para o bookkeeping de
                                 reconciliacao -- allowlist de duas colunas
```

A 000011 estreitou ainda mais a saída `-> done` (CQ-006): em vez de "se o state
virou done, aceita", cada coluna é comparada OLD vs NEW e só as que a
reconciliação legitimamente escreve podem mudar — `state`, `verdict`,
`verdict_expires_at`, `lease_until`, `next_attempt_at`, `reconcile_attempts`,
`next_reconcile_at`, `updated_at`. Identidade e histórico ficam congelados:
`url_digest`, `canonical_url`, `scan_uuid`, `attempts`, `submit_generation`,
`submit_attempt_started_at`, `created_at`. Uma coluna futura que não apareça na
função fica livre, porque PostgreSQL não compara automaticamente campos novos.
Toda adição a `files.link_scans` exige revisar o trigger e uma migration que
classifique explicitamente a coluna na allowlist ou na lista congelada.

O allowlist de duas colunas (`reconcile_attempts`, `next_reconcile_at`) e a parte
sutil, e nao e opcional: **o claim legado nao altera `state`**. Ele altera
`attempts`, `lease_until` e `next_attempt_at` na linha que selecionou. Um ramo que
apenas verificasse "o state nao mudou" teria devolvido a linha inconclusive
direto para o worker antigo. Nomear as duas colunas que podem se mover e o que
faz todo o resto -- o claim legado incluido -- cair na recusa final.

O guard mora no banco porque aquilo de que ele defende e codigo que este banco
nao ve: durante um RollingUpdate duas versoes do worker rodam ao mesmo tempo, e
um guard em Go protege apenas o processo que o contem. Este protege a linha.
`TestLinkScanReconcilePostgreSQL` executa a query legada literal e verifica que
ela afeta zero linhas e nao muta nada.

O chat-service nao precisa de trigger equivalente: `chat.link_scans` nao tinha um,
e o unico caminho de escrita para fora de `inconclusive` (`ReconcileLinkVerdict`)
ja carrega `status = 'inconclusive' AND scan_uuid = $3` na predicate.

#### Efeitos de cada direcao

`inconclusive -> safe`:

- o aviso some no frontend;
- o preview server-side passa a ser permitido a partir dali;
- `message.link_safety_changed` e emitido;
- um reload mostra `safe`;
- **nenhum** `message.created` novo, **nenhuma** mencao duplicada.

`inconclusive -> malicious`:

- a mensagem continua `active` -- ela foi entregue, e apagar a conversa inteira
  seria pior que o problema;
- o conteudo do corpo e retirado no cliente e substituido por "Este link foi
  bloqueado apos a verificacao de seguranca.", com autor e horario preservados.
  O corpo _e_ o link, para efeito de risco: uma URL que o leitor pode selecionar
  e colar e uma URL que o bloqueio nao impediu;
- o preview continua proibido, como sempre esteve;
- o evento vai para todos que podiam ver a mensagem.

### Reconciliacao no reconnect

O reconnect fecha dois tipos de evento perdido. Mensagens do proprio autor que
ainda estao `pending_link_scan` usam o endpoint sender-only abaixo. Mensagens ja
publicadas como `active + inconclusive` usam um snapshot autorizado do target:

```
POST /api/chat/channels/{channel_id}/message-security-snapshots
POST /api/chat/dm/{conversation_id}/message-security-snapshots
{ "message_ids": ["..."] }       <- ate 100 ids visiveis

{ "snapshots": [
    { "message_id": "...", "available": true,
      "status": "active", "link_safety_state": "malicious" },
    { "message_id": "...", "available": false }
] }
```

Essa leitura e uma unica query batch, reautoriza workspace + channel/DM e
retorna somente status, `link_safety_state` e o mesmo eixo da quote. Nao retorna
corpo, URL ou scan UUID. Missing, forbidden e target errado produzem o mesmo
sentinel `available=false`; o cliente retira o snapshot local sem aprender por
que ele nao esta disponivel. Referencias cross-channel sao re-resolvidas pelo
endpoint autorizado de references, porque o cliente do destino pode nunca ter
assinado o canal da source. O mesmo refresh roda no reconnect e na revalidacao
periodica/focus, sem buscar a URL externa.

O fluxo sender-only continua existindo para a bolha ainda withheld:

```
POST /api/chat/messages/link-safety-status
{ "message_ids": ["..."] }        <- ate 100 ids, apenas os que o cliente
                                     ainda mantem como pending

{ "statuses": [
    { "message_id": "...", "state": "pending" },
    { "message_id": "...", "state": "active"  },
    { "message_id": "...", "state": "blocked", "reason": "malicious_link" },
    { "message_id": "...", "state": "blocked", "reason": "link_check_inconclusive" },
    { "message_id": "...", "state": "deleted" }
] }
```

Regras que a definem:

- **escopo**: workspace + membro ativo + `sender_id = quem pergunta`. Uma
  mensagem so e descrita para quem a escreveu;
- **ausencia nao e resposta**: um id inexistente, de outro remetente ou de outro
  workspace simplesmente nao aparece na lista -- os tres sao indistinguiveis, e o
  endpoint nao vira oraculo de existencia. O cliente deixa um id sem resposta
  como estava;
- **`blocked` vem de estado duravel**, nao do outbox: a associacao
  `message_link_scans` (ligada pelo fingerprint do conteudo atual) somada ao
  veredito `malicious` **ou** `inconclusive` em `link_scans`. Ler o outbox
  faria a resposta depender de o dispatcher ter rodado. Quando os dois
  coexistem para a mesma mensagem, `malicious` vence -- e o motivo mais forte,
  e reportar exige a mesma evidencia que ja existe;
- **`deleted` nao e `blocked`**: uma mensagem que sumiu sem veredito malicious
  ou inconclusive que a explique e reportada como indisponivel. Dizer "link
  malicioso" ou "verificacao inconclusiva" sem evidencia e a inferencia que
  este desenho recusa;
- **conteudo nenhum**: sem corpo, sem URL, sem canonical URL, sem query, sem
  scan uuid, sem nada do provedor. Estado, e para a recusa um motivo fixo;
- **falha e conservadora**: se a requisicao falhar, o cliente mantem o pending.
  Nao remove, nao promove, nao bloqueia. O proximo reconnect pergunta de novo;
- **idempotente com o tempo real**: `message.blocked` e a reconciliacao carregam
  o mesmo fato. Em qualquer ordem, uma unica transicao terminal; para `active`,
  uma unica mensagem.

`inconclusive` nao tem o limite abaixo: sem TTL gravado, `blocked` com
`reason = link_check_inconclusive` nao degrada com o tempo -- ve
"Inconclusivo" acima.

Limite conhecido, exclusivo de `malicious`: um veredito `malicious` mais velho
que o `VerdictTTL` volta para a fila assim que alguem envia a mesma URL de
novo, e enquanto esta `pending` ele deixa de explicar o bloqueio -- a resposta
degrada de `blocked` para `deleted`. A direcao e conservadora (o autor e
informado de que a mensagem sumiu, nao de algo que nao se consegue mais
evidenciar) e fora do alcance do fluxo, que reconcilia na mesma sessao do
bloqueio.

### Privacidade

A URL canonica completa e enviada a Cloudflare. Isso e uma fronteira de confianca
externa e esta declarado como tal:

- path e query fazem parte da submissao, porque fazem parte da decisao;
- toda submissao pede `visibility: "Unlisted"`, entao o scan nao entra na
  listagem publica nem na busca do provedor;
- o corpo enviado tem exatamente dois campos, `url` e `visibility`. Nao existe
  campo que pudesse carregar cookie do usuario, o `Authorization` do NChat ou
  qualquer header interno;
- o NChat nao registra a URL completa em log operacional nem em label de metrica;
- a query pode conter informacao sensivel. Quem habilita a flag aceita enviar
  essas URLs a Cloudflare.

Nenhuma afirmacao de privacidade alem disso: o provedor recebe a URL, e o que ele
faz com ela e governado pelo contrato dele.

### Edicao de mensagem e Link Safety sao a mesma transacao

**Invariante.** Depois de um `EditMessage` commitado, o estado e as associacoes
em `chat.message_link_scans` descrevem exclusivamente a nova versao do corpo.
As associacoes anteriores sao copiadas para
`chat.message_edit_history_link_scans`, onde continuam redigindo apenas a
versao historica correspondente se o veredito se tornar malicious depois.

Antes, `EditMessage` atualizava corpo, formato e bookkeeping de edicao e deixava
`link_safety_state`, `link_safety_fingerprint` e `chat.message_link_scans` como
estavam. O resultado era uma mensagem com corpo novo e todos os fatos de Link
Safety descrevendo o corpo **velho**: a URL que o leitor agora ve nunca foi
associada a mensagem, e a URL que decide o estado dela nao esta mais no texto.

A transacao trata em conjunto:

| O que                  | Como                                               |
| ---------------------- | -------------------------------------------------- |
| corpo e formato        | `UPDATE chat.messages`                             |
| associacoes correntes  | `DELETE` + `INSERT` em `chat.message_link_scans`   |
| associacoes historicas | snapshot em `chat.message_edit_history_link_scans` |
| `link_safety_state`    | recomputado pela mesma classificacao do envio      |
| fingerprint            | derivado: vazio quando o corpo novo nao tem URL    |

**Rolling upgrade fica fail-closed.** A migration 000029 adiciona
`link_safety_projection_version`, inicialmente zero. Writers novos gravam ou
avancam essa versao junto com corpo, estado e associacoes. Um trigger detecta o
writer legado que muda `body_text` sem avancar a versao, zera a confianca da
projecao e remove qualquer clearance antigo; se o estado anterior ja era
`malicious`, ele permanece restritivo. No primeiro edit pelo writer novo, esse
corpo legado entra no historico com fingerprint `NULL` e e ocultado, sem tentar
certificar associacoes que podem descrever outra versao. A nova projecao passa a
ser confiavel apenas no mesmo commit do edit atomico.

Nao ha segunda implementacao da classificacao. `linkDecision.editState()` usa a
mesma agregacao do envio, com a mesma precedencia `malicious > pendente >
inconclusive > safe`.

**As URLs sao relidas dentro da transacao.** `assertEditableLinkStates` roda no
mesmo `tx`, entao um veredito que chega entre a classificacao do caller e o
commit vence: uma condenacao que aterrissa nessa janela recusa a edicao inteira
em vez de commitar a classificacao velha.

**Estados nao terminais recusam.** Uma URL sem veredito devolve
`ErrURLCheckPending` e uma condenada devolve `ErrMaliciousURL` — a edicao nao e
caminho para publicar link nao verificado nem link condenado. A recusa nao
half-commita: corpo, estado, fingerprint e associacoes ficam como estavam.

Nao ha `message.created` duplicado e mencoes nao sao redisparadas: a edicao
continua sendo edicao.

`TestEditMessageIsAtomicWithLinkSafetyPostgreSQL` cobre A -> sem URL, A -> B
safe, A -> B inconclusive, A -> B sem veredito, A -> B malicious, multiplas URLs,
a corrida edicao-vs-reconciliacao, e a convergencia reload/realtime.

### Conteudo condenado sai de todas as projecoes

**Invariante.** Uma mensagem cujos links foram condenados nao tem o corpo
retornado por nenhuma leitura que o servidor sirva.

O cliente ja se recusava a desenhar o corpo, mas isso e uma decisao tomada sobre
um dado que ele **ja recebeu**: a URL continuava na resposta, no network tab, em
qualquer payload cacheado, e alcancavel por tres projecoes que leem `body_text`
da mesma linha sem passar perto da checagem do cliente — a citacao, a referencia
cross-target e o historico de edicao.

A supressao e **no SQL**, por uma unica expressao (`withheldIfMalicious`) usada
por todas as projecoes:

| Projecao                | Query                      |
| ----------------------- | -------------------------- |
| corpo principal / lista | `messageColumns`           |
| citacao                 | `quotedMessageColumns`     |
| referencia cross-target | `ResolveMessageReferences` |
| historico de edicao     | `ListMessageEditHistory`   |

O estado e lido **ao vivo** da linha de origem, nao snapshotado: uma mensagem
condenada _depois_ de ter sido citada some daquela citacao na proxima leitura,
sem backfill e sem nada para agendar. Reload e realtime concordam porque saem da
mesma query.

**O historico inteiro, nao so a versao condenada.** Uma edicao que reescreveu a
frase em volta do link manteve o link, entao as versoes anteriores sao justamente
onde a URL tem mais chance de ainda estar escrita. A lista de versoes continua
visivel — o leitor ve que houve edicao e quando —, so o texto sai.

Citacao e referencia carregam `link_safety_state` no JSON para que o cliente diga
"Conteudo ocultado por seguranca." em vez de desenhar um bloco vazio.

`TestMaliciousBodyIsWithheldFromEveryProjectionPostgreSQL` cobre as quatro
projecoes, a listagem de canal, a condenacao posterior a citacao, e a volta por
edicao.

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

Nunca um booleano. `urlsafety.Verdict` tem quatro valores:

| Veredito       | Origem                                                                         | Resultado                                       |
| -------------- | ------------------------------------------------------------------------------ | ----------------------------------------------- |
| `safe`         | o provedor respondeu e nao reportou risco                                      | permite                                         |
| `malicious`    | o provedor reportou pelo menos uma categoria de risco                          | bloqueia                                        |
| `unknown`      | timeout, status inesperado, corpo invalido, `success:false`, credencial errada | bloqueia, **retentavel**                        |
| `inconclusive` | scan confirmado finalizado (`status=finished`) sem veredito utilizavel         | bloqueia, **terminal**, ve "Inconclusivo" acima |

**Erro tecnico nunca vira `safe`.** Um booleano tornaria "o provedor nao
respondeu" indistinguivel de "o provedor disse que esta tudo bem", e uma queda
da Cloudflare viraria sinal verde silencioso. O tipo tem `unknown` como valor
zero exatamente para que um veredito nunca atribuido nao leia como liberado.

`unknown` e `inconclusive` bloqueiam pelo mesmo motivo fail-closed, mas nao sao
o mesmo fato: `unknown` e "nao sabemos ainda, pergunte de novo" -- o worker
tenta novamente com backoff. `inconclusive` e "o provedor respondeu, e a
resposta e que este scan especifico nunca vai produzir um veredito" -- nao ha
"de novo" para esse scan. `IsFinal()` reflete so a metade permissiva dessa
tabela (`safe`/`malicious`); `inconclusive` deliberadamente nao e final, mas e
persistido e tratado como terminal pelos dois stores, exatamente como descrito
na secao "Inconclusivo".

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
sobre um _dominio_, que e exatamente a granularidade em que RF-21 nao pode
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
permanente de uma URL condenada. Ignora-los faria de "hospede na porta 80 de
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
| chat-service | `400`  | `bad_request`            | mais de 10 URLs canonicas distintas na mensagem   |

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

**`link_check_inconclusive` nao e um codigo desta tabela.** Ele nunca aparece
como resposta a um submit -- a mensagem ja foi aceita e retida quando o worker
decide isso depois, entao ele chega como `reason` do evento `message.blocked`
ou da reconciliacao de reconnect (ve "Reconciliacao no reconnect" abaixo), nao
como status HTTP. O frontend mapeia esse motivo para "Nao foi possivel
verificar a seguranca deste link.", deliberadamente sem sugestao de tentar de
novo: ao contrario de `link_check_unavailable`, reenviar nao resubmete nada --
o scan ja terminou, e terminou assim.

## Multiplas URLs

- todas sao verificadas;
- **um** veredito malicioso recusa a operacao inteira, e o loop para ali -- a
  mensagem ja esta recusada e as consultas restantes so gastariam quota;
- a deduplicacao e por URL canonica, entao vinte links para a mesma URL sao uma
  consulta;
- **10 URLs canonicas distintas** por mensagem e o teto. Acima disso a mensagem e
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

O TTL de `safe` e finito porque reputacao muda: uma URL comprometida depois
de liberado nao pode ficar congelado. Falha e cacheada **como falha** -- a
entrada continua recusando; ela existe para que uma queda do provedor custe uma
consulta por URL a cada meio minuto em vez de uma por mensagem.

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
- o Safe Browsing responde _aquela URL e considerada maliciosa_.

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

## Quota e backpressure

A unidade de custo e **URL canonica nova**: uma sem veredito fresco e sem job
ativo. Isso e o que o provedor cobra. Nao e mensagem, nao e request, nao e
caractere.

Custa **zero**:

- cache safe fresco;
- cache malicious fresco;
- mesma URL canonica ja pending/submitting/uncertain/polling;
- URL repetida dentro da mesma mensagem;
- replay idempotente de create ou forward;
- preview repetido da mesma URL.

Uma mensagem com dez links ja cobertos passa mesmo com o orcamento zerado. Isso
e proposital: o orcamento limita gasto no provedor, nao uso do produto.

**Controles:**

| Controle                           | Escopo                     | Onde e aplicado                               |
| ---------------------------------- | -------------------------- | --------------------------------------------- |
| max 10 URLs distintas por mensagem | mensagem                   | extracao, antes de qualquer query             |
| orcamento de URLs novas            | workspace, janela fixa     | admissao, na mesma transacao que cria os jobs |
| backlog cap                        | deployment                 | admissao                                      |
| provider submit rate               | deployment, entre replicas | worker, antes do POST                         |

O orcamento de workspace e **compartilhado** por create de canal, create de DM,
edit e forward. Cada um tinha seu proprio limiter e nenhum contava URL, entao
"esgotei create, agora uso edit" era um bypass documentado.

A admissao e **atomica e all-or-nothing**. Uma mensagem que precisa de quatro
scans novos com um de orcamento nao enfileira nenhum: admitir os tres que cabem
publicaria uma mensagem cujo quarto link nunca teria job, e uma URL sem job fica
retida para sempre. Concorrencia e resolvida por `INSERT ... ON CONFLICT DO
UPDATE ... WHERE` numa unica ida ao banco -- ler e depois escrever nao pode ser
corrigido com replicas concorrentes. Nenhuma chamada Cloudflare acontece dentro
da transacao.

Uma corrida fica deliberadamente aberta e vale dizer qual: linhas existentes sao
travadas, mas uma URL que ainda nao existe nao pode ser travada, entao duas
admissoes simultaneas da mesma URL nova podem cobrar 1 cada. O insert e
idempotente, entao so um job nasce; o custo e ter cobrado duas vezes por um scan.
Erra para o lado de recusar demais em vez de gastar demais.

**Rejeicao** vira `429` com codigo `link_check_capacity` (chat e preview). Nao e
`malicious_url` e nao e `link_check_unavailable`: nada foi decidido sobre o link,
e a tentativa nem aconteceu. O frontend mostra "nao foi possivel verificar os
links agora", nunca aviso de seguranca. Qual teto recusou fica interno -- um
cliente que distinguisse "meu workspace esgotou" de "o deployment esta cheio"
poderia sondar atividade de outro tenant.

**file-service** nao tem workspace confiavel num request de preview -- ele e
buscado para um link, nao para um tenant -- entao o orcamento la e do servico
inteiro, e nao se inventa tenant a partir de header. Justica por chamador
continua no rate limiter de request. Os dois servicos nao compartilham banco: o
requisito "compartilhado" significa que todas as operacoes do mesmo pipeline
dividem o orcamento relevante, nao transacao distribuida entre servicos.

**Revalidacao por TTL** e trabalho novo no provedor: respeita backlog e provider
submit rate. Quando provocada por uma acao nova do workspace (alguem reenvia a
URL), consome tambem o orcamento de admissao daquele workspace. Quando o proprio
worker reabre um veredito vencido de que uma mensagem ja retida depende, nao
depende de request nenhum, mas continua sujeita a backlog e provider rate.

## Operacao

**Sintoma: backlog crescendo.**

Metricas na ordem em que respondem:

1. `nchat_link_scan_pending{state}` e `nchat_link_scan_oldest_pending_age_seconds`
   -- ha fila, e ha quanto tempo;
2. `nchat_link_scan_attempts_total{operation="submit",result}` -- `throttled`
   significa que o proprio deployment esta segurando (veja o provider submit
   rate); `error` significa Cloudflare;
3. `nchat_link_scan_provider_duration_seconds` -- provedor lento;
4. `nchat_link_scan_admissions_total{result="rejected",reason}` -- quem esta
   sendo recusado e por qual teto;
5. `nchat_link_scan_submit_reconciliation_total{result}` -- `not_found` ou
   `error` crescendo significa janela de incerteza enchendo; `stale` significa
   que ha submissao incerta ha mais tempo que o limiar configurado.

Acoes, nessa ordem:

- verificar status do Cloudflare e a quota do plano;
- conferir se `PROVIDER_SUBMIT_LIMIT` corresponde ao contrato real -- um valor
  baixo demais aparece como `throttled` alto e backlog crescendo sem erro nenhum;
- conferir se o backlog cap e o orcamento de workspace estao dimensionados para o
  trafego;
- se `stale` esta subindo, investigar por que a persistencia do uuid esta
  falhando (banco, lease, restart em loop).

**Sintoma: `submit_uncertain` antigo.**

Alertar quando `nchat_link_scan_pending{state="submit_uncertain"} > 0` por um
periodo relevante, ou quando
`nchat_link_scan_oldest_pending_age_seconds` fica alto com backlog incerto
presente -- o gauge de idade cobre todas as linhas nao decididas, incluindo as
incertas, entao "ha um scan cuja submissao esta incerta ha 45 minutos" e visivel
sem nenhum identificador.

Verificar, nessa ordem:

- disponibilidade do Cloudflare;
- `submit_reconciliation_total{result}` -- `error` aponta a Search API,
  `not_found` persistente aponta indexacao ou escopo de token;
- se o token ainda tem a permissao URL Scanner (a Search usa o mesmo escopo);
- backlog geral e provider submit rate.

**Nao** reiniciar o worker para "forcar" um Submit: restart nao resubmete, por
construcao. **Nao** desligar fail-closed: isso libera links nao verificados, que
e o oposto do problema. Se for realmente necessario refazer o scan, e decisao
manual explicita e consciente de que pode duplicar o scan remoto -- nao existe
mecanismo automatico e nenhum foi implementado nesta task.

**Nao** desabilitar fail-closed como primeira reacao. Desligar
`LINK_SAFETY_ENABLED` faz toda mensagem com link passar sem verificacao; a
mensagem retida e o comportamento correto sob indisponibilidade.

## Observabilidade

`nchat_url_safety_checks_total{result}`, conjunto fechado: `hit`, `safe`,
`malicious`, `error`, `not_checkable`. Registrada no file-service.

No chat-service as recusas aparecem como
`nchat_http_requests_total{route,status}` nas duas rotas de mensagem -- o
registry servido pertence ao router, construido depois da fiacao do servico, e
atravessa-lo ate o bootstrap so para uma segunda copia do mesmo contador nao se
paga. Ha um comentario `ponytail:` em `wireLinkSafety` indicando a mudanca caso
o breakdown por veredito seja necessario ali.

Metricas do pipeline, todas com label `service`:

| Metrica                                                   | Tipo      | Labels                           | Para que serve                                                                                                                                               |
| --------------------------------------------------------- | --------- | -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `nchat_link_scan_pending`                                 | gauge     | `service`, `state`               | profundidade da fila; `state=submit_uncertain` e a serie que importa aqui                                                                                    |
| `nchat_link_scan_oldest_pending_age_seconds`              | gauge     | `service`                        | ha quanto tempo a mais antiga espera                                                                                                                         |
| `nchat_link_scan_attempts_total`                          | counter   | `service`, `operation`, `result` | passos do pipeline; `result=throttled` e limite proprio, `uncertain` e janela de incerteza                                                                   |
| `nchat_link_scan_provider_duration_seconds`               | histogram | `service`, `operation`           | latencia de uma troca com Cloudflare                                                                                                                         |
| `nchat_link_scan_revalidations_total`                     | counter   | `service`, `reason`              | vereditos reabertos por expiracao                                                                                                                            |
| `nchat_link_scan_admissions_total`                        | counter   | `service`, `result`, `reason`    | **capacidade**: quantas operacoes foram recusadas e por qual teto                                                                                            |
| `nchat_link_scan_submit_reconciliation_total`             | counter   | `service`, `result`              | **janela de incerteza**: `adopted`, `not_found`, `error`, `ambiguous`, `stale`, `unsupported`                                                                |
| `nchat_link_safety_reconcile_total`                       | counter   | `service`, `source`, `result`    | **recuperacao de inconclusive**: `requested`, `candidate_found`, `no_candidate`, `safe`, `malicious`, `still_inconclusive`, `rate_limited`, `provider_error` |
| `nchat_message_publish_outbox_pending`                    | gauge     | `service`                        | eventos escritos e nao entregues                                                                                                                             |
| `nchat_message_publish_outbox_oldest_pending_age_seconds` | gauge     | `service`                        | idade do mais antigo                                                                                                                                         |

Todos os valores de label vem de conjuntos fechados definidos em
`libs/go/platform/urlsafety`. Nao ha parametro em nenhuma dessas funcoes que
pudesse carregar URL, host, query, workspace, user, message id ou scan uuid.

As metricas do pipeline (`nchat_link_scan_*`, `nchat_message_publish_outbox_*`)
sao **opcionais por contrato**: sem `SetMetrics` o reporter e nil, e nil e o
no-op -- todo metodo de `*PipelineMetrics` tolera receiver nil. Nenhum worker
guarda o campo antes de reportar, e nao deve passar a guardar: a tolerancia mora
em um tipo so, em vez de ser reconstruida em cada ponto de uso, e
`TestLinkScanServiceRunsWithoutMetrics` (chat e file) sustenta esse contrato.
Observabilidade nao e pre-requisito para rodar um controle de seguranca.

**Nunca sao label**: URL, hostname, user ID, token, query string ou resposta do
provedor. O chamador escolhe o valor consultado, entao uma label derivada dele
deixaria um cliente hostil criar uma serie por request.

`source` e um conjunto de dois valores: `manual` (um leitor clicou em "Verificar
novamente") ou `background` (o passe agendado). A distincao importa porque uma e
uma pessoa esperando e a outra e um cronograma rodando, e os remedios sao
diferentes.

O texto da recusa da Cloudflare **nunca** e uma label nem um estado. E uma frase
em ingles escolhida pelo provedor, pode conter o hostname, e uma label derivada
dela seria ao mesmo tempo ilimitada e um vazamento de URL para dentro de um
scrape. A deteccao daquele caso vive inteira em `verdictFromReport`, encapsulada
e testada, e o que sai dela e `ErrScanInconclusive` -- nao a frase.

Nenhum log carrega a URL bruta, o host, o token ou o corpo do provedor. A
reconciliacao tambem nao registra os message ids consultados, nem a URL, nem o
scan uuid -- contagem e resultado bastam.
