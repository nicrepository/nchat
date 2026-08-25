import { act, fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";

import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import DedicatedCallStage from "./DedicatedCallStage";
import FloatingCallWindow from "./FloatingCallWindow";
import GlobalCallIndicator from "./GlobalCallIndicator";
import IncomingCallPopup from "./IncomingCallPopup";

const presentationCSS = readFileSync(resolve("src/calls/CallPresentation.css"), "utf8");

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

// Default media and local-presentation props for FloatingCallWindow tests
// that are not specifically exercising the local identity fallback.
const videoPresent = {
  hasRemoteVideo: true,
  remoteSeed: "peer-seed",
  hasLocalVideo: true,
  localSeed: "local-user",
  localName: "Você",
  localInitials: "V",
};

describe("IncomingCallPopup", () => {
  it("shows a non-modal video call without stealing focus", () => {
    render(
      <IncomingCallPopup
        name="Equipe Produto"
        avatarUrl="/avatar.png"
        callType="video"
        onAccept={noop}
        onReject={noop}
      />,
    );

    const popup = screen.getByRole("dialog", { name: "Chamada recebida" });
    expect(popup).toHaveAttribute("aria-modal", "false");
    expect(screen.getByText("Chamada de vídeo")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Atender/ })).not.toHaveFocus();
  });

  it("covers loading and recoverable identity failure without duplicate retries", async () => {
    const retry = vi.fn(() => Promise.resolve());
    const view = render(
      <IncomingCallPopup
        name="Ana"
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
        callType="audio"
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
    expect(screen.getByRole("button", { name: "Tentar novamente" })).not.toBeDisabled();
  });

  it("accepts an audio call and safely ignores an unavailable identity retry", () => {
    const view = render(
      <IncomingCallPopup name="Ana" callType="audio" onAccept={noop} onReject={noop} />,
    );
    fireEvent.click(screen.getByRole("button", { name: /Atender/ }));
    expect(noop).toHaveBeenCalled();

    view.rerender(
      <IncomingCallPopup
        name="Ana"
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
        activeSpeaker={{ kind: "direct-remote", name: "Ana" }}
        screenShareLabel="Você está compartilhando a tela"
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Reconectando");
    expect(screen.getByText("2 participantes")).toBeInTheDocument();
    expect(screen.getByText("Ana está falando")).toBeInTheDocument();
    expect(screen.getByText("Você está compartilhando a tela")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ativar microfone" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Desativar câmera" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Compartilhar tela" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Expandir em nova aba" })).toBeInTheDocument();
  });

  it("shows the remote presenter's display name when someone else is sharing (issue #611)", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        screenShareLabel="Ana está compartilhando a tela"
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    expect(screen.getByText("Ana está compartilhando a tela")).toBeInTheDocument();
  });

  it("shows no share indicator when screenShareLabel is absent", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    expect(document.querySelector(".floating-call__share")).toBeNull();
  });

  it("drags only from its handle and has no preset position selector", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
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

    expect(screen.queryByRole("combobox", { name: "Posição da chamada" })).not.toBeInTheDocument();
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
        {...videoPresent}
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
        {...videoPresent}
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
        {...videoPresent}
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
        {...videoPresent}
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

  it("reclamps a bottom-anchored window when its own height grows after mount (regression: denied activation could push the retry button off-screen)", () => {
    class ResizeObserverStub {
      static instances: ResizeObserverStub[] = [];
      callback: ResizeObserverCallback;
      constructor(callback: ResizeObserverCallback) {
        this.callback = callback;
        ResizeObserverStub.instances.push(this);
      }
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();
    }
    vi.stubGlobal("ResizeObserver", ResizeObserverStub);
    localStorage.setItem("nchat.call.floating-corner.v1", "bottom-right");
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1024 });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 768 });

    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={1}
        controls={controls}
        onExpand={noop}
        activationRequired
        onActivate={noop}
        {...videoPresent}
      />,
    );

    const windowElement = screen.getByTestId("floating-call-window");
    const observer = ResizeObserverStub.instances.at(-1)!;
    expect(observer.observe).toHaveBeenCalledWith(windowElement);

    Object.defineProperty(windowElement, "getBoundingClientRect", {
      configurable: true,
      value: () => ({ width: 320, height: 240, left: 688, top: 512 }),
    });
    act(() => observer.callback([], observer as unknown as ResizeObserver));
    expect(windowElement.style.transform).toBe("translate3d(688px, 512px, 0)");

    // A denied getUserMedia prompt adds a recovery banner above the
    // activation button without moving the window: simulate the resulting
    // layout growth that a ResizeObserver would report.
    Object.defineProperty(windowElement, "getBoundingClientRect", {
      configurable: true,
      value: () => ({ width: 320, height: 340, left: 688, top: 512 }),
    });
    act(() => observer.callback([], observer as unknown as ResizeObserver));

    // Bottom-right anchored: y must shrink so the taller window's bottom
    // edge stays inside the viewport margin instead of pushing the trailing
    // activation button below the visible viewport.
    expect(windowElement.style.transform).toBe("translate3d(688px, 412px, 0)");

    localStorage.removeItem("nchat.call.floating-corner.v1");
    vi.unstubAllGlobals();
  });

  it("shows a deterministic initials fallback when there is no usable remote video", () => {
    render(
      <FloatingCallWindow
        title="Ana Beatriz"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
        hasRemoteVideo={false}
        remoteSeed="peer-1"
      />,
    );
    const avatar = document.querySelector(".floating-call__avatar");
    expect(avatar).not.toBeNull();
    expect(avatar).toHaveTextContent(initialsFrom("Ana Beatriz"));
    expect(avatar).toHaveClass(`call-avatar--${avatarColorFor("peer-1")}`);
    expect(avatar).toHaveAttribute("aria-hidden", "true");
  });

  it("hides the remote avatar fallback once remote video is usable", () => {
    render(
      <FloatingCallWindow
        title="Ana Beatriz"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
        hasRemoteVideo
        remoteSeed="peer-1"
      />,
    );
    expect(document.querySelector(".floating-call__avatar")).toBeNull();
  });

  it("shows a deterministic initials fallback in the local preview when there is no usable local video", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
        hasLocalVideo={false}
        localSeed="current-user"
        localName="Você"
        localInitials="V"
      />,
    );
    const avatar = document.querySelector(".floating-call__local-avatar");
    expect(avatar).not.toBeNull();
    expect(avatar).toHaveTextContent("V");
    expect(avatar).toHaveClass(`call-avatar--${avatarColorFor("current-user")}`);
    // Unlike a remote/resource fallback (name shown adjacent), the floating
    // local preview has no other visible name anywhere — the avatar itself
    // must carry the accessible identity (issue #612).
    expect(avatar).toHaveAttribute("aria-label", "Você");
  });

  it("hides the local avatar fallback in the floating preview once local video is usable", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
        hasLocalVideo
        localSeed="current-user"
        localName="Você"
        localInitials="V"
      />,
    );
    expect(document.querySelector(".floating-call__local-avatar")).toBeNull();
  });

  it("uses the passed-in localInitials verbatim, never derived from the (você)-suffixed localName (issue #612 blocker)", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={2}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
        hasLocalVideo={false}
        localSeed="current-user"
        localName="Ana (você)"
        localInitials="A"
      />,
    );
    const avatar = document.querySelector(".floating-call__local-avatar")!;
    expect(avatar.textContent).toBe("A");
  });

  it("keeps the participant count in its own stable slot across active-speaker mount/unmount", () => {
    const view = render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={3}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    const countBefore = document.querySelector(".floating-call__count");
    expect(countBefore).not.toBeNull();

    view.rerender(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={3}
        activeSpeaker={{ kind: "direct-remote", name: "Ana" }}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    const countDuring = document.querySelector(".floating-call__count");
    expect(countDuring).toBe(countBefore);
    expect(document.querySelector(".floating-call__speaker")).toHaveTextContent("Ana está falando");

    view.rerender(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={3}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    expect(document.querySelector(".floating-call__count")).toBe(countBefore);
    expect(document.querySelector(".floating-call__speaker")).toBeNull();
  });

  it("keeps the participant count in its own stable slot across screen-share mount/unmount", () => {
    const view = render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={3}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    const countBefore = document.querySelector(".floating-call__count");

    view.rerender(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={3}
        screenShareLabel="Você está compartilhando a tela"
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    expect(document.querySelector(".floating-call__count")).toBe(countBefore);
    expect(document.querySelector(".floating-call__share")).toHaveTextContent(
      "Você está compartilhando a tela",
    );

    view.rerender(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={3}
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    expect(document.querySelector(".floating-call__count")).toBe(countBefore);
    expect(document.querySelector(".floating-call__share")).toBeNull();
  });

  it("renders active speaker and screen share as independent, non-nested slots that coexist", () => {
    render(
      <FloatingCallWindow
        title="Ana"
        status="connected"
        participantCount={3}
        activeSpeaker={{ kind: "direct-remote", name: "Ana" }}
        screenShareLabel="Você está compartilhando a tela"
        controls={controls}
        onExpand={noop}
        {...videoPresent}
      />,
    );
    const stage = document.querySelector(".floating-call__stage")!;
    const count = stage.querySelector(":scope > .floating-call__count");
    const speaker = stage.querySelector(":scope > .floating-call__speaker");
    const share = stage.querySelector(":scope > .floating-call__share");
    expect(count).not.toBeNull();
    expect(speaker).not.toBeNull();
    expect(share).not.toBeNull();
    // Independent siblings, never one nested inside another — that coupling
    // is exactly what made mounting one shift the others.
    expect(speaker?.contains(share!)).toBe(false);
    expect(share?.contains(speaker!)).toBe(false);
    expect(count?.contains(speaker!)).toBe(false);
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
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(screen.getByRole("main", { name: "Chamada Produto" })).toBeInTheDocument();
    expect(screen.getByText("Ana")).toBeInTheDocument();
    expect(screen.getByText("Tela de Ana")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Minimizar para janela flutuante" }),
    ).toBeInTheDocument();
  });

  it("renders the local screen share as the primary tile, labeled 'Sua tela' (issue #611)", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={1}
        participants={[]}
        controls={controls}
        onMinimize={noop}
        localScreenShareActive
        bindLocalScreenShare={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(screen.getByText("Sua tela")).toBeInTheDocument();
    expect(document.querySelectorAll(".dedicated-call__tile--screen")).toHaveLength(1);
  });

  it("local screen share wins the primary tile over a simultaneously active remote share", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={2}
        participants={[{ identity: "remote", displayName: "Ana", hasVideo: false }]}
        controls={controls}
        onMinimize={noop}
        localScreenShareActive
        bindLocalScreenShare={noop}
        screenShareName="Ana"
        bindScreenShare={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    // Never two simultaneous screen tiles — only the local one.
    expect(document.querySelectorAll(".dedicated-call__tile--screen")).toHaveLength(1);
    expect(screen.getByText("Sua tela")).toBeInTheDocument();
    expect(screen.queryByText("Tela de Ana")).not.toBeInTheDocument();
  });

  it("reveals the remote share as primary immediately once local sharing ends", () => {
    const view = render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={2}
        participants={[{ identity: "remote", displayName: "Ana", hasVideo: false }]}
        controls={controls}
        onMinimize={noop}
        localScreenShareActive
        bindLocalScreenShare={noop}
        screenShareName="Ana"
        bindScreenShare={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(screen.getByText("Sua tela")).toBeInTheDocument();

    view.rerender(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={2}
        participants={[{ identity: "remote", displayName: "Ana", hasVideo: false }]}
        controls={controls}
        onMinimize={noop}
        localScreenShareActive={false}
        screenShareName="Ana"
        bindScreenShare={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(screen.queryByText("Sua tela")).not.toBeInTheDocument();
    expect(screen.getByText("Tela de Ana")).toBeInTheDocument();
    expect(document.querySelectorAll(".dedicated-call__tile--screen")).toHaveLength(1);
  });

  it("shows no screen tile and normal participant markup when no share is active", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={2}
        participants={[{ identity: "remote", displayName: "Ana", hasVideo: false }]}
        controls={controls}
        onMinimize={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(document.querySelector(".dedicated-call__tile--screen")).toBeNull();
    expect(screen.getByText("Ana")).toBeInTheDocument();
    expect(screen.getByText("Você")).toBeInTheDocument();
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
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
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
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(screen.getByText("Tela de Participante")).toBeInTheDocument();
  });

  it("shows deterministic initials and color for a camera-off remote participant", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={2}
        participants={[{ identity: "user-1", displayName: "Ana Beatriz", hasVideo: false }]}
        controls={controls}
        onMinimize={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    const avatar = document.querySelector(".dedicated-call__avatar");
    expect(avatar).not.toBeNull();
    expect(avatar).toHaveTextContent(initialsFrom("Ana Beatriz"));
    expect(avatar).toHaveClass(`call-avatar--${avatarColorFor("user-1")}`);
    expect(avatar).toHaveAttribute("aria-hidden", "true");
    // The visible name label stays available to the accessibility tree.
    expect(screen.getByText("Ana Beatriz")).toBeInTheDocument();
  });

  it("derives a single-initial fallback for a simple remote name", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={2}
        participants={[{ identity: "user-2", displayName: "Zoe", hasVideo: false }]}
        controls={controls}
        onMinimize={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(document.querySelector(".dedicated-call__avatar")).toHaveTextContent("Z");
  });

  it("shows a local avatar fallback when there is no local video", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={1}
        participants={[]}
        controls={controls}
        onMinimize={noop}
        hasLocalVideo={false}
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    const avatars = document.querySelectorAll(".dedicated-call__avatar");
    expect(avatars).toHaveLength(1);
    expect(avatars[0]).toHaveClass(`call-avatar--${avatarColorFor("current-user")}`);
    expect(screen.getByText("Você")).toBeInTheDocument();
  });

  it("hides the local avatar fallback once local video is active", () => {
    render(
      <DedicatedCallStage
        title="Produto"
        status="connected"
        participantCount={1}
        participants={[]}
        controls={controls}
        onMinimize={noop}
        hasLocalVideo
        localSeed="current-user"
        localDisplayName="Você"
        localInitials="V"
      />,
    );
    expect(document.querySelector(".dedicated-call__avatar")).toBeNull();
  });
});

describe("active speaker CSS", () => {
  it("keeps the visual state but disables its transition for reduced motion", () => {
    expect(presentationCSS).toMatch(/\.call-speaker-surface--active::after/);
    expect(presentationCSS).toMatch(
      /@media \(prefers-reduced-motion: reduce\)[\s\S]*\.call-speaker-surface::after[\s\S]*transition: none/,
    );
  });
});
