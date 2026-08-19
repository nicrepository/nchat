import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import DedicatedCallStage from "./DedicatedCallStage";
import FloatingCallWindow from "./FloatingCallWindow";
import GlobalCallIndicator from "./GlobalCallIndicator";
import IncomingCallPopup from "./IncomingCallPopup";

const noop = vi.fn();

const controls = {
  microphoneEnabled: false,
  cameraEnabled: true,
  screenShareEnabled: false,
  pendingControl: null,
  onMicrophone: noop,
  onCamera: noop,
  onScreenShare: noop,
  onEnd: noop,
};

describe("IncomingCallPopup", () => {
  it("shows a non-modal channel video call without stealing focus", () => {
    render(
      <IncomingCallPopup
        name="Equipe Produto"
        avatarUrl="/avatar.png"
        targetKind="channel"
        callType="video"
        participantCount={3}
        onAccept={noop}
        onReject={noop}
      />,
    );

    const popup = screen.getByRole("dialog", { name: "Chamada recebida" });
    expect(popup).toHaveAttribute("aria-modal", "false");
    expect(screen.getByText("Canal · chamada de vídeo")).toBeInTheDocument();
    expect(screen.getByText("3 participantes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Atender com câmera" })).not.toHaveFocus();
  });

  it("covers loading and recoverable identity failure without duplicate retries", async () => {
    const retry = vi.fn(() => Promise.resolve());
    const view = render(
      <IncomingCallPopup
        name="Ana"
        targetKind="user"
        callType="audio"
        onAccept={noop}
        onReject={noop}
        identityStatus="loading"
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Preparando");
    view.rerender(
      <IncomingCallPopup
        name="Ana"
        targetKind="dm"
        callType="audio"
        participantCount={1}
        onAccept={noop}
        onReject={noop}
        identityStatus="error"
        onRetryIdentity={retry}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(retry).toHaveBeenCalledOnce();
    await act(async () => undefined);
    expect(screen.getByText("1 participante")).toBeInTheDocument();
  });

  it("accepts an audio call and safely ignores an unavailable identity retry", () => {
    const view = render(
      <IncomingCallPopup
        name="Ana"
        targetKind="user"
        callType="audio"
        onAccept={noop}
        onReject={noop}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Atender" }));
    expect(noop).toHaveBeenCalled();

    view.rerender(
      <IncomingCallPopup
        name="Ana"
        targetKind="user"
        callType="audio"
        onAccept={noop}
        onReject={noop}
        identityStatus="error"
      />,
    );
    expect(() =>
      fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" })),
    ).not.toThrow();
  });
});

describe("FloatingCallWindow", () => {
  it("exposes real state and every call control", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="reconnecting"
        participantCount={2}
        activeSpeakerName="Ana"
        screenShareActive
        controls={controls}
        onExpand={noop}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Reconectando");
    expect(screen.getByText("2 participantes")).toBeInTheDocument();
    expect(screen.getByText("Ana está falando")).toBeInTheDocument();
    expect(screen.getByText("Compartilhando tela")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ativar microfone" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Desativar câmera" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Compartilhar tela" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Expandir em nova aba" })).toBeInTheDocument();
  });

  it("drags only from its handle and has no preset position selector", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
      />,
    );
    const windowElement = screen.getByTestId("floating-call-window");
    const handle = screen.getByTestId("floating-call-handle");
    Object.defineProperty(windowElement, "getBoundingClientRect", {
      value: () => ({ width: 320, height: 240, left: 0, top: 0 }),
    });
    Object.defineProperty(handle, "setPointerCapture", { value: vi.fn() });
    Object.defineProperty(handle, "releasePointerCapture", { value: vi.fn() });
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1024 });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 768 });

    fireEvent.pointerDown(handle, { pointerId: 7, clientX: 700, clientY: 500 });
    fireEvent.pointerMove(handle, { pointerId: 7, clientX: 720, clientY: 520 });
    fireEvent.pointerUp(handle, { pointerId: 7, clientX: 720, clientY: 520 });
    expect(windowElement.style.transform).toContain("translate3d");

    expect(
      screen.queryByRole("combobox", { name: "Posição da chamada" }),
    ).not.toBeInTheDocument();
  });

  it("does not start dragging when the expand control receives the pointer gesture", () => {
    const onExpand = vi.fn();

    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={onExpand}
      />,
    );

    const windowElement = screen.getByTestId("floating-call-window");
    const handle = screen.getByTestId("floating-call-handle");
    const expand = screen.getByRole("button", { name: "Expandir em nova aba" });

    Object.defineProperty(windowElement, "getBoundingClientRect", {
      value: () => ({ width: 320, height: 240, left: 0, top: 0 }),
    });

    const capture = vi.fn();
    Object.defineProperty(handle, "setPointerCapture", { value: capture });
    Object.defineProperty(handle, "releasePointerCapture", { value: vi.fn() });

    Object.defineProperty(window, "innerWidth", {
      configurable: true,
      value: 1024,
    });
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: 768,
    });

    const before = windowElement.style.transform;

    fireEvent.pointerDown(expand, {
      button: 0,
      pointerId: 77,
      clientX: 900,
      clientY: 40,
    });

    fireEvent.pointerMove(handle, {
      pointerId: 77,
      clientX: 500,
      clientY: 400,
    });

    fireEvent.pointerUp(handle, {
      pointerId: 77,
      clientX: 500,
      clientY: 400,
    });

    expect(capture).not.toHaveBeenCalled();
    expect(windowElement.style.transform).toBe(before);

    fireEvent.click(expand);
    expect(onExpand).toHaveBeenCalledOnce();
  });

  it("cleans up drag state on pointercancel just like pointerup (achado #6)", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
      />,
    );
    const windowElement = screen.getByTestId("floating-call-window");
    const handle = screen.getByTestId("floating-call-handle");
    Object.defineProperty(windowElement, "getBoundingClientRect", {
      value: () => ({ width: 320, height: 240, left: 0, top: 0 }),
    });
    const releaseCapture = vi.fn();
    Object.defineProperty(handle, "setPointerCapture", { value: vi.fn() });
    Object.defineProperty(handle, "releasePointerCapture", { value: releaseCapture });
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1024 });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 768 });

    fireEvent.pointerDown(handle, { pointerId: 9, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(handle, { pointerId: 9, clientX: 140, clientY: 140 });
    const draggedTransform = windowElement.style.transform;
    fireEvent.pointerCancel(handle, { pointerId: 9, clientX: 140, clientY: 140 });
    expect(releaseCapture).toHaveBeenCalledWith(9);
    // No snap-to-corner side effect from cancel: the transform is untouched.
    expect(windowElement.style.transform).toBe(draggedTransform);

    // A further move with the same (now-stale) pointer id no longer drags.
    fireEvent.pointerMove(handle, { pointerId: 9, clientX: 400, clientY: 400 });
    expect(windowElement.style.transform).toBe(draggedTransform);

    // An unrelated pointer id is ignored, mirroring pointerup's guard.
    fireEvent.pointerDown(handle, { pointerId: 10, clientX: 10, clientY: 10 });
    fireEvent.pointerCancel(handle, { pointerId: 11, clientX: 10, clientY: 10 });
    expect(releaseCapture).toHaveBeenCalledTimes(1);
  });

  it("ignores wrong pointers, handles resize, and exposes recovery controls", () => {
    const retry = vi.fn();
    render(
      <FloatingCallWindow
        title="Ana"
        status="failed"
        participantCount={1}
        controls={{ ...controls, pendingControl: "microphone" }}
        onExpand={noop}
        activationRequired
        onActivate={noop}
        error="Falha"
        onRetry={retry}
      />,
    );
    const handle = screen.getByTestId("floating-call-handle");
    fireEvent.pointerDown(handle, { button: 1, pointerId: 2, clientX: 10, clientY: 10 });
    fireEvent.pointerMove(handle, { pointerId: 3, clientX: 20, clientY: 20 });
    fireEvent.pointerUp(handle, { pointerId: 3, clientX: 20, clientY: 20 });
    fireEvent(window, new Event("resize"));
    expect(screen.getByRole("button", { name: "Ativar microfone" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Permitir câmera e microfone" }));
    fireEvent.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("uses mobile-safe positioning and renders every recovery variant", () => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: true,
        media: "(max-width: 720px)",
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    );
    render(
      <FloatingCallWindow
        title="Ana"
        status="connecting"
        participantCount={1}
        controls={{ ...controls, screenShareEnabled: true }}
        onExpand={noop}
        identityStatus="loading"
        error="Falha sem nova tentativa"
      />,
    );
    const handle = screen.getByTestId("floating-call-handle");
    fireEvent.pointerDown(handle, { button: 0, pointerId: 1, clientX: 20, clientY: 20 });
    expect(
      screen.getByRole("button", { name: "Parar compartilhamento de tela" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Preparando chamada…")).toBeInTheDocument();
    expect(screen.getByText("Falha sem nova tentativa")).toBeInTheDocument();
    vi.unstubAllGlobals();
  });
});

describe("global and dedicated presentation", () => {
  it("shows when media belongs to another tab", () => {
    render(<GlobalCallIndicator title="Ana" participantCount={2} onReturn={noop} />);
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Trazer chamada para esta aba" }),
    ).toBeInTheDocument();
  });

  it("renders a dedicated responsive stage with participant media", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={2}
        participants={[{ identity: "remote", displayName: "Ana", hasVideo: false }]}
        controls={controls}
        onMinimize={noop}
        screenShareName="Ana"
        bindScreenShare={noop}
      />,
    );
    expect(screen.getByRole("main", { name: "Chamada Produto" })).toBeInTheDocument();
    expect(screen.getByText("Ana")).toBeInTheDocument();
    expect(screen.getByText("Tela de Ana")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Minimizar para janela flutuante" }),
    ).toBeInTheDocument();
  });

  it("renders reconnecting video participants without avatar fallback", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="reconnecting"
        participantCount={1}
        participants={[{ identity: "remote", displayName: "Ana", hasVideo: true }]}
        controls={controls}
        onMinimize={noop}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Reconectando");
    expect(document.querySelector(".dedicated-call__avatar")).toBeNull();
  });

  it("labels an anonymous screen share with the participant fallback", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connecting"
        participantCount={1}
        participants={[]}
        controls={controls}
        onMinimize={noop}
        bindScreenShare={noop}
      />,
    );
    expect(screen.getByText("Tela de Participante")).toBeInTheDocument();
  });
});
