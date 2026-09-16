# Reconcile de Web Push no cliente

Camada de diagnostico e auto-reparo do browser (issue #748, parent #678).
Codigo em `apps/web/src/notifications/webPushBrowser.ts`,
`webPushReconciler.ts` e `pushSubscriptionApi.ts`.

O modelo, a persistencia e as rotas de subscription sao da #745; o registro do
Service Worker e da #747. A UI de Perfil > Notificacoes (#729) consome o snapshot
desde a #862, por `useWebPushHealth()` e `enableWebPush()`, sem listener nem API
de browser propria. Esta camada e o que faz as tres primeiras concordarem.

## Onde encaixa

```text
browser APIs        Service Worker (#747)        notification-service (#745)
     \                     |                              /
      +------------ webPushReconciler ------------------+
                           |
                     health snapshot  ->  Perfil > Notificacoes (#729)
```

Tres modulos, tres responsabilidades:

| Modulo                   | Decide                                       | Nao decide                  |
| ------------------------ | -------------------------------------------- | --------------------------- |
| `webPushBrowser.ts`      | o que as APIs do browser respondem           | quando chamar               |
| `pushSubscriptionApi.ts` | a forma do contrato HTTP da #745             | quando registrar            |
| `webPushReconciler.ts`   | o que diverge, o que reparar, o que publicar | nada de rede nem de browser |

Nenhum componente React chama `Notification.permission`,
`navigator.serviceWorker` ou `registration.pushManager`. O unico consumidor
previsto e `useWebPushHealth()`.

## O snapshot

Uma uniao de dois bracos, nao um registro de campos opcionais: um browser sem
Push API nao tem permission, worker nem subscription para descrever.

```text
{ status: "unavailable", reason: "unsupported" | "insecure_context" | "not_configured" }
{ status: "available",
  permission:   "default" | "granted" | "denied",
  worker:       "registering" | "active" | "failed",
  subscription: "present" | "absent",
  backend:      "unknown" | "connected" | "disconnected" | "invalid" | "unavailable",
  health:       "reconciling" | "healthy" | "reconnect_required" | "error",
  error:        "browser" | "backend" | null }
```

`health` e **derivado**, nunca passado: `healthy` e exatamente "esta subscription
existe e o backend a alcanca". Nenhum caller consegue montar um snapshot que se
declare saudavel enquanto um eixo diz o contrario.

`not_configured` e o notification-service respondendo que este deployment nao
entrega Web Push (`GET /api/notifications/push/config` com `vapid_public_key:
null`, #862). A chave publica vem do mesmo processo que assina com a privada —
ver [notification-web-push.md](notification-web-push.md). Ela so e pedida com
sessao, antes da permission ser considerada, entao uma pessoa nunca e convidada
a "Ativar" num deployment que nao entrega. Falha ao pedi-la — inclusive o 503
`push_delivery_unavailable` de um worker parado — vira `error` de backend, nunca
`not_configured`, e nada e mutado sem ela.

Uma subscription local criada com outra chave (rotacao do par) conta como
ausente: e cancelada e recriada dentro da fila de mutacoes do `PushManager`. Um
browser que nao expoe `options.applicationServerKey` recebe o beneficio da
duvida.

O snapshot **nao tem campo** onde `endpoint`, `p256dh` ou `auth` caibam. O
unico lugar do cliente que le as chaves e `readWebPushCredentials`, cujo
resultado tem um destino so: o corpo do POST.

## A passada

Diagnostica inteiro, so entao converge. Na ordem do codigo (`computeSnapshot`,
`convergeGranted`, `runPass`):

```text
capability
  -> permission (lida)
  -> Service Worker (registration ativa)
  -> sessao autenticada
  -> GET /push/config (chave do deployment)
  -> permission granted?
  -> subscription local (criada com a chave atual?)
  -> estado da subscription no backend
  -> decisao / convergencia (subscribe, register)
  -> publicacao, com a permission relida
```

Ela para antes de mutar qualquer coisa quando: a capability nao e `supported`;
a Notification API nao e legivel; nao houve registration; a registration existe
mas ainda nao ativou (`registering`, que nao e erro e nao vale um timer);
ninguem esta autenticado; o deployment nao entrega (`not_configured`) ou a chave
nao pode ser lida (`error`); ou a permission nao e `granted`.

### A permission de quando a passada comecou

A permission e lida uma vez no inicio, e a passada e varios awaits de extensao.
Dois controles, e nenhuma segunda regra de permission:

- **antes de cada mutacao** — criar, cancelar para recriar e registrar —
  `requireEntitled` confere a sessao e depois a permission. Se ela deixou de ser
  `granted`, a passada para sem criar nem registrar nada;
- **na publicacao**, `withCurrentPermission` relê a permission de qualquer
  snapshot concluido sob `granted`, `healthy` inclusive. `denied` ou `default`
  viram o snapshot dessa permission; nenhum deles e publicado como saudavel.

Nenhum dos dois pede permission, e a geracao de sessao continua valendo: uma
passada que perdeu a sessao e a permission ao mesmo tempo nao publica nada.

`needsRepair` e a decisao inteira, pura, sobre tres valores:

| Situacao                                   | Repara |
| ------------------------------------------ | ------ |
| linha do backend `disabled`                | nao    |
| subscription local ausente                 | sim    |
| backend sem linha para este device         | sim    |
| linha `invalid`                            | sim    |
| endpoint diferente do ultimo ja registrado | sim    |
| tudo igual                                 | nao    |

`disabled` e o unico status nao-ativo que **nao** e divergencia: a #745 o usa
para "o dono desligou", e re-registrar significaria uma pessoa desligar
notificacoes e o proximo foco de janela religar. So `enableWebPush` desfaz isso
— e a diferenca entre as duas intencoes de passada, `diagnose` e `connect`.

`invalid` e o oposto: o provider aposentou o endpoint, e reparar isso e o motivo
desta camada existir.

### Por que "ja registrei este endpoint" e memoria

A #745 deliberadamente nao devolve o endpoint que guarda. Sem isso, "o backend
ja tem estes bytes" e "o browser rotacionou a subscription" seriam
indistinguiveis. O ultimo endpoint registrado com sucesso fica em memoria do
modulo — nunca em storage: e derivado de uma capability URL e nao tem por que
sobreviver a pagina. Depois de um reload, ou de uma troca de sessao, ele e
desconhecido e a primeira passada re-apresenta uma vez, o que o contrato define
como a mesma linha e a mesma geracao.

### Conflito de endpoint

Um `409 push_endpoint_conflict` e o mesmo perfil de browser assinado como outra
pessoa. A recuperacao e a que a #745 documenta — cancelar a subscription local e
registrar a nova que isso produz — e acontece **uma vez**. Um segundo conflito e
falha de verdade.

## Permission

`requestPermission()` e alcancavel a partir de exatamente uma funcao exportada,
`enableWebPush`, e apenas enquanto a permission for `default`. Nao ha chamada a
ela em lugar nenhum do caminho de reconcile — nem no boot, nem no login, nem em
`focus`, nem em `visibilitychange`. `granted` nao precisa de prompt e `denied`
nao pode ser desfeito por um: o browser recusa perguntar de novo, e insistir e
como um site ganha um bloqueio permanente.

E so depois que a sessao atual sabe que o deployment usa a permission (#862).
Dois caminhos:

- **confirmacao existente** — uma passada desta sessao ja leu uma chave do
  deployment: `requestPermission()` roda na mesma task do clique, sem nada
  aguardado antes, nem a passada que por acaso esteja em voo;
- **sem confirmacao** — nunca rodou, `not_configured` ou configuracao ilegivel:
  espera a passada em voo e, se ainda faltar, roda um diagnostico. Sem
  confirmacao depois disso nao ha prompt; a passada seguinte reporta o estado e
  o proximo clique pergunta de novo. A permission e relida depois da espera.

A confirmacao e chaveada pela geracao de sessao, entao uma troca de conta a
esquece sem listener, e uma passada de outra sessao nao a concede nem a revoga.
Uma confirmacao usada pelo caminho rapido pode ser de uma passada anterior a uma
mudanca de configuracao do servidor; a passada `connect` que segue o prompt le
a chave de novo e reporta o estado real.

A UI segue a mesma ordem, mas a regra nao depende dela: `reconciling` e `error`
nao oferecem "Ativar".

Limite pendente de QA real: no caminho sem confirmacao, esperar a requisicao
antes do prompt consome parte da ativacao transitoria do gesto, e um browser que
a expire antes da resposta pode recusar o prompt. O estado continua `default`, e
o clique seguinte ja tem confirmacao e pergunta pelo caminho rapido. Na UI o
botao "Ativar" so aparece depois de uma passada assentada, que e quando a
confirmacao costuma existir. Nada disso foi validado em browser real.

## Concorrencia e lifecycle

- **Uma passada por vez, por sessao.** Chamadas simultaneas da mesma sessao
  compartilham a promise em voo, entao duas nunca viram dois `subscribe()`. O
  cliente nao delega a corrida que ele mesmo criou a idempotencia do backend.
  Uma chamada de **outra** sessao nao compartilha nada: a passada em voo leu o
  mundo sob uma identidade que nao e a dela.
- **Uma passada pertence a sessao que a iniciou.** A geracao e capturada no
  inicio e reconferida imediatamente antes de cada efeito mutavel — e de novo
  depois de cada `await` que anteceda um. Cancelar uma subscription (rotacao de
  chave VAPID ou conflito de endpoint) e um desses awaits: `mintReplacing`
  confere a sessao antes do `unsubscribe()` e de novo antes do `subscribe()`. Se ela mudou, a passada encerra em
  silencio: nao cria subscription, nao registra, nao desliga, nao escreve
  `lastRegisteredEndpoint` e nao publica snapshot. Quem responde e a passada da
  sessao nova.

  Proteger so o commit do snapshot nao basta. Uma passada e uma sequencia de
  `await`s entre browser e backend, e um logout ou um login diferente cabe em
  qualquer intervalo entre dois deles; tudo o que ela fizesse depois disso seria
  feito em nome de uma identidade que nao existe mais — inclusive restaurar um
  `lastRegisteredEndpoint` que o listener de auth acabou de limpar, o que faria
  a sessao nova ler o registro da antiga como seu e chamar de saudavel um
  backend stale.

- **Criar subscription e serializado no browser inteiro.** Geracao de sessao nao
  resolve isto: existe um `PushManager` por browser, nao por sessao, entao duas
  passadas de sessoes **diferentes** — legitimamente independentes — continuam
  sendo duas chamadoras do mesmo objeto, e uma guarda de sessao so consegue
  avisar que a passada ficou stale depois que o `subscribe()` que ela ja iniciou
  voltar. Toda mutacao do `PushManager` passa por uma fila unica, e dentro dela
  a subscription local e **relida** antes de mintar: enfileirar sem reler
  transformaria dois `subscribe()` paralelos em dois sequenciais, que e o mesmo
  bug mais devagar. A fila espera a operacao anterior _assentar_, nunca ter
  sucesso, entao um `subscribe()` recusado ordena as seguintes em vez de
  envenena-las.

  Sao dois controles distintos e nenhum substitui o outro: a geracao protege o
  **contexto de sessao**, a fila protege o **recurso global do browser**.

- **Um conjunto de listeners.** `focus` e `visibilitychange` (so na volta a
  `visible`), registrados fora do React, em `main.tsx`. Um segundo start e no-op
  e devolve o mesmo stopper, entao StrictMode e remount nao acumulam.
- **Coalescing, nao agenda.** Eventos a menos de 2 s de distancia sao um
  reconcile so. Nada dispara sozinho quando a janela expira: nao ha
  `setInterval`, nao ha `setTimeout`, nao ha polling e nao ha timer para vazar.
- Uma troca de sessao nao e coalescida: a conta contra a qual o browser esta
  registrado acabou de virar outra.

## Isolamento de falha

Nada aqui rejeita. Falha de Service Worker, de `getSubscription`, de
`subscribe`, de `unsubscribe` e do notification-service viram snapshot com
categoria (`browser` ou `backend`), nunca excecao que suba. Push e capacidade
auxiliar: um chat que nao tem nada a ver com notificacoes nao pode cair por
causa delas, e nao existe rejeicao sem tratamento para o browser reportar.

O detalhe tecnico fica na excecao que o produziu. A UI recebe a categoria.

## Identidade do device

A #745 chaveia por (workspace, usuario, device), entao sem uma identidade de
device que sobreviva ao reload cada carregamento registraria uma segunda linha
para o mesmo browser. E um valor aleatorio opaco em
`localStorage["nchat.notifications.push.deviceId"]`, validado contra a gramatica
da #745 na leitura — storage local nao e entrada confiavel. Nao e segredo, nao
carrega autoridade e nao concede nada: o servidor tira o usuario da linha da
sessao e nunca de algo que o cliente mande. Um browser que recusa storage o
mantem em memoria pela vida da pagina.
