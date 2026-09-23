import { act } from "@testing-library/react";
import { flushSync } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { removeChannelMember, removeGroupParticipant } = vi.hoisted(() => ({
  removeChannelMember: vi.fn(),
  removeGroupParticipant: vi.fn(),
}));

vi.mock("./chatApi", () => ({ removeChannelMember, removeGroupParticipant }));

import { useMemberRemoval, type MemberRemovalFlow } from "./useMemberRemoval";
import type { RosterParticipant } from "./participantRosterOrder";

/**
 * The removal's lifecycle against React's own phases (issues #469 CQ-2, CQ-3).
 *
 * Deliberately not written with Testing Library's `render`/`rerender`: those
 * run inside `act`, which drives the switch and the flush of every effect as
 * one opaque step. Here the switch is committed against a real root with
 * `flushSync`, so "B is on screen" is a fact the test established rather than
 * something `act` arranged, and the pending write settles after it.
 *
 * What these tests prove, by construction — each one fails if the guard it
 * covers is removed: a write that finishes after another conversation is on
 * screen applies nothing to it, in either direction of identity (`targetId`
 * and `kind`), whether it succeeded or was refused; and a write that finishes
 * after the panel is gone applies nothing at all, while the request itself is
 * never undone.
 *
 * What they cannot prove, and the reason the hook does not rely on them for
 * it: whether the current-target ref is synchronised in the layout phase or
 * the passive one. Measured in this environment (React 19.2 + jsdom), a commit
 * flushes layout *and* passive effects inside the same task — before control
 * returns to the microtask queue — so a promise continuation can never land
 * between the two, and both implementations behave identically here. The
 * layout effect is what makes the invariant hold where the two phases really
 * can be split by the scheduler, which is the browser these tests stand in for.
 */

const participant: RosterParticipant = {
  userId: "u-2",
  displayName: "Fernanda Nicácio",
  subtitle: "Membro",
};

/** Exposes the flow of the last render, so a test can drive it from outside. */
function Harness({
  kind,
  targetId,
  reload,
  fallbackFocusRef,
  onRender,
}: {
  kind: "channel" | "group";
  targetId: string;
  reload: () => void;
  fallbackFocusRef: React.RefObject<HTMLElement | null>;
  onRender: (flow: MemberRemovalFlow) => void;
}) {
  onRender(useMemberRemoval({ kind, targetId, reload, fallbackFocusRef }));
  return null;
}

interface Harnessed {
  root: Root;
  container: HTMLDivElement;
  flow: () => MemberRemovalFlow;
  reload: ReturnType<typeof vi.fn>;
  fallback: HTMLButtonElement;
  /** Commits a new target the way React commits one: synchronously. */
  commitTarget: (next: { kind?: "channel" | "group"; targetId: string }) => void;
}

let mounted: Harnessed | null = null;

function mountHarness(initial: { kind?: "channel" | "group"; targetId: string }): Harnessed {
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const reload = vi.fn();
  const fallback = document.createElement("button");
  document.body.append(fallback);
  const fallbackFocusRef = { current: fallback as HTMLElement | null };
  let latest: MemberRemovalFlow | null = null;
  const onRender = (flow: MemberRemovalFlow) => {
    latest = flow;
  };
  const element = (target: { kind?: "channel" | "group"; targetId: string }) => (
    <Harness
      kind={target.kind ?? "channel"}
      targetId={target.targetId}
      reload={reload}
      fallbackFocusRef={fallbackFocusRef}
      onRender={onRender}
    />
  );

  act(() => {
    root.render(element(initial));
  });

  const harnessed: Harnessed = {
    root,
    container,
    reload,
    fallback,
    flow: () => {
      if (latest === null) throw new Error("the harness never rendered");
      return latest;
    },
    commitTarget: (next) => {
      flushSync(() => {
        root.render(element(next));
      });
    },
  };
  mounted = harnessed;
  return harnessed;
}

beforeEach(() => {
  removeChannelMember.mockReset();
  removeGroupParticipant.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  if (mounted) {
    act(() => mounted?.root.unmount());
    mounted.container.remove();
    mounted = null;
  }
  document.body.innerHTML = "";
});

/** Starts a removal whose request stays pending until the returned settler runs. */
function startPendingRemoval(harness: Harnessed): {
  finished: Promise<void>;
  resolve: () => void;
  reject: (error: Error) => void;
} {
  let resolve: (() => void) | undefined;
  let reject: ((error: Error) => void) | undefined;
  removeChannelMember.mockImplementation(
    () =>
      new Promise<void>((settle, fail) => {
        resolve = () => settle();
        reject = (error: Error) => fail(error);
      }),
  );
  act(() => {
    harness.flow().request(participant, document.createElement("button"));
  });
  let finished: Promise<void> | undefined;
  act(() => {
    finished = harness.flow().confirm();
  });
  if (!finished || !resolve || !reject) throw new Error("the removal never started");
  return { finished, resolve, reject };
}

describe("useMemberRemoval lifecycle", () => {
  // CQ-2. The switch is committed and the write finishes before React has run
  // a single passive effect. Nothing that belongs to the conversation just
  // written to may touch the one now on screen.
  it("applies nothing to a conversation committed before the write finished", async () => {
    const harness = mountHarness({ targetId: "ch-a" });
    const removal = startPendingRemoval(harness);
    harness.fallback.blur();
    const focusedBefore = document.activeElement;

    harness.commitTarget({ targetId: "ch-b" });
    removal.resolve();
    await act(async () => {
      await removal.finished;
    });

    expect(removeChannelMember).toHaveBeenCalledTimes(1);
    expect(removeChannelMember).toHaveBeenCalledWith("ch-a", "u-2");
    expect(harness.reload).not.toHaveBeenCalled();
    expect(harness.flow().notice).toBe("");
    expect(document.activeElement).toBe(focusedBefore);
  });

  // The same id under the other aggregate is a different conversation, and the
  // window is the same one.
  it("treats a kind change committed in the same window as another conversation", async () => {
    const harness = mountHarness({ kind: "channel", targetId: "same-id" });
    const removal = startPendingRemoval(harness);

    harness.commitTarget({ kind: "group", targetId: "same-id" });
    removal.resolve();
    await act(async () => {
      await removal.finished;
    });

    expect(harness.reload).not.toHaveBeenCalled();
    expect(harness.flow().notice).toBe("");
  });

  // CQ-3. The request is never cancelled, so it can finish after the panel is
  // gone — and then there is nothing local left to do.
  it("applies nothing after the panel is unmounted", async () => {
    const harness = mountHarness({ targetId: "ch-a" });
    const removal = startPendingRemoval(harness);
    const elsewhere = document.createElement("button");
    document.body.append(elsewhere);
    elsewhere.focus();

    act(() => harness.root.unmount());
    removal.resolve();
    await act(async () => {
      await removal.finished;
    });

    // The write stands: a destructive request already sent is not undone
    // because the panel closed.
    expect(removeChannelMember).toHaveBeenCalledTimes(1);
    // Nothing local ran for a panel that no longer exists.
    expect(harness.reload).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(elsewhere);
    mounted = null;
    harness.container.remove();
  });

  // Case F: a refusal that lands after the switch belongs to the conversation
  // it was refused for, and is reported to the caller rather than to whatever
  // is on screen.
  it("keeps a refusal that lands after a switch away from the new conversation", async () => {
    const harness = mountHarness({ targetId: "ch-a" });
    const removal = startPendingRemoval(harness);

    harness.commitTarget({ targetId: "ch-b" });
    removal.reject(new Error("forbidden"));
    let caught: unknown = null;
    await act(async () => {
      await removal.finished.catch((error: unknown) => {
        caught = error;
      });
    });

    expect(caught).toBeInstanceOf(Error);
    expect(harness.reload).not.toHaveBeenCalled();
    expect(harness.flow().notice).toBe("");
    expect(harness.flow().member).toBeNull();
  });

  // Case G: the same refusal after the panel is gone runs nothing at all.
  it("keeps a refusal that lands after unmount from doing anything", async () => {
    const harness = mountHarness({ targetId: "ch-a" });
    const removal = startPendingRemoval(harness);
    const elsewhere = document.createElement("button");
    document.body.append(elsewhere);
    elsewhere.focus();

    act(() => harness.root.unmount());
    removal.reject(new Error("forbidden"));
    let caught: unknown = null;
    await act(async () => {
      await removal.finished.catch((error: unknown) => {
        caught = error;
      });
    });

    expect(caught).toBeInstanceOf(Error);
    expect(harness.reload).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(elsewhere);
    mounted = null;
    harness.container.remove();
  });

  // The healthy path through the very same machinery: still on the same
  // conversation, still mounted, so every local effect runs.
  it("announces, refetches and moves focus when the conversation is still the one", async () => {
    const harness = mountHarness({ targetId: "ch-a" });
    const removal = startPendingRemoval(harness);

    removal.resolve();
    await act(async () => {
      await removal.finished;
    });

    expect(harness.flow().notice).toBe("Fernanda Nicácio foi removido do canal.");
    expect(harness.reload).toHaveBeenCalledTimes(1);
    expect(document.activeElement).toBe(harness.fallback);
    expect(harness.flow().member).toBeNull();
  });
});
