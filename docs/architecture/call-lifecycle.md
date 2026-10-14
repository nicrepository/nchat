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

## Presença (issue #798)

A presença lê este ciclo de vida, não mantém outro. Uma pessoa está "em
chamada" quando o próprio domínio de chamadas diz que ela participa de uma:
uma chamada 1:1 em `active` (tocar ainda não é estar em chamada) ou um lease
vivo em `chat.call_participant_leases` de uma chamada de recurso `active`. A
leitura é uma consulta em lote (`PGXPresenceStore.Contexts`) e expira pelo
relógio do PostgreSQL, como o próprio lease.

Toda mudança de participação é anunciada à presença **pela própria transação
de chamada, antes do commit**, com o conjunto que os locks dela tornaram
definitivo (`PresenceFacts.OpenPresenceFacts`, ligado ao hub no bootstrap):

- iniciar uma chamada de recurso, entrar, sair: o ator;
- aceitar ou encerrar uma chamada 1:1: chamador e chamado; tocar, recusar e
  cancelar não mudam participação;
- encerrar uma chamada de recurso: todo titular de lease, lido sob o
  `FOR UPDATE` da linha da chamada — o mesmo lock que entrada, renovação e
  saída tomam. Quem entrou antes do encerramento é anunciado por ele; quem
  chega depois encontra a chamada encerrada (`ErrConflict`) e não entra;
- renovar um lease (`call.presence`): a instrução que renova diz, na mesma
  linha travada, se o lease ainda estava vivo. Estender um lease vivo não muda
  fato nenhum e não é anunciado; renovar um lease vencido recoloca o ator na
  chamada e é anunciado como uma entrada.

O anúncio move a revisão de presença de cada pessoa e a marca como em mudança
até o fim da transação. Se não puder ser feito — o mesmo Valkey de que o limite
de início de chamada já depende —, a transação é desfeita e o cliente recebe
`call.error` com `call_unavailable`, que pode repetir. A leitura de contexto da
presença lê os leases `FOR SHARE`, para não concluir "fora da chamada" de uma
linha que uma renovação ainda não confirmada está substituindo. Um lease vale
até o seu `expires_at`: a presença o trata como prazo (uma composição que leu
"em chamada" não é gravada depois do fim do lease, no relógio do Valkey) e o
encerramento automático (`ExpireDue`) não muda nenhum fato que ainda valesse.
Depois do commit, a instância recalcula a presença dos anunciados que serve;
eventos de ciclo de vida vindos de outra réplica fazem o mesmo. Ver
[presence-websocket.md](../api/presence-websocket.md#versão-da-presença-efetiva-798).
