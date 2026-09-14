# Reconcile de Web Push no cliente

Camada de diagnostico e auto-reparo do browser (issue #748, parent #678).
Codigo em `apps/web/src/notifications/webPushBrowser.ts`,
`webPushReconciler.ts` e `pushSubscriptionApi.ts`.

O modelo, a persistencia e as rotas de subscription sao da #745; o registro do
Service Worker e da #747; a UI de Perfil > Notificacoes (#729) **esta fora** e
nao foi tocada. Esta camada e o que faz as tres primeiras concordarem.

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

`not_configured` e o deployment sem `VITE_NOTIFICATION_VAPID_PUBLIC_KEY`. E a
chave publica VAPID, que todo push service le em cada requisicao assinada; a
metade privada continua sendo variavel de ambiente do servidor.

O snapshot **nao tem campo** onde `endpoint`, `p256dh` ou `auth` caibam. O
unico lugar do cliente que le as chaves e `readWebPushCredentials`, cujo
resultado tem um destino so: o corpo do POST.

## A passada

Diagnostica inteiro, so entao converge.

```text
capability -> permission -> registration -> subscription local -> estado no backend -> decisao
```

Ela para antes de mutar qualquer coisa quando: a capability nao e `supported`;
a Notification API nao e legivel; nao houve registration; a registration existe
mas ainda nao ativou (`registering`, que nao e erro e nao vale um timer); a
permission nao e `granted`; ou ninguem esta autenticado.

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

## Concorrencia e lifecycle

- **Uma passada por vez, por sessao.** Chamadas simultaneas da mesma sessao
  compartilham a promise em voo, entao duas nunca viram dois `subscribe()`. O
  cliente nao delega a corrida que ele mesmo criou a idempotencia do backend.
  Uma chamada de **outra** sessao nao compartilha nada: a passada em voo leu o
  mundo sob uma identidade que nao e a dela.
- **Uma passada pertence a sessao que a iniciou.** A geracao e capturada no
  inicio e reconferida imediatamente antes de cada efeito mutavel — e de novo
  depois de cada `await` que anteceda um. Se ela mudou, a passada encerra em
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
