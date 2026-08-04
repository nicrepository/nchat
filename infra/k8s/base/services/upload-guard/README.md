# upload-guard

Static request-body ceiling for attachment uploads (RF-32, issue #458).

## Why it exists

Traefik v3.6 has exactly one native way to cap a request body — the `buffering`
middleware — and it works by reading the whole body before forwarding it. On the
upload routes that happens _before_ file-service authenticates, authorises or
applies any concurrency control, so an unauthenticated client could fill the
gateway's temporary storage at will. That middleware is therefore banned, and
`scripts/ci/gateway-config-check.sh` fails if it reappears anywhere.

nginx caps a body **while streaming it**, which is the property Traefik cannot
provide. Each proxy does what it is good at: Traefik routes, nginx caps.

## What it is not

It is not the RF-32 policy. Two different controls exist:

| Control           | Where        | Scope                  | Value                                                |
| ----------------- | ------------ | ---------------------- | ---------------------------------------------------- |
| Technical ceiling | this guard   | whole HTTP body        | `536879104` bytes, static                            |
| Workspace policy  | file-service | real bytes of the file | 1–512 MiB, per workspace, administrator-configurable |

`536879104` is `512 MiB + 8 KiB` — the largest limit an administrator may set,
plus multipart framing overhead. The 8 KiB is headroom for boundaries and part
headers, never extra file size. Being pinned at the ceiling, the guard is by
construction at least as large as any policy, so it can never refuse an upload
file-service would have accepted, and no reload has to be coordinated with an
administrative change.

The single source of the number is
`libs/go/platform/uploadpolicy.GatewayHardCapBytes`; the CI gate asserts that
this file, the Kubernetes manifests and the local gateway all agree with it.

## Scope

Only `POST /api/files/(channels|dm)/{id}/attachments` is routed through the
guard. Health, readiness, download, listing, metadata and every administrative
route reach file-service directly, so nothing else pays the extra hop.

## Configuration

`nginx.conf.template` lives here, next to the manifests, because kustomize
refuses to read files outside its own directory — and one template read by both
consumers is worth more than a tidier path with two copies of it. The local
Docker Compose gateway mounts this same file. It is rendered with `envsubst` at
start-up:

| Variable                | Local                       | Kubernetes          |
| ----------------------- | --------------------------- | ------------------- |
| `UPLOAD_GUARD_UPSTREAM` | `host.docker.internal:8083` | `file-service:8083` |
| `UPLOAD_GUARD_PORT`     | `8080`                      | `8080`              |

One template, rendered per environment — the alternative was two copies of the
same rules drifting apart.

## Properties the configuration must keep

- `proxy_request_buffering off` — bytes are forwarded as they arrive.
- `proxy_http_version 1.1` — a chunked request is relayed as chunked.
- `client_max_body_size 536879104` — the cap itself.
- `proxy_next_upstream off` — a POST is never retried; a retry would need the
  body replayed and could create the attachment twice.
- No caching, no request-body logging, no `Authorization` or `Cookie` in logs,
  `server_tokens off`.
- Runs as non-root on port 8080 with a read-only root filesystem; everything
  nginx writes goes to a `/tmp` volume.
