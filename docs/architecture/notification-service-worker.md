# Service Worker e notification click

Camada de browser do pipeline de notificacoes (issue #747, parent #678).
Codigo em `apps/web/public/sw.js` e
`apps/web/src/notifications/serviceWorkerRegistration.ts`.

Registro de `PushSubscription` (#745), envio (#746) e a UI de
Perfil > Notificacoes (#729) **estao fora** desta camada. O diagnostico e o
auto-reparo que decidem _quando_ uma subscription precisa ser criada ou
reapresentada tambem estao, e vivem na #748 — ver
[notification-web-push-reconcile.md](notification-web-push-reconcile.md), que
chama o registrar deste documento em vez de reimplementa-lo.

## Onde encaixa

```text
VAPIDSender (#746)  ->  push service  ->  browser  ->  Service Worker (#747)
                                                        |
                                                        +-> showNotification()
                                                        +-> notificationclick -> SPA
```

Esta camada nao decide **se** alguem deve ser notificado — isso ja foi decidido
pelo Policy Engine (#744) muito antes — e nao decide **o que** a pessoa pode
ver, que continua sendo decidido pelo servidor a cada request que a SPA faz.
Ela decide apenas como um push vira uma notificacao e como um clique vira uma
janela.

## Onde o arquivo mora, e por que

`apps/web/public/sw.js`. O Vite serve `public/` verbatim em dev e copia para a
raiz de `dist/` no build, entao o worker esta em `/sw.js` nos dois modos — e e
essa URL de raiz que torna o escopo `/` legal sem header
`Service-Worker-Allowed`.

E um **classic script**, nao um modulo: module workers ainda nao existem no
Firefox, que e um browser que este produto suporta. A consequencia direta e que
o arquivo nao pode importar nada do bundle da aplicacao — inclusive
`src/lib/safeRedirect.ts`, cuja regra e por isso restabelecida ali dentro, com
os mesmos casos cobertos pelos testes dos dois lados.

A CSP versionada em `infra/docker/web/nginx.conf` ja tem `worker-src 'self'
blob:`; nenhuma mudanca de header foi necessaria.

## Registro

Um unico chamador, `main.tsx`, antes do React renderizar.

```text
registerNotificationServiceWorker()
  -> contexto inseguro, sem serviceWorker ou sem PushManager -> null
  -> register("/sw.js", { scope: "/" })
  -> falha -> console.warn e null
```

O browser ja indexa um registro por (script, escopo), entao a segunda chamada
com o mesmo par nao cria um segundo worker. O que a centralizacao evita e o
caso anterior a isso: **dois call sites que discordam do escopo**, porque
escopo diferente e registro diferente, nao atualizacao do primeiro. A promise
fica memoizada, entao chamadas concorrentes e o double-invoke do StrictMode
compartilham um `register()` so.

Uma falha permanece memoizada. Retentar a cada chamada transformaria um browser
que recusa o worker num loop, e nada que um retry faca muda dentro de um mesmo
carregamento de pagina.

Nada disso pode derrubar a aplicacao: todo caminho resolve `null` e nenhum
rejeita. O chat nao depende do worker existir.

### Update e activation

Nao ha handler de `install`, nao ha handler de `activate`, nao ha
`skipWaiting()` e nao ha `clients.claim()`. O ciclo de vida default — um worker
novo espera ate a ultima aba da origem sumir, e so entao ativa — e o unico que
nao pode trocar o worker debaixo de uma pagina no meio de uma conversa.

O preco e explicito: uma aba aberta antes da primeira ativacao nao e
**controlada**, e `WindowClient.navigate()` e recusado para ela. O
`notificationclick` trata isso como o caso esperado e cai em `focus()`, nao como
erro.

## Push

```text
event.data ausente            -> nada
JSON invalido                 -> nada
nao e objeto                  -> nada
v nao esta em {1, 2}          -> nada
campo obrigatorio ausente/vazio -> nada
v1                           -> ignora title e body_preview
v2 sem title valido          -> titulo generico, sem corpo
v2 com title valido          -> titulo recebido, corpo somente se valido
```

Envelope invalido ou versao desconhecida e recusado. Campos opcionais v2
invalidos usam fallback sem o conteudo rejeitado. O payload e
produzido pelo nosso proprio notification-service, mas chega por um terceiro e e
decifrado pelo browser, entao e validado como se nao fosse nosso.

Os cinco campos obrigatorios sao `id`, `type`, `source_type`, `source_id` e
`occurred_at` — iguais nas duas versoes. `v` tem de ser `1` ou `2`, o contrato de
[notification-web-push.md](notification-web-push.md). Uma versao desconhecida e
recusada por construcao: e para isso que `v` existe, e e por isso que a #870
trocou a versao emitida por um interruptor de deployment em vez de subi-la — um
worker anterior a ela recusaria a v2 e nao mostraria nada.

Aceitar as duas nao e transicao: a v1 continua sendo o que um deployment sem
preview emite, e o que uma notificacao ja em voo carrega quando o servidor e
reconfigurado.

> Um push com permissao concedida que nao chame `showNotification()` faz alguns
> browsers exibirem a propria notificacao generica ("este site foi atualizado em
> segundo plano"). Isso e comportamento da plataforma, e o preco de falhar
> fechado; a alternativa seria mostrar algo derivado de um payload que nao
> satisfaz o contrato.

### Janela visivel e focada: nada (#862)

Antes de mostrar, o worker pergunta `clients.matchAll({ type: "window",
includeUncontrolled: true })`. So uma janela da propria origem com
`visibilityState === "visible"` **e** `focused === true` faz o push terminar
sem notificacao. E o mesmo teste que a pagina aplica antes de desenhar um toast
(`isWindowFocused` em `notificationPresentation.ts`): uma pagina visivel e
focada apresenta o que ve chegar — nada na conversa aberta, toast em outra —, e
uma notificacao de SO por cima seria o alerta redundante que a #678 proibe. A
outbox nao carrega sessao, entao o worker nao sabe qual conversa a pagina
mostra; "existe janela visivel e focada" e o teste inteiro.

Falha da Clients API conta como "nenhuma janela": suprimir por engano perde a
notificacao, nao suprimir custa uma duplicata.

### Visivel sem foco, ou oculta: o push

Uma janela visivel mas sem foco, ou oculta, nao desenha toast
(`shouldExecuteInAppNotification` exige foco), e a pagina nunca levanta uma
notificacao de SO propria no backend atual: `showBrowserMessageNotification` so
roda quando a decisao realtime autoriza `web_push`, e o chat-service nunca
autoriza — o contexto do fan-out WebSocket e `PresenceConnected` com
`WebPushAvailable` falso, e `surface(PresenceConnected)` do Policy Engine so
admite `in_app` e `sound`. Nesses dois casos a notificacao do push e a unica
superficie visual, entao o worker a mostra, e nao ha segunda notificacao de SO.

| Janela do NChat       | Service Worker (push) | Toast da pagina              | Notificacao de SO da pagina |
| --------------------- | --------------------- | ---------------------------- | --------------------------- |
| visivel e focada      | suprime               | sim, fora da conversa aberta | nunca                       |
| visivel sem foco      | mostra                | nao                          | nunca                       |
| oculta                | mostra                | nao                          | nunca                       |
| nenhuma (aba fechada) | mostra                | —                            | —                           |

O som local continua sendo decidido pela pagina (policy, preferencia,
cooldown) e pode tocar nos casos sem foco ao lado da notificacao do push. Isso e
decisao de presenca do Policy Engine (#744/#749), que hoje nao observa foco nem
visibilidade; esta camada nao a refaz.

Testado dos dois lados: os casos de janela em `serviceWorker.test.ts` (focada,
visivel sem foco, oculta, varias janelas), a matriz de atencao em
`notificationPresentation.test.ts` e
`TestRealtimeDecisionNeverAuthorisesTheOSSurfaceForAnyRecipient` no chat-service
(mencao, resposta, mensagem de canal e DM). A apresentacao real no SO nao foi
validada em browser.

### O que a notificacao mostra

Titulo e corpo sao **decididos pelo servidor** desde a #870. O worker nao infere
apresentacao: ele recebe um titulo e um corpo que ja lhe disseram serem seguros,
confere o formato, e mostra.

```text
v2 com title valido            -> e o titulo
senao, titulo conhecido do type -> "Voce foi mencionado no NChat", etc.
senao                           -> "Nova notificacao do NChat"

v2 com title e body_preview validos -> e o corpo
senao                           -> sem corpo nenhum
```

"Valido" sao quatro condicoes, todas obrigatorias:

- e uma string;
- nao excede o teto do contrato — 200 para titulo, 400 para preview, em unidades
  UTF-16;
- nao e composta apenas por whitespace;
- nao e composta apenas por caracteres Unicode da categoria de formato (`Cf`).

A ultima e a que cobre o que `trim()` sozinho nao pega: zero-width space
(`U+200B`), BOM (`U+FEFF`), marcas direcionais (`U+200E`, `U+200F`) e os
overrides bidirecionais. Nenhum deles e whitespace em JavaScript, entao um
titulo feito so deles seria uma string nao vazia que o banner exibe em branco.

A regra e **"apenas invisiveis"**, nao "contem um invisivel", e essa distincao e
o ponto: texto real que carrega `Cf` continua valido, e uma sequencia ZWJ de
emoji — `👨‍👩‍👦`, construida com `U+200D` entre os pictogramas — continua valida,
porque os pictogramas ao lado nao estao na classe. Texto internacional, acentos
combinantes (categoria `Mn`, nao `Cf`) e emojis com pares substitutos tambem
passam inalterados.

O servidor ja remove a categoria `Cf` inteira em `sanitizeLine`
(`webpush_preview.go`), entao um payload nosso nunca chega aqui nesse estado. A
conferencia no Service Worker e defesa adicional, para o payload inesperado: o
push atravessa um terceiro e e decifrado pelo browser, e este arquivo valida
como se o conteudo nao fosse nosso.

Um campo que falha nao e **consertado** — nao ha modificacao por trim, slice ou
substituicao: um campo que nao bate com o contrato nao e um campo com valor
corrigivel, e corrigi-lo seria o worker decidindo apresentacao, que e o que a
#870 moveu para o servidor.

v1 ignora completamente title/body_preview, mesmo quando presentes.
v2 permite somente title; body_preview exige title valido.

O fallback e um so, e e alcancado identicamente por um payload v1, por um payload
v2 que o servidor deixou em branco, e por um payload v2 cujo campo falhou na
conferencia. Isso e deliberado: os motivos de nao haver preview (mensagem
apagada, retida por link scan, acesso revogado, previews desligados) sao
exatamente o que um banner nao pode revelar.

Nada disso e markup. `showNotification()` renderiza texto — nao ha elemento, nao
ha `innerHTML`, nao ha parser — entao uma tag no preview e a sequencia de
caracteres que ela e. Ha teste explicito para isso, porque o dia em que alguem
construir um DOM neste arquivo e o dia em que passa a importar.

`icon` e `badge` sao assets locais (`/assets/nic-labs-icon.png`,
`/assets/favicon.png`). Uma URL do payload nunca vira icone — nenhuma das duas
versoes tem campo para uma.

`tag` e `nchat-notification-<id>`, entao uma reentrega at-least-once da **mesma**
notificacao substitui a que ja esta na tela em vez de empilhar uma segunda.

`timestamp` vem de `occurred_at` quando ele parseia — quando o evento aconteceu,
nao quando o push chegou, que e a unica coisa para a qual esse campo serve.

## notificationclick

```text
1. notification.close()
2. url = notification.data?.url, validada como caminho interno (senao /chat)
3. clients.matchAll({ type: "window", includeUncontrolled: true })
4. primeira janela da propria origem?
     sim -> ja esta no destino? focus()
            senao -> navigate(url) e focus() (recusado -> focus())
     nao -> clients.openWindow(url)
```

`openWindow()` e **fallback**, nunca o primeiro passo, e roda no maximo uma vez:
so ha uma chamada no codigo e ela e inalcancavel quando existe janela elegivel.
Duas janelas candidatas produzem um `focus()`, nao dois `openWindow()`.

Elegivel e uma janela cuja URL comeca com `self.location.origin + "/"` — o
sufixo `/` e o que impede `https://nchat.example.com.evil.test/` de passar por
prefixo.

O destino e revalidado no clique, e nao apenas na criacao, porque uma
notificacao sobrevive na bandeja do sistema a atualizacoes do worker: o handler
que dispara pode ser de uma versao diferente da que criou a notificacao.

### O destino, e o limite dele

A versao 1 carrega `source_type` + `source_id` — id de mensagem, reacao ou
chamada — e a aplicacao **nao tem rota que aceite nenhum dos tres**: resolver um
id de mensagem ate a conversa dela exigiria um endpoint que nao existe. Entao o
destino e `/chat`, e a pessoa chega na sidebar com a conversa nao lida marcada.

Um deep link por conversa precisa de uma referencia de conversa no payload, o
que e mudanca no contrato da #746 e um `v` novo — nao algo a adivinhar aqui.

### Clique nao e autorizacao

O worker nao concede nada. O clique abre um caminho interno e a aplicacao pede
ao servidor tudo o que renderiza, com a sessao que ja tem. Uma notificacao que
chegou a um browser cuja membership foi revogada no meio do caminho cai no fluxo
de recusa de sempre (#475), que responde igual para "nao existe" e "sem
permissao" — e nao em conteudo que este worker tenha deixado passar.

## Testes

| Camada                                    | Onde                                                  |
| ----------------------------------------- | ----------------------------------------------------- |
| payload, apresentacao, click, Clients API | `src/notifications/serviceWorker.test.ts`             |
| registro, idempotencia, degradacao        | `src/notifications/serviceWorkerRegistration.test.ts` |
| registro/ativacao em browser real         | `e2e/notifications-service-worker.spec.ts`            |

O E2E cobre registro, escopo, ativacao e ausencia de registro duplicado. Ele
**nao** cobre a renderizacao de uma notificacao nem o clique nela: o Chromium
headless — o modo em que a suite roda local e no CI — reporta
`Notification.permission` como `denied` e `context.grantPermissions()` nao muda
isso, entao `showNotification()` e recusado e `getNotifications()` volta sempre
vazio. Um push entregue por CDP nao tem efeito observavel para asserir. Isso foi
verificado contra o browser real, nao suposto. Renderizar e clicar exigiriam um
browser headed (servidor X no runner), que e mudanca de harness de CI e nao
desta issue.

## Limites conhecidos

- Sem deep link por conversa, pelo motivo acima.
- Sem cache, sem handler de `fetch`, sem offline. O worker existe para
  notificacao; PWA offline e outro assunto e outra issue.
- A inscricao existe desde a #748, pelo reconcile do browser; este worker
  continua sem saber quem se inscreveu.
- O preview da #870 aparece no banner do sistema operacional, inclusive em tela
  de bloqueio. Nao ha, nem antes nem depois dessa issue, preferencia por usuario
  de "ocultar conteudo"; a politica MVP e o interruptor por deployment
  `NOTIFICATION_PUSH_PREVIEW_ENABLED`, desligado por padrao.
- Um Service Worker anterior a #870 recusa um payload v2 e nao mostra nada. Por
  isso a versao emitida e configuravel: o worker vai primeiro, o interruptor
  depois. Ver "Configuracao" em [notification-web-push.md](notification-web-push.md).
