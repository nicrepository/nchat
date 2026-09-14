# Policy Engine de notificacoes

Autoridade unica de decisao de entrega do NChat (issue #744, parent #678).
Contrato em Go: `libs/go/platform/notificationpolicy`.

Web Push, Service Worker, BroadcastChannel, DND, URGENT, digest pos-expediente,
feriados/ferias e a UI de preferencias **estao fora** desta camada e continuam
nao existindo.

## O que o engine decide

Dado um evento normalizado e o contexto ja resolvido de **um** destinatario,
`Evaluate` produz um Delivery Plan explicito por canal:

| Canal      | O que e                                    |
| ---------- | ------------------------------------------ |
| `in_app`   | a superficie interruptiva no app — o toast |
| `sound`    | o som local                                |
| `web_push` | a notificacao de sistema operacional       |

Unread, read, badge e sidebar **nao** sao canais e nao passam por aqui. Sao
propriedades da mensagem: suprimir um alerta nunca pode esconder a mensagem.

## Onde vive, e por que nao no notification-service

O pacote fica em `libs/go/platform` pelo mesmo motivo de `notificationevent`
(#741) e `workschedule` (#743), que sao justamente as suas duas entradas: a
regra tem mais de um consumidor previsto e nenhum dono natural entre os
servicos.

Sao dois consumidores reais:

- **notification-service**, cujo worker ja tem a costura pronta
  (`worker.Evaluator`, `Verdict`) e hoje a preenche com este engine;
- **chat-service**, que e quem detem a conexao viva por onde um alerta in-app
  trafega.

Um pacote em `services/notification-service/internal/...` **nao pode** ser
importado por chat-service — o Go proibe. O segundo consumidor teria de
reescrever as regras, que e exatamente a duplicacao que a #744 existe para
acabar. Por isso a decisao fica em `libs`, e nao porque o nome da issue cita
notification-service.

## Separacao de camadas

```text
                          notificationpolicy.Evaluate
                              (decisao autoritativa)
                                      |
            +-------------------------+-------------------------+
            |                                                   |
   notification-service                                   chat-service
   linha da outbox -> Context                     mensagem publicada -> Context
   Verdict -> Deliverer (push)                    payload WS -> browser consome
```

`Evaluate` e **pura**: nao le relogio, banco, Valkey, browser nem provider; nao
resolve timezone, nao calcula calendario, nao grava dedupe, nao tenta entregar
nada e nao pode falhar. Todo I/O acontece antes (montagem do `Context`) ou
depois (entrega/transporte).

O browser **consome** a decisao; nao a recalcula. Ele so pode aplicar
restricoes locais monotonicas: `deny` central nunca vira `allow`.

## Consumidor em runtime: push

`services/notification-service/internal/worker/notification_policy.go`.

O worker da outbox (#742) ja tinha a costura — `worker.Evaluator`, que decide se
uma linha `pending` vira `eligible` ou `suppressed`. `NewPolicyEvaluator` e o
adapter que a preenche: mapeia a linha para `notificationpolicy.Context`,
chama `Evaluate` uma vez e traduz o Delivery Plan de volta para o `Verdict` que
o worker ja gravava. O wiring de producao
(`app.notificationWorkerDeps`) nomeia esse evaluator explicitamente.

**Nao existe mais fallback permissivo.** `DeliverEverything` — "tudo que um
producer escreveu e elegivel" — foi removido. Um `Evaluator` ausente no
construtor resolve para a mesma autoridade central, nunca para permitir tudo:
a falta de policy nao pode ser indistinguivel de uma policy que autorizou.

A direcao da dependencia e so uma: `worker -> notificationpolicy`. O engine nao
conhece outbox, claim, lease nem entrega.

### Inputs reais disponiveis hoje (worker)

Vem da propria linha de `chat.notification_outbox`, sem join nenhum:

| Campo do `Context`                      | Coluna                                    |
| --------------------------------------- | ----------------------------------------- |
| `EventID`, `WorkspaceID`, `RecipientID` | `id`, `workspace_id`, `recipient_user_id` |
| `EventType`                             | `kind`                                    |
| `Priority`                              | `priority`                                |
| `Origin`                                | `origin`                                  |

E mais um, resolvido na mesma leitura:

| Campo do `Context`              | Fonte                                                                    |
| ------------------------------- | ------------------------------------------------------------------------ |
| `Preferences.Muted`             | `chat.conversation_notification_prefs`, via `chat.messages` — ver abaixo |
| `Preferences.ConversationLevel` | a mesma linha, na mesma leitura (#136)                                   |

`origin` passou a ser projetado nesta correcao. Era persistido desde a #741 e
nao era lido, entao um import de um ano de mensagens era entregue como se
tivesse acabado de acontecer.

### Inputs ainda indisponiveis no worker, e o estado explicito de cada um

Nenhum e um buraco: cada um e um estado que o contrato ja define e para o qual o
engine tem resposta documentada.

| Campo                        | Estado usado         | Por que, e o que falta para mudar                                                                                                                                 |
| ---------------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Presence`                   | `PresenceUnknown`    | uma linha de outbox nao carrega sessao e este processo nao tem registro delas. A superficie de presenca desconhecida e so push — que e o que este worker entrega. |
| `WorkSchedule`               | `StateNotConfigured` | a #743 entregou o dominio temporal e **deliberadamente nenhum escritor**. E o estado real, nao um default inventado aqui; o engine documenta que ele nao suprime. |
| `Preferences.SoundMode`      | zero (default `all`) | nao existe source of truth server-side: mora no browser. Irrelevante aqui — som nunca esta na superficie de uma presenca desconhecida.                            |
| `Conversation`               | zero                 | provadamente inerte para este consumidor: a unica regra que a le e a preferencia de som.                                                                          |
| `Duplicate`, `BurstCooldown` | zero                 | nao ha coordenador que os informe; o indice unico da outbox ja e a deduplicacao persistente.                                                                      |

`WebPushAvailable` e `true`, e isso e fato estrutural e nao chute:
`app.startNotificationWorker` recusa iniciar um worker sem `Deliverer`, entao um
worker que esta avaliando tem canal por onde entregar.

### Mute e nivel: onde sao resolvidos, e por que nao no engine

`Preferences.Muted` e `Preferences.ConversationLevel` vem de
`chat.conversation_notification_prefs` — o mesmo source of truth que os
endpoints de preferencia escrevem e que a sidebar le por `ListPreferences`.

A #136 acrescentou a coluna `notification_level` e tornou `muted_at` nullable
(migrations `000050`/`000051`); nenhuma tabela nova, nenhum cache novo. Os dois
campos saem da **mesma linha, na mesma leitura**: `mutedProjection` e
`levelProjection` compartilham o join (`preferenceRowJoin`), entao continua
sendo uma statement por batch.

O `EXISTS` do mute passou a testar `p.muted_at IS NOT NULL` em vez da existencia
da linha, e essa e a parte crucial da migration para a entrega: uma linha com
`muted_at` nulo e alguem que pediu para continuar ouvindo mencoes, e ler a
presenca dela como mute silenciaria todos eles.

A resolucao acontece **antes** de `Evaluate`, na projecao que a propria leitura
da outbox ja fazia (`notificationColumns`, em
`notification_outbox_store.go`): um `EXISTS` correlacionado que atravessa
`chat.messages` — a linha da outbox nomeia `message_id`, e a conversa e a da
mensagem (`channel_id` XOR `dm_conversation_id`) — ate a preferencia.

Consequencias que importam:

- **Sem N+1.** O mute e resolvido dentro da consulta que ja existia, entao um
  batch de `BatchSize` eventos custa exatamente **uma** query, a mesma que
  custava antes. Depois disso a montagem do `Context` e `O(n)` e nao toca o
  banco. Uma busca por evento seria uma query por notificacao, que e justamente
  o que a #744 proibe.
- **O engine continua puro.** Storage resolve estado; a policy decide
  consequencia. Nem `storage` nem `worker` contem `if muted { suppress }` — as
  unicas regras que leem esses campos sao `denyMuted` e
  `denyConversationLevel`, no pacote central.
- **Escopo.** Tres predicados, todos obrigatorios: `p.user_id =
o.recipient_user_id` (o mute e individual — a tabela e chaveada por usuario
  exatamente por isso), `p.workspace_id = o.workspace_id` e `m.workspace_id =
o.workspace_id`. O segundo nao e redundante: a tabela de preferencias
  referencia workspace e alvo por FKs **separadas**, entao uma linha que nomeia
  um workspace que nao possui o alvo e inserivel — o predicado e o que a torna
  inutilizavel. Todo identificador comparado vem da linha persistida ou da
  mensagem que ela nomeia; nada que um cliente afirmou participa.
- **Ausencia de linha e "nao silenciado"**, que e o que a tabela ja significa:
  desmutar apaga a linha em vez de gravar `false`. O default e o do produto, nao
  um chute do adapter.
- **Membership nao e reavaliada na leitura.** O caminho de escrita ja a
  estabeleceu (`NotificationPrefStore.Mute` so admite conversa visivel), e
  reaplicar visibilidade aqui faria uma membership revogada **desfazer** um
  mute — a direcao que alerta quem pediu para nao ser alertado.

Provado contra PostgreSQL real em `TestMuteResolutionPostgreSQL`: o mute de um
destinatario nao alcanca outro na mesma conversa, uma preferencia de outro
tenant nao alcanca o evento, e a ausencia de linha volta a ser o default.

## Autoridade central e execucao local

Duas perguntas diferentes, e a fronteira entre elas e o contrato desta issue.

**CENTRAL POLICY** — "quais superficies este destinatario PODE receber?"

Responde o servidor, por destinatario, com os fatos autoritativos que ele tem:
workspace, conversa, tipo de evento, tipo de conversa, prioridade, origem,
horario de trabalho, mute server-side, disponibilidade de canal. O resultado e
o **conjunto maximo de superficies permitidas** para aquela pessoa.

**LOCAL EXECUTION** — "mesmo autorizado, esta sessao deve executar agora?"

Responde o browser, e so pode **remover**. Nao ha caminho de `deny` central para
execucao: cada gate em `soundRules.ts` comeca recusando o que nao for um `allow`
explicito.

### O que ainda e LOCAL_ONLY hoje, e por que

| Fato                                | Estado       | Por que nao e central                                                                                                                                                                                                                              |
| ----------------------------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| aba em foco                         | `LOCAL_ONLY` | nenhum mecanismo reporta foco ao servidor, e esta issue nao cria um. **Gate obrigatorio do `in_app`**: uma janela que ninguem olha nao desenha um toast, e ele nao fica enfileirado para depois — a superficie de SO e a que existe para esse caso |
| conversa visivel **nesta** aba      | `LOCAL_ONLY` | idem: e um fato sobre a UI de um cliente. O engine so o le sob `PresenceForeground`, que e uma presenca que este caminho nunca afirma                                                                                                              |
| modo de som (`off`/`all`/...)       | `LOCAL_ONLY` | mora em `localStorage`; #136/#729 ainda nao entregaram source of truth server-side. So descarta um som ja permitido, usando a classe do servidor                                                                                                   |
| permissao de Notification, autoplay | `LOCAL_ONLY` | capacidade tecnica do browser                                                                                                                                                                                                                      |

**Nenhum desses e policy** e nenhum deles aparece como reason central.

### Classificacao dos inputs do realtime

| Input                           | Classificacao    | Origem                                            |
| ------------------------------- | ---------------- | ------------------------------------------------- |
| `RecipientID`                   | `AUTHORITATIVE`  | a assinatura que esta recebendo (`client.userID`) |
| `WorkspaceID`, `EventType`      | `AUTHORITATIVE`  | a mensagem                                        |
| `Conversation`                  | `AUTHORITATIVE`  | alvo da mensagem                                  |
| `Origin`                        | `AUTHORITATIVE`  | `live` — e a publicacao de um commit              |
| `Preferences.Muted`             | `AUTHORITATIVE`  | `chat.conversation_notification_prefs`            |
| `Preferences.ConversationLevel` | `AUTHORITATIVE`  | a mesma linha, na mesma leitura em lote (#136)    |
| `Presence`                      | `AUTHORITATIVE`  | `PresenceConnected` — ha socket aberto            |
| `WorkSchedule`                  | `NOT_CONFIGURED` | #743 nao entregou escritor                        |
| `ConversationOpen`              | `LOCAL_ONLY`     | inerte aqui; o browser aplica                     |
| `Preferences.SoundMode`         | `LOCAL_ONLY`     | sem source of truth server-side                   |
| `WebPushAvailable`              | `AUTHORITATIVE`  | falso: este e o caminho in-app                    |

### Preferencia que nao pode ser lida

Falha ao resolver as preferencias pessoais **nao** significa "sem preferencia".

`Preferences.Status` distingue as duas coisas:

| Status        | Significado                                                     | Efeito                                  |
| ------------- | --------------------------------------------------------------- | --------------------------------------- |
| `resolved`    | zero value — a leitura aconteceu; o que ela diz pode ser "nada" | defaults normais do produto             |
| `unavailable` | a leitura nao aconteceu ou nao teve sucesso                     | **fail-closed** em todas as superficies |

O zero value e `resolved` de proposito: todo chamador que preenche esses campos
os leu, e so quem tentou e falhou declara o contrario.

`denyUnresolvedPreferences` suprime os tres canais com reason
`preferences_unavailable` — codigo proprio, nunca `muted` nem `user_preference`,
porque o operador precisa saber que o alerta foi retido por **falha** e nao por
escolha. Fica depois das regras corporativas e de evento, entao uma decisao ja
resolvida por algo conhecivel continua registrada sob aquela razao.

A assimetria e deliberada: alertar quem tinha silenciado e uma falha que a
pessoa sofre e nao pode desfazer; reter um alerta custa uma notificacao que ela
ainda ve no app. **A mensagem nunca e afetada** — o fan-out entrega o mesmo
payload, ids, conversa, workspace e origem, e so a decisao dos canais muda. Uma
indisponibilidade de preferencias custa notificacoes, jamais mensagens. Continua
sendo **uma query por broadcast**: a falha nao gera retry nem consulta por
destinatario.

### `PresenceConnected`, e por que o contrato precisou dele

O publisher realtime sabe **uma** coisa sobre a presenca e nao sabe outra: ha um
cliente aberto — o evento esta descendo pelo socket dele — e o foco desse
cliente nunca foi observado. `PresenceForeground` afirmava o segundo;
`PresenceUnknown` jogava fora o primeiro e retirava as duas superficies que so um
cliente vivo executa.

`PresenceConnected` admite as mesmas superficies de `Foreground` e difere no que
**nao** licencia: `denyConversationOpen` exige foco observado, entao a flag
`ConversationOpen` fica inerte em vez de mentir. Foi a menor correcao tipada que
representa "unknown" sem virar "false".

## Precedencia

Uma decisao comeca com os canais que a **presenca** do destinatario poderia
usar, e cada regra so **remove** canais. Nao existe nenhuma operacao que
devolva um canal.

Isso e o que garante o criterio de aceite "politica corporativa prevalece sobre
preferencia pessoal": nao e uma convencao de ordenacao que um `if` posterior
possa furar — nao ha caminho de codigo capaz de reabilitar push, som ou toast
negado pelo horario de trabalho. `TestPreferencesOnlyEverRemoveChannels` e
`TestWorkScheduleOverridesEveryPersonalPreference` provam isso sobre todo o
espaco de entrada, nao sobre exemplos.

Ordem declarada das regras:

| #   | Categoria             | Regra                                   | Reason                               |
| --- | --------------------- | --------------------------------------- | ------------------------------------ |
| 1   | politica corporativa  | fora do expediente                      | `outside_work_hours`                 |
| 2   | evento/contexto       | origem historica/importada/replay       | `historical_or_imported`             |
| 3   | evento/contexto       | tipo de evento silencioso (reacao)      | `silent_event_type`                  |
| 4   | evento/contexto       | conversa aberta e visivel em foreground | `conversation_open`                  |
| 5   | preferencia pessoal   | preferencias do destinatario ilegiveis  | `preferences_unavailable`            |
| 6   | preferencia pessoal   | conversa silenciada                     | `muted`                              |
| 7   | preferencia pessoal   | nivel da conversa (mensagem comum)      | `conversation_level`                 |
| 8   | preferencia pessoal   | preferencia global (off / modo de som)  | `user_preference`                    |
| 9   | disponibilidade       | sem subscription de push utilizavel     | `unsupported_or_unavailable_channel` |
| 10  | estado do coordenador | evento ja entregue                      | `duplicate`                          |
| 11  | estado do coordenador | cooldown de burst                       | `burst_cooldown`                     |

A ordem **nao muda o resultado** — toda regra subtrai, entao o conjunto final
independe da ordem. Ela decide qual reason e registrado quando duas regras
tirariam o mesmo canal, e a leitura util para o operador e essa: um evento fora
do expediente **e** em conversa silenciada e registrado como
`outside_work_hours`, porque e a regra que o destinatario nao pode desfazer.

### Versao da policy

`notificationpolicy.Version` e **2** desde a #136.

Ela mudou porque o proprio contrato manda mudar quando um mesmo `Context` pode
produzir resultado diferente: um `Context` com `ConversationLevelMentionsReplies`
e uma mensagem comum agora produz supressao onde a versao 1 produzia entrega. Um
registro de auditoria anterior a esta mudanca nao pode ser lido como se estas
regras o tivessem decidido.

| Versao | Issue | O que mudou                             |
| ------ | ----- | --------------------------------------- |
| 1      | #744  | conjunto de regras original             |
| 2      | #136  | `denyConversationLevel` entrou na lista |

`preferences_unavailable` (#5) vem antes de `muted` e `user_preference` porque
substitui as duas: nenhuma delas pode responder por um destinatario cujas
preferencias nunca chegaram. E vem depois das regras corporativas e de evento
pelo mesmo criterio de leitura — uma decisao ja resolvida por algo conhecivel
continua registrada sob aquela razao, e nao sob uma falha que nao mudou nada.
Falha ao resolver preferencias **nao** bloqueia a mensagem: ela e entregue
normalmente, apenas as superficies de alerta ficam fail-closed. Ver
"Preferencia que nao pode ser lida".

`muted` (#6) vem antes de `conversation_level` (#7) porque **mute tem
precedencia sobre nivel**: um evento silenciado **e** fora do nivel e registrado
como `muted`, que e a decisao que a pessoa tomou sobre a conversa inteira. O
nivel tem reason proprio e nao reusa `user_preference`, que e a preferencia
**global**: o operador precisa distinguir "estreitou esta conversa" de
"desligou tudo".

## Preferencia por conversa: nivel + mute (#136)

### O modelo, e por que sao duas dimensoes

`chat.conversation_notification_prefs` guarda **duas** coisas independentes:

| Coluna               | Significado                                       |
| -------------------- | ------------------------------------------------- |
| `notification_level` | `all` \| `mentions_replies` — o que merece alerta |
| `muted_at`           | `NULL` = ativa; timestamp = silenciada            |

O modo que a UI mostra e **derivado**, nunca armazenado:

```text
muted_at IS NOT NULL        -> Silenciado
notification_level =
  'mentions_replies'        -> Mencoes e respostas
caso contrario              -> Todas as mensagens
```

Um enum unico com os tres valores foi recusado: ele **apaga** a escolha
anterior. Silenciar `#infraestrutura` pelo menu da sidebar nao pode esquecer que
a pessoa havia escolhido "Mencoes e respostas" no Perfil, e reativar tem de
devolver essa escolha — nao o default. Duas colunas fazem o restore sair de
graca, porque nada nunca sobrescreve a outra.

### Defaults e representacao esparsa

Ausencia de linha significa `all` + nao silenciada. Isso continua valendo:

| Estado                          | Linha        |
| ------------------------------- | ------------ |
| `all` + nao silenciada          | **removida** |
| `mentions_replies` + ativa      | existe       |
| `all` + silenciada              | existe       |
| `mentions_replies` + silenciada | existe       |

Uma linha **nao e mais** prova de mute. Toda leitura testa
`muted_at IS NOT NULL`, e essa e a mudanca que a migration exige de cada
consumidor: ler a existencia da linha como silencio silenciaria justamente quem
pediu para continuar ouvindo mencoes.

### Semantica das operacoes

| Operacao          | `notification_level` | `muted_at` |
| ----------------- | -------------------- | ---------- |
| `Mute`            | **intocado**         | `now()`    |
| `Unmute`          | **intocado**         | `NULL`     |
| `SetLevel(l)`     | `l`                  | `NULL`     |
| `SetLevel('all')` | linha removida       | —          |

`Mute` nao nomeia `notification_level` na lista de colunas do upsert: a omissao
**e** a garantia, em SQL. `SetLevel` limpa o mute de proposito — escolher o que
ouvir e escolher ouvir algo.

### API

Os endpoints de mute da sidebar continuam intactos e com a semantica acima:

```text
POST   /api/chat/channels/{id}/mute     -> silencia, preserva o nivel
DELETE /api/chat/channels/{id}/mute     -> reativa, preserva o nivel
POST   /api/chat/dm/{id}/mute
DELETE /api/chat/dm/{id}/mute
```

E ha um contrato canonico para a preferencia completa:

```text
PUT /api/chat/channels/{id}/notification-preference
PUT /api/chat/dm/{id}/notification-preference

{ "mode": "all" | "mentions_replies" | "muted" }
```

`PUT` porque o corpo declara o estado desejado completo: enviar o mesmo modo
duas vezes e a mesma preferencia. O servidor traduz modo -> colunas; o cliente
nunca aprende que mute e um timestamp nem que o default e a ausencia de linha.
O ator vem da sessao e o workspace do contexto server-side — o payload tem um
unico campo e decodificacao estrita, entao nao existe `user_id` nem
`workspace_id` para um cliente tentar nomear.

### `#geral`

A regra final e a **opcao A** da issue, e vale server-side:

| Modo               | `#geral`     |
| ------------------ | ------------ |
| `all`              | permitido    |
| `mentions_replies` | permitido    |
| `muted`            | **proibido** |

O invariante que `#geral` protege e "todo mundo continua alcancavel por nome", e
`mentions_replies` preserva exatamente isso; silencio nao. A recusa e a mesma de
antes — `c.is_general = false` na statement de mute — e o statement de nivel
nao a carrega. A recusa e indistinguivel de "conversa inexistente" e "sem
acesso": um unico `404` sem enumeracao.

### Rollout: reader-first, writer depois

Producao roda **dois slots** contra **um** banco, e um slot anterior a #136 le
**qualquer** linha de `chat.conversation_notification_prefs` como mute. Uma
linha dizendo "mencoes e respostas, nao silenciada" silenciaria essa pessoa no
slot antigo — uma notificacao perdida por um schema que ela nao conhece.

Entao a entrega e dividida, e a metade que escreve e **desabilitada por
padrao**:

```text
CHAT_CONVERSATION_NOTIFICATION_LEVELS_ENABLED=false
```

#### O que a capability faz, e o que ela nao faz

|                               | capability false       | capability true      |
| ----------------------------- | ---------------------- | -------------------- |
| leitura de linha granular     | **entende**            | entende              |
| policy aplica nivel existente | **sim**                | sim                  |
| `PUT` com `all`               | aceita                 | aceita               |
| `PUT` com `muted`             | aceita                 | aceita               |
| `PUT` com `mentions_replies`  | **503**                | aceita               |
| `/mute` e `/unmute`           | funcionam              | funcionam            |
| Perfil                        | switch binario da #729 | select de tres modos |

Ela gateia **somente escrita granular**. Todo reader deste build entende o
modelo novo com a flag em qualquer posicao — e isso e o que torna seguro
habilitar e depois voltar para o mesmo build: uma linha escrita com o gate
aberto **mantem** seu significado depois que ele fecha. O gate impede
_mudancas_ novas; ele nunca reinterpreta uma linha existente como mute.

`all` e `muted` continuam aceitos com o gate fechado porque **ambos sao
representaveis no modelo antigo**: o default e a ausencia de linha e o mute e
uma linha. Nenhum dos dois produz algo que o slot antigo leria errado.

#### Onde a recusa mora

`SidebarService.SetConversationNotificationPreference`, antes de resolver o
workspace e antes de qualquer escrita:

```go
if mode == NotificationModeMentionsReplies && !s.notificationLevelsEnabled {
    return domain.ErrConversationNotificationLevelsDisabled
}
```

Server-side e estrutural: **nao existe caminho** ate o store para
`mentions_replies` com o gate fechado. O frontend nao e a protecao — ele le a
mesma capability pelo payload da sidebar
(`conversation_notification_levels_enabled`) apenas para nao oferecer um controle
que o servidor recusaria com 503.

#### FASE 1 — reader-first

1. aplicar `000050` e `000051`;
2. deployar o build reader-compatible;
3. `CHAT_CONVERSATION_NOTIFICATION_LEVELS_ENABLED=false` (default);
4. drenar o build anterior;
5. confirmar que nao resta reader antigo de
   `chat.conversation_notification_prefs`: slot HTTP, worker de notificacao,
   conexao WebSocket viva.

Nesse estado: old reader + new reader coexistindo, **nenhum writer granular**,
portanto nenhuma linha nova que o old reader leia errado.

#### FASE 2 — writer enablement

1. mesmo schema, mesmo build;
2. `CHAT_CONVERSATION_NOTIFICATION_LEVELS_ENABLED=true`;
3. validar escrita de `mentions_replies`;
4. rollback seguro = voltar a flag para `false` **no mesmo build**, que continua
   lendo corretamente tudo que ja foi persistido.

### Matriz do policy engine

| Preferencia        | Mensagem comum | Mencao  | Resposta | Chamada |
| ------------------ | -------------- | ------- | -------- | ------- |
| `all`              | permite        | permite | permite  | permite |
| `mentions_replies` | **suprime**    | permite | permite  | permite |
| `muted`            | **suprime**    | suprime | suprime  | suprime |

"Mensagem comum" e `channel_message` ou `direct_message` — os dois tipos sao
nomeados explicitamente, em vez de mencao/resposta serem excluidas de tudo o
mais. A diferenca importa para o que nao e nenhum dos dois: uma **chamada** nao
e mensagem, e um nivel que decide quais mensagens interrompem nao pode calar o
telefone. Mute, que e o que se usa quando se quer que tudo pare, cala. Uma
reacao ja e suprimida antes, por `denySilentEventType`.

Um nivel que este build nao reconhece cai no default (`all`), como
`SoundMode.Effective`: preferencia ilegivel nao pode inventar silencio. A
direcao fail-closed e outro campo — `Preferences.Status`.

**A preferencia so remove canais.** `TestTheConversationLevelOnlyEverRemoveChannels`
e `TestConversationLevelNeverReopensADeniedChannel` provam sobre todo o espaco
de entrada que estreitar uma conversa nunca devolve um canal negado por horario
de trabalho, duplicata, burst ou conversa aberta.

### Classificacao do evento, e por que ela e por destinatario

O nivel divide "fui nomeado ou respondido" de "alguem postou", e de que lado uma
mensagem cai **muda por pessoa**: a mesma mensagem e mencao para quem ela nomeia
e mensagem comum para todos os outros.

No worker de push isso ja vinha resolvido: `chat.notification_outbox.kind` e
escrito pela propria statement que cria a mensagem (`mention` rank 1, `reply`
rank 2, `direct_message` rank 3).

No realtime a classificacao e **a mesma funcao**, nao uma segunda
implementacao: `service.NotificationEventTypeFor` (chat-service,
`internal/service/notification_event_type.go`). Ela mora ao lado do codec de
mencao que le, e fora do pacote `app`, precisamente para que a suite de storage
possa compara-la com o SQL da outbox contra um PostgreSQL real.

Precedencia identica a do CTE da outbox — nomeado, respondido, presente — e
fatos identicos:

- **mencao**: `service.NamedRecipients`, o codec que decide quem recebe linha em
  `chat.message_mentions`. Nenhuma busca por `@` em texto, aqui ou no browser;
- **resposta**: `domain.Message.ReplyToSenderID`, o autor do **parent
  persistido**. O store o preenche de `parent.sender_id` no mesmo join que a
  projecao de mensagem ja fazia, antes de qualquer regra de apresentacao tocar o
  preview — e e o mesmo fato que a outbox le como `parent.sender_id`;
- `@all` conta como mencao **somente** em grupo/DM, que e o escopo da #776 e o
  que `all_mention_recipients` aplica com `dc.type = 'group'`.

#### `Quoted` nao decide policy

`domain.Message.Quoted` / `MessagePayload.quoted` e **projecao visual** e nada
mais: e apagado para mensagem removida, tem o corpo retido para link condenado,
e esta ausente de toda projecao que nao faz join do parent.

Se a classificacao o lesse, uma resposta cujo parent foi apagado deixaria de ser
resposta **no realtime** enquanto a outbox continuaria chamando de `reply` — a
divergencia entre consumidores que este desenho existe para evitar. Por isso o
fato semantico e `ReplyToSenderID`, que sobrevive a todas essas regras.

**Ausencia de `Quoted` nao muda classificacao.**
`TestAReplyStaysAReplyWithoutAVisibleQuotePostgreSQL` prova exatamente isso
contra PostgreSQL real: parent real, DTO visual removido, `reply` nas duas vias.
Com o fato canonico tambem ausente a mensagem volta a ser comum — o
classificador le uma autoridade e nao adivinha.

Tratar um `@all` de canal como mencao pessoal aqui faria o realtime permitir um
alerta que o push classifica como mensagem comum, pelo mesmo motivo.

A equivalencia inteira e contratual, nao presumida:
`TestRealtimeAndOutboxClassifyTheSameMessageIdenticallyPostgreSQL` e
`TestAnOrdinaryGroupMessageClassifiesIdenticallyPostgreSQL` executam a statement
real e comparam, por destinatario, com o que a funcao Go responde.

### Unread nao muda

A preferencia e politica de **alerta**. Uma mensagem comum recebida em
`mentions_replies` continua persistida, continua contando unread, continua no
historico e continua visivel. Nada em `notificationpolicy` toca unread, read
cursor, read receipts, membership ou visibilidade — ver o comentario de pacote.

## Presenca: qual superficie existe

| Presenca                  | in_app | sound | web_push |
| ------------------------- | ------ | ----- | -------- |
| `foreground`              | sim    | sim   | **nao**  |
| `background`              | nao    | nao   | sim      |
| `offline`                 | nao    | nao   | sim      |
| desconhecida / zero value | nao    | nao   | sim      |

Presenca nao concede nada: tudo que ela admite ainda precisa sobreviver as nove
regras. Ela apenas recusa planejar para uma superficie que nao existe — toast
sem pagina aberta, som em browser fechado, push competindo com o cliente vivo
que ja esta mostrando a mensagem.

Uma presenca desconhecida e tratada como "nenhum cliente": nega as duas
superficies que pressupoem alguem olhando, e deixa o push, cuja disponibilidade
e checada separadamente.

`conversation_open` significa a conversa aberta **e visivel**, e a regra **exige
`foreground`** em vez de confiar na flag sozinha. "Aberta" e um fato sobre a UI
de um cliente: e reportado por aquele cliente e sobrevive a uma janela
minimizada, a uma aba fechada e a um laptop desligado. Aceita-la isolada deixaria
um "estou olhando" velho apagar o push de quem nao esta olhando para nada — justo
o canal que existe para esse caso. Em `background`, `offline` ou presenca
desconhecida a regra simplesmente nao se aplica, e o push segue as outras regras.

Janela aberta mas sem foco e `background`, nao `conversation_open`.

## Reasons

Conjunto fechado, valores estaveis, sem conteudo: sao gravados em
`chat.notification_outbox.suppressed_reason` e lidos por um operador meses
depois. `Decision.SuppressedReason()` junta-os com virgula, e o conjunto inteiro
cabe em `notificationevent.SuppressedReasonMaxLen` (testado).

`Decision.Reasons` lista as regras que **mudaram o resultado**, na ordem
declarada acima — nunca na ordem em que se tornaram verdadeiras, porque a ordem
de execucao e sempre a mesma. Uma regra que so tiraria canais que outra ja
tinha tirado nao entra: a lista e aquilo em que a decisao se apoia.

Invariante provada em `TestDecisionInvariants`: **nada e suprimido sem reason**,
e nada que foi entregue carrega reason. E o contrato de
`notificationevent.ValidateSuppressedReason`.

## Elegibilidade

`Eligible()` e derivado dos canais — pelo menos um `allow` — em vez de guardado
ao lado deles. O estado contraditorio (`suppressed` com `web_push: allow`)
simplesmente nao tem representacao.

## Policy version

`notificationpolicy.Version` e constante do pacote e nunca entrada: um chamador
que pudesse escolher a versao poderia escolher uma permissiva, e um registro de
auditoria que nomeia a versao informada pelo chamador nao prova nada. Muda
quando o resultado de um `Context` inalterado puder mudar.

## Relacao com os dominios vizinhos

**`workschedule` (#743)** responde a pergunta temporal; o engine recebe so o
`State`. O timezone **nao** e campo do `Context`: ele ja foi a autoridade dentro
de `Schedule.Evaluate`, e carregar o nome da zona adiante seria convidar um
segundo calendario a discordar do primeiro.

`not_configured` **nao suprime**, e essa e a decisao de produto que a #743
deliberadamente deixou para ca em vez de inventar um default. Ausencia de
schedule nao e "fora do expediente": nenhum workspace tem escritor de jornada
hoje, entao ler o silencio como fora do expediente silenciaria o produto
inteiro. Ja um estado que este build nao conhece **suprime** — e a regra que a
organizacao impoe, e uma resposta ilegivel nao pode virar permissao para
interromper. Direcao oposta a `SoundMode.Effective`, que cai no default do
produto porque som e preferencia, nao permissao.

**`notificationevent` (#741)** fornece `EventType`, `Priority` e `Origin`; nada
e redeclarado aqui. `Origin` e lido como "nao e `live`", entao uma origem
desconhecida (ou o zero value) e suprimida em vez de anunciada — um import de um
ano de mensagens nao pode tocar mil telefones.

`@channel`/`@everyone` nao criam vocabulario novo: o producer de chat-service ja
os grava como `mention` para cada destinatario alcancado, e e assim que chegam.

**Preferencias (#136/#729)**: nenhuma persistencia nova foi criada.
`chat.conversation_notification_prefs` continua sendo o source of truth de
mute — inclusive a semantica de que a ausencia de linha significa "nao
silenciado", que e por isso que todo zero value de `Preferences` e a ausencia de
preferencia. O modo de som repete os quatro valores que o produto ja tem
(`apps/web/src/chat/soundPreference.ts`), para que mover a decisao para ca nao
mude o significado da escolha que o usuario ja fez.

Quem preenche `Preferences` deve as verificacoes: uma preferencia de conversa
pertence a exatamente um `(workspace, recipient, conversation)` e so pode ser
lida por um caminho que ja estabeleceu membership — `ListMuted`, com seus
predicados de visibilidade, e um desses caminhos. O engine le os campos e nao
afirma nada sobre a procedencia deles.

## Fora do engine, por contrato

Dedupe e burst sao **entradas**. O pacote nao tem storage, `SETNX`, TTL nem
timer, e nao pode ter: uma policy que lembra e uma policy que nao pode ser
reexecutada. `Duplicate` e `BurstCooldown` sao a resposta do coordenador, e
aplica-las e tudo que o engine faz com elas.

## Dois consumidores, um engine

| Canal      | Quem avalia                                   | Como chega ao destino                     |
| ---------- | --------------------------------------------- | ----------------------------------------- |
| `web_push` | notification-service, sobre a linha da outbox | `worker.Evaluator` -> `Deliverer`         |
| `sound`    | chat-service, ao publicar o evento realtime   | campo `notification_policy` no WS payload |

Dois consumidores executando **o mesmo pacote** nao sao duas policies. O que
seria duplicacao — e o que existia antes — e duas _implementacoes_ das regras.
Nenhum dos dois servicos redeclara regra: cada um monta um `Context`, chama
`Evaluate` uma vez e traduz a resposta para o seu proprio transporte.

### O consumidor realtime (chat-service)

`services/chat-service/internal/app/notification_policy.go`, chamado de
`domainMessageToWSPayload` — o unico lugar onde o payload WS e montado.

A decisao e **por destinatario**. `Hub.PublishMessageCreated` serializa o payload
uma vez, mas o fan-out (`handleBroadcast`) percorre as assinaturas uma a uma e ja
conhece `client.userID` — e ali que um destinatario existe, entao e ali que a
policy e reavaliada com os fatos dele (`ws.RecipientPolicy`,
`notification_policy_fanout.go`).

Custo: **uma** query por broadcast, nao por inscrito — o subconjunto mutado da
lista inteira e lido num unico statement antes do loop. O loop ja fazia uma query
de autorizacao **por inscrito**, entao isto e estritamente menos do que o caminho
ja fazia. E re-encoda apenas quem tem decisao diferente da publicada; os demais
recebem os bytes ja serializados.

O payload publicado e a decisao de quem **nao expressou preferencia** — nunca uma
afirmacao de que o destinatario nao tem nenhuma. Ver a tabela de classificacao dos inputs acima: cada um e um fato que este
caminho tem ou o estado que o contrato define para nao te-lo, e nenhum e um
placeholder escolhido para dar resposta conveniente.

E o unico fato por destinatario de que o cliente precisa — **"fui nomeado?"** —
viaja como dado que o servidor derivou, nao como regra que o cliente reexecuta:
`service.NamedRecipients`, o mesmo codec de mencao que o resto do servico usa.

### Contrato no fio

```json
"notification_policy": {
  "policy_version": 2,
  "in_app": "allow",
  "sound": "allow",
  "web_push": "deny",
  "reasons": ["outside_work_hours"],
  "sound_class": "general",
  "named_user_ids": ["..."],
  "names_everyone": false
}
```

- `in_app`, `sound`, `web_push`: **os tres canais do Delivery Plan, cada um
  projetado do seu proprio campo de `decision.Channels`** e de mais nada. Nenhum
  e derivado do vizinho e nenhum e `Eligible()` — que e um resumo do plano e nao
  autoriza superficie nenhuma. Um cliente que lesse a resposta de um canal sob o
  nome de outro estaria decidindo entrega de novo, uma superficie por vez.
  `TestChannelsAreProjectedIndependently` cobre isso com os tres canais em
  desacordo entre si, que e o unico arranjo em que uma copia errada aparece.
- `reasons` so aparece em `deny` — um `allow` nao tem o que explicar, e mandar
  lista vazia em toda mensagem seria payload que ninguem le.
- `sound_class`: a classificacao autoritativa compartilhada (`general` para
  canal, `direct` para DM/grupo). O browser nao descobre isso sozinho.
- `named_user_ids`/`names_everyone`: quem a mensagem nomeia. Nao e disclosure
  nova — os tokens de mencao ja estao em `body_text`; o que muda e que a
  autoridade sobre o que conta como mencao passou a ser do servidor. Um `@all`
  digitado como texto puro, que o cliente antes honrava e o servidor nunca
  considerou mencao, deixou de tocar som.
- **O objeto esta sempre presente** em qualquer payload de mensagem que este
  build publica — inclusive para uma mensagem que nao e evento notificavel
  (sistema, removida), que ele declara como `deny` **sem reasons**. Isso e o
  contrato de rollout: ver a secao abaixo.

### Rollout: tres estados, nenhum ambiguo

Um cliente precisa distinguir tres coisas, e as tres sao distinguiveis no fio:

| No fio                        | Significado                                       | O cliente faz                                                     |
| ----------------------------- | ------------------------------------------------- | ----------------------------------------------------------------- |
| sem `notification_policy`     | chat-service anterior a #744 — nao sabe responder | **nao silencia**; aplica so os gates locais, com classe `unknown` |
| `sound: "deny"` sem `reasons` | este build: nao ha evento notificavel             | nao toca                                                          |
| `sound: "deny"` com `reasons` | este build: a policy suprimiu, e diz por que      | nao toca                                                          |
| `sound: "allow"`              | este build: permitido                             | toca, sujeito aos gates                                           |

A primeira linha e a que faltava. Enquanto o objeto era omitido para mensagens
nao notificaveis, "servidor antigo" e "evento nao notificavel" eram os mesmos
bytes — e um cliente que lesse a ausencia como negacao ficaria **mudo** contra
qualquer servidor mais velho que ainda pudesse alcancar. Silencio total, e
indistinguivel de funcionar. Por isso o backend sempre responde, e por isso o
cliente trata a ausencia como "nao me disseram", nunca como "disseram nao".

O estado `unknown` nao e um quarto tipo de evento: e a ausencia de resposta. As
regras locais recusam restringir sobre uma classe que ninguem informou — os
modos `mentions`/`mentions_and_dms` deixam passar, e `off`, mensagem propria,
duplicada, conversa silenciada e aba em foco continuam valendo, porque nenhuma
delas precisa de classificacao. **Nenhuma regra de produto foi reconstruida no
browser para cobrir a janela.**

### Rollout: ordem de troca em producao

Em producao o Blue/Green nao troca os Services de uma vez — `switch_services_to_slot`
patcha um por um, entao entre dois patches a producao esta genuinamente
dividida. Qual metade fica na frente nessa janela decide se a divisao e
inofensiva: **um bundle so e compativel com um backend da sua propria release ou
mais novo, nunca com um mais velho.**

Por isso a ordem e explicita nos dois sentidos
(`scripts/deploy/nchat-prod/lib.sh`, `service_switch_order`):

- **cutover** (`backends-first`): todo backend assume o slot novo **antes** de o
  browser ser servido com o bundle novo;
- **rollback** (`frontends-first`): o browser volta ao bundle antigo **antes** de
  os backends voltarem.

Provado em `scripts/ci/test_prod_blue_green_scripts.sh` pelo `patch-log` — a
ordem real das chamadas ao cluster — e nao por convencao: ha caso para o
cutover, para o rollback, e um que exige que todo Service estavel apareca na
ordem exatamente uma vez.

Isso **fecha a janela em producao**. Nao a fecha em `nchat-dev`, que e um
`kubectl apply` com rolling update: ali os pods trocam independentemente e um
cliente novo pode alcancar um chat-service antigo por alguns segundos. E
exatamente para essa janela — e para o intervalo entre pods durante qualquer
rolling update — que a tabela de tres estados acima existe.

### Fronteira do browser

`apps/web/src/chat/soundRules.ts` e **execucao**, nao autoridade. Cada regra que
sobrou so consegue transformar `allow` em silencio — nunca o contrario — e ha um
gate por superficie, cada um lendo o seu proprio canal:
`shouldExecuteInAppNotification` le `in_app`, `shouldExecuteSound` le `sound`,
`shouldExecuteNativeNotification` le `web_push`. Nenhum consulta o canal do
outro, e os tres comecam recusando qualquer coisa que nao seja um `allow`
central explicito.

**Mute saiu do browser.** Para qualquer payload que este backend produz, o mute
ja foi aplicado server-side para aquele destinatario, e reaplica-lo aqui seria o
browser decidindo policy uma segunda vez. A UI de mute continua existindo, mas
reflete estado do servidor — nao e source of truth. `localMuteApplies` restringe
o uso da copia local ao **caminho legado**, onde nenhuma decisao chegou.

O gate de `in_app` recalcula presenca ou horario: nada disso. A unica condicao
que ele acrescenta e "esta aba esta mostrando esta conversa", que e o fato
`LOCAL_ONLY` que nenhum servidor observa — e que so remove.

Onde o leitor esta (`foreground`/`background`, aba em foco, conversa aberta)
decide **qual mecanismo faz sentido**, nunca se ele e permitido: `announce` so
levanta a nativa quando a janela nao esta em foco, e so toca quando a nativa nao
apareceu. As duas condicoes apenas removem um efeito.

| No browser                                     | Papel                                       |
| ---------------------------------------------- | ------------------------------------------- |
| `policy.in_app !== "allow"` -> sem toast       | consumo da decisao central (canal `in_app`) |
| `policy.sound !== "allow"` -> nao toca         | consumo da decisao central (canal `sound`)  |
| `policy.web_push !== "allow"` -> sem nativa    | consumo da decisao central (`web_push`)     |
| modo de som local (`off`/`all`/`mentions`/...) | preferencia local, so restringe             |
| mensagem propria / duplicada                   | filtro local de um broadcast compartilhado  |
| aba ja exibindo a conversa                     | gate local de execucao, so restringe        |
| permissao de Notification, autoplay, playback  | execucao tecnica (`browserNotification.ts`) |

O que **saiu** do browser: classificacao DM/mencao/reply (agora `sound_class` +
`named_*`), a gramatica de mencao em regex, e a decisao de "pode alertar". O que
nunca esteve la e continua fora: horario de trabalho, origem historica, silencio
de reacao, Web Push, `policy_version` e qualquer leitura de prioridade como
autorizacao — ver a secao seguinte para o unico uso que a prioridade tem no
cliente.

### Classe de notificacao no cliente (#826)

`apps/web/src/chat/notificationClass.ts` responde uma pergunta diferente das
anteriores: **quao forte** um evento pede atencao, dado que ele ja foi
autorizado. Nao e autorizacao e nao pode virar uma: nenhum gate acima le a
classe, ela e resolvida depois deles, e um `deny` central continua encerrando o
assunto.

A precedencia e a da #826, do mais forte ao mais discreto:

```text
URGENT > MENTION > IMPORTANT/NORMAL > IN-CONVERSATION
```

`in-conversation` **substitui** `message` quando o leitor esta de fato
acompanhando a conversa — e a regra da #819 ("conversa aberta/ativa -> Lumen
In-Conversation; conversa nao aberta -> Lumen Message"), nao um degrau que
`message` vence no mesmo contexto. `important` resolve exatamente como
`standard`: a #826 e explicita que ele nao introduz sound class nova nesta
etapa.

Os tres fatos que compoem o attention context sao separados de proposito:

```text
conversationOpen && documentVisible && windowFocused
```

Uma conversa cujo id e o `currentConversationId` nao e uma conversa que o leitor
esta olhando quando a aba esta em background ou a janela perdeu o foco. Colapsar
os tres em um unico booleano "conversa ativa" e exatamente o defeito que a #826
existe para remover.

Duas fronteiras que a policy nao atravessa:

- **estado do browser** e lido na borda (`readAttentionContext`, em
  notificationPresentation) e entregue ja resolvido. A funcao e pura,
  deterministica e sem `document`, `window`, audio, toast ou Service Worker —
  e por isso a matriz de precedencia e testavel como matriz;
- **idempotencia de delivery** nao mora nela. Reprocessar o mesmo evento
  resolve a mesma classe, sempre; o que impede uma segunda entrega e
  notificationBurst (memoria por `message_id`) mais o claim entre abas — ver a
  secao abaixo. Uma memoria dentro da policy faria o mesmo evento resolver
  diferente na segunda vez, que e o oposto de uma decisao estavel.

A prioridade chega pelo payload (`priority`, #821/#840) e e estreitada em
`normalizeMessagePriority` antes de entrar: ausente ou desconhecida vira
`standard`. A direcao fail-closed aqui e essa — o unico efeito possivel desse
eixo e escalar um alerta, entao um valor que este build nao conhece nunca pode
ser o que escala.

### Temporalidade e rajada no browser (#750)

Depois dos gates de execucao, e antes de qualquer superficie rodar, o browser
responde mais uma pergunta local em `apps/web/src/chat/notificationBurst.ts`.
Como todo o resto desta fronteira, ela **so remove** — nao transforma um `deny`
central em som, toast ou nativa, e nao toca em unread, persistencia ou
`policy.web_push`.

#### Novidade vs estado recuperado: fronteira, nao flag

Nao existe campo `origin` no cliente. Se um evento e novidade nao e uma
propriedade do evento — e **qual caminho de codigo o esta segurando**, e essa
separacao e estrutural:

| Caminho                                                      | Papel                          |
| ------------------------------------------------------------ | ------------------------------ |
| fan-out ao vivo (`onMessageCreated` em `useChatSidebar`)     | unico candidato a apresentacao |
| hidratacao (`fetchSidebarData`, primeira pagina da conversa) | state ingestion only           |
| paginacao (`loadMore`)                                       | state ingestion only           |
| recovery de reconnect (refetch coalescido do sidebar)        | state ingestion only           |
| resync (`ws_subscription_ready`)                             | state ingestion only           |

A regra e **aplicada pelo build**: `apps/web/eslint.config.js` proibe qualquer
modulo que nao seja `useChatSidebar.ts` de importar `notificationPresentation`,
entao um caminho de recuperacao nao consegue passar a anunciar mensagens sem
quebrar o lint. O nome do entry point diz o contrato:
`presentLiveMessageNotification`.

Os dois casos restantes ja estao resolvidos por contratos que existem:

- **replay** nao existe para ser classificado. O chat-service declara entrega
  best-effort in-process, sem durabilidade e sem replay (`ws/doc.go`), e um
  evento de bus originado na propria instancia e descartado em vez de ecoado
  (`SourceInstanceID`);
- **import/migracao (#506)** chega ja decidido: `denyHistorical` nega todos os
  canais para qualquer `Origin` que nao seja live. O cliente **consome** esse
  `deny`; nao re-deriva a origem a partir de um sinal proprio.

#### Dedupe: dois donos, semanticas distintas, ambos limitados

| Estrutura                                            | Dono         | Pergunta                          |
| ---------------------------------------------------- | ------------ | --------------------------------- |
| `realtimeMessageLedger.admitRealtimeMessage`         | estado       | ja incorporei esta mensagem?      |
| memoria do `BurstGate` em `notificationPresentation` | apresentacao | esta aba ja anunciou este evento? |

Sao conjuntos diferentes de proposito: o ledger registra toda mensagem live
recebida, tenha ou nao gerado alerta; a memoria de apresentacao registra so o
que **esta aba** anunciou, porque uma aba que perdeu o claim multi-aba nao
anunciou nada. Fundi-los faria unread depender de quem ganhou o Web Lock, que e
exatamente o acoplamento que a #749 evita.

Os dois usam `expiringKeySet` e sao limitados por TTL **e** por capacidade;
nenhum varre nada (entrada cai na leitura apos expirar, ou quando a capacidade
evicta a insercao mais antiga — `Map` itera em ordem de insercao, o que torna a
eviccao um delete O(1)); nenhum agenda timer por mensagem; nenhum guarda corpo,
remetente ou nome de conversa.

| Contrato                     | Valor                                |
| ---------------------------- | ------------------------------------ |
| Retencao do ledger de estado | `REALTIME_LEDGER_TTL_MS` = 5 min     |
| Capacidade do ledger         | `REALTIME_LEDGER_CAPACITY` = 1000    |
| Retencao do dedupe de alerta | `PRESENTATION_MEMORY_TTL_MS` = 5 min |
| Capacidade do dedupe         | `PRESENTATION_MEMORY_CAPACITY` = 500 |
| Janela de som                | `SOUND_COOLDOWN_MS` = 3 s            |
| Capacidade de cooldown       | `SOUND_COOLDOWN_CAPACITY` = 64       |
| Chave do cooldown            | conversa + "nomeia o leitor ou nao"  |

Limitar o ledger e seguro **contra o contrato real**, nao por otimismo: sem
replay e sem eco da propria instancia, e com frames de geracao de socket
superada descartados no cliente, uma segunda entrega do mesmo `message_id` e um
duplicado quase simultaneo — e uma janela de minutos cobre isso com folga. Alem
disso, `unreadCount` e o numero do proprio servidor em toda resposta do sidebar
e `mergeUnread` o prefere ao valor local, entao qualquer deriva e corrigida pelo
proximo refetch em vez de acumular.

A identidade e `message_id`: estavel entre entregas e igual em toda aba. Nunca o
`event_id` do envelope, que o hub gera novo a cada publish, e nunca horario de
chegada.

#### Chave do cooldown

Por conversa, porque uma rajada e um fluxo em uma sala e silenciar o app inteiro
esconderia atividade nao relacionada. E por classe, porque uma sala acelerada e
exatamente quando uma mensagem que **nomeia** o leitor precisa continuar
audivel — separacao que nao e prioridade nova: e `named_user_ids` /
`names_everyone`, que o codec de mencao do servidor ja decide.

#### Relacao com a #749 e com a sessao

O claim por evento continua sendo o unico mecanismo multi-aba; a #750 nao
introduz um segundo. A ordem e: ingestion -> memoria local -> policy/gates ->
claim -> superficies. O cooldown e consumido **dentro** do claim, na aba que
realmente apresentou: uma aba que perdeu o claim nao gasta janela por um som que
nao tocou. Como a janela e por aba, uma rajada custa no maximo um som por aba
que ganhou algo, nunca um som por mensagem.

Ambas as memorias moram no modulo, nao no hook: precisam sobreviver a remount,
troca de rota, StrictMode e nova geracao de WebSocket. A unica fronteira que
respeitam e a de **identidade** — `sessionScoped` (`lib/sessionScoped.ts`) le a
mesma geracao de sessao que `presence.ts` ja usa, entao logout, outra conta ou
token substituido comecam memoria vazia. Sem listener e sem teardown: a
verificacao e uma comparacao de inteiro no acesso.

**O toast nao e silenciado, e substituido.** `InAppMessageAlert` ja mostra um
alerta so, o mais recente (#744), entao uma rajada troca o conteudo em vez de
empilhar: a pilha e limitada por construcao e o leitor continua com a atividade
mais nova para abrir. A agregacao e visual e so visual — nenhuma mensagem sai da
timeline, nenhum unread diminui, nada e marcado como lido.

### Preferencia local de som

O modo de som (`off`/`all`/`mentions`/`mentions_and_dms`) mora em
`localStorage` (`apps/web/src/chat/soundPreference.ts`). Isso e uma limitacao de
**source of truth**, nao uma segunda policy engine: a preferencia nao classifica
o evento nem reabilita nada — ela apenas descarta um som que a policy central ja
tinha permitido, usando a classe que o servidor mandou pronta.

Quando a #729 entregar essa preferencia com persistencia server-side, ela passa a
compor `Preferences.SoundMode` no `Context` e a decisao ja sai resolvida do
engine. O contrato de execucao do browser nao muda: ele continua consumindo
`sound` e aplicando apenas os gates locais.

## Observabilidade das decisoes

Uma decisao e correlacionavel com a notificacao que a causou.

O worker emite, por decisao e **antes** de `MarkEvaluated`:

```text
msg=notification policy decision
notification_id=<id> policy_version=<n> state=eligible|suppressed reason=<code> worker_id=<id>
```

- O evento significa "a policy produziu esta decisao", nao "a decisao foi
  persistida", e por isso vem antes da escrita: **toda** avaliacao fica
  observavel, inclusive aquela cuja escrita depois perde o compare-and-set ou
  encontra um banco recusando — justamente o caso que um operador mais precisa
  ver. O que aconteceu com a escrita e um segundo evento,
  `recordTransitionFailure`, sob o mesmo `notification_id`. Uma notificacao
  decidida duas vezes loga duas vezes, o que e honesto: ela foi decidida duas
  vezes.
- Nao ha coluna `policy_version` na outbox e nenhuma migration foi criada para
  isso. A correlacao por `notification_id` no log e o que a #744 pede
  (auditavel/observavel); persistir a versao seria uma migration que nada le.
- `policy_version` vem do `Verdict` da avaliacao, **nunca** do build. Durante um
  rollout duas replicas rodam rule sets diferentes, e "quais regras decidiram
  esta notificacao" e exatamente a pergunta que uma versao carimbada pelo
  processo nao responde. Por isso tambem nao ha versao no log de startup.
- Todos os campos sao referencia ou vocabulario fechado: id, versao, estado,
  codigo de supressao. Sem corpo, sem endereco, sem subscription, sem token.

Nenhuma metrica nova: `notification_id` jamais poderia ser label de Prometheus,
e os contadores de `eligible`/`suppressed` ja existem.

## Limites conhecidos

- **Push esta wired e ocioso.** O worker so roda com um `Deliverer`, e nenhum
  provider existe ainda (`newNotificationDeliverer` devolve `nil`). Quando ele
  chegar, a decisao ja passa pelo engine.
- **A superficie in-app e minima por escolha.** `InAppMessageAlert` mostra o
  alerta mais recente e nada mais: sem historico, sem persistencia, sem centro de
  notificacoes. Isso e consumo do canal `in_app`, nao uma decisao de produto
  sobre como o NChat notifica dentro da UI — essa decisao pertence a uma issue de
  UX.
- `Priority` e carregada mas nao lida por nenhuma regra. A unica regra que a
  leria — bypass de horario para urgente — e explicitamente proibida pelos
  criterios de aceite da #744.
