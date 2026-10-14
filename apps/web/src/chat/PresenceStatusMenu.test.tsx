import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fetchMock = vi.fn();
vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => fetchMock(...args),
}));

vi.mock("./chatSocket", () => ({
  acquireChatSocket: () => ({
    release: () => {},
    send: () => false,
    isOpen: () => true,
    generation: () => 1,
  }),
}));

import { _resetPresenceStore } from "./presence";
import PresenceStatusMenu from "./PresenceStatusMenu";

const EXPIRES = "2099-10-01T21:00:00.000Z";

function envelope(
  state: string | null,
  expiresAt: string | null = state ? EXPIRES : null,
  writable = true,
) {
  return { data: { state, expires_at: expiresAt, writable } };
}

/** Renders the menu once the server's first answer has been read. */
async function renderMenu() {
  const view = render(
    <MemoryRouter>
      <PresenceStatusMenu selfId="me" displayName="Álvaro Neto">
        {(trigger, summary) => <button {...trigger}>{summary.label}</button>}
      </PresenceStatusMenu>
    </MemoryRouter>,
  );
  await act(async () => {});
  return view;
}

function trigger() {
  return screen.getByRole("button", { name: /definir status/i });
}

function lastRequest(): { method?: string; body?: Record<string, unknown> } {
  const init = fetchMock.mock.calls.at(-1)?.[1] as RequestInit | undefined;
  return { method: init?.method, body: init?.body ? JSON.parse(init.body as string) : undefined };
}

beforeEach(() => {
  fetchMock.mockReset();
  _resetPresenceStore();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("PresenceStatusMenu", () => {
  it("offers the six states, the custom status path and focuses the first one", async () => {
    fetchMock.mockResolvedValue(envelope(null));
    const user = userEvent.setup();
    await renderMenu();

    await user.click(trigger());
    const menu = screen.getByRole("menu", { name: "Status" });
    const states = within(menu)
      .getAllByRole("menuitemradio")
      .map((item) => item.textContent);
    expect(states).toEqual([
      "Disponível",
      "Ocupado",
      "Não perturbe",
      "Volto já",
      "Ausente",
      "Aparecer offline",
    ]);
    expect(within(menu).getByText("Álvaro Neto")).toBeInTheDocument();
    expect(
      within(menu).getByRole("menuitem", { name: "Definir mensagem de status" }),
    ).toHaveAttribute("href", "/profile");
    expect(
      within(menu).queryByRole("menuitem", { name: "Redefinir status" }),
    ).not.toBeInTheDocument();
    expect(within(menu).getAllByRole("menuitemradio")[0]).toHaveFocus();
    expect(trigger()).toHaveAttribute("aria-expanded", "true");
  });

  it("is operable entirely by keyboard, and Escape returns focus to the trigger", async () => {
    fetchMock.mockResolvedValue(envelope(null));
    const user = userEvent.setup();
    await renderMenu();

    trigger().focus();
    await user.keyboard("{Enter}");
    const items = screen.getAllByRole("menuitemradio");
    expect(items[0]).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(items[1]).toHaveFocus();
    await user.keyboard("{ArrowUp}{ArrowUp}");
    expect(screen.getByRole("menuitem", { name: "Definir mensagem de status" })).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger()).toHaveFocus();
  });

  it("chooses a state and a duration with Space and Enter, and sends a concrete instant", async () => {
    fetchMock.mockResolvedValueOnce(envelope(null));
    const user = userEvent.setup();
    await renderMenu();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    trigger().focus();
    await user.keyboard("{Enter}");
    await user.keyboard("{ArrowDown}");
    fetchMock.mockResolvedValueOnce(envelope("busy"));
    await user.keyboard(" ");
    expect(screen.getByText("Por quanto tempo?")).toBeInTheDocument();
    const before = Date.now();
    await user.keyboard("{Enter}");
    const after = Date.now();

    const request = lastRequest();
    expect(request.method).toBe("PUT");
    expect(request.body?.state).toBe("busy");
    const expiresAt = Date.parse(String(request.body?.expires_at));
    expect(expiresAt).toBeGreaterThanOrEqual(before + 3_600_000);
    expect(expiresAt).toBeLessThanOrEqual(after + 3_600_000);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger()).toHaveFocus();
    expect(trigger()).toHaveAccessibleName("Álvaro Neto, Ocupado. Definir status");
  });

  it("returns from the durations to the states", async () => {
    fetchMock.mockResolvedValue(envelope(null));
    const user = userEvent.setup();
    await renderMenu();

    await user.click(trigger());
    await user.click(screen.getByRole("menuitemradio", { name: "Não perturbe" }));
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "1 hora",
      "4 horas",
      "Hoje",
      "Esta semana",
      "Personalizado…",
      "Voltar",
    ]);
    await user.click(screen.getByRole("menuitem", { name: "Voltar" }));
    expect(screen.getAllByRole("menuitemradio")).toHaveLength(6);
  });

  it("accepts a custom end only in the future", async () => {
    fetchMock.mockResolvedValueOnce(envelope(null));
    const user = userEvent.setup();
    await renderMenu();

    await user.click(trigger());
    await user.click(screen.getByRole("menuitemradio", { name: "Volto já" }));
    await user.click(screen.getByRole("menuitem", { name: "Personalizado…" }));
    const dialog = screen.getByRole("dialog", { name: "Duração personalizada" });
    const input = within(dialog).getByLabelText("Volto já até");
    expect(input).toHaveFocus();

    await user.click(within(dialog).getByRole("button", { name: "Aplicar" }));
    expect(within(dialog).getByText("Escolha um horário futuro.")).toBeInTheDocument();
    expect(input).toHaveAttribute("aria-invalid", "true");

    const future = new Date(Date.now() + 2 * 3_600_000);
    const pad = (value: number) => String(value).padStart(2, "0");
    const local = `${future.getFullYear()}-${pad(future.getMonth() + 1)}-${pad(future.getDate())}T${pad(future.getHours())}:${pad(future.getMinutes())}`;
    await user.type(input, local);
    expect(within(dialog).queryByText("Escolha um horário futuro.")).not.toBeInTheDocument();
    fetchMock.mockResolvedValueOnce(envelope("brb"));
    await user.click(within(dialog).getByRole("button", { name: "Aplicar" }));
    expect(lastRequest().body).toEqual({ state: "brb", expires_at: new Date(local).toISOString() });
  });

  it("goes back from the custom end to the durations", async () => {
    fetchMock.mockResolvedValue(envelope(null));
    const user = userEvent.setup();
    await renderMenu();
    await user.click(trigger());
    await user.click(screen.getByRole("menuitemradio", { name: "Ausente" }));
    await user.click(screen.getByRole("menuitem", { name: "Personalizado…" }));
    await user.click(screen.getByRole("button", { name: "Voltar" }));
    expect(screen.getByText("Por quanto tempo?")).toBeInTheDocument();
  });

  it("returns to automatic presence", async () => {
    fetchMock.mockResolvedValueOnce(envelope("appear_offline"));
    const user = userEvent.setup();
    await renderMenu();
    await waitFor(() =>
      expect(trigger()).toHaveAccessibleName("Álvaro Neto, Aparecer offline. Definir status"),
    );

    await user.click(trigger());
    expect(screen.getByRole("menuitemradio", { name: "Aparecer offline" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    fetchMock.mockResolvedValueOnce(envelope(null));
    await user.click(screen.getByRole("menuitem", { name: "Redefinir status" }));
    expect(lastRequest().method).toBe("DELETE");
  });

  it("says when a change failed, shows the server's state again and retries", async () => {
    fetchMock.mockResolvedValueOnce(envelope(null));
    const user = userEvent.setup();
    await renderMenu();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    await user.click(trigger());
    await user.click(screen.getByRole("menuitemradio", { name: "Ocupado" }));
    fetchMock.mockRejectedValueOnce(new Error("503")).mockResolvedValueOnce(envelope(null));
    await user.click(screen.getByRole("menuitem", { name: "Hoje" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível atualizar seu status.",
    );
    expect(trigger()).toHaveAccessibleName("Álvaro Neto. Definir status");

    fetchMock.mockResolvedValueOnce(envelope("busy"));
    await user.click(screen.getByRole("button", { name: "Tentar novamente" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(lastRequest().body?.state).toBe("busy");
  });

  it("closes on a press outside and on a second press of the trigger", async () => {
    fetchMock.mockResolvedValue(envelope(null));
    const user = userEvent.setup();
    await renderMenu();

    await user.click(trigger());
    await user.click(document.body);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();

    await user.click(trigger());
    await user.click(trigger());
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("is a bottom sheet on a narrow screen, positioned by the stylesheet alone", async () => {
    fetchMock.mockResolvedValue(envelope(null));
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query.includes("max-width"),
      media: query,
    }));
    const user = userEvent.setup();
    await renderMenu();

    await user.click(trigger());
    expect(screen.getByRole("menu").style.top).toBe("");
    await user.click(screen.getByRole("menuitem", { name: "Definir mensagem de status" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  // MEDIUM-8: the date-time field keeps its own arrows.
  it("leaves the arrows to the custom date-time field, and Escape still closes", async () => {
    fetchMock.mockResolvedValue(envelope(null));
    const user = userEvent.setup();
    await renderMenu();
    await user.click(trigger());
    await user.click(screen.getByRole("menuitemradio", { name: "Volto já" }));
    await user.click(screen.getByRole("menuitem", { name: "Personalizado…" }));
    const input = screen.getByLabelText("Volto já até");
    expect(input).toHaveFocus();

    for (const key of ["ArrowUp", "ArrowDown"]) {
      const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
      input.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(false);
      expect(input).toHaveFocus();
    }

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger()).toHaveFocus();
  });

  it("offers no change while the server does not accept one, and still shows the state", async () => {
    fetchMock.mockResolvedValueOnce(envelope("dnd", EXPIRES, false));
    const user = userEvent.setup();
    await renderMenu();
    expect(trigger()).toHaveAccessibleName("Álvaro Neto, Não perturbe. Definir status");

    await user.click(trigger());
    const menu = screen.getByRole("menu", { name: "Status" });
    expect(within(menu).queryAllByRole("menuitemradio")).toHaveLength(0);
    expect(within(menu).queryByRole("menuitem", { name: "Redefinir status" })).toBeNull();
    expect(
      within(menu).getByText("Alterar o status não está disponível no momento."),
    ).toBeInTheDocument();
    expect(
      within(menu).getByRole("menuitem", { name: "Definir mensagem de status" }),
    ).toHaveFocus();
  });
});
