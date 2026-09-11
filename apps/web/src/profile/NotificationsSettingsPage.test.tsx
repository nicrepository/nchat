import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import NotificationsSettingsPage from "./NotificationsSettingsPage";
import { getSoundNotificationMode } from "../chat/soundPreference";
import type { AppShellOutletContext } from "../chat/AppShell";
import type { SidebarState } from "../chat/useChatSidebar";
import type { Channel, DMConversation } from "../chat/chatTypes";

const { mockGetRingtoneEnabled, mockSetRingtoneEnabled, mockPlayRingtonePreview } = vi.hoisted(
  () => ({
    mockGetRingtoneEnabled: vi.fn(() => true),
    mockSetRingtoneEnabled: vi.fn(),
    mockPlayRingtonePreview: vi.fn(),
  }),
);

vi.mock("../calls/incomingCallRingtone", () => ({
  getIncomingCallRingtoneEnabled: mockGetRingtoneEnabled,
  setIncomingCallRingtoneEnabled: mockSetRingtoneEnabled,
  playIncomingCallRingtonePreview: mockPlayRingtonePreview,
}));

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.clearAllMocks();
});

/**
 * The page reads the one sidebar instance through the outlet context AppShell
 * publishes, so every render here supplies one. Ready-and-empty by default:
 * the general-notification suites below are about browser permission and local
 * sound preferences, not about conversations.
 */
function makeContext(
  overrides: {
    status?: "loading" | "error" | "ready";
    channels?: Channel[];
    dms?: DMConversation[];
    retry?: AppShellOutletContext["retry"];
    setMuted?: AppShellOutletContext["setMuted"];
  } = {},
): AppShellOutletContext {
  const status = overrides.status ?? "ready";
  const state: SidebarState =
    status === "ready"
      ? {
          status: "ready",
          currentUserId: "user-1",
          workspaceId: "workspace-1",
          channels: overrides.channels ?? [],
          dms: overrides.dms ?? [],
          categories: [],
        }
      : status === "error"
        ? { status: "error", error: "boom" }
        : { status: "loading" };
  return {
    state,
    retry: overrides.retry ?? vi.fn(async () => {}),
    setPinned: vi.fn(async () => {}),
    markRead: vi.fn(),
    renameChannel: vi.fn(async () => {}),
    renameGroup: vi.fn(async () => {}),
    setMuted: overrides.setMuted ?? vi.fn(async () => {}),
    leaveConversation: vi.fn(async () => {}),
    inAppAlert: null,
    dismissInAppAlert: vi.fn(),
  };
}

function channel(id: string, extra: Partial<Channel> = {}): Channel {
  return { id, name: id, type: "public", canWrite: true, ...extra };
}

function dm(id: string, type: DMConversation["type"], extra: Partial<DMConversation> = {}) {
  return { id, name: id, type, participants: [], ...extra } satisfies DMConversation;
}

function renderPage(context: AppShellOutletContext = makeContext()) {
  return render(
    <MemoryRouter initialEntries={["/profile/notifications"]}>
      <Routes>
        <Route path="/profile" element={<Outlet context={context} />}>
          <Route path="notifications" element={<NotificationsSettingsPage />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

/** The body of the card whose heading is `title` — so a channel assertion cannot pass on a group row. */
function card(title: string): HTMLElement {
  return screen.getByRole("heading", { name: title }).closest("section") as HTMLElement;
}

describe("NotificationsSettingsPage — sound notification mode", () => {
  const offOption = () => screen.getByLabelText(/desativado/i) as HTMLInputElement;
  const allOption = () => screen.getByLabelText(/todas as mensagens/i) as HTMLInputElement;
  const mentionsOption = () => screen.getByLabelText(/somente menções/i) as HTMLInputElement;

  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    localStorage.clear();
  });

  it("defaults to 'all' checked when nothing is persisted", () => {
    renderPage();
    expect(screen.getByRole("heading", { name: "Notificações" })).toHaveProperty("tagName", "H2");
    expect(allOption().checked).toBe(true);
    expect(offOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(false);
  });

  it("reflects a previously persisted 'off' mode on mount", () => {
    localStorage.setItem("nchat.notifications.sound.mode", "off");
    renderPage();
    expect(offOption().checked).toBe(true);
  });

  it("reflects a previously persisted 'mentions' mode on mount", () => {
    localStorage.setItem("nchat.notifications.sound.mode", "mentions");
    renderPage();
    expect(mentionsOption().checked).toBe(true);
  });

  it("migrates the legacy boolean preference (false -> off) when no mode is persisted yet", () => {
    localStorage.setItem("nchat.notifications.sound.enabled", "false");
    renderPage();
    expect(offOption().checked).toBe(true);
  });

  it("selecting 'off' persists the mode", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(offOption());

    expect(offOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("off");
  });

  it("selecting 'mentions' persists the mode", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(mentionsOption());

    expect(mentionsOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("mentions");
  });

  it("selecting 'all' back persists the mode", async () => {
    const user = userEvent.setup();
    localStorage.setItem("nchat.notifications.sound.mode", "off");
    renderPage();

    await user.click(allOption());

    expect(allOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("all");
  });

  it("is reachable and selectable via each option's associated label", async () => {
    renderPage();

    await userEvent.click(screen.getByText("Somente menções"));

    expect(mentionsOption().checked).toBe(true);
    expect(allOption().checked).toBe(false);
  });

  it("only one option is checked at a time (radio group behaves as a group)", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(offOption());
    expect(offOption().checked).toBe(true);
    expect(allOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(false);

    await user.click(mentionsOption());
    expect(offOption().checked).toBe(false);
    expect(allOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(true);
  });
});

describe("NotificationsSettingsPage — incoming call ringtone", () => {
  const ringtoneOption = () =>
    screen.getByRole("checkbox", {
      name: "Tocar som para chamadas recebidas",
    }) as HTMLInputElement;

  beforeEach(() => {
    mockGetRingtoneEnabled.mockReturnValue(true);
    mockSetRingtoneEnabled.mockClear();
    mockPlayRingtonePreview.mockClear();
  });

  it("defaults to enabled independently from the message sound mode", () => {
    localStorage.setItem("nchat.notifications.sound.mode", "off");
    renderPage();

    expect(ringtoneOption()).toBeChecked();
    expect(screen.getByLabelText(/desativado/i)).toBeChecked();
  });

  it("reflects a disabled persisted preference", () => {
    mockGetRingtoneEnabled.mockReturnValue(false);
    renderPage();

    expect(ringtoneOption()).not.toBeChecked();
  });

  it("persists changes through its accessible label", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(ringtoneOption());

    expect(ringtoneOption()).not.toBeChecked();
    expect(mockSetRingtoneEnabled).toHaveBeenCalledOnce();
    expect(mockSetRingtoneEnabled).toHaveBeenCalledWith(false);
  });

  it("previews exactly once even when automatic ringtone is disabled", async () => {
    const user = userEvent.setup();
    mockGetRingtoneEnabled.mockReturnValue(false);
    renderPage();

    await user.click(screen.getByRole("button", { name: "Testar som de chamada" }));

    expect(mockPlayRingtonePreview).toHaveBeenCalledOnce();
  });
});

describe("NotificationsSettingsPage — 'Menções e mensagens diretas' sound mode", () => {
  const offOption = () => screen.getByLabelText(/desativado/i) as HTMLInputElement;
  const allOption = () => screen.getByLabelText(/todas as mensagens/i) as HTMLInputElement;
  const mentionsOption = () => screen.getByLabelText(/^somente menções$/i) as HTMLInputElement;
  const mentionsAndDmsOption = () =>
    screen.getByLabelText(/menções e mensagens diretas/i) as HTMLInputElement;

  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    localStorage.clear();
  });

  it("is a normal, equal, non-nested radio option alongside the other three", () => {
    renderPage();

    expect(mentionsAndDmsOption().type).toBe("radio");
    expect(mentionsAndDmsOption()).not.toBeDisabled();
    expect(mentionsAndDmsOption().checked).toBe(false);
  });

  it("selecting it persists the mode", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(mentionsAndDmsOption());

    expect(mentionsAndDmsOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("mentions_and_dms");
  });

  it("reflects a previously persisted value on mount", () => {
    localStorage.setItem("nchat.notifications.sound.mode", "mentions_and_dms");
    renderPage();

    expect(mentionsAndDmsOption().checked).toBe(true);
  });

  it("migrates the legacy 'mentions' + DM-flag combination to this mode on mount", () => {
    localStorage.setItem("nchat.notifications.sound.mode", "mentions");
    localStorage.setItem("nchat.notifications.sound.dmWithoutMention", "true");
    renderPage();

    expect(mentionsAndDmsOption().checked).toBe(true);
    expect(mentionsOption().checked).toBe(false);
  });

  it("behaves as part of the same mutually exclusive group as the other three options", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(mentionsAndDmsOption());
    expect(offOption().checked).toBe(false);
    expect(allOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(false);
    expect(mentionsAndDmsOption().checked).toBe(true);

    await user.click(mentionsOption());
    expect(mentionsAndDmsOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(true);
  });

  it("is reachable and selectable via its associated label", async () => {
    renderPage();

    await userEvent.click(screen.getByText("Menções e mensagens diretas"));

    expect(mentionsAndDmsOption().checked).toBe(true);
  });
});

describe("NotificationsSettingsPage — browser notification permission", () => {
  /** jsdom does not implement Notification — stub it per test like elsewhere in the suite. */
  class MockNotification {
    static permission: NotificationPermission;
    static requestPermission = vi.fn<() => Promise<NotificationPermission>>();
  }

  function stubNotification(permission: NotificationPermission, secureContext = true) {
    MockNotification.permission = permission;
    MockNotification.requestPermission = vi.fn<() => Promise<NotificationPermission>>();
    vi.stubGlobal("isSecureContext", secureContext);
    vi.stubGlobal("Notification", MockNotification);
    return MockNotification;
  }

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const enableBtn = () =>
    screen.queryByRole("button", { name: /ativar notificações do navegador/i });

  it("shows the enable button and prompt only when permission is 'default'", () => {
    stubNotification("default");
    renderPage();

    expect(enableBtn()).not.toBeNull();
    expect(screen.getByText(/ative notificações do navegador/i)).toBeInTheDocument();
  });

  it("reflects 'granted' with no button", () => {
    stubNotification("granted");
    renderPage();

    expect(enableBtn()).toBeNull();
    expect(screen.getByText(/notificações do navegador estão ativadas/i)).toBeInTheDocument();
  });

  it("reflects 'denied' with instructions to change the browser's own setting, no retry/enable button", () => {
    stubNotification("denied");
    renderPage();

    expect(enableBtn()).toBeNull();
    expect(screen.queryByRole("button", { name: /tentar novamente/i })).toBeNull();
    expect(screen.getByText(/bloqueadas/i)).toBeInTheDocument();
    expect(screen.getByText(/configurações do seu navegador/i)).toBeInTheDocument();
  });

  it("'denied' never calls Notification.requestPermission(), on mount or on opening the help", async () => {
    const user = userEvent.setup();
    const mock = stubNotification("denied");
    renderPage();

    await user.click(screen.getByRole("button", { name: /como ativar notificações/i }));

    expect(mock.requestPermission).not.toHaveBeenCalled();
  });

  it("'Como ativar notificações' expands step-by-step instructions, and collapses again on a second click", async () => {
    const user = userEvent.setup();
    stubNotification("denied");
    renderPage();

    const helpBtn = screen.getByRole("button", { name: /como ativar notificações/i });
    expect(helpBtn).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(/ícone de cadeado/i)).not.toBeInTheDocument();

    await user.click(helpBtn);

    expect(helpBtn).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/ícone de cadeado/i)).toBeInTheDocument();
    expect(screen.getByText(/localize a permissão/i)).toBeInTheDocument();
    expect(screen.getByText(/permitir/i)).toBeInTheDocument();
    expect(screen.getByText(/recarregue a página/i)).toBeInTheDocument();

    await user.click(helpBtn);

    expect(helpBtn).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(/ícone de cadeado/i)).not.toBeInTheDocument();
  });

  it("re-reads the permission on window focus (denied -> granted)", async () => {
    const mock = stubNotification("denied");
    renderPage();
    expect(screen.getByText(/bloqueadas/i)).toBeInTheDocument();

    mock.permission = "granted";
    fireEvent(window, new Event("focus"));

    await waitFor(() =>
      expect(screen.getByText(/notificações do navegador estão ativadas/i)).toBeInTheDocument(),
    );
  });

  it("re-reads the permission on window focus (denied -> default) and brings back the enable button", async () => {
    const mock = stubNotification("denied");
    renderPage();
    expect(enableBtn()).toBeNull();

    mock.permission = "default";
    fireEvent(window, new Event("focus"));

    await waitFor(() => expect(enableBtn()).not.toBeNull());
  });

  it("removes the focus and visibilitychange listeners on unmount", () => {
    const windowAdd = vi.spyOn(window, "addEventListener");
    const windowRemove = vi.spyOn(window, "removeEventListener");
    const documentAdd = vi.spyOn(document, "addEventListener");
    const documentRemove = vi.spyOn(document, "removeEventListener");
    stubNotification("denied");
    const { unmount } = renderPage();

    const focusHandler = windowAdd.mock.calls.find(([type]) => type === "focus")?.[1];
    const visibilityHandler = documentAdd.mock.calls.find(
      ([type]) => type === "visibilitychange",
    )?.[1];
    expect(focusHandler).toBeDefined();
    expect(visibilityHandler).toBeDefined();

    unmount();

    expect(windowRemove).toHaveBeenCalledWith("focus", focusHandler);
    expect(documentRemove).toHaveBeenCalledWith("visibilitychange", visibilityHandler);
  });

  it("reflects a genuinely unsupported browser (secure context, no API) with no button", () => {
    vi.unstubAllGlobals();
    vi.stubGlobal("isSecureContext", true);
    renderPage();

    expect(enableBtn()).toBeNull();
    expect(screen.getByText(/não tem suporte a notificações nativas/i)).toBeInTheDocument();
  });

  it("shows the insecure-origin message — not the blocked/denied UI — when the origin isn't secure", () => {
    stubNotification("denied", false);
    renderPage();

    expect(
      screen.getByText(/não estão disponíveis neste endereço.*https ou localhost/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/bloqueadas/i)).not.toBeInTheDocument();
    expect(enableBtn()).toBeNull();
    expect(screen.queryByRole("button", { name: /como ativar notificações/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /tentar novamente/i })).toBeNull();
  });

  it("never calls Notification.requestPermission() on mount", () => {
    const mock = stubNotification("default");
    renderPage();

    expect(mock.requestPermission).not.toHaveBeenCalled();
  });

  it("requests permission only on explicit click and updates the UI with the result", async () => {
    const user = userEvent.setup();
    const mock = stubNotification("default");
    mock.requestPermission.mockResolvedValue("granted");
    renderPage();

    await user.click(enableBtn()!);

    expect(mock.requestPermission).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.getByText(/notificações do navegador estão ativadas/i)).toBeInTheDocument(),
    );
    expect(enableBtn()).toBeNull();
  });

  it("re-reads the permission on visibilitychange without requiring a reload", async () => {
    const mock = stubNotification("default");
    renderPage();
    expect(enableBtn()).not.toBeNull();

    mock.permission = "granted";
    fireEvent(document, new Event("visibilitychange"));

    await waitFor(() =>
      expect(screen.getByText(/notificações do navegador estão ativadas/i)).toBeInTheDocument(),
    );
  });
});

describe("NotificationsSettingsPage — page composition (issue #729)", () => {
  it("never renders the prototype's static MVP scope notice", () => {
    renderPage();

    expect(screen.queryByText(/no mvp, as notificações/i)).toBeNull();
    expect(screen.queryByText(/regras avançadas de expediente/i)).toBeNull();
    expect(screen.queryByText(/web push, badge no navegador/i)).toBeNull();
  });

  it("renders one card per block, under the page's own h2, as h3s in the prototype's order", () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "Notificações" })).toHaveProperty("tagName", "H2");
    const cards = screen
      .getAllByRole("heading", { level: 3 })
      .map((heading) => heading.textContent);
    expect(cards).toEqual([
      "Notificações gerais",
      "Notificações por canal",
      "Notificações por grupos",
      "E-mail digest",
    ]);
  });

  it("gives each card an accessible name taken from its own heading", () => {
    renderPage();

    for (const title of [
      "Notificações gerais",
      "Notificações por canal",
      "Notificações por grupos",
      "E-mail digest",
    ]) {
      expect(screen.getByRole("region", { name: title })).toBeInTheDocument();
    }
  });

  it("offers no badge control, because nothing in the product implements badges", () => {
    renderPage();

    expect(screen.queryByLabelText(/badge/i)).toBeNull();
  });
});

describe("NotificationsSettingsPage — per-channel notifications", () => {
  it("lists only the channels the sidebar state holds, each as a switch", () => {
    renderPage(makeContext({ channels: [channel("geral"), channel("infra")] }));

    const channels = card("Notificações por canal");
    expect(within(channels).getByRole("checkbox", { name: "Notificações de geral" })).toBeChecked();
    expect(within(channels).getByRole("checkbox", { name: "Notificações de infra" })).toBeChecked();
    expect(within(channels).getAllByRole("checkbox")).toHaveLength(2);
  });

  it("reflects an already muted channel as off", () => {
    renderPage(makeContext({ channels: [channel("infra", { muted: true })] }));

    expect(screen.getByRole("checkbox", { name: "Notificações de infra" })).not.toBeChecked();
  });

  it("muting goes through the canonical setMuted flow with the conversation's own target", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn(async () => {});
    renderPage(makeContext({ channels: [channel("infra")], setMuted }));

    await user.click(screen.getByRole("checkbox", { name: "Notificações de infra" }));

    expect(setMuted).toHaveBeenCalledExactlyOnceWith({ kind: "channel", targetId: "infra" }, true);
  });

  it("unmuting sends muted:false through the same flow", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn(async () => {});
    renderPage(makeContext({ channels: [channel("infra", { muted: true })], setMuted }));

    await user.click(screen.getByRole("checkbox", { name: "Notificações de infra" }));

    expect(setMuted).toHaveBeenCalledExactlyOnceWith({ kind: "channel", targetId: "infra" }, false);
  });

  it("a refused mutation reports the failure and leaves the switch on the persisted value", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn(async () => {
      throw new Error("403");
    });
    renderPage(makeContext({ channels: [channel("infra")], setMuted }));

    await user.click(screen.getByRole("checkbox", { name: "Notificações de infra" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível atualizar as notificações de infra. Tente novamente.",
    );
    // The sidebar rolls its own optimistic write back, so the row keeps showing
    // what the server actually holds rather than what the click asked for.
    expect(screen.getByRole("checkbox", { name: "Notificações de infra" })).toBeChecked();
  });

  it("clears a previous failure when the next attempt starts", async () => {
    const user = userEvent.setup();
    const setMuted = vi
      .fn<AppShellOutletContext["setMuted"]>()
      .mockRejectedValueOnce(new Error("503"))
      .mockResolvedValueOnce(undefined);
    renderPage(makeContext({ channels: [channel("infra")], setMuted }));

    const toggle = () => screen.getByRole("checkbox", { name: "Notificações de infra" });
    await user.click(toggle());
    expect(await screen.findByRole("alert")).toBeInTheDocument();

    await user.click(toggle());

    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  });

  it("shows the general channel as unavailable, in words, and never calls setMuted for it", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn(async () => {});
    renderPage(makeContext({ channels: [channel("geral", { isGeneral: true })], setMuted }));

    const toggle = screen.getByRole("checkbox", { name: "Notificações de geral" });
    expect(toggle).toBeDisabled();
    expect(screen.getByText("O canal geral não pode ser silenciado.")).toBeInTheDocument();
    expect(toggle).toHaveAccessibleDescription("O canal geral não pode ser silenciado.");

    await user.click(toggle);

    expect(setMuted).not.toHaveBeenCalled();
  });

  it("is operable from the keyboard", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn(async () => {});
    renderPage(makeContext({ channels: [channel("infra")], setMuted }));

    const toggle = screen.getByRole("checkbox", { name: "Notificações de infra" });
    toggle.focus();
    await user.keyboard(" ");

    expect(setMuted).toHaveBeenCalledExactlyOnceWith({ kind: "channel", targetId: "infra" }, true);
  });
});

describe("NotificationsSettingsPage — per-group notifications", () => {
  it("lists groups in their own card and never a 1:1 conversation", () => {
    renderPage(
      makeContext({
        channels: [channel("geral")],
        dms: [dm("grupo-a", "group"), dm("ana", "1:1"), dm("grupo-b", "group")],
      }),
    );

    const groups = card("Notificações por grupos");
    expect(within(groups).getByRole("checkbox", { name: "Notificações de grupo-a" })).toBeChecked();
    expect(within(groups).getByRole("checkbox", { name: "Notificações de grupo-b" })).toBeChecked();
    expect(within(groups).getAllByRole("checkbox")).toHaveLength(2);
    expect(screen.queryByRole("checkbox", { name: "Notificações de ana" })).toBeNull();
  });

  it("keeps channels out of the groups card and groups out of the channels card", () => {
    renderPage(makeContext({ channels: [channel("geral")], dms: [dm("grupo-a", "group")] }));

    expect(
      within(card("Notificações por canal")).queryByRole("checkbox", {
        name: "Notificações de grupo-a",
      }),
    ).toBeNull();
    expect(
      within(card("Notificações por grupos")).queryByRole("checkbox", {
        name: "Notificações de geral",
      }),
    ).toBeNull();
  });

  it("mutes a group through the same canonical flow, as a dm target", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn(async () => {});
    renderPage(makeContext({ dms: [dm("grupo-a", "group")], setMuted }));

    await user.click(screen.getByRole("checkbox", { name: "Notificações de grupo-a" }));

    expect(setMuted).toHaveBeenCalledExactlyOnceWith({ kind: "dm", targetId: "grupo-a" }, true);
  });

  it("reflects an already muted group as off", () => {
    renderPage(makeContext({ dms: [dm("grupo-a", "group", { muted: true })] }));

    expect(screen.getByRole("checkbox", { name: "Notificações de grupo-a" })).not.toBeChecked();
  });

  it("keeps its failure separate from the channels card", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn(async () => {
      throw new Error("503");
    });
    renderPage(
      makeContext({ channels: [channel("infra")], dms: [dm("grupo-a", "group")], setMuted }),
    );

    await user.click(screen.getByRole("checkbox", { name: "Notificações de grupo-a" }));

    await waitFor(() =>
      expect(within(card("Notificações por grupos")).getByRole("alert")).toBeInTheDocument(),
    );
    expect(within(card("Notificações por canal")).queryByRole("alert")).toBeNull();
  });
});

describe("NotificationsSettingsPage — conversation list states", () => {
  it("reports loading per card without blocking the preferences that already work", () => {
    renderPage(makeContext({ status: "loading" }));

    expect(screen.getAllByRole("status", { name: "" })).toHaveLength(2);
    expect(screen.getAllByText("Carregando suas conversas…")).toHaveLength(2);
    expect(
      screen.getByRole("checkbox", { name: "Tocar som para chamadas recebidas" }),
    ).toBeEnabled();
    expect(screen.getByRole("button", { name: "Testar som de chamada" })).toBeEnabled();
  });

  it("offers a retry per card on failure and leaves the ringtone usable", async () => {
    const user = userEvent.setup();
    const retry = vi.fn(async () => {});
    renderPage(makeContext({ status: "error", retry }));

    const retryButtons = screen.getAllByRole("button", { name: "Tentar novamente" });
    expect(retryButtons).toHaveLength(2);
    expect(screen.getAllByText("Não foi possível carregar suas conversas.")).toHaveLength(2);

    await user.click(retryButtons[0]!);

    expect(retry).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Testar som de chamada" })).toBeEnabled();
  });

  it("states each empty list in its own words", () => {
    renderPage();

    expect(screen.getByText("Você ainda não participa de nenhum canal.")).toBeInTheDocument();
    expect(screen.getByText("Você ainda não participa de nenhum grupo.")).toBeInTheDocument();
  });
});

describe("NotificationsSettingsPage — e-mail digest", () => {
  it("renders the prototype's structure with every control unavailable", () => {
    renderPage();

    const digest = card("E-mail digest");
    for (const frequency of ["Imediato", "Diário", "Semanal"]) {
      const option = within(digest).getByLabelText(frequency);
      expect(option).toBeDisabled();
      expect(option).not.toBeChecked();
    }
    expect(within(digest).getByLabelText("Horário preferido")).toBeDisabled();
    expect(within(digest).getByRole("button", { name: "Salvar preferências" })).toBeDisabled();
  });

  it("explains the unavailability in text, and links it to the controls", () => {
    renderPage();

    const digest = card("E-mail digest");
    expect(
      within(digest).getByText(/o resumo por e-mail ainda não está disponível/i),
    ).toBeInTheDocument();
    expect(
      within(digest).getByRole("button", { name: "Salvar preferências" }),
    ).toHaveAccessibleDescription(/nada preenchido aqui é salvo/i);
  });

  it("never reports a save, and writes nothing to local storage", async () => {
    const user = userEvent.setup();
    localStorage.clear();
    renderPage();

    const digest = card("E-mail digest");
    await user.click(within(digest).getByRole("button", { name: "Salvar preferências" }));

    // No confirmation of any kind, because nothing was written anywhere.
    expect(within(digest).queryByRole("status")).toBeNull();
    expect(within(digest).queryByRole("alert")).toBeNull();
    expect(screen.queryByText(/preferências salvas/i)).toBeNull();
    expect(localStorage.length).toBe(0);
  });
});

/** A promise this test controls, so a write can be observed while it is still in flight. */
function deferred<T = void>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("NotificationsSettingsPage — one write per conversation at a time (issue #729)", () => {
  const toggleFor = (name: string) =>
    screen.getByRole("checkbox", { name: `Notificações de ${name}` });

  it("holds the row unavailable while its own write is in flight and starts no second request", async () => {
    const user = userEvent.setup();
    const pending = deferred();
    const setMuted = vi.fn<AppShellOutletContext["setMuted"]>().mockReturnValue(pending.promise);
    renderPage(makeContext({ channels: [channel("infra")], setMuted }));

    await user.click(toggleFor("infra"));

    expect(setMuted).toHaveBeenCalledOnce();
    await waitFor(() => expect(toggleFor("infra")).toBeDisabled());
    expect(toggleFor("infra")).toHaveAttribute("aria-busy", "true");
    // Unavailability is stated in words, not only by a dimmed control.
    expect(screen.getByText("Salvando…")).toBeInTheDocument();

    await user.click(toggleFor("infra"));

    expect(setMuted).toHaveBeenCalledOnce();

    pending.resolve();

    await waitFor(() => expect(toggleFor("infra")).toBeEnabled());
    expect(screen.queryByText("Salvando…")).toBeNull();
    expect(toggleFor("infra")).toHaveAttribute("aria-busy", "false");
  });

  it("leaves every other channel and group operable while one write is pending", async () => {
    const user = userEvent.setup();
    const pending = deferred();
    const setMuted = vi
      .fn<AppShellOutletContext["setMuted"]>()
      .mockReturnValueOnce(pending.promise)
      .mockResolvedValue(undefined);
    renderPage(
      makeContext({
        channels: [channel("infra"), channel("avisos")],
        dms: [dm("grupo-a", "group")],
        setMuted,
      }),
    );

    await user.click(toggleFor("infra"));
    await waitFor(() => expect(toggleFor("infra")).toBeDisabled());

    expect(toggleFor("avisos")).toBeEnabled();
    expect(toggleFor("grupo-a")).toBeEnabled();

    await user.click(toggleFor("grupo-a"));

    expect(setMuted).toHaveBeenCalledTimes(2);
    expect(setMuted).toHaveBeenLastCalledWith({ kind: "dm", targetId: "grupo-a" }, true);
    // The channel is still the only row waiting on anything.
    expect(screen.getAllByText("Salvando…")).toHaveLength(1);
    expect(toggleFor("grupo-a")).toBeEnabled();

    pending.resolve();
    await waitFor(() => expect(toggleFor("infra")).toBeEnabled());
  });

  it("shows the canonical value after a confirmed write, never a local copy of the click", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn<AppShellOutletContext["setMuted"]>().mockResolvedValue(undefined);
    renderPage(makeContext({ channels: [channel("infra")], setMuted }));

    await user.click(toggleFor("infra"));

    expect(setMuted).toHaveBeenCalledExactlyOnceWith({ kind: "channel", targetId: "infra" }, true);
    // The sidebar state this page renders from is what it renders: the page
    // keeps no preference of its own that could outlive a refused write.
    await waitFor(() => expect(toggleFor("infra")).toBeEnabled());
    expect(toggleFor("infra")).toBeChecked();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("frees the row again after a refusal, and the next attempt goes through", async () => {
    const user = userEvent.setup();
    const setMuted = vi
      .fn<AppShellOutletContext["setMuted"]>()
      .mockRejectedValueOnce(new Error("503"))
      .mockResolvedValueOnce(undefined);
    renderPage(makeContext({ channels: [channel("infra")], setMuted }));

    await user.click(toggleFor("infra"));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível atualizar as notificações de infra. Tente novamente.",
    );
    await waitFor(() => expect(toggleFor("infra")).toBeEnabled());
    expect(toggleFor("infra")).toBeChecked();

    await user.click(toggleFor("infra"));

    expect(setMuted).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  });

  it("a pending channel write does not block a group write, and the reverse holds too", async () => {
    const user = userEvent.setup();
    const channelWrite = deferred();
    const groupWrite = deferred();
    const setMuted = vi
      .fn<AppShellOutletContext["setMuted"]>()
      .mockReturnValueOnce(channelWrite.promise)
      .mockReturnValueOnce(groupWrite.promise);
    renderPage(
      makeContext({ channels: [channel("infra")], dms: [dm("grupo-a", "group")], setMuted }),
    );

    await user.click(toggleFor("infra"));
    await waitFor(() => expect(toggleFor("infra")).toBeDisabled());
    await user.click(toggleFor("grupo-a"));
    await waitFor(() => expect(toggleFor("grupo-a")).toBeDisabled());

    expect(setMuted.mock.calls.map(([target]) => target)).toEqual([
      { kind: "channel", targetId: "infra" },
      { kind: "dm", targetId: "grupo-a" },
    ]);

    // Each row is released by its own write, independently of the other's.
    groupWrite.resolve();
    await waitFor(() => expect(toggleFor("grupo-a")).toBeEnabled());
    expect(toggleFor("infra")).toBeDisabled();

    channelWrite.resolve();
    await waitFor(() => expect(toggleFor("infra")).toBeEnabled());
  });
});
