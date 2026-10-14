/**
 * PinnedMessagesSection — the details panel's pin collection (issue #896).
 *
 * The section is rendered directly with the props its composition root hands
 * it, and an authoritative reload is modelled the way it happens in production:
 * a new collection arriving as props. What is asserted is what a reader gets —
 * rows, states, controls, their names and where focus lands.
 */

import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Message, PinnedItem } from "./chatTypes";
import PinnedMessagesSection, { type PinnedMessages } from "./PinnedMessagesSection";
import type { PinMutationOutcome, PinsCollection } from "./usePins";

function pin(id: string, overrides: Partial<Message> = {}, pinnedAt = "2026-07-15T12:30:00Z") {
  const item: PinnedItem = {
    message: {
      id,
      senderId: "user-1",
      senderDisplayName: `Autor ${id}`,
      senderEmail: "",
      kind: "user",
      bodyText: `corpo ${id}`,
      bodyFormat: "v3",
      isRemoved: false,
      status: "active",
      createdAt: "2026-07-15T12:00:00Z",
      updatedAt: "2026-07-15T12:00:00Z",
      isEdited: false,
      editCount: 0,
      reactions: [],
      isFavorited: false,
      isForwarded: false,
      ...overrides,
    },
    pinnedByUserId: "user-2",
    pinnedAt,
  };
  return item;
}

function pinsOf(count: number): PinnedItem[] {
  return Array.from({ length: count }, (_, index) => pin(`m-${index + 1}`));
}

function view(collection: PinsCollection, overrides: Partial<PinnedMessages> = {}): PinnedMessages {
  return {
    conversationKey: "channel:c-1",
    collection,
    pendingIds: new Set(),
    onNavigate: vi.fn(),
    onUnpin: vi.fn(() => Promise.resolve<PinMutationOutcome>("persisted")),
    onRetry: vi.fn(),
    ...overrides,
  };
}

function ready(pins: PinnedItem[]): PinsCollection {
  return { status: "ready", pins };
}

function renderSection(pins: PinnedMessages) {
  const utils = render(
    <PinnedMessagesSection pins={pins} emptyText="Nenhuma mensagem fixada neste canal." />,
  );
  return {
    ...utils,
    update: (next: PinnedMessages) =>
      utils.rerender(
        <PinnedMessagesSection pins={next} emptyText="Nenhuma mensagem fixada neste canal." />,
      ),
  };
}

function rows() {
  return within(screen.getByRole("list", { name: "Mensagens fixadas" })).getAllByRole("listitem");
}

function openButton(id: string) {
  return screen.getByRole("button", {
    name: new RegExp(`^Ir para a mensagem de Autor ${id}( |$)`),
  });
}

function unpinButton(id: string) {
  return screen.getByRole("button", {
    name: new RegExp(`^Desafixar mensagem de Autor ${id}( |$)`),
  });
}

describe("PinnedMessagesSection — collection states", () => {
  it("announces loading and never claims the conversation has no pins meanwhile", () => {
    renderSection(view({ status: "loading" }));

    expect(screen.getByRole("heading", { name: "Mensagens fixadas" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Carregando mensagens fixadas…");
    expect(screen.queryByTestId("chat-details-pin-empty")).not.toBeInTheDocument();
  });

  it("shows zero pins as an empty state, not as an error", () => {
    renderSection(view(ready([])));

    expect(screen.getByTestId("chat-details-pin-empty")).toHaveTextContent(
      "Nenhuma mensagem fixada neste canal.",
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
  });

  it("reports a failed load distinctly from empty and offers a retry", async () => {
    const pins = view({ status: "error" });
    renderSection(pins);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Não foi possível carregar as mensagens fixadas.",
    );
    expect(screen.queryByTestId("chat-details-pin-empty")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(pins.onRetry).toHaveBeenCalledTimes(1);
  });

  it("shows one pin with its body, author and pin time", () => {
    renderSection(view(ready([pin("m-1")])));

    expect(rows()).toHaveLength(1);
    expect(rows()[0]).toHaveTextContent("corpo m-1");
    expect(rows()[0]).toHaveTextContent(/Autor m-1 · .+, \d{2}:\d{2}/);
  });

  it("keeps the received order", () => {
    renderSection(view(ready([pin("m-3"), pin("m-1"), pin("m-2")])));

    expect(rows().map((row) => row.textContent)).toEqual([
      expect.stringContaining("corpo m-3"),
      expect.stringContaining("corpo m-1"),
      expect.stringContaining("corpo m-2"),
    ]);
  });

  it("renders a removed message as the placeholder and keeps it in the collection", () => {
    renderSection(view(ready([pin("m-1", { isRemoved: true, bodyText: "", status: "deleted" })])));

    expect(rows()[0]).toHaveTextContent("Mensagem removida.");
    expect(unpinButton("m-1")).toBeInTheDocument();
  });

  it("drops an unusable pin time instead of printing it", () => {
    renderSection(view(ready([pin("m-1", {}, "not-a-date")])));

    expect(rows()[0]).not.toHaveTextContent("Invalid Date");
    expect(openButton("m-1")).toHaveTextContent(/^Autor m-1$/);
  });

  it("renders rich text as inert markup, never as HTML", () => {
    renderSection(
      view(ready([pin("m-1", { bodyText: "<img src=x onerror=alert(1)> **forte**" })])),
    );

    expect(rows()[0]).toHaveTextContent("<img src=x onerror=alert(1)>");
    expect(document.querySelector("img")).toBeNull();
    expect(within(rows()[0]).getByText("forte").tagName).toBe("STRONG");
  });
});

describe("PinnedMessagesSection — expansion", () => {
  it("offers no expansion for exactly five pins", () => {
    renderSection(view(ready(pinsOf(5))));

    expect(rows()).toHaveLength(5);
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
  });

  it("shows five of six, reveals the rest, and collapses back to five", async () => {
    renderSection(view(ready(pinsOf(6))));
    expect(rows()).toHaveLength(5);

    const toggle = screen.getByRole("button", { name: /Ver todos Mensagens fixadas/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(toggle);

    expect(rows()).toHaveLength(6);
    expect(openButton("m-6")).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    await userEvent.click(screen.getByRole("button", { name: /Mostrar menos Mensagens fixadas/ }));
    expect(rows()).toHaveLength(5);
    expect(screen.queryByText("corpo m-6")).not.toBeInTheDocument();
  });
});

describe("PinnedMessagesSection — navigation and unpin", () => {
  it("navigates by message id from the row's main control, by pointer and by keyboard", async () => {
    const pins = view(ready([pin("m-9"), pin("m-4")]));
    renderSection(pins);

    await userEvent.click(openButton("m-4"));
    expect(pins.onNavigate).toHaveBeenLastCalledWith("m-4");

    openButton("m-9").focus();
    await userEvent.keyboard("{Enter}");
    expect(pins.onNavigate).toHaveBeenLastCalledWith("m-9");
    await userEvent.keyboard(" ");
    expect(pins.onNavigate).toHaveBeenCalledTimes(3);
    expect(pins.onUnpin).not.toHaveBeenCalled();
  });

  it("unpins by message id from a sibling control that never navigates", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2")]));
    renderSection(pins);

    await userEvent.click(unpinButton("m-2"));

    expect(pins.onUnpin).toHaveBeenCalledWith("m-2");
    expect(pins.onNavigate).not.toHaveBeenCalled();
    // Siblings, never nested: a button inside a button is invalid HTML.
    expect(openButton("m-2").querySelector("button")).toBeNull();
    expect(unpinButton("m-2").closest(".chat-details__pin-open")).toBeNull();
  });

  it("names every control by what it does and to which message", () => {
    renderSection(view(ready([pin("m-1")])));

    expect(openButton("m-1")).toHaveAccessibleName(/^Ir para a mensagem de Autor m-1 · /);
    expect(openButton("m-1")).toHaveAccessibleDescription("corpo m-1");
    expect(unpinButton("m-1")).toHaveAccessibleName(/^Desafixar mensagem de Autor m-1 · /);
  });

  it("marks only the pending row busy, refuses a repeat there, and leaves the others actionable", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2")]), { pendingIds: new Set(["m-1"]) });
    renderSection(pins);

    const pending = screen.getByRole("button", { name: /^Desafixando mensagem de Autor m-1 / });
    expect(pending).toHaveAttribute("aria-disabled", "true");
    // Still focusable, so a failure leaves focus where it was.
    expect(pending).not.toBeDisabled();
    await userEvent.click(pending);
    expect(pins.onUnpin).not.toHaveBeenCalled();
    expect(rows()).toHaveLength(2);

    expect(unpinButton("m-2")).not.toHaveAttribute("aria-disabled");
    await userEvent.click(unpinButton("m-2"));
    expect(pins.onUnpin).toHaveBeenCalledWith("m-2");
  });

  it("keeps the row and focus on its control when the unpin fails", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    update({ ...pins, pendingIds: new Set(["m-1"]) });
    // Failed: the lock is released and the authoritative list is unchanged.
    update({ ...pins, pendingIds: new Set() });

    expect(rows()).toHaveLength(2);
    expect(unpinButton("m-1")).toHaveFocus();
  });
});

describe("PinnedMessagesSection — focus after removal", () => {
  it("moves focus to the row that takes the removed one's place", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-2"));
    update({ ...pins, collection: ready([pin("m-1"), pin("m-3")]) });

    expect(openButton("m-3")).toHaveFocus();
  });

  it("moves focus to the previous row when the last one is removed", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-2"));
    update({ ...pins, collection: ready([pin("m-1")]) });

    expect(openButton("m-1")).toHaveFocus();
  });

  it("moves focus to the empty state when the only pin is removed, never to <body>", async () => {
    const pins = view(ready([pin("m-1")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    update({ ...pins, collection: ready([]) });

    expect(screen.getByTestId("chat-details-pin-empty")).toHaveFocus();
    expect(document.body).not.toHaveFocus();
  });

  it("leaves focus alone when the reader has already moved it", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    openButton("m-3").focus();
    update({ ...pins, collection: ready([pin("m-2"), pin("m-3")]) });

    expect(openButton("m-3")).toHaveFocus();
  });

  it("picks the successor by identity when a pin is inserted before the row mid-request", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-2"));
    // Realtime inserts m-0 at the top while the unpin is in flight...
    update({ ...pins, collection: ready([pin("m-0"), pin("m-1"), pin("m-2"), pin("m-3")]) });
    expect(unpinButton("m-2")).toHaveFocus();
    // ...then m-2 leaves. Its old position (index 1) now holds m-1.
    update({ ...pins, collection: ready([pin("m-0"), pin("m-1"), pin("m-3")]) });

    expect(openButton("m-3")).toHaveFocus();
  });

  it("picks the successor by identity when the insertion and the removal land together", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-2"));
    update({ ...pins, collection: ready([pin("m-0"), pin("m-1"), pin("m-3")]) });

    expect(openButton("m-3")).toHaveFocus();
  });

  it.each([
    ["m-2 first", ["m-2", "m-1"]],
    ["m-1 first", ["m-1", "m-2"]],
  ])(
    "keeps concurrent unpins apart whatever order they settle in (%s)",
    async (_order, settleOrder) => {
      const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]));
      const { update } = renderSection(pins);

      await userEvent.click(unpinButton("m-1"));
      await userEvent.click(unpinButton("m-2"));
      expect(pins.onUnpin).toHaveBeenNthCalledWith(1, "m-1");
      expect(pins.onUnpin).toHaveBeenNthCalledWith(2, "m-2");

      let remaining = ["m-1", "m-2", "m-3"];
      for (const settled of settleOrder) {
        remaining = remaining.filter((id) => id !== settled);
        update({ ...pins, collection: ready(remaining.map((id) => pin(id))) });
      }

      // Focus followed the reader to m-2's control; when that row went, it
      // moved to what came after it — never back to a row the reader left.
      expect(openButton("m-3")).toHaveFocus();
    },
  );

  it("does not take focus back once the reader has tabbed away", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    // Onwards to the next row's unpin control — not the successor recovery
    // would pick (m-2's navigation control).
    await userEvent.tab();
    await userEvent.tab();
    expect(unpinButton("m-2")).toHaveFocus();
    update({ ...pins, collection: ready([pin("m-2"), pin("m-3")]) });

    expect(unpinButton("m-2")).toHaveFocus();
  });

  it("does not recover after a blur with no destination while the row was still there", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    // relatedTarget is null here, yet the row is still in the document: the
    // reader let go of focus; the removal did not take it.
    act(() => unpinButton("m-1").blur());
    expect(unpinButton("m-1")).toBeInTheDocument();
    update({ ...pins, collection: ready([pin("m-2")]) });

    expect(openButton("m-2")).not.toHaveFocus();
    expect(
      screen.getByRole("button", { name: /^Desafixar mensagem de Autor m-2/ }),
    ).not.toHaveFocus();
  });

  it("does not steal focus back after the reader clicks plain text while the unpin is pending", async () => {
    const user = userEvent.setup();
    const pins = view(ready([pin("m-1"), pin("m-2")]), { pendingIds: new Set(["m-1"]) });
    const { update } = renderSection({ ...pins, pendingIds: new Set() });

    await user.click(unpinButton("m-1"));
    update(pins);
    const pending = screen.getByRole("button", { name: /^Desafixando mensagem de Autor m-1/ });
    expect(pending).toHaveFocus();

    // A heading is not focusable: clicking it drops focus with no destination.
    await user.click(screen.getByRole("heading", { name: "Mensagens fixadas" }));
    expect(pending).toBeInTheDocument();
    expect(pending).not.toHaveFocus();

    update({ ...pins, pendingIds: new Set(), collection: ready([pin("m-2")]) });

    expect(openButton("m-2")).not.toHaveFocus();
    expect(unpinButton("m-2")).not.toHaveFocus();
  });

  it.each([
    ["m-1 first", ["m-1", "m-2"]],
    ["m-2 first", ["m-2", "m-1"]],
  ])(
    "hands ownership back to a pending row the reader returns to (%s)",
    async (_order, settleOrder) => {
      const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]));
      const { update } = renderSection(pins);

      await userEvent.click(unpinButton("m-1"));
      await userEvent.click(unpinButton("m-2"));
      // Back to m-1's control while both are still in the list.
      await userEvent.tab({ shift: true });
      await userEvent.tab({ shift: true });
      expect(unpinButton("m-1")).toHaveFocus();

      let remaining = ["m-1", "m-2", "m-3"];
      remaining = remaining.filter((id) => id !== settleOrder[0]);
      update({ ...pins, collection: ready(remaining.map((id) => pin(id))) });
      if (settleOrder[0] === "m-1") {
        // m-1 owned focus when it went: it moves to what followed it.
        expect(openButton("m-2")).toHaveFocus();
      } else {
        // m-2 went without focus: nothing moves.
        expect(unpinButton("m-1")).toHaveFocus();
      }

      remaining = remaining.filter((id) => id !== settleOrder[1]);
      update({ ...pins, collection: ready(remaining.map((id) => pin(id))) });

      expect(openButton("m-3")).toHaveFocus();
    },
  );

  it("does nothing with focus when the section itself goes away mid-unpin", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2")]));
    const { unmount } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    unmount();

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(document.body).toHaveFocus();
  });

  it("gives the empty state focus without making it a tab stop", async () => {
    const pins = view(ready([pin("m-1")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    update({ ...pins, collection: ready([]) });

    const empty = screen.getByTestId("chat-details-pin-empty");
    expect(empty).toHaveFocus();
    expect(empty).toHaveAttribute("tabindex", "-1");
  });
});

describe("PinnedMessagesSection — a refused unpin ends its request", () => {
  /** An unpin whose outcome the test decides. */
  function outcome() {
    let settle!: (value: PinMutationOutcome) => void;
    const promise = new Promise<PinMutationOutcome>((resolve) => {
      settle = resolve;
    });
    return { promise, settle };
  }

  it("does not follow a later removal with focus after the unpin was rejected", async () => {
    const refused = outcome();
    const pins = view(ready([pin("m-1"), pin("m-2")]), {
      onUnpin: vi.fn(() => refused.promise),
    });
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    await act(async () => refused.settle("rejected"));
    // Refused: m-1 stays, is actionable again, and still holds focus.
    expect(unpinButton("m-1")).not.toHaveAttribute("aria-disabled");
    expect(unpinButton("m-1")).toHaveFocus();

    // Later, m-1 leaves for another reason (someone else unpinned it).
    update({ ...pins, collection: ready([pin("m-2")]) });

    expect(openButton("m-2")).not.toHaveFocus();
    expect(unpinButton("m-2")).not.toHaveFocus();
  });

  it("cancels only the refused message's request, never another one's", async () => {
    const first = outcome();
    const second = outcome();
    const pins = view(ready([pin("m-1"), pin("m-2"), pin("m-3")]), {
      onUnpin: vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise),
    });
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    await userEvent.click(unpinButton("m-2"));
    await act(async () => first.settle("rejected"));

    // m-1's request is gone: back on its control, its removal is not followed.
    await userEvent.tab({ shift: true });
    await userEvent.tab({ shift: true });
    expect(unpinButton("m-1")).toHaveFocus();
    update({ ...pins, collection: ready([pin("m-2"), pin("m-3")]) });
    expect(openButton("m-2")).not.toHaveFocus();

    // m-2's request survived: its removal, with focus on it, is followed.
    unpinButton("m-2").focus();
    await act(async () => second.settle("persisted"));
    update({ ...pins, collection: ready([pin("m-3")]) });

    expect(openButton("m-3")).toHaveFocus();
  });

  it("lets a retry after a refusal register a new request", async () => {
    const refused = outcome();
    const accepted = outcome();
    const pins = view(ready([pin("m-1"), pin("m-2")]), {
      onUnpin: vi.fn().mockReturnValueOnce(refused.promise).mockReturnValueOnce(accepted.promise),
    });
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    await act(async () => refused.settle("rejected"));
    await userEvent.click(unpinButton("m-1"));
    expect(pins.onUnpin).toHaveBeenCalledTimes(2);

    update({ ...pins, collection: ready([pin("m-2")]) });
    await act(async () => accepted.settle("persisted"));

    expect(openButton("m-2")).toHaveFocus();
  });

  it("keeps the request when the write succeeded but the read confirming it failed", async () => {
    const pins = view(ready([pin("m-1"), pin("m-2")]));
    const { update } = renderSection(pins);

    await userEvent.click(unpinButton("m-1"));
    // Persisted, not confirmed: the old list stays, marked stale.
    update({
      ...pins,
      collection: { status: "ready", pins: [pin("m-1"), pin("m-2")], refreshFailed: true },
    });
    expect(unpinButton("m-1")).toHaveFocus();

    // The reader retries the read, which comes back without m-1.
    update({ ...pins, collection: ready([pin("m-2")]) });

    expect(openButton("m-2")).toHaveFocus();
  });
});

describe("PinnedMessagesSection — refresh failure", () => {
  it("keeps the list, says the update failed and offers a retry", async () => {
    const pins = view({ status: "ready", pins: [pin("m-1")], refreshFailed: true });
    renderSection(pins);

    expect(rows()).toHaveLength(1);
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Não foi possível atualizar as mensagens fixadas.",
    );
    expect(screen.queryByTestId("chat-details-pin-empty")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(pins.onRetry).toHaveBeenCalledTimes(1);
  });

  it("shows neither the notice nor the retry for a confirmed list", () => {
    renderSection(view(ready([pin("m-1")])));

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Tentar novamente" })).not.toBeInTheDocument();
  });
});

describe("PinnedMessagesSection — sender without a name", () => {
  it("names the author in words and never by any part of the sender id", () => {
    const senderId = "9f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f";
    renderSection(view(ready([pin("m-1", { senderId, senderDisplayName: "", senderEmail: "" })])));

    const row = rows()[0];
    expect(row).toHaveTextContent("Autor não identificado");
    expect(row.textContent).not.toContain(senderId.slice(0, 8));
    for (const button of within(row).getAllByRole("button")) {
      expect(button).toHaveAccessibleName(/Autor não identificado/);
      expect(button.getAttribute("aria-label")).not.toContain(senderId.slice(0, 8));
    }
  });
});
