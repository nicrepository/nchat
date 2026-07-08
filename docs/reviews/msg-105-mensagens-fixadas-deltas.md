# MSG-105 Mensagens Fixadas - Deltas de Review

Data: 2026-07-08

## Decisoes Humanas

- Pins agora valem para canais e DMs. RF-05 citava canal; DM e uma expansao de escopo decidida agora, nao bug.
- Todos que podem ler o container podem fixar e desafixar, incluindo Guest quando a regra atual de leitura permitir.
- Pin e unpin usam o mesmo gate. Qualquer leitor do container pode desafixar pin criado por outra pessoa.
- DM cobre conversa 1:1 e grupo existente, sem diferenca de permissao para pins.
- O limite de 50 e por container individual: `(target_type, target_id)`.

## Design Review Delta

- `chat.message_pins` usa `target_type/target_id/message_id` como chave primaria.
- A autorizacao de pins foi movida para o store SQL e reutiliza a mesma regra polimorfica de leitura de mensagens usada por favoritos: workspace ativo, membro ativo, canal publico/privado legivel ou DM ativa com membro ativo.
- `PermissionService.CanRead` continua sendo o caminho de canal para fluxos de mensagens existentes; pins/favorites compartilham a regra SQL porque ambos precisam resolver canal ou DM no mesmo ponto.
- `CanModerateChannel`, `authorizeModerate` e `domain.CanPinInChannel` foram removidos porque a regra de papel deixou de existir.
- A CTE de insert confirma o tipo real do container da mensagem: `target_type='channel'` exige `channel_id=target_id` e `dm_conversation_id IS NULL`; `target_type='dm'` exige o inverso.

## Security Review Delta

- O IDOR novo de confusao canal/DM fica bloqueado pela CTE de insert descrita acima.
- `Pin()` e `Unpin()` registram `actor_user_id`, `target_type`, `target_id` e `message_id`; conteudo da mensagem nao e logado.
- Pin/unpin usam rate limit dedicado de 10/min por usuario, separado de envio de mensagem.
- `pin.updated` passa a carregar target generico e continua sendo rechecado por assinante no hub antes da entrega.
- Dedup de `pin.updated` em janela curta ficou fora desta rodada: o evento e idempotente, pequeno, e o cliente refaz a lista autoritativa; adicionar estado temporal no hub nao compensou agora.
