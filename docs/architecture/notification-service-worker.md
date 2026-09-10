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
v != 1                        -> nada
campo obrigatorio ausente/vazio -> nada
```

Fail-closed em todos: nao existe default seguro para inventar. O payload e
produzido pelo nosso proprio notification-service, mas chega por um terceiro e e
decifrado pelo browser, entao e validado como se nao fosse nosso.

Os cinco campos obrigatorios sao `id`, `type`, `source_type`, `source_id` e
`occurred_at`, e `v` tem de ser exatamente `1` — o contrato de
[notification-web-push.md](notification-web-push.md). Uma versao desconhecida e
recusada por construcao: e para isso que `v` existe.

> Um push com permissao concedida que nao chame `showNotification()` faz alguns
> browsers exibirem a propria notificacao generica ("este site foi atualizado em
> segundo plano"). Isso e comportamento da plataforma, e o preco de falhar
> fechado; a alternativa seria mostrar algo derivado de um payload que nao
> satisfaz o contrato.

### O que a notificacao mostra

Titulo por `type`, escrito no proprio worker, de um conjunto fechado; tipo
desconhecido cai num titulo generico. **Nao ha corpo**: a versao 1 nao carrega
remetente nem preview, porque `chat.notification_outbox` nao guarda nenhum dos
dois, entao nao existe texto vindo do payload que possa chegar a tela.

`icon` e `badge` sao assets locais (`/assets/nic-labs-icon.png`,
`/assets/favicon.png`). Uma URL do payload nunca vira icone — e a versao 1 nem
tem campo para uma.

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
- Sem reconcile de `PushSubscription` no frontend (#745 entregou o backend); o
  worker registra, mas ninguem ainda se inscreve.
