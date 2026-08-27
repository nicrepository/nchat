import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import VoiceRecorderPanel from "./VoiceRecorderPanel";
import type { VoiceRecorderControls } from "./useVoiceRecorder";

function recorder(overrides: Partial<VoiceRecorderControls> = {}): VoiceRecorderControls {
  return {
    phase: "idle",
    elapsedMs: 65_000,
    previewUrl: null,
    error: null,
    uploadProgress: null,
    supported: true,
    start: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    stop: vi.fn(),
    discard: vi.fn(),
    send: vi.fn(),
    ...overrides,
  };
}

describe("VoiceRecorderPanel", () => {
  it("renders nothing while the recorder is idle", () => {
    const { container } = render(<VoiceRecorderPanel recorder={recorder()} />);

    expect(container).toBeEmptyDOMElement();
  });

  it("shows the microphone permission state", () => {
    render(<VoiceRecorderPanel recorder={recorder({ phase: "requesting_permission" })} />);

    expect(screen.getByRole("status")).toHaveTextContent("Aguardando permissão do microfone");
  });

  it("shows denied permission and lets the user close it", () => {
    const controls = recorder({ phase: "denied" });

    render(<VoiceRecorderPanel recorder={controls} />);

    expect(screen.getByRole("alert")).toHaveTextContent("Permissão de microfone negada");

    fireEvent.click(screen.getByTestId("chat-voice-close"));

    expect(controls.discard).toHaveBeenCalledTimes(1);
  });

  it("switches the recording controls between pause and resume", () => {
    const recording = recorder({ phase: "recording" });

    const { container, rerender } = render(<VoiceRecorderPanel recorder={recording} />);

    expect(screen.getByTestId("chat-voice-elapsed")).toHaveTextContent("1:05");

    const dot = container.querySelector(".chat-msg-area__voice-dot");
    expect(dot).toHaveClass("chat-msg-area__voice-dot--live");

    expect(screen.getByTestId("chat-voice-pauseresume")).toHaveAccessibleName("Pausar gravação");

    fireEvent.click(screen.getByTestId("chat-voice-pauseresume"));
    expect(recording.pause).toHaveBeenCalledTimes(1);

    const paused = recorder({ phase: "paused" });

    rerender(<VoiceRecorderPanel recorder={paused} />);

    expect(container.querySelector(".chat-msg-area__voice-dot")).not.toHaveClass(
      "chat-msg-area__voice-dot--live",
    );

    expect(screen.getByTestId("chat-voice-pauseresume")).toHaveAccessibleName("Retomar gravação");

    fireEvent.click(screen.getByTestId("chat-voice-pauseresume"));
    fireEvent.click(screen.getByTestId("chat-voice-discard"));
    fireEvent.click(screen.getByTestId("chat-voice-stop"));

    expect(paused.resume).toHaveBeenCalledTimes(1);
    expect(paused.discard).toHaveBeenCalledTimes(1);
    expect(paused.stop).toHaveBeenCalledTimes(1);
  });

  it("renders the review player, review error and send controls", () => {
    const controls = recorder({
      phase: "reviewing",
      previewUrl: "blob:voice-preview",
      error: "Não foi possível enviar a gravação.",
    });

    render(<VoiceRecorderPanel recorder={controls} />);

    expect(screen.getByTestId("chat-voice-preview-player")).toBeInTheDocument();

    expect(screen.getByTestId("chat-voice-error")).toHaveTextContent(
      "Não foi possível enviar a gravação.",
    );

    fireEvent.click(screen.getByTestId("chat-voice-send"));
    fireEvent.click(screen.getByTestId("chat-voice-discard"));

    expect(controls.send).toHaveBeenCalledTimes(1);
    expect(controls.discard).toHaveBeenCalledTimes(1);
  });

  it("shows upload state both without and with progress", () => {
    const { rerender } = render(
      <VoiceRecorderPanel
        recorder={recorder({
          phase: "uploading",
          uploadProgress: null,
        })}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Enviando mensagem de voz");
    expect(screen.getByRole("status")).not.toHaveTextContent("%");

    rerender(
      <VoiceRecorderPanel
        recorder={recorder({
          phase: "uploading",
          uploadProgress: {
            loaded: 25,
            total: 100,
          },
        })}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("25%");
  });

  it("shows both the explicit and fallback failure messages", () => {
    const controls = recorder({
      phase: "failed",
      error: "Falha conhecida.",
    });

    const { rerender } = render(<VoiceRecorderPanel recorder={controls} />);

    expect(screen.getByTestId("chat-voice-error")).toHaveTextContent("Falha conhecida.");

    fireEvent.click(screen.getByTestId("chat-voice-close"));
    expect(controls.discard).toHaveBeenCalledTimes(1);

    rerender(
      <VoiceRecorderPanel
        recorder={recorder({
          phase: "failed",
          error: null,
        })}
      />,
    );

    expect(screen.getByTestId("chat-voice-error")).toHaveTextContent(
      "Não foi possível gravar a mensagem de voz.",
    );
  });
});
