/**
 * Voice recorder state machine (issue #670).
 *
 * `idle -> requesting_permission -> recording <-> paused -> reviewing ->
 * uploading -> idle`, with `denied` and `failed` reachable from the requesting
 * and uploading steps. Discarding is reachable from every non-idle phase and
 * always returns to `idle`.
 *
 * # What this hook owns and what it deliberately does not
 *
 *  - `getUserMedia` is called only from `start()` — a direct response to the
 *    user pressing the mic button, never on mount and never speculatively.
 *  - This is a second, independent microphone consumer from LiveKit's call
 *    stack: it requests its own stream and holds its own MediaRecorder, and
 *    shares no device manager with calls. Most browsers happily serve two
 *    concurrent `getUserMedia` audio consumers from the same input device; on
 *    hardware that cannot, the second request fails and surfaces here as an
 *    ordinary `failed` phase — recording and calling remain two unrelated
 *    features that happen to both want the microphone, exactly as intended.
 *  - Every exit — discard, send, error, the destination changing (switching
 *    conversation), and unmount — stops every media track, revokes the
 *    preview object URL and clears the timer. There is no path that leaves
 *    any of the three behind.
 *  - Duration is wall-clock, measured by this hook, not decoded from the
 *    blob. It is sent as a display hint only (see filesApi.uploadAttachment's
 *    VoiceMessageUploadOptions) and never trusted by the server for anything
 *    beyond that.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import type { UploadProgress } from "../lib/api";
import type { AttachmentUploadTarget } from "./useAttachmentUpload";
import { uploadAttachment } from "./filesApi";

export type VoiceRecorderPhase =
  | "idle"
  | "requesting_permission"
  | "recording"
  | "paused"
  | "reviewing"
  | "uploading"
  | "denied"
  | "failed";

export interface VoiceRecorderState {
  phase: VoiceRecorderPhase;
  /** Milliseconds of actual recording, excluding paused time. */
  elapsedMs: number;
  /** Local object URL for the reviewing player. Never sent anywhere. */
  previewUrl: string | null;
  error: string | null;
  uploadProgress: UploadProgress | null;
}

export interface VoiceRecorderControls extends VoiceRecorderState {
  /** False when this browser offers no MediaRecorder format this backend accepts. */
  supported: boolean;
  start: () => void;
  pause: () => void;
  resume: () => void;
  stop: () => void;
  discard: () => void;
  send: () => void;
}

// Ordered by preference: the first one this browser can both record and the
// backend will accept (see domain.VoiceCompatibleContent on the server).
// WebM/Opus first for Chromium and Firefox; MP4/AAC last for Safari, which
// does not support WebM at all.
const CANDIDATE_MIME_TYPES = [
  "audio/webm;codecs=opus",
  "audio/webm",
  "audio/ogg;codecs=opus",
  "audio/ogg",
  "audio/mp4;codecs=mp4a.40.2",
  "audio/mp4",
];

function pickRecordingMimeType(): string | undefined {
  if (typeof MediaRecorder === "undefined" || typeof MediaRecorder.isTypeSupported !== "function") {
    return undefined;
  }
  return CANDIDATE_MIME_TYPES.find((type) => MediaRecorder.isTypeSupported(type));
}

function extensionFor(mimeType: string): string {
  if (mimeType.startsWith("audio/webm")) return "webm";
  if (mimeType.startsWith("audio/ogg")) return "ogg";
  if (mimeType.startsWith("audio/mp4")) return "m4a";
  return "dat";
}

const TIMER_TICK_MS = 200;

export interface VoiceRecorderOptions {
  target: AttachmentUploadTarget | null;
  maxUploadBytes: number | null;
  /**
   * Called once a recording has been uploaded, with the resulting attachment
   * id, to actually send the message. Returning true consumes the recording
   * (the hook resets to idle); returning false — the send itself failed —
   * leaves the uploaded blob available to retry from `reviewing`.
   */
  onUploaded: (attachmentId: string) => Promise<boolean>;
}

const initialState: VoiceRecorderState = {
  phase: "idle",
  elapsedMs: 0,
  previewUrl: null,
  error: null,
  uploadProgress: null,
};

export function useVoiceRecorder({
  target,
  maxUploadBytes,
  onUploaded,
}: VoiceRecorderOptions): VoiceRecorderControls {
  const [state, setState] = useState<VoiceRecorderState>(initialState);
  const stateRef = useRef(state);

  const streamRef = useRef<MediaStream | null>(null);
  const recorderRef = useRef<MediaRecorder | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  const mimeTypeRef = useRef<string>("");
  const blobRef = useRef<Blob | null>(null);
  const previewUrlRef = useRef<string | null>(null);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const segmentStartRef = useRef(0);
  const accumulatedMsRef = useRef(0);
  const discardingRef = useRef(false);
  const uploadControllerRef = useRef<AbortController | null>(null);
  // True exactly while this hook instance is unmounted; false for the whole
  // time it is actually mounted. It is not merely "set once" — the lifecycle
  // effect below resets it to false on every setup and only its cleanup sets
  // it back to true — which is what survives React StrictMode's development
  // double-invoke (setup → cleanup → setup again on the same instance)
  // without getting stuck true forever after the synthetic first cleanup.
  //
  // `stateRef` alone cannot substitute for this: on a real unmount no further
  // render ever runs, so `stateRef.current.phase` stays frozen at whatever it
  // last was — still "requesting_permission" if that is where the user left
  // it — instead of reflecting that nobody is looking at this hook any more.
  // This is the one signal the pending getUserMedia continuation below needs
  // and the phase machine cannot provide.
  const abandonedRef = useRef(false);
  const targetRef = useRef(target);
  const maxUploadBytesRef = useRef(maxUploadBytes);
  const onUploadedRef = useRef(onUploaded);

  // Refs mirroring the latest render's values for callbacks that fire later
  // (event handlers, timers, promise continuations) to read without becoming
  // a dependency that would tear down and rebuild the MediaRecorder wiring.
  // Written from an effect, never during render.
  useEffect(() => {
    stateRef.current = state;
    targetRef.current = target;
    maxUploadBytesRef.current = maxUploadBytes;
    onUploadedRef.current = onUploaded;
  });

  const supported =
    typeof navigator !== "undefined" &&
    typeof navigator.mediaDevices?.getUserMedia === "function" &&
    pickRecordingMimeType() !== undefined;

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const stopTracks = useCallback(() => {
    streamRef.current?.getTracks().forEach((track) => track.stop());
    streamRef.current = null;
  }, []);

  const revokePreview = useCallback(() => {
    if (previewUrlRef.current !== null) {
      URL.revokeObjectURL(previewUrlRef.current);
      previewUrlRef.current = null;
    }
  }, []);

  /** Full teardown: tracks stopped, timer cleared, preview revoked, blob dropped. */
  const resetToIdle = useCallback(() => {
    clearTimer();
    stopTracks();
    revokePreview();
    uploadControllerRef.current?.abort();
    uploadControllerRef.current = null;
    recorderRef.current = null;
    chunksRef.current = [];
    blobRef.current = null;
    accumulatedMsRef.current = 0;
    setState(initialState);
  }, [clearTimer, revokePreview, stopTracks]);

  const startTimer = useCallback(() => {
    clearTimer();
    segmentStartRef.current = performance.now();
    timerRef.current = setInterval(() => {
      const elapsed = accumulatedMsRef.current + (performance.now() - segmentStartRef.current);
      setState((current) => ({ ...current, elapsedMs: elapsed }));
    }, TIMER_TICK_MS);
  }, [clearTimer]);

  const handleStop = useCallback(() => {
    clearTimer();
    stopTracks();
    const finalElapsed = accumulatedMsRef.current + (performance.now() - segmentStartRef.current);
    if (discardingRef.current) {
      discardingRef.current = false;
      setState(initialState);
      return;
    }
    const blob = new Blob(chunksRef.current, { type: mimeTypeRef.current });
    chunksRef.current = [];
    blobRef.current = blob;
    const url = URL.createObjectURL(blob);
    previewUrlRef.current = url;
    setState({
      phase: "reviewing",
      elapsedMs: finalElapsed,
      previewUrl: url,
      error: null,
      uploadProgress: null,
    });
  }, [clearTimer, stopTracks]);

  const start = useCallback(() => {
    if (!supported || !targetRef.current || stateRef.current.phase !== "idle") return;
    const mimeType = pickRecordingMimeType();
    if (!mimeType) return;
    setState({ ...initialState, phase: "requesting_permission" });
    navigator.mediaDevices
      .getUserMedia({ audio: true })
      .then((stream) => {
        // The hook may have unmounted while permission was pending — see
        // abandonedRef's own comment. Checked first and before anything is
        // assigned to a ref any other code path reads, so an abandoned call
        // can never hand its stream to a lifecycle nothing will ever tear
        // down again: the track is stopped immediately and nothing else
        // here runs.
        if (abandonedRef.current) {
          stream.getTracks().forEach((track) => track.stop());
          return;
        }
        // The user may have discarded/navigated to a different conversation
        // while permission was pending; do not start recording into a phase
        // that has already moved on.
        if (stateRef.current.phase !== "requesting_permission") {
          stream.getTracks().forEach((track) => track.stop());
          return;
        }
        streamRef.current = stream;
        mimeTypeRef.current = mimeType;
        const recorder = new MediaRecorder(stream, { mimeType });
        recorderRef.current = recorder;
        chunksRef.current = [];
        recorder.ondataavailable = (event) => {
          if (event.data.size > 0) chunksRef.current.push(event.data);
        };
        recorder.onstop = handleStop;
        recorder.onerror = () => {
          clearTimer();
          stopTracks();
          setState({
            ...initialState,
            phase: "failed",
            error: "A gravação falhou inesperadamente.",
          });
        };
        recorder.start();
        accumulatedMsRef.current = 0;
        setState({ ...initialState, phase: "recording" });
        startTimer();
      })
      .catch((error: unknown) => {
        // Nothing to update on an unmounted hook — this is what stops a
        // rejection that arrives after the fact from resurrecting state
        // nobody will ever read.
        if (abandonedRef.current) return;
        const denied =
          error instanceof DOMException &&
          (error.name === "NotAllowedError" || error.name === "PermissionDeniedError");
        setState({
          ...initialState,
          phase: denied ? "denied" : "failed",
          error: denied ? null : "Não foi possível acessar o microfone.",
        });
      });
  }, [clearTimer, handleStop, startTimer, stopTracks, supported]);

  const pause = useCallback(() => {
    if (stateRef.current.phase !== "recording" || !recorderRef.current) return;
    accumulatedMsRef.current += performance.now() - segmentStartRef.current;
    clearTimer();
    recorderRef.current.pause();
    setState((current) => ({ ...current, phase: "paused" }));
  }, [clearTimer]);

  const resume = useCallback(() => {
    if (stateRef.current.phase !== "paused" || !recorderRef.current) return;
    recorderRef.current.resume();
    setState((current) => ({ ...current, phase: "recording" }));
    startTimer();
  }, [startTimer]);

  const stop = useCallback(() => {
    const phase = stateRef.current.phase;
    if (phase !== "recording" && phase !== "paused") return;
    recorderRef.current?.stop();
  }, []);

  const discard = useCallback(() => {
    const phase = stateRef.current.phase;
    if (phase === "recording" || phase === "paused") {
      discardingRef.current = true;
      recorderRef.current?.stop();
      return;
    }
    resetToIdle();
  }, [resetToIdle]);

  const send = useCallback(() => {
    const blob = blobRef.current;
    if (stateRef.current.phase !== "reviewing" || !blob || !targetRef.current) return;
    const controller = new AbortController();
    uploadControllerRef.current = controller;
    const elapsedMs = stateRef.current.elapsedMs;
    setState((current) => ({ ...current, phase: "uploading", error: null }));
    const file = new File([blob], `voice-message.${extensionFor(mimeTypeRef.current)}`, {
      type: mimeTypeRef.current,
    });
    uploadAttachment(
      targetRef.current,
      file,
      maxUploadBytesRef.current,
      controller.signal,
      (progress) => setState((current) => ({ ...current, uploadProgress: progress })),
      { purpose: "voice_message", durationMs: elapsedMs },
    )
      .then((attachment) => onUploadedRef.current(attachment.id))
      .then((consumed) => {
        if (controller.signal.aborted) return;
        if (consumed) {
          resetToIdle();
        } else {
          setState((current) => ({
            ...current,
            phase: "reviewing",
            error: "Não foi possível enviar a mensagem.",
            uploadProgress: null,
          }));
        }
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof DOMException && error.name === "AbortError") return;
        setState((current) => ({
          ...current,
          phase: "reviewing",
          error: "Não foi possível enviar a gravação.",
          uploadProgress: null,
        }));
      });
  }, [resetToIdle]);

  // Conversation switch: never carry a recording, its stream or its preview
  // across destinations. The target's identity (kind:id) is the same key
  // useAttachmentUpload keys its own reset on.
  const targetKey = target ? `${target.kind}:${target.id}` : "";
  const ownerKeyRef = useRef(targetKey);
  useEffect(() => {
    if (ownerKeyRef.current !== targetKey) {
      ownerKeyRef.current = targetKey;
      resetToIdle();
    }
  }, [resetToIdle, targetKey]);

  // Unmount (route change, logout): the same full teardown, plus marking the
  // hook abandoned first — before anything else runs — so a getUserMedia
  // call still pending at this instant has something to check the moment it
  // settles, however long after this cleanup that turns out to be.
  //
  // The setup body resets the marker to false, which is what makes this
  // StrictMode-safe. In development, StrictMode mounts every component
  // twice — effect setup, its cleanup, then setup again — on the same
  // instance, to surface effects that are not idempotent. Without the reset
  // here, that synthetic first cleanup would leave abandonedRef stuck at
  // true forever, and every later getUserMedia continuation would see a hook
  // it wrongly believes has been unmounted (the bug this comment is fixing).
  // A *real* unmount never runs this setup body again, so the marker set by
  // its cleanup is the last thing that ever executes — exactly the signal
  // the getUserMedia continuation still needs.
  useEffect(() => {
    abandonedRef.current = false;
    return () => {
      abandonedRef.current = true;
      clearTimer();
      stopTracks();
      revokePreview();
      uploadControllerRef.current?.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- mount/unmount lifecycle marker only, refs carry the rest
  }, []);

  return {
    ...state,
    supported,
    start,
    pause,
    resume,
    stop,
    discard,
    send,
  };
}
