import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { removeChannelMember, removeGroupParticipant } = vi.hoisted(() => ({
  removeChannelMember: vi.fn(),
  removeGroupParticipant: vi.fn(),
}));

vi.mock("./chatApi", () => ({ removeChannelMember, removeGroupParticipant }));

import { useMemberRemoval } from "./useMemberRemoval";
import type { RosterParticipant } from "./participantRosterOrder";

/**
 * The removal flow's own invariants (issue #469).
 *
 * The panel's tests prove the flow end to end through the rendered dialog;
 * these prove the two guards that exist precisely so a caller *cannot* reach
 * the flow in an impossible state — and that focus never chases an element
 * that has left the document.
 */

const participant: RosterParticipant = {
  userId: "u-2",
  displayName: "Fernanda Nicácio",
  subtitle: "Membro",
};

function setup(kind: "channel" | "group" = "channel", targetId = "ch-1") {
  const reload = vi.fn();
  const fallback = document.createElement("button");
  document.body.append(fallback);
  const fallbackFocusRef = { current: fallback as HTMLElement | null };
  const view = renderHook(() => useMemberRemoval({ kind, targetId, reload, fallbackFocusRef }));
  return { ...view, reload, fallback };
}

beforeEach(() => {
  document.body.innerHTML = "";
  removeChannelMember.mockReset().mockResolvedValue(undefined);
  removeGroupParticipant.mockReset().mockResolvedValue(undefined);
});

describe("useMemberRemoval", () => {
  it("opens for one person and closes on success, announcing what happened", async () => {
    const { result, reload, fallback } = setup();
    const trigger = document.createElement("button");
    document.body.append(trigger);

    act(() => result.current.request(participant, trigger));
    expect(result.current.member).toEqual({ userId: "u-2", displayName: "Fernanda Nicácio" });

    await act(() => result.current.confirm());

    expect(removeChannelMember).toHaveBeenCalledWith("ch-1", "u-2");
    expect(result.current.member).toBeNull();
    expect(result.current.notice).toBe("Fernanda Nicácio foi removido do canal.");
    expect(reload).toHaveBeenCalledTimes(1);
    // The row is gone; focus went to the control that outlives it.
    expect(document.activeElement).toBe(fallback);
  });

  it("removes a group participant through the group endpoint", async () => {
    const { result } = setup("group", "conv-1");

    act(() => result.current.request(participant, document.createElement("button")));
    await act(() => result.current.confirm());

    expect(removeGroupParticipant).toHaveBeenCalledWith("conv-1", "u-2");
    expect(removeChannelMember).not.toHaveBeenCalled();
    expect(result.current.notice).toBe("Fernanda Nicácio foi removido do grupo.");
  });

  // Confirming without a person under confirmation is not a state the panel
  // can produce — the dialog is mounted from `member` — and it must stay a
  // no-op rather than a request with an empty target.
  it("sends nothing when there is nobody under confirmation", async () => {
    const { result, reload } = setup();

    await act(() => result.current.confirm());

    expect(removeChannelMember).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
  });

  it("returns focus to the control that opened it when cancelled", () => {
    const { result, reload } = setup();
    const trigger = document.createElement("button");
    document.body.append(trigger);

    act(() => result.current.request(participant, trigger));
    act(() => result.current.cancel());

    expect(result.current.member).toBeNull();
    expect(removeChannelMember).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(trigger);
  });

  // A refetch can revoke the capability while the dialog is open, which
  // unmounts the row's button. Focusing a detached node would drop focus to
  // <body>; not focusing anything leaves it where it is.
  it("does not chase a trigger that has left the document", () => {
    const { result } = setup();
    const trigger = document.createElement("button");
    document.body.append(trigger);
    const elsewhere = document.createElement("button");
    document.body.append(elsewhere);

    act(() => result.current.request(participant, trigger));
    trigger.remove();
    elsewhere.focus();
    act(() => result.current.cancel());

    expect(document.activeElement).toBe(elsewhere);
  });

  // Removing and catching up are two different outcomes, and only the first
  // one is the removal. A refetch that fails must not be reported as "não foi
  // possível remover" — the dialog would offer a retry, and the retry would
  // send a second DELETE for somebody already gone.
  it("does not turn a committed removal into a failure when reconciliation fails", async () => {
    const reload = vi.fn(() => {
      throw new Error("refetch exploded");
    });
    const { result } = renderHook(() =>
      useMemberRemoval({
        kind: "channel",
        targetId: "ch-1",
        reload,
        fallbackFocusRef: { current: null },
      }),
    );

    act(() => result.current.request(participant, document.createElement("button")));
    await act(() => result.current.confirm());

    expect(removeChannelMember).toHaveBeenCalledTimes(1);
    expect(result.current.member).toBeNull();
    expect(result.current.notice).toBe("Fernanda Nicácio foi removido do canal.");
  });

  // ── A write that outlives the conversation it was started for ─────────────
  //
  // The DELETE is allowed to finish — a destructive write is never cancelled
  // because the reader navigated — but everything the panel does afterwards
  // describes a conversation, and by then it may be describing another one.

  it("applies nothing to the conversation the reader switched to", async () => {
    const reload = vi.fn();
    const fallback = document.createElement("button");
    const elsewhere = document.createElement("button");
    document.body.append(fallback, elsewhere);
    const fallbackFocusRef = { current: fallback as HTMLElement | null };
    let settle: (() => void) | undefined;
    removeChannelMember.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
    );

    const { result, rerender } = renderHook(
      ({ targetId }) => useMemberRemoval({ kind: "channel", targetId, reload, fallbackFocusRef }),
      { initialProps: { targetId: "ch-a" } },
    );

    act(() => result.current.request(participant, document.createElement("button")));
    let confirmed: Promise<void> | undefined;
    act(() => {
      confirmed = result.current.confirm();
    });
    expect(removeChannelMember).toHaveBeenCalledWith("ch-a", "u-2");

    // The reader moves on while the write is still in flight.
    rerender({ targetId: "ch-b" });
    elsewhere.focus();

    await act(async () => {
      settle?.();
      await confirmed;
    });

    // The write stands, and nothing about it reached the conversation now on
    // screen: no announcement, no refetch, no focus taken from the reader.
    expect(removeChannelMember).toHaveBeenCalledTimes(1);
    expect(result.current.notice).toBe("");
    expect(reload).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(elsewhere);
    // And no stale confirmation is left to reappear on the way back.
    expect(result.current.member).toBeNull();
    rerender({ targetId: "ch-a" });
    expect(result.current.member).toBeNull();
  });

  // The same id in the other aggregate is a different conversation: a channel
  // and a chat.dm_conversations row are separate id spaces.
  it("treats the same id under another kind as another conversation", async () => {
    const reload = vi.fn();
    const fallbackFocusRef = { current: null as HTMLElement | null };
    let settle: (() => void) | undefined;
    removeChannelMember.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
    );

    const { result, rerender } = renderHook(
      ({ kind }: { kind: "channel" | "group" }) =>
        useMemberRemoval({ kind, targetId: "same-id", reload, fallbackFocusRef }),
      { initialProps: { kind: "channel" } as { kind: "channel" | "group" } },
    );

    act(() => result.current.request(participant, document.createElement("button")));
    let confirmed: Promise<void> | undefined;
    act(() => {
      confirmed = result.current.confirm();
    });
    rerender({ kind: "group" });
    await act(async () => {
      settle?.();
      await confirmed;
    });

    expect(reload).not.toHaveBeenCalled();
    expect(result.current.notice).toBe("");
  });

  // A confirmation opened for the conversation the reader moved to is that
  // conversation's own state and must survive the older write finishing.
  it("leaves a confirmation opened meanwhile for another conversation alone", async () => {
    const reload = vi.fn();
    const fallbackFocusRef = { current: null as HTMLElement | null };
    let settle: (() => void) | undefined;
    removeChannelMember.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
    );

    const { result, rerender } = renderHook(
      ({ targetId }) => useMemberRemoval({ kind: "channel", targetId, reload, fallbackFocusRef }),
      { initialProps: { targetId: "ch-a" } },
    );

    act(() => result.current.request(participant, document.createElement("button")));
    let confirmed: Promise<void> | undefined;
    act(() => {
      confirmed = result.current.confirm();
    });
    rerender({ targetId: "ch-b" });
    act(() => result.current.request(participant, document.createElement("button")));

    await act(async () => {
      settle?.();
      await confirmed;
    });

    expect(result.current.member).toEqual({ userId: "u-2", displayName: "Fernanda Nicácio" });
  });

  it("survives the conversation being closed while the write is in flight", async () => {
    const reload = vi.fn();
    const fallbackFocusRef = { current: null as HTMLElement | null };
    let settle: (() => void) | undefined;
    removeChannelMember.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
    );
    const errors: unknown[] = [];
    const consoleError = vi.spyOn(console, "error").mockImplementation((...args) => {
      errors.push(args);
    });

    const { result, unmount } = renderHook(() =>
      useMemberRemoval({ kind: "channel", targetId: "ch-a", reload, fallbackFocusRef }),
    );

    act(() => result.current.request(participant, document.createElement("button")));
    let confirmed: Promise<void> | undefined;
    act(() => {
      confirmed = result.current.confirm();
    });
    unmount();

    await act(async () => {
      settle?.();
      await confirmed;
    });

    expect(removeChannelMember).toHaveBeenCalledTimes(1);
    expect(errors).toEqual([]);
    consoleError.mockRestore();
  });

  // The panel is not remounted on a conversation switch, so the open state has
  // to be keyed: a confirmation opened for one conversation must not survive
  // into the next one.
  it("closes itself when the conversation changes underneath it", () => {
    const reload = vi.fn();
    const fallbackFocusRef = { current: null as HTMLElement | null };
    const { result, rerender } = renderHook(
      ({ targetId }) => useMemberRemoval({ kind: "channel", targetId, reload, fallbackFocusRef }),
      { initialProps: { targetId: "ch-1" } },
    );

    act(() => result.current.request(participant, document.createElement("button")));
    expect(result.current.member).not.toBeNull();

    rerender({ targetId: "ch-2" });

    expect(result.current.member).toBeNull();
  });
});
