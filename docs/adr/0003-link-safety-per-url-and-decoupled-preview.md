# ADR-0003 — Link Safety por URL e preview desacoplado

## Status

Accepted

## Data

2026-09-14

## Contexto

O RF-21 usava `message.status = pending_link_scan` para reter a mensagem
inteira ate o Cloudflare URL Scanner decidir toda URL nela, com verdict
agregado por mensagem e sem prazo para estados nao terminais (issues #566,
#526, #807). Em `nchat-dev` isso deixou mensagens presas indefinidamente e
levou ao desligamento da feature.

## Decisão

1. **Quatro eixos independentes**: lifecycle da mensagem, safety por target
   (URL canonica), clicabilidade e preview. `message.status` nunca e proxy de
   nenhum dos outros; uma mensagem publica imediatamente.
2. **Backend e autoridade** das entidades de link (`links[]` no payload,
   `href` so quando autorizado); o cliente nao decide por parsing proprio.
3. **Todo estado nao terminal tem deadline** (`deadline_at`) e uma varredura
   que roda independentemente das feature flags; `unknown` e o estado terminal
   sem clearance.
4. **Provider abstraido** (`urlsafety.URLReputationProvider`) com circuit
   breaker proprio; Cloudflare URL Scanner permanece o unico adapter e so
   produz SAFE com clearance positivo. Fast deny (denylist) so nega.
5. **Preview so apos `safe` explicito**, em pipeline proprio, workspace-scoped,
   com fetcher SSRF-hardened compartilhado (`libs/go/platform/linkfetch`) e
   `og:image` transformada em thumbnail derivado armazenado e servido pelo NChat.
6. **Policy `balanced`** como unica policy implementada, estruturada em um
   ponto (`domain.LinkAccess`) para admitir `strict`/`permissive` no futuro.

## Consequências

- `chat.link_scans`/`chat.message_link_scans` viram target/ocorrencia; nova
  `chat.link_previews`; migration `chat/000050`.
- URL maliciosa nao recusa mais o envio: bloqueia so o proprio link, com o span
  retirado do corpo em leitura; citacoes/referencias seguem retendo o corpo
  inteiro (projecao agregada mantida).
- O resolver de `pending_link_scan` fica como adapter de drenagem ate nenhum
  ambiente ter linhas nesse status.
- `CHAT_LINK_PREVIEW_ENABLED` e uma flag separada, desligada em todos os
  overlays ate a fase de rollout correspondente (exige egress 80/443 do
  chat-service).
