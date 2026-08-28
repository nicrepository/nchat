# Spike LiveKit 1:1 (removido)

O emissor de desenvolvimento `POST /spike/token` foi removido porque aceitava
`identity` e `room` definidos pelo cliente. As variaveis `MEDIA_SPIKE_*` e o alias
`LIVEKIT_URL` nao fazem mais parte do media-service.

A preparacao autenticada da V1.0 esta documentada em
[media-livekit-token.md](../api/media-livekit-token.md). Ela valida a sessao no
PostgreSQL, autoriza canal ou DM e deriva identidade, sala, grants e TTL
exclusivamente no servidor.
