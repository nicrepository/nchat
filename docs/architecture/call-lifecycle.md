# RF-23 — ciclo de vida de chamada 1:1

`chat.calls` é a fonte de verdade. A máquina de estados é:

```text
criação -> ringing -> active -> ended
                   \-> declined
                   \-> cancelled
                   \-> timed_out
```

Somente o destinatário atende ou recusa; somente o originador cancela enquanto
toca; qualquer participante encerra uma chamada ativa. Estados terminais são
imutáveis. Um usuário pode participar de no máximo uma chamada `ringing` ou
`active` por vez.

Criação serializa os dois participantes com advisory locks transacionais. Toda
transição bloqueia a linha com `FOR UPDATE`; expiradores concorrentes selecionam
com `FOR UPDATE SKIP LOCKED`. O relógio do PostgreSQL decide expiração e somente
uma atualização incrementa `version`, portanto accept/decline/cancel/timeout
concorrentes têm um vencedor determinístico entre réplicas.

Não há timer por chamada nem estado global em memória. `expires_at` persiste e um
worker de cada réplica busca chamadas vencidas; reinício não perde timeouts.
Valkey Pub/Sub distribui eventos entre instâncias, com entrega best-effort; após
reconexão, `call.sync` lê novamente o PostgreSQL.

O `media-service` não decide o ciclo de vida. Ele valida a sessão e consulta
`chat.calls` para comprovar `status = 'active'` e participação antes de derivar a
sala `call:<uuid>` e assinar um token LiveKit curto para a identidade autenticada.
