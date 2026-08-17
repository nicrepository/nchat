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

**Fail-closed e terminal, nao apenas fail-closed.** Uma mensagem cujos links
resolvem a inconclusive (e nenhum malicious) e bloqueada exatamente como uma
mensagem maliciosa: status `deleted`, evento `message.blocked` com
`reason = link_check_inconclusive`, endereçado somente ao remetente. A
diferenca para `malicious` esta inteira no motivo relatado ao autor -- o
mecanismo de bloqueio, a auditoria e a nao-visibilidade para o resto do canal
sao os mesmos.

O que torna esse estado deliberadamente diferente de `safe`/`malicious` e a
ausencia total de um caminho de volta:

- **sem polling futuro** -- `RecordVerdict`/`RecordLinkVerdict` escreve
  `state = 'inconclusive'` (file-service) ou `status = 'inconclusive'`
  (chat-service), e as queries de claim (`ClaimDueScans`,
  `ClaimDueLinkScans`) excluem esse estado explicitamente, do mesmo jeito que
  excluem `done`;
- **sem TTL** -- ao contrario de `safe`/`malicious`, nenhuma expiracao e
  gravada. Nao ha data a partir da qual o estado "vence": ele nao vence;
- **sem reabertura automatica** -- `ReopenExpiredVerdicts` e
  `EnsureLinkScans`/`AdmitScan` sao escopados a `status IN ('safe',
'malicious')`. Uma nova mensagem citando a mesma URL, ou o worker reabrindo
  um veredito vencido, nunca tocam uma linha `inconclusive` -- exatamente o
  loop de resubmissao automatica que esta correcao existe para nao introduzir;
- **sem carona no Backlog** -- as metricas de fila (`Backlog`,
  `LinkScanBacklog`) tambem excluem o estado, entao um scan terminado assim
  nao aparece como fila crescendo;
- **nunca um veredito carregavel** -- `LoadVerdict`/`LoadLinkVerdicts` so
  devolvem `safe`/`malicious`; um `inconclusive` nunca e lido de volta como
  liberacao, imediato ou depois de qualquer tempo decorrido.

A unica forma de um `inconclusive` deixar de ser `inconclusive` e uma acao
deliberada fora deste pipeline (por exemplo, um operador apagando a linha para
forcar um novo scan) -- nunca o passar do tempo, nunca um restart do worker,
nunca uma nova mensagem citando a mesma URL.

Sem mencao ou notificacao: como a mensagem nunca chega a `active`, a mesma
regra de "mencao adiada, nunca perdida" abaixo a mantem fora de
`chat.notification_outbox` -- a CTE que libera notificacoes so dispara para
mensagens promovidas (`published`), nunca para bloqueadas.

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

worker: todas as URLs safe                -> active + evento message.created na outbox
worker: qualquer URL malicious            -> deleted, evento message.blocked (reason=malicious_link)
worker: qualquer URL inconclusive         -> deleted, evento message.blocked (reason=link_check_inconclusive)
worker: provedor indisponivel/scan rodando -> permanece pending_link_scan, retry com backoff
```

Malicious e inconclusive sao os dois motivos que **bloqueiam** uma mensagem
retida; nenhum dos dois nunca vira `active`.

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

### Reconciliacao no reconnect

O caso que o tempo real nao cobre: o autor esta offline quando o veredito sai.
Para `malicious` e para `inconclusive` isso e terminal -- a mensagem passa a
`deleted`, nenhum outro evento sera emitido, e nao existe mensagem para buscar
depois. Sem uma pergunta explicita, a bolha do autor fica em "verificando"
para sempre.

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

| Metrica                                                   | Tipo      | Labels                           | Para que serve                                                                                |
| --------------------------------------------------------- | --------- | -------------------------------- | --------------------------------------------------------------------------------------------- |
| `nchat_link_scan_pending`                                 | gauge     | `service`, `state`               | profundidade da fila; `state=submit_uncertain` e a serie que importa aqui                     |
| `nchat_link_scan_oldest_pending_age_seconds`              | gauge     | `service`                        | ha quanto tempo a mais antiga espera                                                          |
| `nchat_link_scan_attempts_total`                          | counter   | `service`, `operation`, `result` | passos do pipeline; `result=throttled` e limite proprio, `uncertain` e janela de incerteza    |
| `nchat_link_scan_provider_duration_seconds`               | histogram | `service`, `operation`           | latencia de uma troca com Cloudflare                                                          |
| `nchat_link_scan_revalidations_total`                     | counter   | `service`, `reason`              | vereditos reabertos por expiracao                                                             |
| `nchat_link_scan_admissions_total`                        | counter   | `service`, `result`, `reason`    | **capacidade**: quantas operacoes foram recusadas e por qual teto                             |
| `nchat_link_scan_submit_reconciliation_total`             | counter   | `service`, `result`              | **janela de incerteza**: `adopted`, `not_found`, `error`, `ambiguous`, `stale`, `unsupported` |
| `nchat_message_publish_outbox_pending`                    | gauge     | `service`                        | eventos escritos e nao entregues                                                              |
| `nchat_message_publish_outbox_oldest_pending_age_seconds` | gauge     | `service`                        | idade do mais antigo                                                                          |

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

Nenhum log carrega a URL bruta, o host, o token ou o corpo do provedor. A
reconciliacao tambem nao registra os message ids consultados -- contagem e
resultado bastam.
