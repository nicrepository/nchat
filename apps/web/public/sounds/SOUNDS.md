# Notification sound inventory

Served from `/sounds/`, played via `apps/web/src/chat/messageSound.ts`. No external CDN, no third-party asset.

## message-received.wav

| Field   | Value                                                              |
| ------- | ------------------------------------------------------------------ |
| Format  | PCM, mono, 16-bit, 22050 Hz, ~0.3s                                 |
| Content | Two-tone chime (660Hz → 880Hz, 150ms each, fade in/out edges)      |
| License | Original, self-authored for this project — no external source      |
| SHA-256 | `57f875af7a39c397ac5b028af1c0289d68be4376389b75dc7da9a9216b2748b0` |

Synthesized locally with a one-off Python script (stdlib `wave`/`struct`/`math` only, no
external tools or network access). The script itself isn't part of the build — only the
resulting binary is committed, same as the self-hosted fonts in `apps/web/public/fonts/`.

## Verification

```bash
sha256sum apps/web/public/sounds/*.wav
```
