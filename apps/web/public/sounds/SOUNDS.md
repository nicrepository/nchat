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

## incoming-call.wav

"NChat Soft Ring" — additive synthesis of a soft mallet/marimba-like timbre
(fundamental plus a few quieter, faster-decaying harmonic partials, short
raised-cosine attack), three notes (D5 → F#5 → A5) with uneven spacing and a
discreet high partial for a light, airy character. Not a physical model of
any real instrument and not a reproduction of any existing product's
melody, rhythm, or sound signature.

| Field      | Value                                                                                                |
| ---------- | ---------------------------------------------------------------------------------------------------- |
| Format     | PCM WAV, mono, 16-bit, 22050 Hz                                                                      |
| Duration   | ~1.35s                                                                                               |
| Content    | Incoming-call ringtone ("NChat Soft Ring" — Airy variant) for direct 1:1 calls, voice and video      |
| Provenance | Original, synthesized specifically for this project; no external audio samples or third-party source |
| SHA-256    | `20207813be3d26a33e7be9825968fc2a4c3b83ac08c0c36ad8c871f702f8eb28`                                   |

## Verification

```bash
sha256sum apps/web/public/sounds/*.wav
```
