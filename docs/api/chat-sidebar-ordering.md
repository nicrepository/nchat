# Chat Service — Ordenacao da sidebar por atividade

> **Issue:** [#414](https://github.com/nicrepository/nchat/issues/414) — Ordenar
> canais, grupos e DMs pela atividade mais recente.
> **Scope:** dois metadados no payload de `GET /api/chat/sidebar` e a regra de
> ordenacao que o cliente aplica sobre eles. Nao ha endpoint novo, nao ha
> migration e nao ha preview da ultima mensagem.

---

## Contrato

`GET /api/chat/sidebar` passa a devolver, em cada item de `channels` e de
`dm_conversations`:

```json
{
  "id": "…",
  "created_at": "2026-08-04T12:00:00.900123Z",
  "last_message_at": "2026-08-04T12:00:00.900123Z"
}
```

| Campo             | Tipo                   | Significado                                                                |
| ----------------- | ---------------------- | -------------------------------------------------------------------------- |
| `created_at`      | RFC 3339 UTC           | Quando a conversa foi criada. Chave de desempate das conversas vazias      |
| `last_message_at` | RFC 3339 UTC ou `null` | `created_at` da mensagem mais recente persistida; `null` se nao ha nenhuma |

`last_message_at` e sempre serializado, inclusive quando e `null`: um cliente
precisa distinguir "sem atividade" de "este servidor nao publica atividade".

### Precisao

Os dois campos sao sempre **UTC** e usam RFC 3339 **com a fracao preservada**
(`time.RFC3339Nano`). O servidor emite ate a precisao que o banco tem:
`chat.messages.created_at` e `TIMESTAMPTZ`, ou seja, microssegundos.

Isso nao e cosmetico. A query escolhe a ultima mensagem por
`ORDER BY created_at DESC, id DESC`, entao duas conversas escritas no mesmo
segundo sao realmente ordenadas. Truncar para segundos publicaria as duas como o
mesmo instante e entregaria a decisao aos desempates por nome e ID — uma ordem
diferente da que o banco tem, e possivelmente diferente da que o evento
WebSocket (que nunca truncou) ja mostrou.

`RFC3339Nano` remove zeros a direita, entao o mesmo instante chega escrito de
formas diferentes conforme o valor:

```
2026-08-04T12:00:00.900123Z   microssegundos
2026-08-04T12:00:00.9Z        .900000000, sem os zeros
2026-08-04T12:00:00Z          segundo exato, sem fracao
```

O cliente compara instantes, nunca strings, entao as tres formas se comportam
como o que sao.

Sao **dois** campos e nao um `COALESCE` pre-resolvido porque a regra depende de
saber se existe mensagem, nao apenas de qual timestamp e maior — ver
[Regra de ordenacao](#regra-de-ordenacao).

O valor e o `created_at` da propria linha em `chat.messages`, atribuido pelo
banco na escrita. Nunca e um timestamp do cliente, nem o instante em que um
evento WebSocket foi publicado ou recebido. Edicao, reacao, digitacao, renomear
a conversa e `updated_at` da conversa **nao** contam como atividade.

Nada mais da mensagem viaja: nem corpo, nem autor, nem `message_id`, nem
`kind`. Mensagem removida e soft delete — a linha e o `created_at` permanecem e
o placeholder continua sendo o fim da conversa, entao ela **continua contando
como atividade**, sem expor conteudo algum.

## Autorizacao

O metadado nao tem permissao propria: ele e resolvido por um `LEFT JOIN LATERAL`
dentro da mesma query autorizada que decide se o item aparece na sidebar.

- Canal privado sem membership: nao aparece na lista, logo nao tem atividade lida.
- DM/grupo sem participacao ativa: idem.
- Isolamento de workspace: o `workspace_id` esta no predicado da propria query.

O lateral roda por linha ja admitida pelos joins de membership, e projeta apenas
`created_at`. Nao ha consulta adicional por conversa: uma statement responde a
lista inteira, qualquer que seja o numero de itens
(`TestPGXSidebarActivityPostgreSQL` conta as statements emitidas).

Os indices que o caminho usa ja existem desde `000004_chat_messages`:
`idx_messages_channel (workspace_id, channel_id, created_at, id)` e
`idx_messages_dm (workspace_id, dm_conversation_id, created_at, id)`. Nenhuma
migration foi necessaria.

## Regra de ordenacao

A ordem e computada no cliente (`apps/web/src/chat/sidebarOrder.ts`) e aplicada
**por secao** — Canais, Mensagens diretas e Grupos sao tres listas disjuntas,
entao um canal nunca desloca um grupo ou uma DM. O comparador e total e nao
depende da estabilidade do `Array.prototype.sort`:

1. item **com** mensagem antes de item **sem** mensagem;
2. entre os que tem, `last_message_at` decrescente;
3. entre os que nao tem, `created_at` decrescente;
4. empate: nome normalizado (trim + lowercase), ascendente;
5. empate final: `id`, ascendente.

O passo 1 e o motivo de os dois campos serem separados: uma conversa vazia
criada agora nao pode ultrapassar uma conversa escrita meses atras, e
`last_message_at ?? created_at` faria exatamente isso.

Timestamps sao comparados como instantes, nao como strings, para que offsets
diferentes ordenem corretamente. `Date` so guarda milissegundos, entao
`parseInstant` decompoe o valor em `{ epochMilliseconds, subMillisecondNanoseconds }`:
a fracao e preenchida a direita ate nove digitos, os tres primeiros digitos vao
para `Date.parse` (que resolve o offset) e o resto e comparado a seguir. Duas
mensagens a 78µs de distancia sao dois instantes diferentes e ordenam como tal.
A mesma precisao vale para `created_at` no fallback.

Um valor ausente ou inparseavel e tratado como "sem instante", de forma
previsivel, e o item cai nas chaves seguintes. Um valor fora do formato acima
ainda passa por `Date.parse` inteiro, com resolucao de milissegundos.

O relogio local do browser nao participa de nada disso: toda comparacao usa
valores que o servidor emitiu.

O backend continua devolvendo a lista na sua ordem historica
(`position, display_name` para canais). Ela nao e a ordem de exibicao e nao
precisa ser: o cliente reordena a cada render, entao recarregar a pagina produz
exatamente a mesma ordem enquanto o estado persistido nao muda.

## Atualizacao em tempo real

A sidebar promove uma conversa ao receber `message.created` e usa
`payload.created_at` — o `created_at` persistido da mensagem. Isso vale tanto
para mensagem recebida quanto para a propria mensagem do usuario: o eco que o
servidor emite depois de persistir e a confirmacao, e nenhum relogio do browser
participa.

- **Monotonico**: aplica-se `max(atual, evento)` com a mesma precisao abaixo do
  milissegundo, entao um evento fora de ordem nao faz a conversa retroceder nem
  quando a diferenca esta so nos microssegundos.
- **Idempotente**: o mesmo evento aplicado n vezes tem o efeito de uma; o eco da
  propria mensagem nao duplica nada.
- **Sem fabricar item**: evento para uma conversa que nao esta na lista nao cria
  linha nenhuma — quem decide o que o usuario ve e a API.
- **Evento sem payload** (mensagem com referencia RF-09, ou evento relayado por
  outra instancia, que tem o payload removido no bus): nao ha timestamp
  confiavel, entao dispara-se o refetch canonico ja usado por
  `conversation.available`, que coalesce rajadas.

## Refetch e corrida

O refetch e autoritativo para **pertencimento** e o estado local e autoritativo
para **atividade mais recente**:

- item que o servidor parou de devolver (acesso revogado) desaparece;
- item novo aparece;
- para os que sobrevivem, mantem-se o maior entre o `last_message_at` do
  servidor e o que ja se conhecia. Instantes iguais escritos de formas
  diferentes comparam como iguais e nao contam como mudanca.

Isso resolve a corrida "refetch comeca → evento novo chega → resposta antiga
chega depois" sem contador de geracao: a resposta antiga nao pode desfazer a
promocao. E seguro porque atividade nunca anda para tras no banco — a exclusao e
logica e preserva `created_at`.

`retry()` (reload de fato) limpa a lista antes, entao volta a valer estritamente
o que esta persistido.
