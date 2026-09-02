import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import * as authApi from "../auth/authApi";
import * as authSession from "../lib/authSession";
import SidebarUserMenu from "./SidebarUserMenu";

vi.mock("../auth/authApi");
vi.mock("../lib/authSession", async (importOriginal) => ({
  ...(await importOriginal<typeof authSession>()),
  clearTokens: vi.fn(),
}));

afterEach(() => vi.clearAllMocks());

function renderMenu() {
  return render(
    <MemoryRouter>
      <button type="button">Fora</button>
      <SidebarUserMenu />
    </MemoryRouter>,
  );
}

describe("SidebarUserMenu", () => {
  it("opens a menu with only authorized account actions", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    const menu = screen.getByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Meu perfil" })).toHaveAttribute(
      "href",
      "/profile",
    );
    expect(within(menu).queryByRole("menuitem", { name: "Administração" })).not.toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Sair" })).toBeInTheDocument();
  });

  it("closes on Escape and returns focus to the trigger", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: /menu da conta/i });
    await user.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("Sair calls logout, clears tokens, and never throws on a failed request", async () => {
    vi.mocked(authApi.logout).mockRejectedValueOnce(new Error("network"));
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    await user.click(screen.getByRole("menuitem", { name: "Sair" }));
    await waitFor(() => expect(authSession.clearTokens).toHaveBeenCalledTimes(1));
  });

  it("Tab closes the menu and restores focus to the trigger, like Escape", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: /menu da conta/i });
    await user.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.keyboard("{Tab}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("ArrowDown and ArrowUp cycle focus between the menu items, wrapping at both ends", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    const menu = screen.getByRole("menu");
    const items = within(menu).getAllByRole("menuitem");
    expect(items[0]).toHaveFocus();

    await user.keyboard("{ArrowDown}");
    expect(items[1]).toHaveFocus();

    await user.keyboard("{ArrowUp}");
    expect(items[0]).toHaveFocus();

    // Wraps backward past the first item to the last.
    await user.keyboard("{ArrowUp}");
    expect(items[items.length - 1]).toHaveFocus();
  });

  it("clicking Meu perfil closes the menu without restoring focus to the trigger", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: /menu da conta/i });
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Meu perfil" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).not.toHaveFocus();
  });

  it("closes when a mousedown lands outside the menu and its trigger, without restoring focus", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: /menu da conta/i });
    await user.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Fora" }));

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).not.toHaveFocus();
  });

  it("a key other than Escape/Tab/ArrowDown/ArrowUp does nothing", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    const menu = screen.getByRole("menu");
    const items = within(menu).getAllByRole("menuitem");
    expect(items[0]).toHaveFocus();

    await user.keyboard("a");

    expect(screen.getByRole("menu")).toBeInTheDocument();
    expect(items[0]).toHaveFocus();
  });

  it("ArrowDown moves to the first item when no item currently has focus", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    const menu = screen.getByRole("menu");
    const items = within(menu).getAllByRole("menuitem");
    act(() => items[0].blur());
    expect(items[0]).not.toHaveFocus();

    fireEvent.keyDown(menu, { key: "ArrowDown" });

    expect(items[0]).toHaveFocus();
  });

  it("opens the menu above the trigger when there is room above but not below", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: /menu da conta/i });
    vi.spyOn(trigger, "getBoundingClientRect").mockReturnValue({
      top: 300,
      bottom: 320,
      left: 10,
      right: 30,
      width: 20,
      height: 20,
      x: 10,
      y: 300,
      toJSON: () => ({}),
    });

    await user.click(trigger);

    // opensAbove: 300 (top) - 6 (gap) - 120 (fallback height) = 174, >= the 8px margin.
    expect(screen.getByRole("menu")).toHaveStyle({ top: "174px" });
  });
});
