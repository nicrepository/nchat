# RF-58 / #798 — presença por WebSocket

O `chat-service` é a única autoridade sobre presença. Tudo viaja pela conexão
WebSocket existente (subprotocolo `nchat.v1`), pelo mesmo Hub e pelo mesmo
barramento de broadcast usados por `message.created`. O cliente não pode nomear
o usuário cuja presença muda nem afirmar nada sobre outra pessoa.

## Modelo (issue #798)

Quatro fatos independentes, combinados num único lugar
(`domain.ResolvePresence`, função pura):

| Fato                | Dono                                     | Valores                                                  |
| ------------------- | ---------------------------------------- | -------------------------------------------------------- |
| alcance das sessões | tracker + diretório (este documento)     | ativo, ocioso, nenhuma sessão válida                     |
| estado manual       | `chat.user_presence` (servidor)          | available, busy, dnd, brb, away, appear_offline + expira |
| atividade           | domínio de chamadas (`chat.calls`/lease) | in_call; in_meeting e presenting reservados              |
| mensagem de status  | perfil (`auth.users.custom_status`)      | texto livre — **não** é presença e não entra aqui        |

Presença efetiva (o que os outros veem): `available`, `busy`, `dnd`, `brb`,
`away`, `offline`. Precedência, da mais forte para a mais fraca:

1. nenhuma sessão válida → `offline` (um estado manual não cria conectividade);
2. `appear_offline` → `offline`, sem atividade;
3. `dnd`, `busy`, `brb`, `away` manuais → esse estado (uma chamada não os desfaz);
4. atividade (chamada) → `busy` — e nunca `away` por ociosidade;
5. `available` manual → `available`, mesmo ocioso;
6. alguma sessão ativa → `available`;
7. todas as sessões ociosas → `away`.

A atividade só é pública onde explica o estado (`busy`, `dnd`): "Volto já"
escolhido durante uma chamada não anuncia a chamada. Um estado manual expirado
simplesmente deixa de existir na leitura (o banco compara `manual_expires_at`
com o próprio relógio), e a presença volta ao automático recalculada das fontes
atuais — nada de "estado anterior" guardado para restaurar.

`appear_offline` é uma escolha de exibição, não de conectividade: as sessões
continuam conectadas, recebem tudo, e o WebSocket não é fechado.

## Comandos

Nenhum comando WebSocket muda presença.

O sinal de entrada do WebSocket que afeta presença é **atividade**: qualquer
frame recebido numa conexão autenticada renova o temporizador de inatividade
daquela conexão, creditado à identidade que o handshake afirmou. O cliente envia
`ping` em interação real (`keydown`, `pointerdown`, `touchstart`, `wheel`, `focus`
da janela, aba voltando a ficar visível), no máximo um a cada 25 s. Esconder a
aba ou perder o foco **não** envia nada: trocar de aba nunca é, sozinho, ausência.

O estado manual é escrito por HTTP (abaixo), não pelo WebSocket.

## API do estado manual (issue #798)

`/api/chat/presence/me` — sempre o próprio usuário da sessão; o workspace é
resolvido no servidor como em toda rota de chat. Orçamento de 30 requisições
por minuto por usuário (leitura e escrita).

- `GET` → `{"data": {"state": "dnd", "expires_at": "2026-10-01T21:00:00Z", "writable": true}}`,
  ou `{"state": null, "expires_at": null, "writable": true}` para presença
  automática. `writable` é o gate de rollout `CHAT_MANUAL_PRESENCE_ENABLED`
  (padrão `false`; ver o runbook de blue/green, seção 16c): fechado, a leitura
  responde normalmente e `PUT`/`DELETE` respondem
  `503 manual_presence_unavailable` sem tocar o banco.
- `PUT` com corpo **exatamente** `{"state": "...", "expires_at": "<RFC 3339>"}`.
  `state` ∈ `available|busy|dnd|brb|away|appear_offline`; `expires_at` entre
  1 minuto e 31 dias à frente do relógio do servidor. Campos desconhecidos
  (`user_id`, `workspace_id`, `updated_at`…) são 400. Membro inativo é 403.
  Responde o estado armazenado; o instante de atualização é do banco.
- `DELETE` → volta ao automático e responde `{"state": null, "expires_at": null, "writable": true}`.

Toda escrita bem-sucedida publica `presence.settings_changed` (abaixo) **antes**
de responder: o cliente pode receber o aviso enquanto o próprio `PUT` ainda está
em voo, e trata isso (uma leitura iniciada antes da confirmação nunca
sobrescreve o que a escrita confirmou). O
cliente transforma "1 hora", "4 horas", "Hoje" (fim do dia local),
"Esta semana" (fim do domingo local) e "Personalizado" num instante concreto no
fuso do navegador; nenhuma frase chega ao servidor. Escritas concorrentes: vence
a última a confirmar, deterministicamente.

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
    "state": "online",
    "availability": "busy",
    "activity": "in_call",
    "updated_at": "2026-08-11T10:05:00.123456789Z"
  },
  "created_at": "2026-08-11T10:05:00Z"
}
```

`target_type` é `channel` ou `dm`. `updated_at` é o instante em que o servidor
decidiu aquele estado, em RFC 3339.

- `state` é o RF-58 legado — `online`, `away` ou `offline` — e é **sempre** a
  projeção de `availability`: `available`/`busy`/`dnd` → `online`,
  `away`/`brb` → `away`, `offline` → `offline`. Clientes anteriores à #798
  continuam mostrando algo verdadeiro.
- `availability` (#798) é a presença efetiva. Ausente num servidor antigo; um
  valor que o cliente não conhece cai para `state`, nunca para um palpite.
- `activity` (#798) só aparece com `busy` ou `dnd`.

Para um `offline`, `updated_at` é o "visto por último" público. Para quem escolheu
aparecer offline, é o instante da escolha — e nada depois dele: a desconexão
real de quem está oculto não publica nada, então nenhum terceiro distingue
"aparecendo offline" de "offline".

Um evento só é publicado quando o que os observadores veem muda: ficar ocioso
sob Não perturbe, a atividade de quem está oculto, uma segunda aba — nada disso
vai ao fio. O payload não carrega e-mail, sessão, dispositivo, IP, user-agent,
`last_activity_at` cru, estado manual, expiração, nem qualquer metadado de
infraestrutura.

### `presence.settings_changed` (#798)

Direção: servidor → as sessões do **próprio** usuário. Sem payload: diz apenas
"releia `GET /api/chat/presence/me`". Atravessa o barramento como evento
endereçado a um destinatário (`recipient_user_id` = `target_id` = o usuário;
qualquer outra forma é descartada na recepção). Cada réplica que o recebe e
serve aquele usuário também recompõe e republica a presença dele.

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

`users` lista apenas quem está presente, então toda entrada tem `state`
`online` ou `away` (com `availability`/`activity` como no evento). Quem está
offline — inclusive quem escolheu aparecer offline — simplesmente não aparece.
O roster é composto com o estado manual e a chamada de todos os nomeados numa
única leitura ao banco; se essa leitura falha, o snapshot vai vazio e
`complete: false` (ninguém oculto vaza porque o banco estava lento). O
`updated_at` de cada entrada é a decisão mais recente que qualquer instância
publicou sobre a pessoa, então um snapshot nunca é mais velho que o evento que
o precedeu.

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
- **Atividade** publica apenas a transição `away → online`, e só se ela muda o
  que os outros veem. Atividade em quem já está `online` não gera evento.
- **Inatividade** (`defaultPresenceAwayTimeout`, 5 min) torna a conexão ociosa
  quando **todas** as conexões do usuário estão inativas. Uma conexão ativa —
  em qualquer aba, dispositivo ou réplica — mantém o usuário disponível; uma
  chamada ativa impede `away`; um estado manual vale por cima. O limiar é uma
  constante central testada com relógio falso, nunca uma espera real.
- **Desconectar** (#798) não publica nada de imediato quando cai a última
  conexão local: começa o _grace_ de desconexão (`defaultPresenceDisconnectGrace`,
  45 s). Durante ele o usuário continua presente para todos — inclusive as
  asserções no diretório e os snapshots lidos em qualquer réplica — e uma
  reconexão dentro dele (troca de Wi-Fi, reconexão do proxy, deploy) não
  produz evento algum. Só quando o grace acaba sem conexão de volta é publicado
  `offline` — e só se nenhuma outra instância ainda afirma a pessoa: nesse caso
  o que se publica é o estado agregado, nunca um falso offline.
- O `offline`, quando publicado, é endereçado aos alvos que a conexão assinava **mais
  os que esta instância ainda afirma sobre ele** — a aba que lia um canal pode ter
  fechado antes, e a instância que escreveu aquela asserção é a única que pode
  retirá-la. Esse registro de posse é limitado às conversas em que a pessoa é
  visível e some junto com ela; não é autoridade sobre nada, porque cada
  publicação passa por `CanAccess` de qualquer forma. Fechar uma de duas sessões
  não publica nada.
- **Queda abrupta** converge pelo mesmo caminho: o heartbeat de transporte
  (ping/pong, 30 s com 10 s de espera) derruba a conexão morta, o Hub a remove,
  começa o grace, e o fim do grace produz o `offline`. Esse é o _lease_ da
  sessão: crash, rede perdida, sleep ou processo encerrado convergem para
  offline em ~85 s (40 + 45), sem `beforeunload`, fechamento limpo da aba nem
  close frame. São três tempos distintos, com semânticas distintas: liveness da
  instância (`instanceLivenessTTL`, 90 s, no diretório), lease da sessão
  (heartbeat + grace) e ociosidade do usuário (5 min).

  **Janela real até o `offline`** (as constantes acima, sem espera real nos
  testes):

  | caso                               | nominal      | pior caso                                   |
  | ---------------------------------- | ------------ | ------------------------------------------- |
  | fechamento limpo da última aba     | 45 s (grace) | 60 s (grace + varredura do tracker, 15 s)   |
  | queda abrupta (rede, sleep, crash) | ~55–85 s     | ~100 s (40 s detecção + 45 s + 15 s)        |
  | Valkey/banco indisponível no fim   | —            | + 30 s por varredura de contexto com falha  |
  | réplica que servia a pessoa morre  | —            | ~135 s (90 s liveness + 45 s reconciliação) |

  A varredura do tracker é `grace/3`; a detecção de transporte é um ping a cada
  30 s com 10 s de espera pelo pong. Uma falha de leitura nunca vira `offline`:
  a publicação fica devida e é refeita na varredura de contexto seguinte
  (`chat_presence_deferred_total`). Na morte de uma réplica não há publicação
  de quem viu a saída — a correção chega como `presence.snapshot` da
  reconciliação — e por isso esse caso **não** grava "visto por último".

- **Contexto** (#798) — estado manual, chamada — muda sem nenhum socket mudar.
  A instância que trata a mudança republica na hora (`RefreshPresence`), o
  `presence.settings_changed` leva a mudança às outras réplicas, e uma varredura
  de contexto (`presenceContextSweepInterval`, 30 s; uma leitura em lote por
  workspace, só dos usuários com sessão local ou em grace) pega o que expira
  sozinho: estado manual vencido, lease de chamada que caducou. Publica somente
  o que mudou. Quando a varredura percebe que o estado manual de alguém mudou
  sem escrita (expirou), cada réplica envia `presence.settings_changed` às
  sessões **locais** daquela pessoa, uma vez — é o que faz o menu e o avatar do
  próprio usuário saírem do estado vencido sem depender de relógio do navegador.
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

O próprio estado manual também é relido a cada (re)conexão: um
`presence.settings_changed` enviado enquanto o socket estava caído se perdeu.
Além disso o cliente agenda um timer no `expires_at` que recebeu; quando ele
vence, o estado manual deixa de ser mostrado como ativo e o cliente relê a API —
sem esperar aviso nenhum. Se o relógio do servidor ainda o mantém, o cliente
não insiste: o aviso da varredura do servidor resolve.

## Múltiplas instâncias

`presence.updated` atravessa o barramento Valkey como os demais eventos, com
supressão de eco por `source_instance_id` e canonicalização estrita na recepção
(alvo `channel`/`dm`, `user_id` UUID, `state` no conjunto fechado,
`availability` e `activity` nos seus conjuntos fechados, `availability`
coerente com `state` e `activity` só com `busy`/`dnd`; qualquer outro payload é
descartado).

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

Uma aba ociosa em outra réplica não esconde um telefone em uso. O `updated_at`
de um snapshot não vem destas asserções: é a versão da presença efetiva (ver
"Versão da presença efetiva" abaixo), a mesma que os eventos carregam.

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

### Alcance por pessoa (#798)

Os rosters por alvo dizem quem está numa conversa; não dizem tudo sobre uma
pessoa — duas sessões dela podem não compartilhar conversa alguma, ou uma delas
pode não ter assinado nada ainda. A agregação da pessoa tem por isso um hash
próprio, independente de subscription:

```
nchat:chat:ws:presence-user:{workspace}:{user}
  r:{runtime instance id} -> state|nanos|geração   # alcance; cada instância só o seu campo
  p                       -> availability|activity|seg|nseg   # projeção efetiva
  v                       -> revisão dos fatos (abaixo)
  w:{token}               -> ms até quando   # mudança de fato em andamento
```

O prazo de uma marca `w:` **nasce na autoridade**: o início envia uma duração
(30 s), e o script grava `TIME + duração`. Quem data a marca é o mesmo relógio
que a julga e que a remove, então nenhuma defasagem entre o relógio da aplicação
e o do Valkey encurta ou estica uma marca.

Uma marca vencida é uma mudança que **ninguém encerrou** — o escritor morreu ou
o fim falhou —, e o banco pode ter mudado sob ela. Removê-la é uma
**recuperação**: cada início de mudança e cada commit de projeção sobre a pessoa
remove, no mesmo script, até `factsMarkReapLimit` (32) marcas cujo prazo já
passou no relógio do Valkey — nunca uma em vigor — e, se removeu alguma, avança
a revisão **uma vez**. Uma leitura feita durante a mudança carrega a revisão que
o início produziu; a recuperação a torna antiga, e ela não sobrevive à expiração
da marca. Marcas só nascem num início de mudança, que recupera antes de gravar a
sua; então uma pessoa cujos fins falham repetidamente carrega no máximo as
marcas em vigor e uma vencida, por mais que o hash seja renovado. Sem `SCAN`,
sem leitura do workspace: só o hash da pessoa que o script já está tocando.

TTL de 24 h, renovado pelas escritas e pelo heartbeat da instância
(`instanceHeartbeatInterval`, 30 s). A liveness de cada campo `r:` é a mesma do
diretório (as chaves de instância, lidas num `MGET`), e campos de instâncias
mortas são removidos na leitura com o mesmo limite
`presenceDeadFieldReapLimit`. Sem `SCAN`, sem `KEYS`, sem leitura por conversa.

**Uma autoridade, papéis separados.**

| camada                       | responde                                 | quem escreve                                          |
| ---------------------------- | ---------------------------------------- | ----------------------------------------------------- |
| alcance (`r:` do hash acima) | quão alcançável a pessoa está no cluster | cada instância, só o próprio campo                    |
| roster por alvo              | a quem entregar numa conversa (fan-out)  | cada instância, só a própria asserção                 |
| ponte legada                 | sessões de réplicas anteriores à #798    | ninguém novo — só lida, e só durante o rollout        |
| fatos efetivos               | alcance + estado manual + chamada        | hash acima, `chat.user_presence`, domínio de chamadas |
| projeção pública             | estado efetivo, versão, visto por último | o script de projeção (abaixo)                         |

**Modo moderno** (toda instância escreve alcance por pessoa): o hash da pessoa é
a autoridade de alcance. O roster de uma conversa decide só quem aparece nela e
para quem o evento vai; uma entrada do roster escrita por uma instância
moderna repete, possivelmente atrasada, o que o hash já diz e **não é lida**
como alcance. Um roster que ficou `online` porque a escrita por alvo falhou não
promove ninguém: o snapshot mostra o `away` do hash.

**Modo misto** (rollout, gate de presença manual fechado): uma réplica anterior
à #798 só escreve nos rosters. Cada instância grava na própria chave de
liveness a capacidade `user-reach-v1`; a de uma réplica antiga diz `1`. Só as
entradas de roster de instâncias vivas **sem** essa capacidade entram, como
sessões adicionais — preenchem o que falta, nunca substituem nem duplicam uma
sessão moderna, e uma leitura falha ou vazia não remove nada. Uma sessão legada
ativa conta como outro dispositivo (um desktop moderno ocioso não a esconde). A
ponte procura no primeiro roster da pessoa; uma sessão legada que não divide
conversa com ela não é vista. Ela é **desligada** quando
`CHAT_MANUAL_PRESENCE_ENABLED=true`, que por sua vez só abre quando nenhuma
réplica antiga resta (runbook, seção 16c).

Consequências:

- um desktop ocioso não publica `away` enquanto um notebook digita por outra
  réplica, mesmo sem conversa em comum;
- um dispositivo saindo não publica `offline` enquanto outro segue conectado em
  qualquer réplica;
- **falha não fabrica conhecimento**: se gravar o alcance, lê-lo, ler o contexto
  ou projetar falhar, nada é publicado, nada de "visto por último" é escrito, e
  a publicação fica **devida** (com a audiência que tinha). A varredura de
  contexto seguinte a refaz contra a resposta real; se nesse meio-tempo outra
  sessão manteve a pessoa presente, não sai evento nenhum;
- a memória de "o que esta réplica publicou" só existe enquanto ela serve a
  pessoa (ou lhe deve uma publicação); some quando a última sessão local sai.

**Ciclo de vida e fencing do alcance.** O tracker dá a cada ciclo de vida local
de uma pessoa — da primeira conexão ao offline que a última desconexão produz —
uma geração, monotônica no processo. Reconectar dentro do grace é o mesmo ciclo;
depois dele, um novo. O campo `r:` guarda a geração, e só um script Lua escreve
nele: uma afirmação ou retirada com geração **menor** que a guardada é recusada
atomicamente, sem escrever nada e sem mover a revisão. Uma saída decidida para
o ciclo 10 não retira, em réplica nenhuma, o alcance que o ciclo 11 registrou.
No desligamento, o processo retira todos os seus ciclos (geração máxima).

**Uma reconexão só é pública depois de registrada.** Nenhuma composição — nem a
desta réplica — usa o tracker local no lugar do próprio campo: o alcance desta
instância é o que está no hash. Uma reconexão passa a contar quando a sua
publicação grava a nova geração; até lá ninguém a mostra. Se a retirada do
ciclo anterior chega ao hash antes disso (a reconexão aconteceu com a retirada
já a caminho), há um intervalo em que a pessoa **não tem ciclo registrado** e
é, publicamente, offline — e a saída superada não publica `offline` nem grava
"visto por último" (abaixo); a publicação da reconexão, na mesma fila,
registra a nova geração e a anuncia. Não há janela em que o tracker diga online
e alguma réplica mostre online sem alcance no hash, nem em que um ciclo
registrado seja apagado por um anterior.

**Transição local obsoleta.** Além do fencing, o hub confere o tracker ao
receber a transição (uma saída que não é mais verdade é descartada e não toma
a cobertura do grace), ao tirá-la da fila (a mudança passa a ser o estado atual
do tracker, com a geração atual — a coalescência pode ter posto uma saída
depois da reconexão), antes de projetar e antes de publicar ou gravar "visto
por último".

### Versão da presença efetiva (#798)

`updated_at` de um evento ou de um snapshot é o instante em que a presença
efetiva da pessoa **assumiu o valor atual**, e é o mesmo em todas as réplicas.

**Fatos, versões e prazos.** Cada fato de que a projeção depende tem uma
versão ou um prazo, e o commit confere todos:

| fato                          | onde nasce                          | quando deixa de valer             | o que o invalida no commit                                    |
| ----------------------------- | ----------------------------------- | --------------------------------- | ------------------------------------------------------------- |
| alcance de uma instância      | `r:` (conectar, ativo↔ocioso, sair) | retirada, ou a instância morre    | revisão (`reachScript` move ao mudar estado ou geração)       |
| instância viva                | chave de liveness (heartbeat)       | TTL de 90 s sem heartbeat         | `EXISTS` de cada instância contada, no script                 |
| campo de instância morta      | reap na leitura                     | —                                 | revisão (`reapScript` move quando remove)                     |
| hash da pessoa                | qualquer escrita                    | TTL de 24 h                       | revisão (hash sumido lê como 0)                               |
| estado manual (PUT/DELETE)    | `chat.user_presence`                | `expires_at`, ou outra escrita    | escrita: revisão + marca; fim: prazo (`ValidUntil`)           |
| chamada 1:1 ativa             | `chat.calls`                        | fim da chamada                    | revisão + marca, anunciadas pela própria transação            |
| participação por lease        | `chat.call_participant_leases`      | sair/encerrar, ou o lease vence   | entrar/sair/encerrar/ressuscitar: revisão + marca; fim: prazo |
| extensão de lease ainda vivo  | `chat.call_participant_leases`      | — (não muda o fato)               | nada: quem usou o lease antigo está limitado pelo fim dele    |
| marca de mudança em andamento | `w:` no hash                        | fim da mudança, ou o prazo (30 s) | em vigor → conflito; vencida → recuperação (revisão + 1)      |

**Relógios: um para cada pergunta.** Os instantes persistidos são UTC absolutos
(fim do estado manual, fim do lease, prazo da marca). Cada pergunta tem um único
relógio que a responde:

| pergunta                                             | relógio                                                                                                              |
| ---------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| o fato (estado manual, chamada) vale **na leitura**? | o do PostgreSQL (`clock_timestamp()` na consulta de contexto)                                                        |
| uma renovação de lease é extensão ou retorno?        | o do PostgreSQL, na mesma linha travada (abaixo)                                                                     |
| a composição ainda vale **no commit**?               | o da autoridade: `TIME` do Valkey dentro do script; sem Valkey, o relógio do armazenamento local lido sob o seu lock |
| quando nasce e quando vence uma marca `w:`?          | o mesmo da autoridade do commit                                                                                      |
| que versão a projeção recebe?                        | o de quem compõe, nunca para trás: `max(agora, guardado + 1 ns)`                                                     |
| o que a interface mostra (contagem regressiva)       | o do navegador — só exibição                                                                                         |

**O relógio da aplicação não decide validade nenhuma.** Ele só data versões.

O compositor **não** rejulga com o próprio relógio um fato que o banco disse
estar valendo: um estado manual devolvido pela leitura — Disponível, Ocupado,
Não perturbe, Volto já, Ausente ou Aparecer offline — é usado como veio, com o
seu `expires_at` como `ValidUntil`, até que a autoridade do commit o recuse.
Um relógio da aplicação adiantado não transforma um "Não perturbe" ainda válido
no banco e no Valkey em "Disponível", nem revela quem escolheu aparecer offline;
o mesmo vale para o filtro de "aparecer offline" do painel de detalhes do canal.
Estado manual sem fim não gera prazo nenhum. O compositor entrega o fim de cada
fato temporal usado (`ValidUntil`, o menor deles) a quem decide o commit. O instante que o
compositor capturou (`ProjectionCommit.Now`) ordena versões e não decide
validade. Quem leu `Ocupado` válido até T10, montou o commit em T9 e só chegou
ao script em T11 é recusado — o script lê o tempo quando executa, não recebe um
tempo antigo — e recompõe, sem depender de varredura, aviso ou outro commit.
`linearização ≥ prazo ⇒ expirado`, com o prazo exatamente no instante do commit
contando como vencido. A defasagem entre o relógio do PostgreSQL e o do Valkey
é o limite dessa medida: no pior caso, um commit é recusado até a leitura
seguinte (relógio do banco atrás) ou um estado termina um pouco antes
(relógio do banco à frente) — nunca um fato vencido no commit é publicado.

**Mudanças de fato no banco.** Estado manual e participação em chamada só mudam
anunciados à presença. Não há transação entre PostgreSQL e Valkey; a ordem a
torna segura:

1. **Início**: para cada pessoa afetada, marcas vencidas são recuperadas (acima),
   a revisão avança e uma marca `w:` é gravada com prazo de 30 s do relógio da
   autoridade. **Se falhar, nada muda**: o PUT/DELETE responde
   `503 presence_unavailable`, o comando de chamada responde `call_unavailable`
   — como já acontece quando o limite de início de chamada (também no Valkey)
   está indisponível.
2. A escrita no banco é **confirmada** (limitada a 10 s). O prazo da marca é
   maior que tudo o que pode separar o início da confirmação — a resposta do
   início (`directoryWriteTimeout`, 2 s) mais a escrita —, e as três são
   durações, então a relação vale em qualquer relógio; uma constante editada
   além dela não compila (`presence_facts.go`).
3. **Fim**: a revisão avança de novo e a marca é removida.

Estado manual: `Hub.ChangePresenceFacts` envolve a escrita inteira. Chamadas:
quem sabe quem é afetado é a própria transação, depois de pegar os locks — então
ela abre o anúncio (`Hub.OpenPresenceFacts`) **antes do commit**, com o conjunto
que esses locks tornaram definitivo:

- entrar, iniciar uma chamada de recurso, sair: o ator, sob o lock do
  participante e da linha da chamada;
- aceitar ou encerrar uma chamada 1:1: as duas partes (tocar não é estar em
  chamada; recusar ou cancelar não anunciam nada);
- **encerrar uma chamada de recurso**: todo titular de lease, lido sob o
  `FOR UPDATE` da linha da chamada. Entrada, renovação e saída também passam por
  esse lock, então o conjunto não muda até o commit: quem entrou antes é
  anunciado pelo encerramento; quem chega depois encontra a chamada encerrada e
  é recusado;
- **renovar um lease**: a mesma instrução que renova (CTE com `FOR UPDATE` +
  `RETURNING`) diz se o lease estava vivo. Estendê-lo não é mudança de fato e
  não é anunciado; renovar um lease **vencido** recoloca o ator na chamada — é
  anunciado como uma entrada. Vencido é `expires_at <= clock_timestamp()`, o
  complemento exato de "vivo" na leitura de contexto. A leitura de contexto lê
  os leases `FOR SHARE`: se sobrepõe uma renovação ainda não confirmada, espera
  por ela e lê o lease novo; uma renovação depois dela é julgada num instante
  posterior. Um lease que a leitura viu vencido só volta por uma renovação
  julgada retorno — anunciada.

Manter a transação de chamada aberta durante o início no Valkey segura o lock
daquela chamada (ou daquele participante) por um round trip, limitado por
`directoryWriteTimeout`; nenhuma outra chamada ou pessoa espera.

A máquina de estados da marca:

```text
INÍCIO         agora = TIME; v++ (+1 se recuperou vencidas); w[token] = agora + 30 s
EM ANDAMENTO   commit encontra w em vigor           → conflito
FIM NORMAL     v++; apaga w                          (não espera o prazo)
FIM AUSENTE    TIME passa do prazo (escritor morreu ou o fim falhou)
  próximo início ou commit sobre a pessoa:
               remove até 32 w vencidas; v++ uma vez → recuperação
  composição lida durante a mudança (v do início)    → conflito, relê o banco
  composição nova                                   → lê o banco como ficou
```

Um compositor que leu a revisão antes do início conflita. Um que a leu durante a
mudança encontra a marca em vigor no commit e conflita — ou, se a marca venceu
sem fim, encontra a revisão que a recuperação moveu e conflita do mesmo jeito.
Um que a leu depois do fim já lê o banco novo. Se o escritor morreu antes de
escrever, a recomposição depois da recuperação lê o fato antigo; se morreu
depois, lê o novo. Uma revisão extra (início que deu certo seguido de escrita
que falhou) só causa uma recomposição. Assim, uma composição capturada antes ou
durante uma mudança de fato nunca é commitada depois que a mudança ficou
visível sem uma nova leitura dos fatos.

O encerramento automático de chamadas (`ExpireDue`) não anuncia nada: ele só
encerra chamadas tocando que ninguém atendeu e chamadas de recurso sem lease
vivo — fatos que já tinham deixado de valer pelo prazo.

**Composição e commit.** Uma publicação ou um snapshot:

1. lê o hash da pessoa — alcance, projeção e revisão — **antes** do estado
   manual e da chamada no banco;
2. resolve a presença efetiva com o que leu e calcula `ValidUntil`;
3. confere de novo o tracker e chama o script de projeção com o token lido
   (revisão, `ValidUntil`, instâncias contadas), que em uma única operação
   atômica, no relógio do Valkey:
   - `agora ≥ ValidUntil`, ou alguma instância contada morreu → **expirado**;
   - primeiro recupera marcas vencidas (podendo avançar a revisão); depois,
     marca de mudança em vigor, ou revisão diferente da lida → **conflito** —
     mesmo que a composição seja igual à projeção guardada: fatos que mudaram
     não confirmam nada;
   - projeção igual à guardada (ou `offline` sem projeção) → **inalterada**;
   - senão → **aplicada** em `max(agora do compositor, guardado + 1 ns)`, e a
     revisão avança.
4. Num conflito ou expiração, relê e recompõe — até `projectionAttempts` (3)
   vezes. Esgotado o limite, a publicação fica devida
   (`chat_presence_deferred_total{reason="projection_conflict"}`) e nada
   obsoleto é publicado; o snapshot reporta a projeção já gravada.

O **ponto de linearização** é o script de projeção: uma projeção pública
representa um conjunto de fatos que, no instante do seu commit — medido pela
autoridade —, tinha a mesma revisão lida, nenhuma mudança em andamento, nenhum
fato temporal vencido e toda instância contada viva. Entre réplicas o hash é o
mesmo, então toda réplica compete pela mesma revisão. Projeção, versão e
revisão nunca regridem. Valor malformado em `p`, `v` ou `r:` é erro explícito,
nunca sobrescrito em silêncio.

Daí: a versão avança somente quando a projeção muda — por alcance, estado
manual, expiração ou chamada, venha a mudança de onde vier —; dois snapshots sem
mudança carregam a mesma versão; um snapshot que vê uma mudança que nenhum
evento levou ainda já a carrega com versão nova, e o evento que vier depois traz
a mesma, não uma maior; e duas réplicas que publicam a mesma mudança publicam a
mesma versão. Uma réplica publica quando a projeção mudou, quando a versão que
ela entregou às próprias salas ficou para trás, ou quando há audiência nova
(subscribe, reconexão).

**Snapshot projeta antes de filtrar.** Quem é omitido de um roster (offline,
aparecendo offline) tem a projeção reconciliada antes: o snapshot que é o
primeiro a ver alguém se esconder grava o `offline` público, de modo que
`PublicLastSeen` e o próximo evento sobre a pessoa dizem o mesmo instante.

**Instância única (sem Valkey).** O armazenamento local guarda o mesmo contrato.
Ao esquecer quem saiu (sem alcance, projeção `offline`), não guarda nada por
pessoa: dois valores do processo inteiro mantêm a ordem — o último instante
emitido (todo instante novo é posterior, mesmo com o relógio voltando) e a
revisão de quem não tem registro (move ao esquecer alguém e a cada mudança de
fato sobre quem não tem registro; uma composição iniciada antes conflita). Uma
mudança de fato sobre alguém que este processo não serve não cria registro: só
a marca em andamento, apagada ao terminar. O armazenamento data a marca e decide
prazo e marca no seu próprio relógio, lido sob o lock no momento da decisão, e
recupera a marca vencida avançando a revisão, como o script.

## Contexto de chamada

Ver [call-lifecycle.md](../architecture/call-lifecycle.md#presença-issue-798).
Em chamada é a participação que o próprio domínio de chamadas afirma; abrir um
modal de chamada não é estar em chamada. Ao terminar, a presença é recalculada
das fontes atuais: volta o estado manual ainda válido, ou o automático.

## Não perturbe

DND é consumido pela policy de notificações como
`Preferences.DoNotDisturb` (policy version 3): silencia toast, som e push e
nada mais — ver
[notification-policy.md](../architecture/notification-policy.md#precedencia).

## Visto por último

`chat.user_presence.last_seen_at` é escrito pelo servidor somente com evidência
autoritativa de saída: a réplica cuja publicação moveu a projeção para
`offline` — ou a encontrou já `offline` — **e** cuja leitura do cluster não
encontrou sessão válida da pessoa em instância viva alguma. O valor gravado é a
versão da projeção `offline`, o mesmo `updated_at` que o evento levou;
monotônico (`GREATEST`), idempotente e nunca vindo de um cliente. **Não** é
escrito ao escolher aparecer offline (as sessões continuam lá), numa falha de
leitura (fica devido, como a publicação), nem quando uma réplica morre sem que
nenhuma outra veja a saída (ver a tabela da janela real em "Ciclo de vida").
Quando quem estava aparecendo offline sai de verdade, o registro recebe o
instante em que a pessoa se escondeu — o último em que alguém a viu —, não o da
saída; quem nunca foi mostrado não ganha registro.

O `last_seen_at` do perfil 1:1 lê primeiro a projeção (`Hub.PublicLastSeen`) e
só depois o registro: enquanto alguém aparece offline o registro guarda a saída
real anterior, e respondê-lo distinguiria "aparecendo offline" de "offline".
Falha ao ler a projeção omite o campo em vez de cair no registro.

Aparece no `updated_at` do evento `offline` e em `last_seen_at` do perfil 1:1
(`GET /api/chat/dm/{id}/profile`), este último **somente** enquanto a pessoa
está offline. A UI o mostra no
cabeçalho, no painel e no tooltip — nunca permanentemente na sidebar. Uma
futura política de privacidade do workspace só precisa omitir esses dois campos.

## Observabilidade

Presença não é registrada em `INFO`. Só há log em falha operacional real
(publicação no barramento, fila cheia, leitura de contexto), sempre sem
`user_id`, sessão ou e-mail. Métricas (rótulos de conjunto fechado, nunca
usuário, sessão ou workspace):

- `chat_presence_transitions_total{availability}` — mudanças de presença
  efetiva publicadas;
- `chat_presence_disconnect_grace_total{outcome="recovered"|"expired"}` —
  reconexões dentro do grace (flicker evitado) e graces que viraram offline;
- `chat_presence_deferred_total{reason="reach_write"|"reach_read"|"context_read"|"projection"|"projection_conflict"}`
  — publicações adiadas porque uma leitura ou escrita falhou (refeitas na
  varredura de contexto); crescer continuamente é Valkey ou banco com problema;
- `chat_presence_manual_update_total{result}` e
  `chat_presence_manual_update_duration_seconds{result}` — escritas do estado
  manual por resultado (`success`, `invalid`, `denied`, `rate_limited`, `error`)
  e latência.
