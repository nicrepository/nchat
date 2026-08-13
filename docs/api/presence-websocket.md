# RF-58 — presença por WebSocket

O `chat-service` é a única autoridade sobre presença. Os estados são `online`,
`away` e `offline`; o cliente não escolhe nenhum deles e não pode nomear o
usuário cuja presença muda. Tudo viaja pela conexão WebSocket existente
(subprotocolo `nchat.v1`), pelo mesmo Hub e pelo mesmo barramento de broadcast
usados por `message.created`.

## Comandos

Nenhum. Não existe comando de presença.

O único sinal de entrada que afeta presença é **atividade**: qualquer frame
recebido numa conexão autenticada renova o temporizador de inatividade daquela
conexão, creditado à identidade que o handshake afirmou. `ClientMessage` não tem
campo de presença nem de estado, então não há forma de um cliente declarar-se
`online` ou dizer algo sobre outra pessoa.

## Eventos

### `presence.updated`

Direção: servidor → cliente. Roteado por `(workspace_id, target_type,
target_id)`, exatamente como `message.created`, e re-autorizado por assinante no
fan-out.

```json
{
  "schema_version": 1,
  "type": "presence.updated",
  "event_id": "<uuid>",
  "workspace_id": "<uuid>",
  "target_type": "channel",
  "target_id": "<uuid>",
  "presence": {
    "user_id": "<uuid>",
    "state": "away",
    "updated_at": "2026-08-11T10:05:00.123456789Z"
  },
  "created_at": "2026-08-11T10:05:00Z"
}
```

`target_type` é `channel` ou `dm`. `state` é `online`, `away` ou `offline`.
`updated_at` é o instante em que o servidor decidiu aquele estado, em RFC 3339.

O payload não carrega e-mail, sessão, dispositivo, IP, user-agent, último acesso
nem qualquer metadado de infraestrutura.

### `presence.snapshot`

Direção: servidor → cliente. Enviado a **um** cliente, logo após o `subscribed`
daquele alvo. Não vai ao barramento e não tem destinatário no envelope, porque é
a resposta a uma assinatura que acabou de ser autorizada.

```json
{
  "type": "presence.snapshot",
  "target_type": "channel",
  "target_id": "<uuid>",
  "users": [{ "user_id": "<uuid>", "state": "online", "updated_at": "..." }],
  "complete": true,
  "taken_at": "2026-08-12T09:00:00.000000000Z"
}
```

`taken_at` é o instante em que o servidor leu esse roster, do mesmo relógio de
`updated_at`. Um snapshot completo **substitui** a visão daquela conversa no
cliente — inclusive removendo quem ele não nomeia —, e é `taken_at` que impede
uma leitura antiga de desfazer uma transição posterior: o cliente mantém o que
sabe ser mais novo e descarta o resto.

`users` lista apenas quem está presente, então toda entrada é `online` ou
`away`. Quem está offline simplesmente não aparece.

É enviado **uma vez por subscription real**. Reenviar `subscribe` para um alvo já
assinado não produz outro snapshot nem outro anúncio: nada entrou. Sair e
reassinar é uma nova subscription e produz um novo snapshot.

### Autorização do sujeito

O diretório guarda **assertions de presença**, não roster. Ele responde "que
estado foi afirmado", nunca "esta pessoa pertence a esta conversa" — e quem perde
acesso enquanto está ocioso não produz transição alguma, então nada mais
revisitaria essa assertion.

Por isso todo sujeito incluído em um `presence.snapshot` inicial **ou** numa
correção de reconciliação é filtrado pelo mesmo `SubscriptionAuthorizer.CanAccess`
usado em todo o resto — sem segunda política, sem consulta específica para Guest.
Negado sai da resposta e deixa de ser desejado, o que o delta transforma em
`Forget`. Erro de autorização não é permissão: o sujeito é omitido e o snapshot
deixa de ser `complete`, porque uma lista encurtada por falha apresentada como
completa viraria a afirmação de que alguém está offline.

Custo: uma autorização por candidato, limitada pelo bound de 500 que já existe.
É um trade-off deliberado — este serviço não tem leitura canônica de roster em
lote (as listagens de membros e participantes são prévias filtradas por presença e
limitadas, não memberships), e inventar uma segunda consulta de membership seria
inventar uma segunda política de autorização.

### Cobertura: o que a ausência significa

`complete` é o que torna a ausência interpretável, e é por isso que ele existe.

- `complete: true` — a lista é todo mundo presente **naquele alvo**, com
  autoridade suficiente para o cliente concluir que alguém que ele esperava ali
  e não encontrou está offline. Só é afirmado quando o servidor de fato tem essa
  visão: instância única (sem bus), ou resposta do diretório compartilhado.
- `complete: false` — a resposta não tem essa autoridade: ou foi interrompida no
  limite (`presenceSnapshotMaxUsers`, 500), ou a instância divide clientes com
  outras réplicas e não conseguiu consultar o diretório compartilhado. As
  entradas continuam válidas, porque cada uma afirma uma pessoa; nada pode ser
  concluído sobre quem falta.
- campo ausente — tratado como `false`. Um servidor que não afirmou não autoriza
  inferência.

O escopo é sempre **um alvo**. Um snapshot de canal não diz nada sobre uma
conversa, e um snapshot vazio de um alvo não torna offline alguém de outro. O
cliente guarda a cobertura por alvo e só lê ausência como offline dentro do alvo
em que está renderizando aquela pessoa; fora disso o estado permanece `unknown` e
nenhum indicador é desenhado.

Não há truncamento silencioso: a lista nunca é encurtada sem dizer.

Imediatamente antes de montar o snapshot, o servidor **reautoriza** o leitor com
o mesmo `SubscriptionAuthorizer.CanAccess` do subscribe e do fan-out. Uma
membership revogada entre o subscribe e o snapshot resulta em: nenhum snapshot,
subscription removida e o erro genérico `room_access_denied`, que não distingue
"não existe" de "não pode".

## Quem recebe

Presença é publicada **uma vez por alvo em que o sujeito é visível** — e em
nenhum outro lugar. Os candidatos vêm das subscriptions **ativas** do próprio
sujeito (estado ativo, nunca histórico de onde já esteve) mais os alvos em que
esta instância ainda mantém uma asserção dele; cada candidato é então confirmado
com `CanAccess(sujeito, alvo)`. Negação retira a asserção e entrega offline
naquele alvo, então quem removeu o acesso deixa de ver a pessoa em vez de
congelar no último estado. Não existe broadcast de workspace: ele permitiria a qualquer
membro, inclusive Guest, enumerar todos os conectados, o que nenhuma leitura
existente concede.

Consequência prática: A vê a presença de B quando compartilham um canal ou uma
conversa que ambos assinam. Cada destinatário passa pela mesma re-verificação de
autorização (`SubscriptionAuthorizer.CanAccess`) que qualquer outro evento, e a
política de canal continua sendo `chat.channel_visible_to_user`. Guest não ganha
caminho novo algum.

Um usuário em vários alvos compartilhados gera um evento por alvo. As cópias são
idempotentes por construção (mesmo `user_id`, mesmo `state`, mesmo `updated_at`).

## Ordenação e idempotência

`updated_at` é a chave de ordenação e vem sempre do relógio do servidor. O
cliente:

- aplica uma atualização mais nova que a já aplicada;
- descarta uma mais antiga — entrega fora de ordem é normal, não excepcional;
- ignora uma repetida.

Nenhuma decisão de autoridade usa o relógio do navegador.

## Ciclo de vida

- **Conectar** publica a transição que causar. Presença é agregada por usuário:
  abrir uma segunda sessão enquanto `away` volta o usuário para `online`, e quem
  já o observa é avisado na hora, sem depender de a nova conexão assinar nada. A
  audiência vem das subscriptions **existentes** do usuário, então uma primeira
  conexão não tem a quem anunciar e não publica nada.
- **Assinar** publica duas coisas: o `presence.snapshot` para quem entrou e o
  `presence.updated` de quem entrou para os demais assinantes daquele alvo.
- **Atividade** publica apenas a transição `away → online`. Atividade em quem já
  está `online` não gera evento.
- **Inatividade** (`defaultPresenceAwayTimeout`, 5 min) publica `away` quando
  **todas** as conexões do usuário estão inativas. Uma conexão ativa mantém o
  usuário `online`.
- **Desconectar** publica `offline` somente quando cai a última conexão do
  usuário naquele workspace. É endereçado aos alvos que a conexão assinava **mais
  os que esta instância ainda afirma sobre ele** — a aba que lia um canal pode ter
  fechado antes, e a instância que escreveu aquela asserção é a única que pode
  retirá-la. Esse registro de posse é limitado às conversas em que a pessoa é
  visível e some junto com ela; não é autoridade sobre nada, porque cada
  publicação passa por `CanAccess` de qualquer forma. Fechar uma de duas sessões
  não publica nada.
- **Queda abrupta** converge pelo mesmo caminho: o heartbeat de transporte
  (ping/pong, 30 s com 10 s de espera) derruba a conexão morta, o Hub a remove e
  a remoção é o que produz o `offline`. Não se depende de `beforeunload`, de
  fechamento limpo da aba nem do close frame.
- **Sessão revogada** (logout, dispositivo revogado, usuário suspenso) fecha a
  conexão pelo mesmo caminho, e portanto obedece às mesmas regras: outras sessões
  válidas do mesmo usuário o mantêm presente. Ver "Sessão" abaixo.

### Entrega sob pressão

O fan-out coalesce por usuário: uma mudança nova substitui a anterior daquele
usuário que ainda não saiu. Sob pressão perdem-se estados intermediários —
`online → away → offline` colapsa em `offline` — nunca o estado final. Não há
fila limitada que descarte o que chegou por último, e por isso não é necessário
nenhum mecanismo de ressincronização: nada é perdido para recuperar depois.

## Sessão

A sessão é validada antes do upgrade, como em qualquer rota autenticada, e
**revalidada periodicamente** enquanto a conexão vive
(`DefaultSessionRevalidateInterval`, 60 s). A verificação usa o mesmo
`SessionValidator` das rotas HTTP e o `sid` do token já validado — nunca um
identificador vindo do cliente — e roda na goroutine de heartbeat que já existe:
sem goroutine nova, no máximo uma consulta indexada por minuto por conexão, e
nenhuma consulta por frame.

- Sessão revogada/expirada/usuário suspenso: a conexão é encerrada. Ela deixa de
  receber eventos (as subscriptions vão junto) e deixa de sustentar presença.
- Erro transitório (banco indisponível, timeout): a conexão é mantida e a próxima
  volta do ticker tenta de novo. Uma falha de infraestrutura não é evidência de
  que uma sessão terminou, e derrubar todos os sockets por causa dela
  transformaria um erro transitório em desconexão em massa.

Nada da sessão é registrado em log: nem o `sid`, nem o usuário.

## Reconexão

A conexão nova reassina seus alvos e recebe um `presence.snapshot` por alvo. O
cliente descarta o que aprendeu na conexão anterior ao abrir a nova: o servidor
não reproduz o que aconteceu enquanto a aba esteve fora, então o snapshot é a
única correção possível. Enquanto o snapshot de um alvo não chegou, ninguém é
offline por ausência ali: o usuário fica `unknown` e o indicador não é desenhado
— nunca cinza/offline.

## Múltiplas instâncias

`presence.updated` atravessa o barramento Valkey como os demais eventos, com
supressão de eco por `source_instance_id` e canonicalização estrita na recepção
(alvo `channel`/`dm`, `user_id` UUID, `state` no conjunto fechado; qualquer
outro payload é descartado).

Eventos resolvem quem **muda**. Um assinante que chega depois precisa de quem já
estava lá e não vai mudar — o barramento não faz replay — então o snapshot tem
uma autoridade própria:

**Instância única (sem bus).** O processo é o cluster inteiro; suas conexões são
a resposta completa. `complete: true`.

**Diretório compartilhado.** Um hash por alvo em Valkey,
`nchat:chat:ws:presence:{workspace}:{tipo}:{alvo}`, **campo =
`{user id}|{runtime instance id}`**, valor = `state|instante`.

O campo é a _asserção_, não a pessoa. Com o user ID sozinho, uma réplica
sobrescrevia o que outra havia afirmado sobre o mesmo usuário e o `HDEL` dela na
desconexão apagava uma conexão viva que outra ainda servia. Cada processo é dono
exatamente do próprio campo; `Forget` nunca toca o de ninguém.

#### Duas identidades, e por quê

`WS_INSTANCE_ID` é identidade **lógica**: configuração, útil ao operador, usada
pelo bus para descartar o próprio eco. Nada garante que seja única — um
Deployment com valor fixo entrega a mesma string a todas as réplicas.

O diretório usa uma identidade **física**, gerada por `uuid` na inicialização de
cada execução do processo: não vem do ambiente, não vem do JWT, não vem de frame
WS, não é persistida e muda a cada restart. Dois pods com
`WS_INSTANCE_ID=chat-service` escrevem `U|runtime-A` e `U|runtime-B`, nunca o
mesmo campo.

Isso não é cosmético: **todo** o raciocínio de ordenação abaixo pressupõe um
único escritor por campo, e essa premissa não pode depender de um manifesto
estar correto. Ela é garantida pelo código.

O estado da pessoa é a **agregação** das asserções vivas, por prioridade
semântica e não por recência:

```
qualquer instância viva online -> online
senão qualquer uma away        -> away
senão                          -> offline
```

Uma aba ociosa em outra réplica não esconde um telefone em uso. O instante
reportado é o da asserção vencedora, então uma releitura que não mudou nada
reporta o mesmo instante.

Escrito no mesmo ponto em que a presença já é publicada, com os mesmos alvos, num
único pipeline — e só depois de o **sujeito** passar por `CanAccess` naquele
alvo. Lido no snapshot com um `HGETALL` mais um `MGET` das poucas instâncias
citadas: sem consulta por usuário, sem `SCAN`, sem `KEYS`. `complete: true`.

### Ordem entre escritas do mesmo campo

Ser dono do campo resolve a disputa _entre_ processos, não a disputa do processo
consigo mesmo: duas publicações do mesmo usuário escrevem o mesmo campo, e uma
delas pode ficar presa numa chamada lenta ao diretório. Sem ordem, uma publicação
enfileirada antes de um unsubscribe voltava depois do resubscribe e regravava
`online/t0` por cima de `away/t1`.

Como o campo tem um escritor só, ordenar localmente basta — sem CAS remoto, sem
Lua, sem tombstone, sem lock distribuído. Duas garantias:

- **Um sequenciador por usuário.** Todo `Record` e todo `Forget` passa por ele:
  publicação, unsubscribe, desconexão, revogação de acesso ou de sessão,
  reconciliação e shutdown. Nenhum ciclo de vida fala com o diretório por conta
  própria. Usuários diferentes não se bloqueiam.
- **Uma versão por asserção, criada quando a intenção nasce.** Não quando o
  trabalho começa a executar: uma versão emitida na execução faria trabalho
  antigo parecer novo só por ter sido lento, que é exatamente o caso a rejeitar.
  A versão viaja com o trabalho; ao executar, ela é comparada com a versão
  corrente daquela asserção, e trabalho ultrapassado é descartado **antes** do
  `HSET`/`HDEL` — não reparado depois, porque reparo é uma segunda escrita
  correndo com a primeira.

A versão avança quando a intenção muda: subscribe que cria cobertura,
unsubscribe que a remove, estado que precisa ser publicado, retirada. Heartbeat e
renovação de lease não mudam intenção e não avançam nada.

É um contador lógico, não um relógio: duas operações podem cair no mesmo
instante, e comparar `updated_at` não ordenaria nada. É dado interno — não vem do
cliente e não aparece no protocolo.

### Campos de processos mortos

Liveness decide se uma asserção conta, mas não a apaga, e o lease do hash é
renovado por todos os outros participantes da conversa. Sem limpeza, o hash ganha
um campo por processo que já serviu um membro e nunca encolhe.

O `HGETALL` do snapshot e o `MGET` de liveness já dizem quais campos são de
processos mortos. Depois de montar o roster, esses campos são removidos num
`HDEL` pipelined — sem `SCAN`, sem `KEYS`, nada além do que a leitura já trouxe,
e no máximo `presenceDeadFieldReapLimit` por chamada (leituras seguintes
convergem). Falhar aqui não falha o snapshot: o roster já está correto e a
próxima leitura tenta de novo.

Isso só é seguro por causa da identidade física: o campo pertence a **uma
execução** de um processo, que nenhum restart e nenhum outro pod pode reivindicar
de volta.

### Convergência para quem já está conectado

Eventos não chegam de um processo que morreu, e um observador já conectado nunca
pediria um snapshot novo. Por isso cada instância **reconcilia** periodicamente
(`presenceReconcileInterval`, metade do TTL de liveness) os alvos que têm
assinantes locais: relê o roster cluster-aware, compara com o último entregue e
envia um `presence.snapshot` **somente quando mudou**. Um alvo parado custa uma
leitura por varredura e nada na rede.

Isso cobre, com um mecanismo só: réplica morta sem cleanup, transição perdida,
sala que a conexão que saiu não assinava, e sujeito que perdeu acesso enquanto
estava ocioso.

No **shutdown gracioso** a instância retira as próprias asserções antes de parar,
então as demais convergem sem esperar o TTL. Ela não anuncia offline pelos
usuários: não sabe se estão conectados em outro lugar, e quem sabe reconcilia.

**Bus sem diretório, ou diretório indisponível.** A instância só pode falar por
si: responde com o próprio roster e `complete: false`. Nada de falso offline; o
cliente mantém `unknown` para quem não conhece.

## Observabilidade

Presença não é registrada em `INFO`. Só há log em falha operacional real
(publicação no barramento, fila cheia), sempre sem `user_id`, sessão ou e-mail.
