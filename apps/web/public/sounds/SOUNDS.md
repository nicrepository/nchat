# Notification sound inventory — the Lumen family

Served from `/sounds/`, same-origin, no external CDN and no third-party asset.

Nothing plays these files directly. The one place a key becomes an
`HTMLAudioElement` is `apps/web/src/notifications/notificationSound.ts`
(issue #827): callers name a `NotificationSound`, never a path, and the
key-to-asset map in that module is the only place a file name appears in the
frontend. If a sound needs to be added, renamed or replaced, that map and this
file are the two things to change.

## The seven sounds

| Key               | Asset                             | Played for                                           |
| ----------------- | --------------------------------- | ---------------------------------------------------- |
| `message`         | `nchat_lumen_message.wav`         | An ordinary message the reader is not looking at     |
| `in-conversation` | `nchat_lumen_in_conversation.wav` | An ordinary message in the conversation on screen    |
| `mention`         | `nchat_lumen_mention.wav`         | A message naming the reader (or everyone)            |
| `urgent`          | `nchat_lumen_urgent.wav`          | A message its author marked urgent                   |
| `incoming-call`   | `nchat_lumen_incoming_call.wav`   | A ringing inbound 1:1 call, and its settings preview |
| `call-start`      | `nchat_lumen_call_start.wav`      | A call becoming connected                            |
| `call-end`        | `nchat_lumen_call_end.wav`        | A call finishing                                     |

The four message-side keys are spelled exactly like `NotificationClass`
(`apps/web/src/chat/notificationClass.ts`, issue #826), which is what lets the
resolved class _be_ the sound key with no second mapping in between — the
compiler checks it at the call site.

### When `in-conversation` is heard (issue #829)

Only when the reader is demonstrably attending the conversation — it is open in
this tab, the tab is visible, and the window has focus, all three together. Any
one of them missing and the event is not in-conversation at all; it falls back
to whatever `notificationClass` resolves instead, which for ordinary room
activity the reader is away from stays silent under `soundRules`' ambient gate.

`urgent` and `mention` outrank it, so an urgent message in the attended
conversation is `urgent` and never both. One logical event resolves to exactly
one class, so `message` and `in-conversation` can never sound for the same
message.

### Keys that are ready but not yet heard

Two of the seven have an asset, a key and a player, and nothing calls them:

- **`call-start` / `call-end`** — no call-lifecycle consumer publishes these
  events yet. #827 scope is the assets and the player; wiring them belongs to
  the call lifecycle work, not here.

## Format and provenance

Every file is PCM WAV, stereo, 16-bit, 48000 Hz, and originates from the
official Lumen sound family authored for this project. No external audio
samples and no third-party source. Committed as binaries, the same way the
self-hosted fonts in `apps/web/public/fonts/` are.

| Asset                             | Duration | Size      | SHA-256                                                            |
| --------------------------------- | -------- | --------- | ------------------------------------------------------------------ |
| `nchat_lumen_message.wav`         | 0.88s    | 169004 B  | `064a7f3bfbc3f2bc49939af6f67152d53f4cc36c910bf6f69eb8eae2f75c08fa` |
| `nchat_lumen_in_conversation.wav` | 0.42s    | 80684 B   | `eaaacba9c5652cd5a3c0c99acd8474aaf0e6ef1b4aa1ee375b0e57fb067d5139` |
| `nchat_lumen_mention.wav`         | 1.02s    | 195884 B  | `85ac7eb2459f59144ea3180ec3eaa3160f1a62953da56773da7ce8e6fa4bc9fd` |
| `nchat_lumen_urgent.wav`          | 1.45s    | 278444 B  | `7fa8efb6ec5d07ca59bf3e9b9d8588076dad60e5c21bea29ef5c681571211d05` |
| `nchat_lumen_incoming_call.wav`   | 6.00s    | 1152044 B | `03c3f7831f15e5090e5f9295b25d7c6ef6ed398062d20fa1588bab4913adaf19` |
| `nchat_lumen_call_start.wav`      | 0.78s    | 149804 B  | `49a370dfdbfdaa9c5b91ecb045b5846388074a63aab16393dda3cd091c2be6a9` |
| `nchat_lumen_call_end.wav`        | 0.86s    | 165164 B  | `d26d728de5e8aa60dd21c80800626878678e8a1680aa0415cb9f42266efb91a9` |

### The ringtone is a phrase, not a single ring

`nchat_lumen_incoming_call.wav` contains four rings of ~1.4s followed by ~0.56s
of silence — the ring cadence is composed _into_ the file. That is why
`RINGTONE_REPEAT_MS` in `apps/web/src/calls/incomingCallRingtone.ts` is 6000 and
not the 3500 the previous, much shorter asset needed: repeating on any shorter
interval restarts the file mid-phrase and turns the ring into a stutter.

## Replaced assets

`message-received.wav` and `incoming-call.wav` were the pre-Lumen sounds. Both
were removed in #827 rather than kept: their only consumers (`chat/messageSound.ts`
and the ringtone's own `new Audio(...)`) were replaced by the central player, so
leaving the files behind would have left two assets nothing could reach.

## Verification

```bash
sha256sum apps/web/public/sounds/*.wav
```

`notificationSound.test.ts` also checks structurally that every key in the map
resolves to a file that actually ships in this directory, so a mapping that
outlives its asset fails the suite rather than the browser.
