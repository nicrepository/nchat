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
 *    conversation), and unmount — stops every media track and clears the
 *    timer. The preview object URL is revoked on the same exits by whoever
 *    owns it: this hook when it runs alone, the conversation's draft store
 *    once a finished recording has been handed to it (issues #769, #929) —
 *    a recording that survives a conversation switch keeps its URL until it
 *    is discarded, sent or the drafts are cleared. There is no path that
 *    leaves any of the three behind.
 *  - Duration is wall-clock, measured by this hook, not decoded from the
 *    blob. It is sent as a display hint only (see filesApi.uploadAttachment's
 *    VoiceMessageUploadOptions) and never trusted by the server for anything
 *    beyond that.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { UploadProgress } from "../lib/api";
import { randomId } from "../lib/randomId";
import type { AttachmentUploadTarget } from "./useAttachmentUpload";
import { deleteAttachmentDraft, uploadAttachment } from "./filesApi";
import type { ConversationDraftsApi, DraftVoiceMessage } from "./useConversationDrafts";

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
  /**
   * Makes this recorder reflect the conversation draft's authoritative
   * recording (issue #929). The draft owns the blob, the preview URL and
   * the identity; this only adopts or drops what it holds:
   *
   *  - the same recording: nothing to do;
   *  - none: the recording shown here was consumed or discarded elsewhere,
   *    so this goes back to idle without revoking anything;
   *  - a different one — finalized by a recorder whose composer is already
   *    gone — is adopted as it is, for review. No new blob, no new URL.
   *
   * A live recording or an upload in progress owns this hook, and is never
   * interrupted by it.
   */
  reconcileWithDraft: (voice: DraftVoiceMessage | null) => void;
  /**
   * Abandons whatever this recorder is doing for a session that has ended
   * (`clearAllDrafts` — issue #929, fifth review): a permission still being
   * asked for, a capture in progress, a recording under review, a send on
   * its way. The microphone is released and the recorder goes back to idle.
   * Local only: the store ended the session itself and released the
   * recording it owned, so nothing is revoked or written here.
   */
  resetForSessionEnd: () => void;
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
  /**
   * Issue #769: the conversation's draft, and the key this recording
   * belongs to. Both optional so every pre-#769 caller keeps today's
   * behavior — a recording that never survives a conversation switch.
   */
  drafts?: ConversationDraftsApi;
  draftKey?: string | null;
}

const initialState: VoiceRecorderState = {
  phase: "idle",
  elapsedMs: 0,
  previewUrl: null,
  error: null,
  uploadProgress: null,
};

/** What this hook starts from: a recording the draft already holds (issue #769), or nothing. */
interface RecorderSeed {
  state: VoiceRecorderState;
  mimeType: string;
  blob: Blob | null;
  previewUrl: string | null;
  /** The draft identity of the recording under review, when a draft store holds it. */
  voiceId: string | null;
}

function seedFrom(voice: DraftVoiceMessage | null): RecorderSeed {
  if (!voice)
    return { state: initialState, mimeType: "", blob: null, previewUrl: null, voiceId: null };
  return {
    state: {
      phase: "reviewing",
      elapsedMs: voice.durationMs,
      previewUrl: voice.previewUrl,
      error: null,
      uploadProgress: null,
    },
    mimeType: voice.mimeType,
    blob: voice.blob,
    previewUrl: voice.previewUrl,
    voiceId: voice.id,
  };
}

/** One press of the mic button: its microphone, its recorder, its audio. */
interface RecordingAttempt {
  readonly id: number;
  /** The session it was started in — see useDraftBoundary. */
  readonly generation: number | null;
  stream: MediaStream | null;
  recorder: MediaRecorder | null;
  chunks: Blob[];
  /**
   * Set when this take is being thrown away rather than finished (issue
   * #929, seventh review), so its own finalization produces no blob and no
   * object URL. It belongs to the attempt and not to the hook: an attempt
   * whose finalization turns out to be stale — the session ended while the
   * microphone was open — never comes back to consume it, and a flag left
   * behind in the hook would be read by the *next* take and swallow it.
   */
  discard: boolean;
}

export function useVoiceRecorder({
  target,
  maxUploadBytes,
  onUploaded,
  drafts,
  draftKey,
}: VoiceRecorderOptions): VoiceRecorderControls {
  // Computed once, at this instance's creation, exactly like ChatComposer
  // seeds useChatEditor's initialContent — never re-read on a later drafts
  // change, since a hydrated composer remounts a fresh instance rather than
  // reusing one across conversations (issue #769).
  const seed = useMemo(
    () => seedFrom(drafts?.getDraft(draftKey ?? "")?.voiceMessage ?? null),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- seed-once, see comment above
    [],
  );
  const [state, setStateValue] = useState<VoiceRecorderState>(seed.state);
  /**
   * The phase every handler reads — `stop`, `pause`, `resume`, `send`,
   * `discard` — and therefore the authority the UI is drawn from. It is
   * written by `setState` below, in the same statement that schedules the
   * render, so the two can never be one commit apart: a reader whose click
   * lands in the same frame as the button appearing is answered by the
   * phase that frame is showing (issue #929, fourth review). Reading state
   * back from an effect made the handler lag the render by exactly that
   * window, and refused the click.
   */
  const stateRef = useRef(seed.state);

  /**
   * The recording attempt on screen, and the only one allowed to change
   * anything (issue #929, sixth review). Every attempt owns its own
   * microphone, recorder and audio; its callbacks close over it and say
   * nothing once it has been replaced — by a discard and a new take, or by
   * the end of the session. Generation alone could not tell those apart:
   * two attempts follow one another inside the same session all the time.
   */
  const currentRecordingRef = useRef<RecordingAttempt | null>(null);
  const attemptSequenceRef = useRef(0);
  const mimeTypeRef = useRef<string>(seed.mimeType);
  const blobRef = useRef<Blob | null>(seed.blob);
  const previewUrlRef = useRef<string | null>(seed.previewUrl);
  const voiceIdRef = useRef<string | null>(seed.voiceId);
  /**
   * The store generation this recording belongs to (issue #929, third
   * review). Captured when the recording starts, checked before its
   * finalization writes anything: `clearAllDrafts` — logout, account
   * switch — ends the session a recording was made in, and a blob
   * finalized afterwards belongs to nobody. Navigation does not change it.
   */
  const generationRef = useRef<number | null>(null);
  const draftsRef = useRef(drafts);
  const draftKeyRef = useRef(draftKey);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const segmentStartRef = useRef(0);
  const accumulatedMsRef = useRef(0);
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

  /**
   * Every state change of this hook, recorded and scheduled together. The
   * updater form reads the phase that is already committed here, which is
   * the same value the handlers read.
   */
  const setState = useCallback(
    (next: VoiceRecorderState | ((current: VoiceRecorderState) => VoiceRecorderState)) => {
      const value = typeof next === "function" ? next(stateRef.current) : next;
      stateRef.current = value;
      setStateValue(value);
    },
    [],
  );

  // Refs mirroring the latest render's values for callbacks that fire later
  // (event handlers, timers, promise continuations) to read without becoming
  // a dependency that would tear down and rebuild the MediaRecorder wiring.
  // Written from an effect, never during render.
  useEffect(() => {
    targetRef.current = target;
    maxUploadBytesRef.current = maxUploadBytes;
    onUploadedRef.current = onUploaded;
    draftsRef.current = drafts;
    draftKeyRef.current = draftKey;
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

  /**
   * Whether the recording being finalized still belongs to the session it
   * was started in. Without a draft store there is no session to leave, so
   * the recording is always this hook's own.
   */
  const belongsToSession = useCallback((generation: number | null) => {
    const store = draftsRef.current;
    if (!store || generation === null) return true;
    return store.isGenerationCurrent(generation);
  }, []);

  /** Releases one attempt's microphone, whoever's attempt it is. */
  const releaseMicrophone = useCallback((attempt: RecordingAttempt | null) => {
    attempt?.stream?.getTracks().forEach((track) => track.stop());
    if (attempt) attempt.stream = null;
  }, []);

  const stopTracks = useCallback(() => {
    releaseMicrophone(currentRecordingRef.current);
  }, [releaseMicrophone]);

  /** Stops whatever is being captured right now, if anything is. */
  const stopCurrentRecorder = useCallback(() => {
    currentRecordingRef.current?.recorder?.stop();
  }, []);

  /**
   * Ends the take being captured and marks it, on itself, as one to throw
   * away: whenever its recorder gets around to stopping, it finalizes
   * nothing.
   */
  const discardTake = useCallback(() => {
    const attempt = currentRecordingRef.current;
    if (attempt) attempt.discard = true;
    stopCurrentRecorder();
  }, [stopCurrentRecorder]);

  /** Whether `attempt` is still the one on screen, in the session it began in. */
  const isCurrentAttempt = useCallback(
    (attempt: RecordingAttempt) =>
      currentRecordingRef.current === attempt && belongsToSession(attempt.generation),
    [belongsToSession],
  );

  /**
   * Whether the preview URL is this hook's to revoke. With a draft store
   * wired, a finished recording — URL included — belongs to the draft the
   * moment handleStop hands it over, and the store revokes it exactly when
   * the recording leaves the draft (issue #929). Two owners revoking the
   * same URL is how a recording that should survive a conversation switch
   * ends up with a dead player.
   */
  const ownsPreview = useCallback(() => !(draftsRef.current && draftKeyRef.current), []);

  const isRecordingCurrent = useCallback(
    () => belongsToSession(generationRef.current),
    [belongsToSession],
  );

  const revokePreview = useCallback(() => {
    if (previewUrlRef.current !== null && ownsPreview()) {
      URL.revokeObjectURL(previewUrlRef.current);
    }
    previewUrlRef.current = null;
  }, [ownsPreview]);

  /**
   * Full teardown of this hook's own state: tracks stopped, timer cleared,
   * blob dropped, preview released. Says nothing to the draft: a confirmed
   * send consumes its recording through the composer's send snapshot, and a
   * discard tells the store explicitly (issue #929).
   */
  const resetToIdle = useCallback(() => {
    clearTimer();
    stopTracks();
    revokePreview();
    uploadControllerRef.current?.abort();
    uploadControllerRef.current = null;
    currentRecordingRef.current = null;
    blobRef.current = null;
    voiceIdRef.current = null;
    generationRef.current = null;
    accumulatedMsRef.current = 0;
    setState(initialState);
  }, [clearTimer, revokePreview, setState, stopTracks]);

  const startTimer = useCallback(() => {
    clearTimer();
    segmentStartRef.current = performance.now();
    timerRef.current = setInterval(() => {
      const elapsed = accumulatedMsRef.current + (performance.now() - segmentStartRef.current);
      setState((current) => ({ ...current, elapsedMs: elapsed }));
    }, TIMER_TICK_MS);
  }, [clearTimer, setState]);

  const handleStop = useCallback(
    (attempt: RecordingAttempt) => {
      // An attempt that is no longer the one on screen — replaced by a newer
      // take, or left behind by the end of its session — releases what it
      // owns and says nothing else (issue #929, sixth review). Nothing of
      // the recording now in progress is touched, and no blob or object URL
      // is ever made for audio nobody will hear.
      if (!isCurrentAttempt(attempt)) {
        releaseMicrophone(attempt);
        attempt.chunks = [];
        return;
      }
      clearTimer();
      stopTracks();
      const finalElapsed = accumulatedMsRef.current + (performance.now() - segmentStartRef.current);
      currentRecordingRef.current = null;
      // Either way this attempt is over: what it captured is a blob or it
      // is nothing, and from here on its recorder speaks for a take nobody
      // is looking at any more — a failure it reports afterwards must not
      // take away the recording it just produced.
      if (attempt.discard) {
        attempt.chunks = [];
        setState(initialState);
        return;
      }
      const blob = new Blob(attempt.chunks, { type: mimeTypeRef.current });
      attempt.chunks = [];
      const url = URL.createObjectURL(blob);
      blobRef.current = blob;
      previewUrlRef.current = url;
      // Issue #769: this is the one place a finished-but-unsent recording
      // becomes real, and it can fire *after* this hook has already
      // unmounted — see the unmount cleanup below, which stops the recorder
      // but deliberately leaves finalizing the blob to this handler. Writing
      // to the draft store here (rather than only from an effect that reacts
      // to `state`) is what lets the recording survive a conversation switch
      // instead of being silently dropped once nothing local is listening.
      // abandonedRef, not mountedRef: the two mean the same thing here and
      // abandonedRef is guaranteed already up to date at this instant (set
      // synchronously by the unmount cleanup before the recorder ever gets a
      // chance to stop), whereas mountedRef is a different ref this same
      // hook does not otherwise use.
      const key = draftKeyRef.current;
      if (draftsRef.current && key) {
        voiceIdRef.current = randomId();
        draftsRef.current.setVoiceMessage(key, {
          id: voiceIdRef.current,
          blob,
          previewUrl: url,
          durationMs: finalElapsed,
          mimeType: mimeTypeRef.current,
        });
      }
      if (abandonedRef.current) return;
      setState({
        phase: "reviewing",
        elapsedMs: finalElapsed,
        previewUrl: url,
        error: null,
        uploadProgress: null,
      });
    },
    [clearTimer, isCurrentAttempt, releaseMicrophone, setState, stopTracks],
  );

  const start = useCallback(() => {
    if (!supported || !targetRef.current || stateRef.current.phase !== "idle") return;
    const mimeType = pickRecordingMimeType();
    if (!mimeType) return;
    // Captured here, at the reader's press, so the whole recording —
    // permission, capture, finalization — belongs to this session. The
    // local copy is what the continuations below read: the ref is cleared
    // when this recorder goes idle, and a request still in flight then
    // still has to know which session asked for it.
    const startGeneration = draftsRef.current?.captureGeneration() ?? null;
    generationRef.current = startGeneration;
    const attempt: RecordingAttempt = {
      id: ++attemptSequenceRef.current,
      generation: startGeneration,
      stream: null,
      recorder: null,
      chunks: [],
      discard: false,
    };
    currentRecordingRef.current = attempt;
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
        // The microphone was granted to *this* attempt. If it is no longer
        // the one on screen — the session ended, or the reader started
        // another take — the stream is released and nothing else happens:
        // whatever is recording now is none of this attempt's business
        // (issue #929, fifth and sixth reviews).
        if (!isCurrentAttempt(attempt)) {
          stream.getTracks().forEach((track) => track.stop());
          return;
        }
        // The user may have discarded/navigated to a different conversation
        // while permission was pending; do not start recording into a phase
        // that has already moved on.
        if (stateRef.current.phase !== "requesting_permission") {
          stream.getTracks().forEach((track) => track.stop());
          currentRecordingRef.current = null;
          return;
        }
        attempt.stream = stream;
        mimeTypeRef.current = mimeType;
        const recorder = new MediaRecorder(stream, { mimeType });
        attempt.recorder = recorder;
        attempt.chunks = [];
        recorder.ondataavailable = (event) => {
          // Into this attempt's own buffer: a chunk of an abandoned take
          // can never end up in the audio of the one being recorded now.
          if (event.data.size > 0) attempt.chunks.push(event.data);
        };
        recorder.onstop = () => handleStop(attempt);
        recorder.onerror = () => {
          if (!isCurrentAttempt(attempt)) {
            releaseMicrophone(attempt);
            return;
          }
          clearTimer();
          stopTracks();
          currentRecordingRef.current = null;
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
        // nobody will ever read. A refusal belonging to a session that has
        // ended says nothing to the one that replaced it either.
        if (abandonedRef.current) return;
        // A refusal for an attempt that is over says nothing to the one
        // that replaced it, in this session or the next.
        if (!isCurrentAttempt(attempt)) return;
        const denied =
          error instanceof DOMException &&
          (error.name === "NotAllowedError" || error.name === "PermissionDeniedError");
        setState({
          ...initialState,
          phase: denied ? "denied" : "failed",
          error: denied ? null : "Não foi possível acessar o microfone.",
        });
      });
  }, [
    clearTimer,
    handleStop,
    isCurrentAttempt,
    releaseMicrophone,
    setState,
    startTimer,
    stopTracks,
    supported,
  ]);

  const pause = useCallback(() => {
    const recorder = currentRecordingRef.current?.recorder;
    if (stateRef.current.phase !== "recording" || !recorder) return;
    accumulatedMsRef.current += performance.now() - segmentStartRef.current;
    clearTimer();
    recorder.pause();
    setState((current) => ({ ...current, phase: "paused" }));
  }, [clearTimer, setState]);

  const resume = useCallback(() => {
    const recorder = currentRecordingRef.current?.recorder;
    if (stateRef.current.phase !== "paused" || !recorder) return;
    recorder.resume();
    setState((current) => ({ ...current, phase: "recording" }));
    startTimer();
  }, [setState, startTimer]);

  const stop = useCallback(() => {
    const phase = stateRef.current.phase;
    if (phase !== "recording" && phase !== "paused") return;
    stopCurrentRecorder();
  }, [stopCurrentRecorder]);

  const discard = useCallback(() => {
    const phase = stateRef.current.phase;
    if (phase === "recording" || phase === "paused") {
      discardTake();
      return;
    }
    resetToIdle();
    // The one exit that is the reader's own decision about the recording:
    // it leaves the draft here, and the store releases its URL with it.
    const key = draftKeyRef.current;
    const store = draftsRef.current;
    if (store && key && store.getDraft(key)?.voiceMessage) store.setVoiceMessage(key, null);
  }, [discardTake, resetToIdle]);

  /**
   * Whether a step of the send that is under way still has anyone to
   * report to: the request was not aborted, and the session that started
   * it is still the store's (issue #929, fifth review).
   */
  const isSendCurrent = useCallback(
    (controller: AbortController) => !controller.signal.aborted && isRecordingCurrent(),
    [isRecordingCurrent],
  );

  /**
   * Hands the uploaded recording to the composer, which turns it into a
   * message. A session that ended in the meantime gets no message: the
   * attachment the server already made is cleaned up the same way a
   * removed one is, and nothing of this recording reaches the new session.
   */
  const publishRecording = useCallback(
    async (controller: AbortController, attachmentId: string) => {
      if (!isSendCurrent(controller)) {
        void deleteAttachmentDraft(attachmentId).catch(() => undefined);
        return false;
      }
      return onUploadedRef.current(attachmentId);
    },
    [isSendCurrent],
  );

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
      (progress) => {
        if (!isSendCurrent(controller)) return;
        setState((current) => ({ ...current, uploadProgress: progress }));
      },
      { purpose: "voice_message", durationMs: elapsedMs },
    )
      .then((attachment) => publishRecording(controller, attachment.id))
      .then((consumed) => {
        if (!isSendCurrent(controller)) return;
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
        if (!isSendCurrent(controller)) return;
        if (error instanceof DOMException && error.name === "AbortError") return;
        setState((current) => ({
          ...current,
          phase: "reviewing",
          error: "Não foi possível enviar a gravação.",
          uploadProgress: null,
        }));
      });
  }, [isSendCurrent, publishRecording, resetToIdle, setState]);

  /** Takes the draft's recording as this hook's own, for review. Local only. */
  const adopt = useCallback(
    (voice: DraftVoiceMessage) => {
      clearTimer();
      stopTracks();
      currentRecordingRef.current = null;
      accumulatedMsRef.current = 0;
      mimeTypeRef.current = voice.mimeType;
      blobRef.current = voice.blob;
      previewUrlRef.current = voice.previewUrl;
      voiceIdRef.current = voice.id;
      setState(seedFrom(voice).state);
    },
    [clearTimer, setState, stopTracks],
  );

  const resetForSessionEnd = useCallback(() => {
    const phase = stateRef.current.phase;
    if (phase === "idle") return;
    if (phase === "recording" || phase === "paused") {
      // Discarded rather than finalized: the blob of a session that ended
      // is worth nothing, and this way no object URL is ever made for it.
      discardTake();
    }
    resetToIdle();
  }, [discardTake, resetToIdle]);

  const reconcileWithDraft = useCallback(
    (voice: DraftVoiceMessage | null) => {
      if ((voice?.id ?? null) === voiceIdRef.current) return;
      const phase = stateRef.current.phase;
      if (phase !== "idle" && phase !== "reviewing") return;
      if (voice) adopt(voice);
      else resetToIdle();
    },
    [adopt, resetToIdle],
  );

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
      const phase = stateRef.current.phase;
      const hasDraftTarget = Boolean(draftsRef.current && draftKeyRef.current);
      if (hasDraftTarget && (phase === "recording" || phase === "paused")) {
        // Issue #769 ("GRAVAÇÃO DE VOZ EM ANDAMENTO"): never send
        // automatically and never lose the bytes already captured. The mic
        // goes dark immediately; `recorder.stop()` is the standard, direct
        // way to flush the final chunk and fire `onstop` — handleStop
        // (still wired, its closures unaffected by this component being
        // gone) finalizes the blob and writes it to the origin
        // conversation's draft once that fires, however long that takes.
        stopTracks();
        stopCurrentRecorder();
        return;
      }
      if (hasDraftTarget && (phase === "reviewing" || phase === "uploading")) {
        // Already handed to the draft store the moment handleStop produced
        // it — ownership of the preview URL/blob transferred there, so
        // this unmount must not revoke or drop it. A send in flight keeps
        // going too (issue #929): its acknowledgement consumes the
        // recording from the draft by identity, exactly as a text send's
        // does, and a failure leaves it there for the reader to retry.
        return;
      }
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
    reconcileWithDraft,
    resetForSessionEnd,
  };
}
