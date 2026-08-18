import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import CallPanel from "./CallPanel";
import type { CallMediaController, ParticipantMedia } from "./useCallMedia";
import type { CallController } from "./useCallSignaling";
import type { ResourceCallController, ResourceCallTarget } from "./useResourceCallSession";

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
    participants: [],
    bindRemoteAudio: vi.fn(),
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

  it("disables only the microphone control while a microphone operation is pending", () => {
    const calls = controller("active", currentUserId);
    renderPanel(calls, media({ pendingControl: "microphone" }));

    expect(screen.getByRole("button", { name: "Desativar microfone" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Desativar câmera" })).toBeEnabled();
  });

  it("does not disable the microphone control while only the camera operation is pending", () => {
    const calls = controller("active", currentUserId);
    renderPanel(calls, media({ pendingControl: "camera" }));

    expect(screen.getByRole("button", { name: "Desativar microfone" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Desativar câmera" })).toBeDisabled();
  });

  it("re-enables the microphone control once a recoverable error clears pendingControl", () => {
    const calls = controller("active", currentUserId);
    renderPanel(
      calls,
      media({
        pendingControl: null,
        error: "Não foi possível acessar ou alterar o microfone.",
        microphoneEnabled: true,
      }),
    );

    const micButton = screen.getByRole("button", { name: "Desativar microfone" });
    expect(micButton).toBeEnabled();
    expect(micButton).toHaveAttribute("aria-pressed", "true");
  });

  it("keeps the microphone label bound only to the local microphoneEnabled prop, distinct from audio activation", () => {
    const calls = controller("active", currentUserId);
    renderPanel(calls, media({ microphoneEnabled: false, audioActivationRequired: true }));

    expect(screen.getByRole("button", { name: "Ativar microfone" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    expect(screen.getByRole("button", { name: "Ativar áudio da chamada" })).toBeInTheDocument();
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

  it("keeps Recusar enabled while accept's own permission preflight is pending (RF-23)", async () => {
    const user = userEvent.setup();
    const calls = controller("ringing");
    calls.pending = true;
    renderPanel(calls);

    expect(screen.getByRole("button", { name: "Atender" })).toBeDisabled();
    const decline = screen.getByRole("button", { name: "Recusar" });
    expect(decline).toBeEnabled();

    await user.click(decline);
    expect(calls.decline).toHaveBeenCalledOnce();
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

// ── RF-24: resource room (channel/group) ────────────────────────────────────

const idleCalls: CallController = {
  call: null,
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

const channelTarget: ResourceCallTarget = {
  kind: "channel",
  id: "00000000-0000-4000-8000-000000000701",
  name: "geral",
};

function resourceController(
  overrides: Partial<ResourceCallController> = {},
): ResourceCallController {
  return {
    active: channelTarget,
    status: "active",
    error: null,
    errorOperation: null,
    join: vi.fn(async () => undefined),
    leave: vi.fn(async () => undefined),
    ...overrides,
  };
}

function participant(overrides: Partial<ParticipantMedia> = {}): ParticipantMedia {
  return {
    identity: "00000000-0000-4000-8000-000000000801",
    displayName: "Pedro Almeida",
    hasVideo: false,
    hasAudio: true,
    bindVideo: vi.fn(),
    ...overrides,
  };
}

function renderResourcePanel(
  resource: ResourceCallController,
  mediaController?: ReturnType<typeof media>,
  calls: CallController = idleCalls,
) {
  return render(
    <CallPanel
      calls={calls}
      currentUserId={currentUserId}
      identityStatus="ready"
      retryIdentity={retryIdentity}
      participantName="Ana Lima"
      media={mediaController}
      resource={resource}
    />,
  );
}

describe("CallPanel resource room (RF-24)", () => {
  it("renders the channel name and participant count, local tile included", () => {
    renderResourcePanel(
      resourceController(),
      media({ participants: [participant(), participant({ identity: "identity-b" })] }),
    );

    expect(screen.getByText("geral")).toBeInTheDocument();
    expect(screen.getByText("Chamada · 3 participantes")).toBeInTheDocument();
    expect(screen.getByText("Você")).toBeInTheDocument();
  });

  it("renders a tile per participant with no fixed limit", () => {
    const many = Array.from({ length: 15 }, (_, i) => participant({ identity: `identity-${i}` }));
    renderResourcePanel(resourceController(), media({ participants: many }));

    expect(screen.getByText("Chamada · 16 participantes")).toBeInTheDocument();
    for (const p of many) {
      expect(screen.getByTestId(`participant-tile-${p.identity}`)).toBeInTheDocument();
    }
  });

  it("shows the avatar fallback for a participant without video and a tile for one with video", () => {
    renderResourcePanel(
      resourceController(),
      media({
        participants: [
          participant({ identity: "no-video", hasVideo: false }),
          participant({ identity: "has-video", hasVideo: true }),
        ],
      }),
    );

    const withoutVideo = screen.getByTestId("participant-tile-no-video");
    // Fallback avatar only renders for the participant without video.
    expect(withoutVideo.querySelector(".call-panel__fallback--tile")).not.toBeNull();
    const withVideo = screen.getByTestId("participant-tile-has-video");
    expect(withVideo.querySelector(".call-panel__fallback--tile")).toBeNull();
  });

  it("shows a connecting status while joining", () => {
    renderResourcePanel(resourceController({ status: "connecting" }), media());
    expect(screen.getByText("Entrando na chamada…")).toBeInTheDocument();
  });

  it("shows the join error with a retry that calls join() again", async () => {
    const resource = resourceController({
      status: "error",
      error: "Não foi possível entrar.",
      errorOperation: "join",
    });
    renderResourcePanel(resource, media());

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível entrar.");
    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(resource.join).toHaveBeenCalledExactlyOnceWith(channelTarget);
    expect(resource.leave).not.toHaveBeenCalled();
  });

  it("shows the leave error with a retry that calls leave() again, never join()", async () => {
    const resource = resourceController({
      status: "error",
      error: "Não foi possível sair da chamada. Tente novamente.",
      errorOperation: "leave",
    });
    renderResourcePanel(resource, media());

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Não foi possível sair da chamada. Tente novamente.",
    );
    await userEvent.click(screen.getByRole("button", { name: "Tentar sair novamente" }));
    expect(resource.leave).toHaveBeenCalledOnce();
    expect(resource.join).not.toHaveBeenCalled();
  });

  it("leave() disconnects only locally: no call.end, no signaling call involved", async () => {
    const resource = resourceController();
    renderResourcePanel(resource, media());

    await userEvent.click(screen.getByRole("button", { name: "Sair da chamada" }));

    expect(resource.leave).toHaveBeenCalledOnce();
    expect(idleCalls.end).not.toHaveBeenCalled();
  });

  it("always offers the camera control, even before it is turned on (issue #540 follow-up)", () => {
    renderResourcePanel(
      resourceController(),
      media({ cameraEnabled: false, hasLocalVideo: false }),
    );

    expect(screen.getByRole("button", { name: "Ativar câmera" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /microfone/i })).toBeInTheDocument();
  });

  it("shows each remote participant's real display name on their tile, never a generic label", () => {
    renderResourcePanel(
      resourceController(),
      media({
        participants: [
          participant({
            identity: "identity-pedro",
            displayName: "Pedro Almeida",
            hasVideo: false,
          }),
          participant({ identity: "identity-maria", displayName: "Maria Silva", hasVideo: true }),
        ],
      }),
    );

    expect(screen.getByTestId("participant-tile-identity-pedro")).toHaveTextContent(
      "Pedro Almeida",
    );
    expect(screen.getByTestId("participant-tile-identity-maria")).toHaveTextContent("Maria Silva");
    // Avatar fallback initials come from the real name, not from "Participante".
    expect(screen.getByText("PA")).toBeInTheDocument();
  });

  it("falls back to a generic label when a participant has no resolved display name", () => {
    renderResourcePanel(
      resourceController(),
      media({ participants: [participant({ displayName: "Participante" })] }),
    );

    expect(screen.getByText("Participante")).toBeInTheDocument();
  });

  it("toggles the local microphone from the resource room controls", async () => {
    const mediaController = media();
    renderResourcePanel(resourceController(), mediaController);

    await userEvent.click(screen.getByRole("button", { name: "Desativar microfone" }));

    expect(mediaController.toggleMicrophone).toHaveBeenCalledOnce();
  });

  it("toggles the local camera from the resource room controls", async () => {
    const mediaController = media();
    renderResourcePanel(resourceController(), mediaController);

    await userEvent.click(screen.getByRole("button", { name: "Desativar câmera" }));

    expect(mediaController.toggleCamera).toHaveBeenCalledOnce();
  });

  it("prevents the native dialog cancel (Escape) from closing the resource room", () => {
    renderResourcePanel(resourceController(), media());

    const event = new Event("cancel", { cancelable: true });
    screen.getByTestId("resource-call-panel").dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
  });

  it("renders nothing when there is no active resource room", () => {
    const { container } = renderResourcePanel(resourceController({ active: null }), media());
    expect(container).toBeEmptyDOMElement();
  });

  it("swallows a rejected leave() at the click handler instead of leaving it unhandled", async () => {
    const resource = resourceController({
      leave: vi.fn(async () => {
        throw new Error("cleanup failed");
      }),
    });
    renderResourcePanel(resource, media());

    await userEvent.click(screen.getByRole("button", { name: "Sair da chamada" }));

    expect(resource.leave).toHaveBeenCalledOnce();
  });
});

// ── RF-25: transient LiveKit reconnect must stay visible in the grid too ────

describe("CallPanel resource room reconnect indicator (RF-25)", () => {
  it("shows a reconnecting status while media reconnects without leaving the room", () => {
    renderResourcePanel(
      resourceController({ status: "active" }),
      media({ status: "reconnecting" }),
    );

    expect(screen.getByText("Reconectando mídia…")).toBeInTheDocument();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
    // Never an error/retry affordance while merely reconnecting.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("clears the reconnecting status once media reports connected again", () => {
    const { rerender } = renderResourcePanel(
      resourceController({ status: "active" }),
      media({ status: "reconnecting" }),
    );
    expect(screen.getByText("Reconectando mídia…")).toBeInTheDocument();

    rerender(
      <CallPanel
        calls={idleCalls}
        currentUserId={currentUserId}
        identityStatus="ready"
        retryIdentity={retryIdentity}
        participantName="Ana Lima"
        media={media({ status: "connected" })}
        resource={resourceController({ status: "active" })}
      />,
    );

    expect(screen.queryByText("Reconectando mídia…")).not.toBeInTheDocument();
  });

  it("never shows the reconnecting status while still joining for the first time", () => {
    renderResourcePanel(
      resourceController({ status: "connecting" }),
      media({ status: "reconnecting" }),
    );

    expect(screen.getByText("Entrando na chamada…")).toBeInTheDocument();
    expect(screen.queryByText("Reconectando mídia…")).not.toBeInTheDocument();
  });
});

// ── RF-24 code review achado 2: audio activation recovery ──────────────────

describe("CallPanel resource room audio activation (RF-24 code review achado 2)", () => {
  it("offers an explicit action when browser audio activation is required", async () => {
    const mediaController = media({ audioActivationRequired: true });
    renderResourcePanel(resourceController(), mediaController);

    const activate = screen.getByRole("button", { name: "Ativar áudio da chamada" });
    await userEvent.click(activate);

    expect(mediaController.activateAudio).toHaveBeenCalledOnce();
  });

  it("shows the loading state while the adapter is still being fetched", () => {
    renderResourcePanel(resourceController(), media({ mediaLoading: true }));

    expect(screen.getByRole("button", { name: "Carregando áudio da chamada" })).toBeDisabled();
  });

  it("shows the activating state while startAudio() is pending", () => {
    renderResourcePanel(resourceController(), media({ audioStarting: true }));

    expect(screen.getByRole("button", { name: "Ativando áudio da chamada" })).toBeDisabled();
  });

  it("hides the activation action once audio is confirmed playable", () => {
    renderResourcePanel(resourceController(), media({ audioActivationRequired: false }));

    expect(screen.queryByRole("button", { name: /áudio da chamada/ })).not.toBeInTheDocument();
  });

  it("keeps a recoverable error visible without leaving the room", () => {
    renderResourcePanel(
      resourceController(),
      media({ audioActivationRequired: true, error: "O navegador bloqueou o áudio." }),
    );

    expect(screen.getByRole("button", { name: "Ativar áudio da chamada" })).toBeInTheDocument();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("RF-23 audio activation recovery is unaffected by the RF-24 addition", () => {
    const calls = controller("active", currentUserId);
    renderPanel(calls, media({ audioActivationRequired: true }));

    expect(screen.getByRole("button", { name: "Ativar áudio da chamada" })).toBeInTheDocument();
  });
});

// ── RF-24 code review achado 3: RF-23 x RF-24 arbitration ───────────────────

describe("CallPanel RF-23 visible while RF-24 is active (RF-24 code review achado 3)", () => {
  it("shows the incoming direct call and lets the user see who is calling while the resource room stays active", () => {
    const calls = controller("ringing");
    renderResourcePanel(resourceController(), media(), calls);

    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
    expect(screen.getByRole("alert", { name: "Chamada de vídeo de Ana Lima" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Atender" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Recusar" })).toBeInTheDocument();
  });

  it("declining the incoming call keeps the resource room active and calls decline()", async () => {
    const calls = controller("ringing");
    renderResourcePanel(resourceController(), media(), calls);

    await userEvent.click(screen.getByRole("button", { name: "Recusar" }));

    expect(calls.decline).toHaveBeenCalledOnce();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("accepting the incoming call invokes accept()", async () => {
    const calls = controller("ringing");
    renderResourcePanel(resourceController(), media(), calls);

    await userEvent.click(screen.getByRole("button", { name: "Atender" }));

    expect(calls.accept).toHaveBeenCalledOnce();
  });

  it("does not show the incoming banner for an outgoing (non-incoming) ringing call", () => {
    const calls = controller("ringing", currentUserId);
    renderResourcePanel(resourceController(), media(), calls);

    expect(screen.queryByRole("alert", { name: /Chamada de/ })).not.toBeInTheDocument();
  });

  it("does not show the incoming banner once the resource room is not active", () => {
    const calls = controller("ringing");
    renderPanel(calls, media());

    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("RF-24 code review round 2 achado 3: Recusar stays enabled and still calls decline() while Atender's preflight is pending", async () => {
    const calls = { ...controller("ringing"), pending: true };
    renderResourcePanel(resourceController(), media(), calls);

    expect(screen.getByRole("button", { name: "Atender" })).toBeDisabled();
    const recusar = screen.getByRole("button", { name: "Recusar" });
    expect(recusar).toBeEnabled();

    await userEvent.click(recusar);

    expect(calls.decline).toHaveBeenCalledOnce();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });
});

// ── Precedence: an active RF-23 call always outranks an active RF-24 room ───

describe("CallPanel precedence between an active RF-23 call and an active RF-24 room", () => {
  it("shows the RF-23 dialog once the direct call is confirmed active, even while the resource room is still marked active", () => {
    const calls = controller("active", currentUserId);
    renderResourcePanel(resourceController(), media(), calls);

    expect(
      screen.getByRole("dialog", { name: "Chamada de vídeo com Ana Lima" }),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
  });

  it("keeps showing the RF-24 room while the direct call is only ringing, not yet active", () => {
    const calls = controller("ringing");
    renderResourcePanel(resourceController(), media(), calls);

    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: /Chamada de vídeo com/ })).not.toBeInTheDocument();
  });

  it("shows the RF-24 room again once the direct call reaches a terminal state without ever taking over media", () => {
    const calls = controller("ended", currentUserId);
    renderResourcePanel(resourceController(), media(), calls);

    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });
});
