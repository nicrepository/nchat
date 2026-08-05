import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import CallPanel from "./CallPanel";
import type { CallMediaController } from "./useCallMedia";
import type { CallController } from "./useCallSignaling";

const currentUserId = "00000000-0000-4000-8000-000000000301";
const otherUserId = "00000000-0000-4000-8000-000000000302";
const retryIdentity = async () => undefined;

function controller(
  status: "ringing" | "active" | "declined" | "cancelled" | "timed_out" | "ended",
  callerId = otherUserId,
  callType: "audio" | "video" = "video",
): CallController {
  return {
    call: {
      call_id: "00000000-0000-4000-8000-000000000303",
      request_id: "00000000-0000-4000-8000-000000000304",
      caller_id: callerId,
      callee_id: callerId === currentUserId ? otherUserId : currentUserId,
      call_type: callType,
      status,
      version: 1,
      created_at: "2026-07-30T12:00:00Z",
      occurred_at: "2026-07-30T12:00:00Z",
      expires_at: "2026-07-30T12:00:30Z",
    },
    pending: false,
    error: null,
    mediaReady: false,
    mediaActivationRequired: false,
    start: vi.fn(() => true),
    accept: vi.fn(() => true),
    decline: vi.fn(() => true),
    cancel: vi.fn(() => true),
    end: vi.fn(() => true),
    retryMedia: vi.fn(async () => undefined),
    activateMedia: vi.fn(async () => undefined),
    clearTerminal: vi.fn(),
  };
}

function media(overrides: Partial<CallMediaController> = {}): CallMediaController {
  return {
    status: "connected",
    microphoneEnabled: true,
    cameraEnabled: true,
    hasLocalVideo: true,
    hasRemoteMedia: true,
    hasRemoteVideo: true,
    mediaLoading: false,
    audioStarting: false,
    audioActivationRequired: false,
    error: null,
    pendingControl: null,
    bindLocalMedia: vi.fn(),
    bindRemoteMedia: vi.fn(),
    toggleMicrophone: vi.fn(async () => undefined),
    toggleCamera: vi.fn(async () => undefined),
    activateAudio: vi.fn(async () => undefined),
    ...overrides,
  };
}

function renderPanel(calls: CallController, mediaController?: ReturnType<typeof media>) {
  return render(
    <CallPanel
      calls={calls}
      currentUserId={currentUserId}
      identityStatus="ready"
      retryIdentity={retryIdentity}
      participantId={otherUserId}
      participantName="Ana Lima"
      participantAvatarUrl="/media/ana.png"
      media={mediaController}
    />,
  );
}

function deferredValue<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("CallPanel", () => {
  it("renders remote video, local preview and stateful controls", () => {
    const calls = controller("active", currentUserId);
    const mediaController = media();
    renderPanel(calls, mediaController);

    expect(
      screen.getByRole("dialog", { name: "Chamada de vídeo com Ana Lima" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("call-remote-media")).toBeInTheDocument();
    expect(screen.getByTestId("call-local-media")).toBeInTheDocument();
    expect(mediaController.bindRemoteMedia).toHaveBeenCalledWith(expect.any(HTMLDivElement));
    expect(mediaController.bindLocalMedia).toHaveBeenCalledWith(expect.any(HTMLDivElement));
    expect(screen.getByRole("button", { name: "Desativar microfone" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "Desativar câmera" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("renders audio calls without an empty video stage or camera control", () => {
    const calls = controller("active", currentUserId, "audio");
    renderPanel(
      calls,
      media({ cameraEnabled: false, hasLocalVideo: false, hasRemoteVideo: false }),
    );

    expect(screen.getByText("Chamada de áudio")).toBeInTheDocument();
    expect(screen.getByText("AL")).toBeInTheDocument();
    expect(screen.queryByTestId("call-remote-media")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /câmera/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Desativar microfone" })).toBeEnabled();
  });

  it("offers an explicit user action when browser audio activation is required", async () => {
    const user = userEvent.setup();
    const calls = controller("active", currentUserId);
    const mediaController = media({ audioActivationRequired: true });
    const view = renderPanel(calls, mediaController);

    await user.click(screen.getByRole("button", { name: "Ativar áudio da chamada" }));

    expect(mediaController.activateAudio).toHaveBeenCalledOnce();

    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantId={otherUserId}
        participantName="Ana Lima"
        media={media({ audioActivationRequired: false })}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Ativar áudio da chamada" }),
    ).not.toBeInTheDocument();
  });

  it("disables audio activation while startAudio is pending", () => {
    renderPanel(
      controller("active", currentUserId),
      media({ audioActivationRequired: true, audioStarting: true }),
    );

    expect(screen.getByRole("button", { name: "Ativando áudio da chamada" })).toBeDisabled();
  });

  it("shows camera-off fallbacks for remote and local video", () => {
    const calls = controller("active", currentUserId);
    renderPanel(
      calls,
      media({ cameraEnabled: false, hasLocalVideo: false, hasRemoteVideo: false }),
    );

    expect(screen.getByText("A câmera de Ana Lima está desligada")).toBeInTheDocument();
    expect(screen.getByText("Sua câmera está desligada")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ativar câmera" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("toggles media and suppresses repeated end clicks", () => {
    const calls = controller("active", currentUserId);
    const mediaController = media();
    renderPanel(calls, mediaController);

    fireEvent.click(screen.getByRole("button", { name: "Desativar microfone" }));
    fireEvent.click(screen.getByRole("button", { name: "Desativar câmera" }));
    const endButton = screen.getByRole("button", { name: "Encerrar chamada" });
    fireEvent.click(endButton);
    fireEvent.click(endButton);

    expect(mediaController.toggleMicrophone).toHaveBeenCalledOnce();
    expect(mediaController.toggleCamera).toHaveBeenCalledOnce();
    expect(calls.end).toHaveBeenCalledOnce();
    expect(endButton).toBeDisabled();
  });

  it("re-enables ending after the server rejects the action", () => {
    const calls = controller("active", currentUserId);
    const view = renderPanel(calls, media());
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));

    calls.error = "A chamada já mudou de estado.";
    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantId={otherUserId}
        participantName="Ana Lima"
        media={media()}
      />,
    );

    expect(screen.getByRole("button", { name: "Encerrar chamada" })).toBeEnabled();
  });

  it.each([
    ["connecting", "Conectando mídia…"],
    ["reconnecting", "Reconectando mídia…"],
    [
      "permission-denied",
      "Acesso à câmera ou ao microfone foi bloqueado. Ajuste a permissão no navegador e tente novamente.",
    ],
  ] as const)("announces the %s media state", (status, label) => {
    const calls = controller("active", currentUserId);
    renderPanel(calls, media({ status, error: status === "permission-denied" ? label : null }));

    expect(screen.getByRole(status === "permission-denied" ? "alert" : "status")).toHaveTextContent(
      label,
    );
  });

  it("ends permission retry from the controller promise after token failure", async () => {
    const user = userEvent.setup();
    const calls = controller("active", currentUserId);
    const retryOperation = deferredValue<void>();
    calls.retryMedia = vi.fn(() => retryOperation.promise);
    const denied = media({
      status: "permission-denied",
      error: "Permissão de câmera negada pelo navegador.",
    });
    const view = renderPanel(calls, denied);
    const retry = screen.getByRole("button", { name: "Tentar mídia novamente" });

    retry.focus();
    await user.keyboard("{Enter}");
    await user.click(retry);

    expect(calls.retryMedia).toHaveBeenCalledOnce();
    expect(calls.end).not.toHaveBeenCalled();
    expect(retry).toBeDisabled();

    calls.error = "Não foi possível preparar a mídia da chamada. Tente novamente.";
    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantId={otherUserId}
        participantName="Ana Lima"
        media={media({ status: "idle", error: null })}
      />,
    );
    expect(screen.getByRole("button", { name: "Tentar mídia novamente" })).toBeDisabled();

    await act(async () => retryOperation.resolve());
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Tentar mídia novamente" })).toBeEnabled(),
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Não foi possível preparar a mídia da chamada. Tente novamente.",
    );

    await user.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    expect(calls.retryMedia).toHaveBeenCalledTimes(2);
  });

  it("restores media controls when permission retry succeeds", async () => {
    const user = userEvent.setup();
    const calls = controller("active", currentUserId);
    const view = renderPanel(
      calls,
      media({
        status: "permission-denied",
        error: "Permissão de câmera negada pelo navegador.",
      }),
    );

    await user.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantId={otherUserId}
        participantName="Ana Lima"
        media={media({ status: "connecting", microphoneEnabled: false, cameraEnabled: false })}
      />,
    );
    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantId={otherUserId}
        participantName="Ana Lima"
        media={media()}
      />,
    );

    await waitFor(() =>
      expect(
        screen.queryByText(/Acesso à câmera ou ao microfone foi bloqueado/),
      ).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "Desativar microfone" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Desativar câmera" })).toBeEnabled();
    expect(calls.end).not.toHaveBeenCalled();
  });

  it("shows action failures and offers the existing media retry", () => {
    const calls = controller("active", currentUserId);
    renderPanel(calls, media({ status: "error", error: "Falha ao alterar o microfone." }));

    expect(screen.getByRole("alert")).toHaveTextContent("Falha ao alterar o microfone.");
    fireEvent.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    expect(calls.retryMedia).toHaveBeenCalledOnce();
  });

  it("keeps incoming and outgoing ringing actions keyboard accessible", async () => {
    const user = userEvent.setup();
    const incoming = controller("ringing");
    const view = renderPanel(incoming);

    expect(screen.getByText("Ana Lima está chamando")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Atender" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(incoming.accept).toHaveBeenCalledOnce();

    const outgoing = controller("ringing", currentUserId);
    view.rerender(
      <CallPanel
        calls={outgoing}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantId={otherUserId}
        participantName="Ana Lima"
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Cancelar chamada" }));
    expect(outgoing.cancel).toHaveBeenCalledOnce();
  });

  it("surfaces a denied accept permission as an accessible alert while the call stays ringing", () => {
    const calls = controller("ringing");
    calls.error =
      "Acesso ao microfone foi negado ou bloqueado. Libere a permissão e tente novamente.";
    renderPanel(calls);

    expect(screen.getByRole("alert")).toHaveTextContent("Acesso ao microfone foi negado");
    expect(screen.getByRole("button", { name: "Atender" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Recusar" })).toBeEnabled();
  });

  it("surfaces a denied outgoing-call permission as an accessible toast with no call created", () => {
    const calls: CallController = {
      call: null,
      pending: false,
      error: "Acesso à câmera e ao microfone foi negado ou bloqueado.",
      mediaReady: false,
      mediaActivationRequired: false,
      start: vi.fn(() => true),
      accept: vi.fn(() => true),
      decline: vi.fn(() => true),
      cancel: vi.fn(() => true),
      end: vi.fn(() => true),
      retryMedia: vi.fn(async () => undefined),
      activateMedia: vi.fn(async () => undefined),
      clearTerminal: vi.fn(),
    };

    renderPanel(calls);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Acesso à câmera e ao microfone foi negado ou bloqueado.",
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("waits for identity before showing incoming call actions", () => {
    const calls = controller("ringing");
    const view = render(
      <CallPanel
        calls={calls}
        currentUserId=""
        identityStatus="loading"
        retryIdentity={retryIdentity}
        participantName="Ana Lima"
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Preparando chamada…");
    expect(screen.queryByRole("button", { name: "Atender" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Recusar" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();
    expect(calls.accept).not.toHaveBeenCalled();
    expect(calls.decline).not.toHaveBeenCalled();
    expect(calls.cancel).not.toHaveBeenCalled();

    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantName="Ana Lima"
      />,
    );

    expect(screen.getByRole("button", { name: "Atender" })).toHaveFocus();
    expect(screen.getByRole("button", { name: "Recusar" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();
  });

  it("keeps identity recovery accessible and deduplicated inside the dialog", async () => {
    const user = userEvent.setup();
    const calls = controller("ringing");
    const retryOperation = deferredValue<void>();
    const retryIdentity = vi.fn(() => retryOperation.promise);
    const view = render(
      <CallPanel
        calls={calls}
        currentUserId=""
        identityStatus="error"
        retryIdentity={retryIdentity}
        participantName="Ana Lima"
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível preparar a chamada");
    const retry = screen.getByRole("button", { name: "Tentar novamente" });
    expect(retry).toHaveFocus();
    await user.keyboard("{Enter}");
    await user.click(retry);

    expect(retryIdentity).toHaveBeenCalledOnce();
    expect(retry).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Atender" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Recusar" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();

    await act(async () => retryOperation.resolve());
    expect(retry).toBeEnabled();

    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantName="Ana Lima"
      />,
    );
    expect(screen.getByRole("button", { name: "Atender" })).toHaveFocus();
    expect(screen.getByRole("button", { name: "Recusar" })).toBeInTheDocument();
  });

  it("keeps an active call neutral and recoverable until identity is ready", () => {
    const calls = controller("active");
    render(
      <CallPanel
        calls={calls}
        currentUserId=""
        identityStatus="error"
        retryIdentity={retryIdentity}
        participantName="Participante"
        media={media()}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível preparar a chamada");
    expect(screen.getByRole("button", { name: "Tentar novamente" })).toHaveFocus();
    expect(screen.queryByRole("button", { name: "Desativar microfone" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Encerrar chamada" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Desativar câmera" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("call-remote-media")).not.toBeInTheDocument();
    expect(screen.queryByTestId("call-local-media")).not.toBeInTheDocument();
  });

  it("waits for identity before showing outgoing call actions", () => {
    const calls = controller("ringing", currentUserId);
    const view = render(
      <CallPanel
        calls={calls}
        currentUserId=""
        identityStatus="loading"
        retryIdentity={retryIdentity}
        participantName="Ana Lima"
      />,
    );

    expect(screen.queryByRole("button", { name: "Atender" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Recusar" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();

    view.rerender(
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantName="Ana Lima"
      />,
    );

    expect(screen.getByRole("button", { name: "Cancelar chamada" })).toHaveFocus();
    expect(screen.queryByRole("button", { name: "Atender" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Recusar" })).not.toBeInTheDocument();
  });

  it("shows terminal status and clears it", () => {
    const calls = controller("timed_out");
    renderPanel(calls);

    expect(screen.getByText("Chamada não atendida")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Fechar" }));
    expect(calls.clearTerminal).toHaveBeenCalledOnce();
  });

  it("cleans the terminal-state timer on unmount", () => {
    vi.useFakeTimers();
    const calls = controller("ended");
    const view = renderPanel(calls);

    view.unmount();
    vi.advanceTimersByTime(4_000);

    expect(calls.clearTerminal).not.toHaveBeenCalled();
    vi.useRealTimers();
  });
});
