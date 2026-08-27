/**
 * Composer UI for the voice-recorder state machine (issue #670).
 *
 * Renders nothing at `idle` — ChatComposer shows its ordinary editor then.
 * Every other phase replaces the editor with exactly this panel, so the two
 * inputs (typing, recording) are never both live at once.
 */

import AudioPlayer from "./AudioPlayer";
import { formatTime } from "./audioTimeFormat";
import type { VoiceRecorderControls } from "./useVoiceRecorder";

function Icon({ name }: { name: string }) {
  return (
    <span className="material-symbols-outlined" aria-hidden="true">
      {name}
    </span>
  );
}

export default function VoiceRecorderPanel({ recorder }: { recorder: VoiceRecorderControls }) {
  const { phase, elapsedMs, previewUrl, error, uploadProgress } = recorder;

  if (phase === "idle") return null;

  return (
    <div
      className="chat-msg-area__voice-recorder"
      role="group"
      aria-label="Gravação de mensagem de voz"
      data-testid="chat-voice-recorder"
    >
      {phase === "requesting_permission" && (
        <p className="chat-msg-area__voice-status" role="status">
          Aguardando permissão do microfone…
        </p>
      )}

      {phase === "denied" && (
        <>
          <p className="chat-msg-area__voice-status" role="alert">
            Permissão de microfone negada. Permita o acesso ao microfone nas configurações do
            navegador para gravar uma mensagem de voz.
          </p>
          <button
            type="button"
            className="chat-msg-area__voice-btn"
            aria-label="Fechar"
            data-testid="chat-voice-close"
            onClick={recorder.discard}
          >
            <Icon name="close" />
          </button>
        </>
      )}

      {(phase === "recording" || phase === "paused") && (
        <>
          <span
            className={`chat-msg-area__voice-dot${phase === "recording" ? " chat-msg-area__voice-dot--live" : ""}`}
            aria-hidden="true"
          />
          <span className="chat-msg-area__voice-elapsed" data-testid="chat-voice-elapsed">
            {formatTime(elapsedMs / 1000)}
          </span>
          <button
            type="button"
            className="chat-msg-area__voice-btn"
            aria-label="Descartar gravação"
            data-testid="chat-voice-discard"
            onClick={recorder.discard}
          >
            <Icon name="delete" />
          </button>
          <button
            type="button"
            className="chat-msg-area__voice-btn"
            aria-label={phase === "recording" ? "Pausar gravação" : "Retomar gravação"}
            data-testid="chat-voice-pauseresume"
            onClick={phase === "recording" ? recorder.pause : recorder.resume}
          >
            <Icon name={phase === "recording" ? "pause" : "fiber_manual_record"} />
          </button>
          <button
            type="button"
            className="chat-msg-area__voice-btn chat-msg-area__voice-btn--primary"
            aria-label="Parar gravação e revisar"
            data-testid="chat-voice-stop"
            onClick={recorder.stop}
          >
            <Icon name="check" />
          </button>
        </>
      )}

      {phase === "reviewing" && (
        <>
          <AudioPlayer
            label="Pré-visualização da gravação"
            src={previewUrl}
            loading={false}
            failed={false}
            onRequestLoad={() => undefined}
            durationHint={elapsedMs / 1000}
            testIdPrefix="chat-voice-preview"
          />
          <button
            type="button"
            className="chat-msg-area__voice-btn"
            aria-label="Descartar gravação"
            data-testid="chat-voice-discard"
            onClick={recorder.discard}
          >
            <Icon name="delete" />
          </button>
          <button
            type="button"
            className="chat-msg-area__voice-btn chat-msg-area__voice-btn--primary"
            aria-label="Enviar mensagem de voz"
            data-testid="chat-voice-send"
            onClick={recorder.send}
          >
            <Icon name="send" />
          </button>
        </>
      )}

      {phase === "uploading" && (
        <p className="chat-msg-area__voice-status" role="status">
          Enviando mensagem de voz…
          {uploadProgress && ` ${Math.floor((uploadProgress.loaded / uploadProgress.total) * 100)}%`}
        </p>
      )}

      {phase === "failed" && (
        <>
          <p className="chat-msg-area__voice-status" role="alert" data-testid="chat-voice-error">
            {error ?? "Não foi possível gravar a mensagem de voz."}
          </p>
          <button
            type="button"
            className="chat-msg-area__voice-btn"
            aria-label="Fechar"
            data-testid="chat-voice-close"
            onClick={recorder.discard}
          >
            <Icon name="close" />
          </button>
        </>
      )}

      {phase === "reviewing" && error && (
        <p className="chat-msg-area__voice-status" role="alert" data-testid="chat-voice-error">
          {error}
        </p>
      )}
    </div>
  );
}
