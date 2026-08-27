import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ReactionBadge from "./ReactionBadge";
import { useReactionPresence } from "./useReactionPresence";
import type { MessageReaction } from "./chatTypes";

const me = "me-123";

function reaction(over: Partial<MessageReaction> = {}): MessageReaction {
  return { emoji: "👍", count: 1, reactedByMe: false, users: [], ...over };
}

function renderBadge(over: Partial<MessageReaction> = {}, onToggle = vi.fn()) {
  render(
    <ReactionBadge
      messageId="m1"
      reaction={reaction(over)}
      currentUserId={me}
      onToggle={onToggle}
    />,
  );
  return onToggle;
}

describe("ReactionBadge", () => {
  it("shows the emoji, the count and whether the reader reacted", () => {
    renderBadge({ count: 3, reactedByMe: true });
    const badge = screen.getByRole("button", { name: "Remover reação 👍" });
    expect(badge).toHaveTextContent("3");
    expect(badge).toHaveAttribute("aria-pressed", "true");
  });

  it("offers to add when the reader has not reacted", () => {
    renderBadge({ count: 2 });
    expect(screen.getByRole("button", { name: "Adicionar reação 👍" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("toggles the reaction it renders", async () => {
    const onToggle = renderBadge({ count: 1 });
    await userEvent.click(screen.getByRole("button", { name: "Adicionar reação 👍" }));
    expect(onToggle).toHaveBeenCalledWith("m1", "👍");
  });

  // Who reacted is in the DOM at all times as the button's description, so it
  // reaches a screen reader without a pointer and without a request. The
  // stylesheet is what makes it *visible* on hover and on focus.
  it("describes its authors through the button, not only through a tooltip", () => {
    renderBadge({
      count: 4,
      reactedByMe: true,
      users: [
        { userId: me, displayName: "Eu" },
        { userId: "u2", displayName: "Caio Almeida" },
      ],
    });
    const badge = screen.getByRole("button", { name: "Remover reação 👍" });
    const description = document.getElementById(badge.getAttribute("aria-describedby") ?? "");
    expect(description).toHaveTextContent("👍: Você, Caio Almeida e mais 2");
    expect(badge).toHaveAccessibleDescription("👍: Você, Caio Almeida e mais 2");
  });

  it("describes a reaction whose authors the server could not name", () => {
    renderBadge({ count: 2 });
    expect(screen.getByRole("button", { name: "Adicionar reação 👍" })).toHaveAccessibleDescription(
      "👍: 2 pessoas",
    );
  });

  // The pop animation is CSS on an element React remounts when the count
  // changes. Nothing else may replay it — hovering the badge must not.
  it("re-mounts the animated emoji only when the count changes", () => {
    const { rerender } = render(
      <ReactionBadge
        messageId="m1"
        reaction={reaction({ count: 1 })}
        currentUserId={me}
        onToggle={vi.fn()}
      />,
    );
    const before = document.querySelector(".chat-msg-area__reaction-emoji");

    rerender(
      <ReactionBadge
        messageId="m1"
        reaction={reaction({ count: 1, users: [{ userId: "u2", displayName: "Caio" }] })}
        currentUserId={me}
        onToggle={vi.fn()}
      />,
    );
    expect(document.querySelector(".chat-msg-area__reaction-emoji")).toBe(before);

    rerender(
      <ReactionBadge
        messageId="m1"
        reaction={reaction({ count: 2 })}
        currentUserId={me}
        onToggle={vi.fn()}
      />,
    );
    expect(document.querySelector(".chat-msg-area__reaction-emoji")).not.toBe(before);
  });
});

/**
 * A reaction that is removed has to be seen leaving. Before this, the last badge
 * vanished between two frames, which reads as a glitch rather than as an action.
 *
 * These tests drive the exit the way a browser does — the badge is drawn, then
 * its own animationend removes it — because that is the contract the component
 * relies on. jsdom runs no animations, so the event is dispatched here.
 */
describe("reaction exit", () => {
  function Reactions({ reactions }: { reactions: MessageReaction[] }) {
    const { rendered, onExited } = useReactionPresence(reactions);
    return (
      <div>
        {rendered.map(({ reaction: item, exiting }) => (
          <ReactionBadge
            key={item.emoji}
            messageId="m1"
            reaction={item}
            currentUserId={me}
            onToggle={vi.fn()}
            exiting={exiting}
            onExited={onExited}
          />
        ))}
      </div>
    );
  }

  const cheer = reaction({ emoji: "🎉", count: 1, reactedByMe: true });
  const rocket = reaction({ emoji: "🚀", count: 2 });

  function slotOf(emoji: string): HTMLElement {
    const badge = screen.getByRole("button", { name: new RegExp(`reação ${emoji}`) });
    return badge.closest(".chat-msg-area__reaction-slot") as HTMLElement;
  }

  it("keeps the last badge on screen until its animation ends", () => {
    const { rerender } = render(<Reactions reactions={[cheer]} />);
    expect(screen.getByRole("button", { name: "Remover reação 🎉" })).toBeInTheDocument();

    rerender(<Reactions reactions={[]} />);

    // Still drawn, and now inert: not clickable, not focusable, not announced.
    const slot = document.querySelector(".chat-msg-area__reaction-slot") as HTMLElement;
    expect(slot).toHaveAttribute("data-exiting", "true");
    expect(slot).toHaveAttribute("aria-hidden", "true");
    expect(slot.querySelector("button")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Remover reação 🎉" })).toBeNull();

    fireEvent.animationEnd(slot);

    expect(document.querySelector(".chat-msg-area__reaction-slot")).toBeNull();
  });

  it("leaves the other badges untouched while one is on its way out", () => {
    const { rerender } = render(<Reactions reactions={[cheer, rocket]} />);

    rerender(<Reactions reactions={[rocket]} />);

    expect(slotOf("🚀")).not.toHaveAttribute("data-exiting");
    expect(screen.getByRole("button", { name: "Adicionar reação 🚀" })).toBeEnabled();
    const exiting = document.querySelectorAll("[data-exiting]");
    expect(exiting).toHaveLength(1);

    fireEvent.animationEnd(exiting[0]);
    expect(document.querySelectorAll(".chat-msg-area__reaction-slot")).toHaveLength(1);
  });

  // A rollback after a refused request, and a re-add by the same reader, are the
  // same thing to this component: the reaction is back before it finished going.
  it("cancels the exit when the reaction comes back, without duplicating it", () => {
    const { rerender } = render(<Reactions reactions={[cheer]} />);
    rerender(<Reactions reactions={[]} />);
    expect(document.querySelector("[data-exiting]")).not.toBeNull();

    rerender(<Reactions reactions={[cheer]} />);

    expect(document.querySelectorAll(".chat-msg-area__reaction-slot")).toHaveLength(1);
    expect(document.querySelector("[data-exiting]")).toBeNull();
    expect(screen.getByRole("button", { name: "Remover reação 🎉" })).toBeEnabled();
  });

  // The finding: `leaving` was only refreshed when its size changed, so one
  // badge being replaced by another of the same cardinality left the departed
  // one drawn beside the live one — same key, duplicated node, and the emoji
  // that really left never animated out.
  it("swaps the leaving badge when one reaction replaces another", () => {
    const { rerender } = render(<Reactions reactions={[cheer, rocket]} />);

    rerender(<Reactions reactions={[rocket]} />);
    rerender(<Reactions reactions={[cheer]} />);

    // 🎉 is back, exactly once and interactive; 🚀 is the one on its way out.
    const slots = document.querySelectorAll(".chat-msg-area__reaction-slot");
    expect(slots).toHaveLength(2);
    expect(screen.getAllByRole("button", { name: /reação 🎉/ })).toHaveLength(1);
    expect(slotOf("🎉")).not.toHaveAttribute("data-exiting");
    const exiting = document.querySelectorAll("[data-exiting]");
    expect(exiting).toHaveLength(1);
    expect(exiting[0].textContent).toContain("🚀");

    fireEvent.animationEnd(exiting[0]);

    // Only the departed one goes.
    expect(document.querySelectorAll(".chat-msg-area__reaction-slot")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Remover reação 🎉" })).toBeEnabled();
  });

  it("does not report an exit for a badge that is not leaving", () => {
    render(<Reactions reactions={[cheer]} />);
    const slot = slotOf("🎉");

    // The enter animation ends too; it must not unmount anything.
    fireEvent.animationEnd(slot);

    expect(screen.getByRole("button", { name: "Remover reação 🎉" })).toBeInTheDocument();
  });
});
