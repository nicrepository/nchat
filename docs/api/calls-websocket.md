# RF-23 — chamadas 1:1 por WebSocket

O `chat-service` coordena o estado autoritativo. O cliente autentica o WebSocket
existente com o subprotocolo `nchat.v1`; `user_id` e `workspace_id` vêm da sessão,
nunca do payload.

## Comandos

```json
{"type":"call.start","request_id":"<uuid>","target_user_id":"<uuid>","call_type":"audio"}
{"type":"call.accept","call_id":"<uuid>"}
{"type":"call.decline","call_id":"<uuid>"}
{"type":"call.cancel","call_id":"<uuid>"}
{"type":"call.end","call_id":"<uuid>"}
{"type":"call.sync"}
```

`call_type` aceita `audio` ou `video`. `request_id` torna o início idempotente
para o originador. Campos desconhecidos, identidade, participantes, sala, token
ou estado escolhidos pelo cliente são rejeitados.

## Eventos

Os dois participantes recebem eventos `call.ringing`, `call.accepted`,
`call.declined`, `call.cancelled`, `call.timed_out` e `call.ended`. O envelope
usa `schema_version`, `event_id`, `target_type: "user"`, `target_id` e `call`:

```json
{
  "schema_version": 1,
  "type": "call.accepted",
  "event_id": "<uuid>",
  "target_type": "user",
  "target_id": "<authenticated-user-uuid>",
  "call": {
    "call_id": "<uuid>",
    "request_id": "<uuid>",
    "caller_id": "<uuid>",
    "callee_id": "<uuid>",
    "call_type": "video",
    "status": "active",
    "version": 2,
    "created_at": "2026-07-30T12:00:00Z",
    "occurred_at": "2026-07-30T12:00:04Z",
    "expires_at": "2026-07-30T12:00:30Z"
  }
}
```

Falhas esperadas usam `call.error` com `operation`, `call_id` quando aplicável e
códigos estáveis: `call_invalid`, `call_not_found`, `call_invalid_state`,
`call_rate_limited` ou `call_unavailable`. Elas não fecham a conexão.

O cliente aplica somente versões crescentes para o mesmo `call_id`. `call.sync`
reenvia a chamada `ringing` ou `active` do usuário após reconexão.
