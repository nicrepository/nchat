# LiveKit local development

The local Compose stack (profile `media`) runs a standalone LiveKit server for dev,
in preparation for the media-service work planned in V1.0 Sprints 9-10.

## Endpoints

- Signaling / HTTP / WebSocket: `http://localhost:7880` (`ws://localhost:7880`)
- RTC over TCP fallback: `localhost:7881`
- RTC over UDP: `localhost:50100-50110` (dev-only narrow range)

Ports and the API key/secret can be changed in `infra/compose/.env.dev`.

## Configuration

`livekit.yaml.template` is committed and contains no secrets. It is rendered into
`livekit.runtime.yaml` (gitignored) by `scripts/dev/_media_env.sh` before the
container starts. The API key/secret pair is never written to a file — it is
passed to the container at runtime via the `LIVEKIT_KEYS` environment variable.

## Not Configured

- Production TLS / public domain.
- Redis-backed distributed mode (single dev node only).
- Recording/egress, ingress, or agents.
- Kubernetes deployment.

## Smoke Test

```bash
make dev-media-up
make dev-media-validate
make dev-media-down
```

See `docs/runbooks/task-livekit-coturn-dev.md` for full details, including
Windows 11 / Docker Desktop troubleshooting.
