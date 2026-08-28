import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import NotificationsSettingsPage from "./NotificationsSettingsPage";
import { getSoundNotificationMode } from "../chat/soundPreference";

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

function renderPage() {
  return render(<NotificationsSettingsPage />);
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
