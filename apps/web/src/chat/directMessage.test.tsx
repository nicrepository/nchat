/**
 * The open-DM coordinator, as a coordinator (issue #895).
 *
 * The surfaces are proved where they live — ChatMessageArea.test.tsx mounts the
 * real timeline beside the real sidebar panel. What is proved here is the thing
 * underneath both: one operation per recipient, origins that come and go
 * independently, a single navigation per result, and subscriptions narrow
 * enough that one person becoming pending is not news to a component watching
 * somebody else.
 */

import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mockGetOrCreateDirectDM = vi.hoisted(() => vi.fn());
vi.mock("./chatApi", () => ({
  getOrCreateDirectDM: (userId: string, signal?: AbortSignal) =>
    mockGetOrCreateDirectDM(userId, signal),
}));

import {
  createDirectMessageCoordinator,
  inertDirectMessage,
  useDirectMessagePending,
  type DirectMessageCoordinator,
  type DirectMessageDeps,
} from "./directMessage";
import { ApiRequestError } from "../lib/api";

/** A request this test settles by hand, plus the signal it was given. */
function deferred() {
  let resolve!: (value: { conversationId: string; created: boolean }) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<{ conversationId: string; created: boolean }>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function setup(overrides: Partial<DirectMessageDeps> = {}) {
  const navigate = vi.fn();
  const refreshConversations = vi.fn();
  const deps = { currentUserId: "me", navigate, refreshConversations, ...overrides };
  return {
    coordinator: createDirectMessageCoordinator(deps),
    navigate,
    refreshConversations,
    deps,
  };
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("directMessageCoordinator — one operation per recipient", () => {
  it("sends one request however many origins ask for the same person", () => {
    mockGetOrCreateDirectDM.mockReturnValue(deferred().promise);
    const { coordinator } = setup();

    coordinator.open("ana", "timeline");
    coordinator.open("ana", "sidebar");
    coordinator.open("ana", "timeline");

    expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(1);
    expect(mockGetOrCreateDirectDM).toHaveBeenCalledWith("ana", expect.any(AbortSignal));
    expect(coordinator.isPending("ana")).toBe(true);
  });

  it("keeps different recipients independent", () => {
    mockGetOrCreateDirectDM.mockImplementation(() => deferred().promise);
    const { coordinator } = setup();

    coordinator.open("ana", "timeline");
    coordinator.open("bruno", "timeline");

    expect(mockGetOrCreateDirectDM.mock.calls.map((call) => call[0])).toEqual(["ana", "bruno"]);
    expect(coordinator.isPending("ana")).toBe(true);
    expect(coordinator.isPending("bruno")).toBe(true);
  });

  it("refuses a conversation with yourself, and an empty recipient", () => {
    const { coordinator } = setup();

    coordinator.open("me", "timeline");
    coordinator.open("", "timeline");

    expect(mockGetOrCreateDirectDM).not.toHaveBeenCalled();
  });

  it("navigates once for one result, however many origins were waiting", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator, navigate, refreshConversations } = setup();

    coordinator.open("ana", "timeline");
    coordinator.open("ana", "sidebar");
    await act(async () => {
      pending.resolve({ conversationId: "dm-ana", created: true });
    });

    expect(navigate).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith("/chat/dm/dm-ana");
    expect(refreshConversations).toHaveBeenCalledTimes(1);
    expect(coordinator.isPending("ana")).toBe(false);
  });

  it("navigates to the conversation the server answered with, never to the recipient", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator, navigate } = setup();

    coordinator.open("ana", "timeline");
    await act(async () => {
      pending.resolve({ conversationId: "dm-7 /../x", created: false });
    });

    expect(navigate).toHaveBeenCalledWith(`/chat/dm/${encodeURIComponent("dm-7 /../x")}`);
  });

  it("lets a new attempt start once the previous one finished", async () => {
    const first = deferred();
    mockGetOrCreateDirectDM.mockReturnValueOnce(first.promise);
    mockGetOrCreateDirectDM.mockReturnValueOnce(deferred().promise);
    const { coordinator } = setup();

    coordinator.open("ana", "timeline");
    await act(async () => {
      first.resolve({ conversationId: "dm-ana", created: false });
    });
    coordinator.open("ana", "timeline");

    expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(2);
  });
});

describe("directMessageCoordinator — origins and cancellation", () => {
  it("aborts an operation nobody is waiting for any more", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator, navigate } = setup();

    coordinator.open("ana", "sidebar");
    const signal = mockGetOrCreateDirectDM.mock.calls[0][1] as AbortSignal;
    coordinator.releaseOrigin("sidebar");

    expect(signal.aborted).toBe(true);
    expect(coordinator.isPending("ana")).toBe(false);
    // Even a reply that was already on the wire changes nothing.
    await act(async () => {
      pending.resolve({ conversationId: "dm-ana", created: true });
    });
    expect(navigate).not.toHaveBeenCalled();
  });

  it("keeps an operation another origin still wants, and still navigates for it", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator, navigate } = setup();

    coordinator.open("ana", "sidebar");
    const signal = mockGetOrCreateDirectDM.mock.calls[0][1] as AbortSignal;
    coordinator.open("ana", "timeline");
    coordinator.releaseOrigin("sidebar");

    expect(signal.aborted).toBe(false);
    expect(coordinator.isPending("ana")).toBe(true);

    await act(async () => {
      pending.resolve({ conversationId: "dm-ana", created: true });
    });
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("releases only what that origin was waiting for", () => {
    mockGetOrCreateDirectDM.mockImplementation(() => deferred().promise);
    const { coordinator } = setup();

    coordinator.open("ana", "sidebar");
    coordinator.open("bruno", "timeline");
    coordinator.releaseOrigin("sidebar");

    expect(coordinator.isPending("ana")).toBe(false);
    // Somebody else's operation is untouched by another origin going away.
    expect(coordinator.isPending("bruno")).toBe(true);
  });

  it("publishes no refusal for an origin that has gone", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator } = setup();

    coordinator.open("ana", "sidebar");
    coordinator.releaseOrigin("sidebar");
    await act(async () => {
      pending.reject(new ApiRequestError(500, "internal", "boom"));
    });

    expect(coordinator.error()).toBeNull();
  });

  it("aborts everything on dispose", () => {
    mockGetOrCreateDirectDM.mockImplementation(() => deferred().promise);
    const { coordinator } = setup();
    coordinator.open("ana", "timeline");
    const signal = mockGetOrCreateDirectDM.mock.calls[0][1] as AbortSignal;

    coordinator.dispose();

    expect(signal.aborted).toBe(true);
    expect(coordinator.isPending("ana")).toBe(false);
  });
});

describe("directMessageCoordinator — the refusal", () => {
  it("gives a target the server will not name its own words", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator } = setup();

    coordinator.open("ana", "timeline");
    await act(async () => {
      pending.reject(new ApiRequestError(404, "not_found", "user not available"));
    });

    expect(coordinator.error()).toBe("Esta pessoa não está mais disponível para conversa direta.");
  });

  it("keeps the generic line for anything else", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator } = setup();

    coordinator.open("ana", "timeline");
    await act(async () => {
      pending.reject(new TypeError("network"));
    });

    expect(coordinator.error()).toBe("Não foi possível abrir a conversa. Tente novamente.");
  });

  it("says nothing about an abort", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    const { coordinator } = setup();

    coordinator.open("ana", "timeline");
    await act(async () => {
      pending.reject(new DOMException("aborted", "AbortError"));
    });

    expect(coordinator.error()).toBeNull();
  });

  it("clears on the next attempt, and notifies its subscribers once per change", async () => {
    const first = deferred();
    mockGetOrCreateDirectDM.mockReturnValueOnce(first.promise);
    mockGetOrCreateDirectDM.mockReturnValueOnce(deferred().promise);
    const { coordinator } = setup();
    const notified = vi.fn();
    coordinator.subscribeError(notified);

    coordinator.open("ana", "timeline");
    await act(async () => {
      first.reject(new TypeError("network"));
    });
    expect(notified).toHaveBeenCalledTimes(1);

    coordinator.open("bruno", "timeline");
    expect(coordinator.error()).toBeNull();
    expect(notified).toHaveBeenCalledTimes(2);
  });
});

describe("inertDirectMessage", () => {
  it("is a complete, silent stand-in", () => {
    // Used by a surface mounted without the shell's coordinator. Every member
    // has to exist and do nothing: a partial stand-in would turn "no shell" into
    // a crash inside a control somebody clicked.
    expect(() => {
      inertDirectMessage.open("ana", "origin");
      inertDirectMessage.releaseOrigin("origin");
      inertDirectMessage.setDeps({ currentUserId: "me", navigate: () => {} });
      inertDirectMessage.dispose();
    }).not.toThrow();

    expect(inertDirectMessage.isPending("ana")).toBe(false);
    expect(inertDirectMessage.error()).toBeNull();
    // Both subscriptions hand back a working unsubscribe rather than undefined.
    expect(() => inertDirectMessage.subscribePending("ana", () => {})()).not.toThrow();
    expect(() => inertDirectMessage.subscribeError(() => {})()).not.toThrow();
    expect(mockGetOrCreateDirectDM).not.toHaveBeenCalled();
  });
});

/**
 * The reason the message list stopped re-rendering.
 *
 * A component watching one person must not be invalidated because a different
 * person became pending. The set that used to be handed down could not express
 * that: any change to it was a change for every holder.
 */
describe("useDirectMessagePending — one person at a time", () => {
  let coordinator: DirectMessageCoordinator;
  const renders = new Map<string, number>();

  function Watcher({ recipientId }: { recipientId: string }) {
    const pending = useDirectMessagePending(coordinator, recipientId);
    renders.set(recipientId, (renders.get(recipientId) ?? 0) + 1);
    return <span data-testid={recipientId}>{String(pending)}</span>;
  }

  beforeEach(() => {
    renders.clear();
    mockGetOrCreateDirectDM.mockImplementation(() => deferred().promise);
    coordinator = setup().coordinator;
  });

  it("does not re-render a watcher when somebody else becomes pending", () => {
    render(
      <>
        <Watcher recipientId="ana" />
        <Watcher recipientId="bruno" />
      </>,
    );
    const brunoRenders = renders.get("bruno");

    act(() => {
      coordinator.open("ana", "timeline");
    });

    expect(screen.getByTestId("ana")).toHaveTextContent("true");
    expect(screen.getByTestId("bruno")).toHaveTextContent("false");
    expect(renders.get("bruno")).toBe(brunoRenders);
  });

  it("re-renders the watcher whose person it is, in both directions", async () => {
    const pending = deferred();
    mockGetOrCreateDirectDM.mockReturnValue(pending.promise);
    render(<Watcher recipientId="ana" />);
    const before = renders.get("ana")!;

    act(() => {
      coordinator.open("ana", "timeline");
    });
    expect(screen.getByTestId("ana")).toHaveTextContent("true");

    await act(async () => {
      pending.resolve({ conversationId: "dm-ana", created: true });
    });
    expect(screen.getByTestId("ana")).toHaveTextContent("false");
    expect(renders.get("ana")).toBeGreaterThan(before);
  });

  it("answers false, and subscribes to nothing, without a coordinator", () => {
    function Detached() {
      const pending = useDirectMessagePending(undefined, "ana");
      return <span data-testid="detached">{String(pending)}</span>;
    }
    render(<Detached />);

    expect(screen.getByTestId("detached")).toHaveTextContent("false");
  });

  it("stops holding a listener bucket once its last watcher unmounts", () => {
    const { unmount } = render(<Watcher recipientId="ana" />);
    unmount();

    // Nothing to assert directly about the internals; what matters is that a
    // notification after unmount is harmless rather than a leak reaching a dead
    // component.
    expect(() => coordinator.open("ana", "timeline")).not.toThrow();
    expect(coordinator.isPending("ana")).toBe(true);
  });
});
