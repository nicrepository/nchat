import { render, screen, waitFor, within } from "@testing-library/react";
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
      <SidebarUserMenu />
    </MemoryRouter>,
  );
}

describe("SidebarUserMenu", () => {
  it("opens a menu with Meu perfil, Administração and Sair", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    const menu = screen.getByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Meu perfil" })).toHaveAttribute(
      "href",
      "/profile",
    );
    expect(within(menu).getByRole("menuitem", { name: "Administração" })).toHaveAttribute(
      "href",
      "/admin/users",
    );
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
});
