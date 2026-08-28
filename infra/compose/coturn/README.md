# coturn (TURN/STUN) local development

The local Compose stack (profile `media`) runs a standalone coturn server for dev,
providing STUN/TURN for the LiveKit dev stack and for future media-service work.

## Endpoints

- STUN/TURN: `localhost:3478` (UDP + TCP)
- TURN relay: `localhost:49160-49200` (UDP, dev-only narrow range)

Ports, realm and the shared secret can be changed in `infra/compose/.env.dev`.

## Configuration

`turnserver.conf.template` is committed and contains no secrets. It is rendered
into `turnserver.runtime.conf` (gitignored) by `scripts/dev/_media_env.sh` before
the container starts.

coturn uses `use-auth-secret` / `static-auth-secret` (TURN REST API style,
time-limited HMAC credentials) — it is **not** an open relay. The admin CLI
(`no-cli`) is disabled and the relay port range is restricted.

## Not Configured

- TLS (`turns:`) — no public certs in dev, see runbook "fora do escopo".
- Peer IP allow/deny lists — acceptable here because every port is published on
  `127.0.0.1` only (see compose.dev.yml); revisit before any non-dev exposure.
- High availability / clustering.

## Smoke Test

```bash
make dev-media-up
make dev-media-validate
make dev-media-down
```

See `docs/runbooks/task-livekit-coturn-dev.md` for full details, including
Windows 11 / Docker Desktop troubleshooting.
