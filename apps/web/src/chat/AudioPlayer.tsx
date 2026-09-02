/**
 * Shared inline audio player core (issue #670).
 *
 * Used both by AttachmentAudio (an ordinary audio file's row) and by the
 * voice-message compact bubble in MessageAttachments.tsx — the two look
 * different around it, but play/pause/seek/rate/loading/error are one
 * implementation so they can never drift.
 *
 * # Design choices, briefly
 *
 *  - Native `<audio>` decodes, seeks and buffers; only the button row and the
 *    seek bar are custom, because the one thing every native control surface
 *    lacks consistently is a discoverable 1x/1.5x/2x control.
 *  - `src` starts `null` and is supplied by the caller only once armed: this
 *    is the "lazy/on-demand" requirement — a message list with several audio
 *    attachments must not fetch all of them just because they scrolled into
 *    view. Pressing Play is what arms it; see AttachmentAudio.
 *  - Exactly one player may be "claimed" at a time, tab-wide, via
 *    audioPlaybackSingleton — starting one pauses whatever else was playing.
 *  - All state here is local to one player instance. A timeline of many
 *    players re-renders only the one whose time actually ticked, never the
 *    others, because React state is component-local by construction; the
 *    `timeupdate` handler additionally throttles to ~4/s so a single long
 *    playback does not re-render even its own row 60 times a second.
 *  - Playback rate persists across the whole app via localStorage: a
 *    per-viewer convenience, never anything the server needs to know.
 *  - A download control (issue #740) is drawn only when the caller offers one,
 *    between the time and the rate. It is an errand run beside playback and
 *    never through it: it neither arms `src` nor touches the element, so it
 *    works before the first Play and during one, and its own failure is a note
 *    beside the controls rather than anything the player has to recover from.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { claimAudioPlayback, releaseAudioPlayback } from "./audioPlaybackSingleton";
import { formatTime } from "./audioTimeFormat";

const RATE_CYCLE = [1, 1.5, 2] as const;
type PlaybackRate = (typeof RATE_CYCLE)[number];

const RATE_STORAGE_KEY = "nchat.audioPlaybackRate";

function loadStoredRate(): PlaybackRate {
  try {
    const raw = window.localStorage.getItem(RATE_STORAGE_KEY);
    const parsed = raw ? Number(raw) : 1;
    return (RATE_CYCLE as readonly number[]).includes(parsed) ? (parsed as PlaybackRate) : 1;
  } catch {
    return 1;
  }
}

function storeRate(rate: PlaybackRate): void {
  try {
    window.localStorage.setItem(RATE_STORAGE_KEY, String(rate));
  } catch {
    // Best effort: a private tab or a full quota just means the next player
    // falls back to 1x, same as a first visit.
  }
}

function nextRate(rate: PlaybackRate): PlaybackRate {
  const index = RATE_CYCLE.indexOf(rate);
  return RATE_CYCLE[(index + 1) % RATE_CYCLE.length];
}

// WebM/MediaRecorder can report Infinity (or later revise to NaN/0) from
// `loadedmetadata`/`durationchange`; only a finite, positive value is usable
// as the effective duration for the seek bar and the time label.
function isValidDuration(value: number | undefined): value is number {
  return typeof value === "number" && Number.isFinite(value) && value > 0;
}

/**
 * An optional download control, drawn between the time label and the rate
 * button (issue #740).
 *
 * The player owns where it sits, how it looks and its own in-flight state; the
 * caller owns what "download" means and how the bytes are obtained. Absent —
 * the recorder's local preview, an ordinary audio file whose row already
 * carries a Baixar action — the control is not rendered at all, so nothing is
 * offered twice.
 */
export interface AudioDownloadAction {
  /** Accessible name, e.g. "Baixar mensagem de voz". */
  label: string;
  /** Pointer tooltip, e.g. "Baixar áudio". */
  title: string;
  /** Starts the download. Rejecting shows a note beside the player. */
  start: () => Promise<void>;
}

export interface AudioPlayerProps {
  /** aria-label stem, e.g. "Áudio: reunião.mp3" or "Mensagem de voz". */
  label: string;
  /** Object URL to play, or null before the caller has armed loading. */
  src: string | null;
  /** True while `src` is being fetched (network), distinct from decode buffering. */
  loading: boolean;
  /** True when the bytes could not be fetched at all. */
  failed: boolean;
  /** Called at most once, the first time the user asks to play before `src` exists. */
  onRequestLoad: () => void;
  /** Server- or client-declared length, seconds, shown before metadata loads. */
  durationHint?: number;
  /** Offers a download beside the controls. Omitted, none is drawn. */
  download?: AudioDownloadAction;
  testIdPrefix: string;
}

/**
 * The download button and the one thing it can say.
 *
 * Its own component so the in-flight state belongs to the control that owns it
 * rather than to the player: nothing here can reach playback, which is exactly
 * the guarantee this feature needs. The failure note is a sibling rather than a
 * child of the button so it can take a row of its own (see the `--download`
 * rule), which is why this returns a fragment instead of an element.
 */
function AudioDownloadControl({
  action,
  testIdPrefix,
}: {
  action: AudioDownloadAction;
  testIdPrefix: string;
}) {
  const [state, setState] = useState<"idle" | "running" | "failed">("idle");

  // Touches nothing about playback: no pause, no play, no currentTime, no
  // rate. Downloading a message you are listening to is a normal thing to do,
  // so the two run side by side.
  //
  // The button stays enabled while the download resolves — disabling a focused
  // control would take the keyboard user's place in the page away from them —
  // so the guard against a double-start is here, and it is scoped to this one
  // control. A failed attempt can be retried straight away.
  const start = useCallback(() => {
    if (state === "running") return;
    setState("running");
    action.start().then(
      () => setState("idle"),
      () => setState("failed"),
    );
  }, [action, state]);

  return (
    <>
      <button
        type="button"
        className="chat-msg-area__audio-download"
        aria-label={action.label}
        title={action.title}
        aria-busy={state === "running"}
        data-testid={`${testIdPrefix}-download`}
        onClick={start}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          download
        </span>
      </button>
      {/* A failed download says so and changes nothing else — the player keeps
          playing, keeps its position and keeps its rate. */}
      {state === "failed" && (
        <span
          className="chat-msg-area__audio-note chat-msg-area__audio-note--download"
          role="alert"
          data-testid={`${testIdPrefix}-download-error`}
        >
          Não foi possível baixar o áudio.
        </span>
      )}
    </>
  );
}

export default function AudioPlayer({
  label,
  src,
  loading,
  failed,
  onRequestLoad,
  durationHint,
  download,
  testIdPrefix,
}: AudioPlayerProps) {
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const lastTickRef = useRef(0);
  const wantsPlayRef = useRef(false);
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(isValidDuration(durationHint) ? durationHint : 0);
  const [buffering, setBuffering] = useState(false);
  const [decodeError, setDecodeError] = useState(false);
  const [rate, setRate] = useState<PlaybackRate>(loadStoredRate);

  // Armed once src arrives, if the user asked to play before it existed.
  useEffect(() => {
    if (src && wantsPlayRef.current) {
      wantsPlayRef.current = false;
      audioRef.current?.play()?.catch(() => setDecodeError(true));
    }
  }, [src]);

  useEffect(() => {
    const el = audioRef.current;
    if (!el) return;
    el.playbackRate = rate;
    // Modern browsers already default true; set explicitly and tolerate the
    // prefixed forms older WebKit still ships, so speeding up never pitches
    // a voice message up like a tape on fast-forward.
    const withVendorPrefixes = el as HTMLAudioElement & {
      mozPreservesPitch?: boolean;
      webkitPreservesPitch?: boolean;
    };
    el.preservesPitch = true;
    withVendorPrefixes.mozPreservesPitch = true;
    withVendorPrefixes.webkitPreservesPitch = true;
  }, [rate]);

  // Keyed on `src` rather than `[]`: the <audio> element only exists once
  // `src` is non-null (see the render below), so this is what lets the
  // cleanup release the actual claimed element instead of the `null` a
  // mount-only effect would have captured before the element ever appeared.
  useEffect(() => {
    const el = audioRef.current;
    if (!el) return;
    return () => releaseAudioPlayback(el);
  }, [src]);

  const handlePlayPause = useCallback(() => {
    if (!src) {
      if (!loading && !failed) {
        wantsPlayRef.current = true;
        onRequestLoad();
      }
      return;
    }
    const el = audioRef.current;
    if (!el) return;
    if (playing) el.pause();
    else el.play()?.catch(() => setDecodeError(true));
  }, [failed, loading, onRequestLoad, playing, src]);

  const handleRateCycle = useCallback(() => {
    setRate((current) => {
      const next = nextRate(current);
      storeRate(next);
      return next;
    });
  }, []);

  const handleSeek = useCallback((event: React.ChangeEvent<HTMLInputElement>) => {
    const value = Number(event.target.value);
    if (audioRef.current) audioRef.current.currentTime = value;
    setCurrentTime(value);
  }, []);

  // WebM can report duration on `loadedmetadata` and revise it again on a
  // later `durationchange`; both go through the same validity guard so an
  // invalid revision (Infinity/NaN/0/negative) never overwrites a good one.
  const handleDurationCandidate = useCallback((value: number) => {
    if (isValidDuration(value)) setDuration(value);
  }, []);

  const showError = failed || decodeError;
  const showLoading = loading && !showError;

  return (
    <div className="chat-msg-area__audio-player" data-testid={`${testIdPrefix}-player`}>
      {src && (
        <audio
          ref={audioRef}
          src={src}
          preload="metadata"
          aria-label={label}
          data-testid={`${testIdPrefix}-audio-el`}
          onPlay={() => {
            if (audioRef.current) claimAudioPlayback(audioRef.current);
            setPlaying(true);
          }}
          onPause={() => setPlaying(false)}
          onEnded={() => {
            setPlaying(false);
            // The last timeupdate before "ended" may have been throttled
            // away, so force the visual progress to the end explicitly.
            setCurrentTime((prev) => (isValidDuration(duration) ? duration : prev));
          }}
          onLoadedMetadata={(e) => handleDurationCandidate(e.currentTarget.duration)}
          onDurationChange={(e) => handleDurationCandidate(e.currentTarget.duration)}
          onTimeUpdate={(e) => {
            const now = e.currentTarget.currentTime;
            if (Math.abs(now - lastTickRef.current) < 0.25 && now !== 0) return;
            lastTickRef.current = now;
            setCurrentTime(now);
          }}
          onWaiting={() => setBuffering(true)}
          onPlaying={() => setBuffering(false)}
          onCanPlay={() => setBuffering(false)}
          onError={() => setDecodeError(true)}
        />
      )}
      <button
        type="button"
        className="chat-msg-area__audio-playbtn"
        aria-label={playing ? `Pausar ${label}` : `Reproduzir ${label}`}
        disabled={showError}
        data-testid={`${testIdPrefix}-playpause`}
        onClick={handlePlayPause}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          {showLoading ? "hourglass_empty" : playing ? "pause" : "play_arrow"}
        </span>
      </button>
      <input
        type="range"
        className="chat-msg-area__audio-seek"
        aria-label={`Progresso de ${label}`}
        min={0}
        max={duration || 0}
        step={0.1}
        value={Math.min(currentTime, duration || 0)}
        disabled={!src || showError}
        data-testid={`${testIdPrefix}-seek`}
        onChange={handleSeek}
      />
      <span className="chat-msg-area__audio-time" data-testid={`${testIdPrefix}-time`}>
        {formatTime(currentTime)} / {formatTime(duration)}
      </span>
      {download && <AudioDownloadControl action={download} testIdPrefix={testIdPrefix} />}
      <button
        type="button"
        className="chat-msg-area__audio-rate"
        aria-label={`Velocidade de reprodução: ${rate}x. Alterar velocidade.`}
        disabled={showError}
        data-testid={`${testIdPrefix}-rate`}
        onClick={handleRateCycle}
      >
        {rate}x
      </button>
      {buffering && !showError && (
        <span
          className="chat-msg-area__audio-note"
          role="status"
          data-testid={`${testIdPrefix}-buffering`}
        >
          Carregando…
        </span>
      )}
      {showError && (
        <span
          className="chat-msg-area__audio-note"
          role="alert"
          data-testid={`${testIdPrefix}-error`}
        >
          Não foi possível reproduzir o áudio.
        </span>
      )}
    </div>
  );
}
