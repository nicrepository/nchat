import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ConversationActionsMenu from "./ConversationActionsMenu";
import type { ConversationAction } from "./conversationActions";

/**
 * ConversationActionsMenu — the parts of the popover the sidebar's own tests
 * cannot reach (issue #527).
 *
 * ChatSidebar.test.tsx covers the menu as the sidebar uses it: which actions a
 * row offers and what running one does. What is left, and what this file is
 * for, is the popover's own behaviour — where it places itself now that it is
 * portalled to <body> with viewport coordinates, and the keyboard moves that the
 * sidebar never exercises.
 *
 * The geometry assertions are deterministic and not pixel-perfect: the trigger's
 * rect is stated by the test, and what is asserted is the *decision* the
 * component makes from it — above or below, clamped or not.
 */

const actions: ConversationAction[] = [
  { id: "pin", label: "Fixar no topo", icon: "pin", group: "frequent" },
  { id: "mark-read", label: "Marcar como lido", icon: "check", group: "frequent" },
  { id: "details", label: "Detalhes do canal", icon: "info", group: "manage" },
  { id: "leave", label: "Sair do canal", icon: "logout", group: "destructive", destructive: true },
];

function renderMenu(overrides: { actions?: ConversationAction[] } = {}) {
  const onAction = vi.fn();
  const onOpenChange = vi.fn();
  render(
    <ConversationActionsMenu
      triggerLabel="Mais opções para canal Plataforma"
      actions={overrides.actions ?? actions}
      open
      onOpenChange={onOpenChange}
      onAction={onAction}
    />,
  );
  return { onAction, onOpenChange };
}

/**
 * Renders the menu closed, states the trigger's rect, then opens it — so the
 * position is computed from the geometry the test chose rather than from
 * jsdom's all-zero default.
 */
function renderWithTriggerRect(rect: Partial<DOMRect>, actionList: ConversationAction[] = actions) {
  const onOpenChange = vi.fn();
  const view = render(
    <ConversationActionsMenu
      triggerLabel="Mais opções para canal Plataforma"
      actions={actionList}
      open={false}
      onOpenChange={onOpenChange}
      onAction={vi.fn()}
    />,
  );
  const trigger = screen.getByRole("button", { name: "Mais opções para canal Plataforma" });
  trigger.getBoundingClientRect = () => ({ top: 0, bottom: 0, left: 0, right: 0, ...rect }) as DOMRect;
  view.rerender(
    <ConversationActionsMenu
      triggerLabel="Mais opções para canal Plataforma"
      actions={actionList}
      open
      onOpenChange={onOpenChange}
      onAction={vi.fn()}
    />,
  );
  return screen.getByRole("menu");
}

const items = () => screen.getAllByRole("menuitem");

describe("ConversationActionsMenu — popover placement", () => {
  // jsdom's viewport is 1024x768. Four items is 4*36 + 16 = 160px of menu.

  it("opens below the trigger when there is room under it", () => {
    const menu = renderWithTriggerRect({ top: 80, bottom: 100, right: 300 });

    // bottom + 4px gap.
    expect(menu.style.top).toBe("104px");
  });

  // The row at the bottom of the sidebar is the case the portal exists for: an
  // absolutely positioned menu was clipped there, and a fixed one that still
  // opened downwards would run off the viewport.
  it("flips above the trigger when the menu would run past the bottom edge", () => {
    const menu = renderWithTriggerRect({ top: 736, bottom: 760, right: 300 });

    // top - gap - height(160).
    expect(menu.style.top).toBe("572px");
  });

  // Neither direction fits, so it opens downwards rather than off the top: a
  // menu whose first item is above the viewport cannot be reached at all.
  it("opens below when there is no room in either direction", () => {
    const many = Array.from({ length: 20 }, (_, index) => ({
      ...actions[0]!,
      id: actions[0]!.id,
      label: `Ação ${index}`,
    }));
    const menu = renderWithTriggerRect({ top: 10, bottom: 40, right: 300 }, many);

    expect(menu.style.top).toBe("44px");
  });

  it("clamps to the left edge instead of hanging off it", () => {
    const menu = renderWithTriggerRect({ top: 80, bottom: 100, right: 100 });

    // Right-aligning a 224px menu with a trigger at x=100 would put it at -124.
    expect(menu.style.left).toBe("8px");
  });

  it("clamps to the right edge instead of hanging off it", () => {
    const menu = renderWithTriggerRect({ top: 80, bottom: 100, right: 1020 });

    // 1024 - 224 - 8.
    expect(menu.style.left).toBe("792px");
  });

  it("right-aligns with the trigger when both edges have room", () => {
    const menu = renderWithTriggerRect({ top: 80, bottom: 100, right: 500 });

    expect(menu.style.left).toBe("276px");
  });
});

describe("ConversationActionsMenu — keyboard", () => {
  it("jumps to the last item with End and back to the first with Home", async () => {
    const user = userEvent.setup();
    renderMenu();

    await user.keyboard("{End}");
    expect(items()[items().length - 1]).toHaveFocus();

    await user.keyboard("{Home}");
    expect(items()[0]).toHaveFocus();
  });

  // Focus can leave the list without leaving the popover — a click on its
  // padding blurs the item it was on. The next arrow press must enter the list
  // at the first item rather than compute a move from an index that is not there.
  it("enters the list when nothing in it holds focus", () => {
    renderMenu();

    (document.activeElement as HTMLElement).blur();
    fireEvent.keyDown(screen.getByRole("menu"), { key: "ArrowDown" });

    expect(items()[0]).toHaveFocus();
  });

  it("wraps around both ends", async () => {
    const user = userEvent.setup();
    renderMenu();

    // Focus opens on the first item.
    await user.keyboard("{ArrowUp}");
    expect(items()[items().length - 1]).toHaveFocus();

    await user.keyboard("{ArrowDown}");
    expect(items()[0]).toHaveFocus();
  });

  // A target that offers nothing renders an empty popover. Every keyboard move
  // has to be inert there rather than throwing on an absent item.
  it("does nothing on a menu with no actions", async () => {
    const user = userEvent.setup();
    renderMenu({ actions: [] });

    screen.getByRole("menu").focus();
    await user.keyboard("{ArrowDown}{ArrowUp}{Home}{End}");

    expect(screen.queryAllByRole("menuitem")).toHaveLength(0);
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });

  it("leaves a key it does not handle to the browser", async () => {
    const user = userEvent.setup();
    const { onAction, onOpenChange } = renderMenu();

    await user.keyboard("x");

    expect(onAction).not.toHaveBeenCalled();
    expect(onOpenChange).not.toHaveBeenCalled();
  });
});
