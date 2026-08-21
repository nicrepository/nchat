# Limite de reações por mensagem

Cada usuário pode manter no máximo cinco reações distintas ativas em uma mesma
mensagem, independentemente de a conversa ser uma DM, grupo ou canal. Reações
de outras pessoas não consomem esse limite.

Repetir uma reação já selecionada continua sendo um _toggle_: ela é removida e
libera imediatamente uma vaga. Uma sexta reação distinta é rejeitada pelo
servidor e não publica um evento `reaction.updated`.

## Erro WebSocket

Quando o limite é atingido, o servidor responde ao cliente que enviou o
comando `reaction.toggle` com:

```json
{
  "type": "error",
  "code": "reaction_limit_reached",
  "limit": 5
}
```

O cliente deve manter as reações já escolhidas removíveis e informar que uma
reação existente precisa ser removida antes de adicionar outra.

## Consistência e dados existentes

O banco serializa as mutações pela combinação `message_id` e `user_id`; assim,
duas abas não podem ocupar a última vaga com emojis diferentes. Não há migração
destrutiva para dados legados: mensagens que já tenham mais de cinco reações do
mesmo usuário continuam visíveis e removíveis, mas novas reações ficam
bloqueadas até que a quantidade volte a ser menor que cinco.
