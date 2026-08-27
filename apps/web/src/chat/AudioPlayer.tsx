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
  testIdPrefix: string;
}

export default function AudioPlayer({
  label,
  src,
  loading,
  failed,
  onRequestLoad,
  durationHint,
  testIdPrefix,
}: AudioPlayerProps) {
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const lastTickRef = useRef(0);
  const wantsPlayRef = useRef(false);
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(durationHint ?? 0);
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
          onEnded={() => setPlaying(false)}
          onLoadedMetadata={(e) => setDuration(e.currentTarget.duration)}
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
