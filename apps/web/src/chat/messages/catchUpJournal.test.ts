import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Message } from "../chatTypes";
import { catchUpJournalLimit, useCatchUpJournal } from "./catchUpJournal";
import type { Action } from "./types";

/** Issue #947 / CQ947-01, 05: what the journal records, in what form, and its bound. */

const sent = { id: "m-9", bodyText: "A" } as Message;
const fact: Action = { type: "favorite_set", messageId: "m-1", isFavorited: true };

function journal() {
  const dispatch = vi.fn<(action: Action) => void>();
  const { result } = renderHook(() => useCatchUpJournal(dispatch));
  return { journal: result.current, dispatch };
}

describe("catch-up journal", () => {
  it("passes every action through, and records nothing while no read is open", () => {
    const { journal: j, dispatch } = journal();

    act(() => j.dispatch(fact));

    expect(dispatch).toHaveBeenCalledWith(fact);
    const open = j.open();
    expect(open.entries).toEqual([]);
  });

  it("records server facts, and a send only as the message it delivered", () => {
    const { journal: j } = journal();
    const open = j.open();

    j.dispatch(fact);
    j.dispatch({ type: "sent", message: sent, parentMessageId: "m-1" });

    expect(open.entries).toEqual([fact, { type: "ws_received", message: sent }]);
  });

  // CQ947-R2-01: a refusal is a server fact; the rebase takes only its timeline effect.
  it("records a refusal of a message being scanned", () => {
    const { journal: j } = journal();
    const open = j.open();
    const refused: Action = { type: "message_blocked", messageId: "m-5", reason: "malicious_link" };

    j.dispatch(refused);

    expect(open.entries).toEqual([refused]);
  });

  it("never records an intent or anything the composer owns", () => {
    const { journal: j } = journal();
    const open = j.open();

    for (const action of [
      { type: "reaction_optimistic", messageId: "m-1", emoji: "👍" },
      { type: "reaction_revert", messageId: "m-1", emoji: "👍", error: "x" },
      { type: "edit_optimistic", messageId: "m-1", body: "b", bodyFormat: "v3", editedAt: "t" },
      { type: "edit_revert", message: sent, optimisticEditedAt: "t" },
      { type: "sending" },
      { type: "reply_set", message: sent },
      { type: "send_error", error: "x" },
    ] as Action[]) {
      j.dispatch(action);
    }

    expect(open.entries).toEqual([]);
  });

  it("holds exactly the limit, and overflows past it without growing", () => {
    const { journal: j } = journal();
    const open = j.open();

    for (let i = 0; i < catchUpJournalLimit; i += 1) j.dispatch(fact);
    expect(open).toMatchObject({ overflowed: false });
    expect(open.entries).toHaveLength(catchUpJournalLimit);

    j.dispatch(fact);
    j.dispatch(fact);
    expect(open.overflowed).toBe(true);
    expect(open.entries).toHaveLength(0);
  });

  it("stops recording into a journal that was closed or replaced", () => {
    const { journal: j } = journal();
    const first = j.open();
    const second = j.open();

    // A superseded attempt finishing late must not stop the current one.
    j.close(first);
    j.dispatch(fact);
    j.close(second);
    j.dispatch(fact);

    expect(first.entries).toEqual([]);
    expect(second.entries).toEqual([fact]);
  });
});
