import { expect, test, type Page } from "@playwright/test";

/**
 * E2E coverage for the profile/settings redesign (issue #672).
 *
 * Mirrors auth.spec.ts's helper style (page.route per endpoint, sessionStorage
 * token seeding under "nchat_at") and responsive-layout.spec.ts's viewport
 * pattern. Every mocked request/response shape below is read directly from
 * the real client code rather than invented:
 *   - apps/web/src/profile/profileApi.ts       (GET/PATCH /auth/me, POST/DELETE /auth/me/avatar)
 *   - apps/web/src/profile/sessionsApi.ts      (GET/DELETE /auth/me/sessions[/:id])
 *   - services/auth-service/internal/http/session_handler.go (cross-user DELETE returns a bare 404, same as "not found")
 */

const CURRENT_USER_ID = "e2e-user";
const CURRENT_USER_NAME = "E2E User";

// A 1x1 transparent PNG, so an <img src> pointed at a mocked avatar URL
// actually loads instead of erroring out and falling back to initials.
const ONE_PIXEL_PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=",
  "base64",
);

interface MockProfile {
  id: string;
  display_name: string;
  avatar_url?: string;
  job_title?: string;
  bio?: string;
  timezone?: string;
  custom_status?: string;
}

function defaultProfile(overrides: Partial<MockProfile> = {}): MockProfile {
  return { id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, ...overrides };
}

async function seedSession(page: Page) {
  await page.addInitScript(() => {
    sessionStorage.setItem("nchat_at", "e2e-at");
  });
}

/**
 * GET/PATCH /api/auth/me and POST/DELETE /api/auth/me/avatar, sharing one
 * mutable profile so refreshSelfProfile()'s follow-up GET (called by
 * AvatarDialog/ProfileEditDialog after a confirmed mutation) reflects it —
 * exactly the contract selfProfile.ts relies on.
 */
async function mockProfileApi(page: Page, overrides: Partial<MockProfile> = {}) {
  const state: MockProfile = defaultProfile(overrides);

  await page.route("**/api/auth/me", async (route) => {
    const request = route.request();
    if (request.method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: state }),
      });
      return;
    }
    if (request.method() === "PATCH") {
      const body = request.postDataJSON() as Record<string, unknown>;
      if ("display_name" in body) state.display_name = String(body.display_name);
      if ("job_title" in body) state.job_title = String(body.job_title);
      if ("bio" in body) state.bio = String(body.bio);
      if ("timezone" in body) state.timezone = String(body.timezone);
      if ("custom_status" in body) state.custom_status = String(body.custom_status);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: state }),
      });
      return;
    }
    await route.continue();
  });

  await page.route("**/api/auth/me/avatar", async (route) => {
    const method = route.request().method();
    if (method === "POST") {
      state.avatar_url = "/media/avatars/e2e-user-uploaded.png";
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: { avatar_url: state.avatar_url } }),
      });
      return;
    }
    if (method === "DELETE") {
      delete state.avatar_url;
      await route.fulfill({ status: 204 });
      return;
    }
    await route.continue();
  });

  return state;
}

/** Wire shapes read from chatApi.ts's SidebarChannelResponse / SidebarDMResponse. */
interface MockSidebarChannel {
  id: string;
  slug: string;
  display_name: string;
  type: "public" | "private";
  can_write: boolean;
  is_general?: boolean;
  muted?: boolean;
  /** The level half of the preference (issue #136), independent of `muted`. */
  notification_level?: MockNotificationLevel;
}

interface MockSidebarDM {
  id: string;
  /** The server's own discriminator, exactly as chat.dm_conversations.type spells it. */
  type: "direct" | "group";
  name: string;
  muted?: boolean;
  notification_level?: MockNotificationLevel;
}

type MockNotificationLevel = "all" | "mentions_replies";
type MockNotificationMode = MockNotificationLevel | "muted";

async function mockChatSidebarApi(
  page: Page,
  conversations: {
    channels?: MockSidebarChannel[];
    dms?: MockSidebarDM[];
    /**
     * The issue #136 rollout gate, as the server publishes it on this payload.
     *
     * Defaulted to `true` so the scenarios below exercise the granular control.
     * Production defaults to `false`; the gated scenario at the end of this
     * block covers what the page renders then.
     */
    notificationLevelsEnabled?: boolean;
  } = {},
) {
  // Mutable so the refetch that follows a confirmed mute returns what the
  // server now holds — which is what makes "reload and it is still there" a
  // real assertion rather than a re-render of the optimistic guess.
  const channels = conversations.channels ?? [];
  const dms = conversations.dms ?? [];

  await page.route("**/api/chat/sidebar", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          current_user_id: CURRENT_USER_ID,
          workspace: { id: "e2e-workspace", name: "E2E", slug: "e2e" },
          conversation_notification_levels_enabled: conversations.notificationLevelsEnabled ?? true,
          channels,
          dm_conversations: dms,
        },
      }),
    }),
  );
  // fetchSidebarData() awaits this alongside the sidebar via Promise.all; an
  // unmocked route here would reject that whole call and never render the
  // sidebar footer this spec's avatar assertions depend on.
  await page.route("**/api/chat/channel-categories", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { groups: [] } }),
    }),
  );

  return { channels, dms };
}

interface PreferenceRequest {
  method: string;
  pathname: string;
  /** The body of a PUT to the canonical route; absent for the mute shortcut. */
  mode?: string;
}

/**
 * The two preference surfaces chat-service actually serves, over one store:
 *
 *   POST/DELETE /api/chat/{channels|dm}/{id}/mute
 *     the sidebar's shortcut (chatApi.ts's setConversationMuted). No body at
 *     all, and — this is the contract issue #136 adds — it writes `muted` and
 *     leaves `notification_level` exactly as it was.
 *
 *   PUT /api/chat/{channels|dm}/{id}/notification-preference
 *     the canonical write (setConversationNotificationMode), `{"mode": ...}`,
 *     where `all` and `mentions_replies` set the level and clear the mute, and
 *     `muted` sets the mute and preserves the level.
 *
 * Both write straight into the arrays the sidebar route serves, so the refetch
 * the hook performs after a confirmed write — and a reload — observe the
 * persisted value rather than the optimistic guess. The translation here is the
 * server's own, so what the spec asserts is the real contract and not a shape
 * invented for the test.
 */
async function mockNotificationPreferenceApi(
  page: Page,
  store: { channels: MockSidebarChannel[]; dms: MockSidebarDM[] },
) {
  const requests: PreferenceRequest[] = [];
  let failNext = false;

  const rowFor = (kind: "channel" | "dm", id: string) =>
    kind === "channel"
      ? store.channels.find((channel) => channel.id === id)
      : store.dms.find((dm) => dm.id === id);

  const refuse = async (route: import("@playwright/test").Route) => {
    failNext = false;
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "internal", message: "boom" } }),
    });
  };

  const targetIdFrom = (pathname: string, segmentsFromEnd: number) =>
    decodeURIComponent(
      pathname.split("/").slice(-segmentsFromEnd, -(segmentsFromEnd - 1))[0] ?? "",
    );

  const handleMute =
    (kind: "channel" | "dm") => async (route: import("@playwright/test").Route) => {
      const method = route.request().method();
      const { pathname } = new URL(route.request().url());
      requests.push({ method, pathname });
      if (failNext) {
        await refuse(route);
        return;
      }
      const row = rowFor(kind, targetIdFrom(pathname, 2));
      // The level is untouched, which is the whole invariant: the shortcut owns
      // one dimension and the profile owns the other.
      if (row) row.muted = method === "POST";
      await route.fulfill({ status: 204 });
    };

  const handlePreference =
    (kind: "channel" | "dm") => async (route: import("@playwright/test").Route) => {
      const method = route.request().method();
      const { pathname } = new URL(route.request().url());
      const body = route.request().postDataJSON() as { mode?: string } | null;
      requests.push({ method, pathname, mode: body?.mode });
      if (failNext) {
        await refuse(route);
        return;
      }
      const mode = body?.mode as MockNotificationMode | undefined;
      if (mode !== "all" && mode !== "mentions_replies" && mode !== "muted") {
        await route.fulfill({
          status: 400,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "bad_request", message: "mode is invalid" } }),
        });
        return;
      }
      const row = rowFor(kind, targetIdFrom(pathname, 2));
      if (row) {
        if (mode === "muted") {
          row.muted = true;
        } else {
          row.muted = false;
          row.notification_level = mode;
        }
      }
      await route.fulfill({ status: 204 });
    };

  await page.route("**/api/chat/channels/*/mute", handleMute("channel"));
  await page.route("**/api/chat/dm/*/mute", handleMute("dm"));
  await page.route("**/api/chat/channels/*/notification-preference", handlePreference("channel"));
  await page.route("**/api/chat/dm/*/notification-preference", handlePreference("dm"));

  return {
    requests,
    failOnce() {
      failNext = true;
    },
    /** What the "server" holds, so a test can state the persisted end state. */
    stored(kind: "channel" | "dm", id: string) {
      const row = rowFor(kind, id);
      return { muted: Boolean(row?.muted), level: row?.notification_level ?? "all" };
    },
  };
}

/** Serves any avatar URL this spec mocks with a real, loadable image. */
async function mockAvatarImages(page: Page) {
  await page.route("**/media/avatars/**", (route) =>
    route.fulfill({ status: 200, contentType: "image/png", body: ONE_PIXEL_PNG }),
  );
}

interface MockSession {
  id: string;
  device_id: string | null;
  created_at: string;
  last_seen_at: string;
  idle_expires_at: string;
  absolute_expires_at: string | null;
  revoked_at: string | null;
  ip_address?: string;
  user_agent?: string;
  current: boolean;
}

function makeSession(overrides: Partial<MockSession> & { id: string }): MockSession {
  return {
    device_id: null,
    created_at: "2026-08-01T10:00:00Z",
    last_seen_at: "2026-08-27T10:00:00Z",
    idle_expires_at: "2026-08-28T10:00:00Z",
    absolute_expires_at: null,
    revoked_at: null,
    // What the real endpoint serves: the mask, never the address (issue #859).
    ip_address: "203.0.*.*",
    user_agent: "Mozilla/5.0 (E2E)",
    current: false,
    ...overrides,
  };
}

/** GET/DELETE /api/auth/me/sessions[/:id], including the standard HTTP envelope. */
async function mockSessionsApi(page: Page, initial: MockSession[]) {
  let sessions = [...initial];

  await page.route("**/api/auth/me/sessions", async (route) => {
    const method = route.request().method();
    if (method === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { data: sessions, pagination: { limit: 50, next_cursor: null } },
        }),
      });
      return;
    }
    if (method === "DELETE") {
      sessions = sessions.filter((session) => session.current);
      await route.fulfill({ status: 204 });
      return;
    }
    await route.continue();
  });

  await page.route("**/api/auth/me/sessions/*", async (route) => {
    if (route.request().method() !== "DELETE") {
      await route.continue();
      return;
    }
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split("/").pop() ?? "");
    // Real handler behaviour (session_handler.go DeleteMySession): any id
    // that is not this caller's own active session — unknown or someone
    // else's — comes back as the exact same 404.
    if (!sessions.some((session) => session.id === id)) {
      await route.fulfill({
        status: 404,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "not_found", message: "session not found" } }),
      });
      return;
    }
    sessions = sessions.filter((session) => session.id !== id);
    await route.fulfill({ status: 204 });
  });

  return {
    remove(sessionId: string) {
      sessions = sessions.filter((session) => session.id !== sessionId);
    },
  };
}

/**
 * Rows are addressed by the friendly browser label the UI derives from the
 * User-Agent (issue #854), not by the raw string, which is no longer shown.
 */
function sessionRow(page: Page, browserLabel: string) {
  return page.getByTestId("session-row").filter({ hasText: browserLabel });
}

const UA = {
  firefoxLinux: "Mozilla/5.0 (X11; Linux x86_64; rv:142.0) Gecko/20100101 Firefox/142.0",
  chromeWindows:
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36",
  safariIphone:
    "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1",
  edgeWindows:
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0",
} as const;

test.describe("Profile & account settings (#672)", () => {
  test.beforeEach(async ({ page }) => {
    await seedSession(page);
    await mockProfileApi(page);
    await mockChatSidebarApi(page);
    await mockAvatarImages(page);
    await page.goto("/profile");
    await expect(page.getByTestId("chat-shell")).toBeVisible();
  });

  test("opens /profile with the sidebar still present, edits and saves the display name without a reload", async ({
    page,
  }) => {
    await expect(page.getByTestId("chat-shell")).toBeVisible();
    await page.getByRole("button", { name: "Editar" }).click();
    const dialog = page.getByRole("dialog", { name: "Editar perfil" });
    await expect(dialog).toBeVisible();
    await page.getByLabel("Nome de exibição").fill("Novo Nome");
    await dialog.getByRole("button", { name: /salvar alterações/i }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole("heading", { name: "Novo Nome" })).toBeVisible();
    // The shared self-profile cache also drives the sidebar footer — proof
    // this landed without a reload rather than a page-local copy of it.
    await expect(page.locator(".chat-sidebar__user-name")).toHaveText("Novo Nome");
  });

  test("keeps the shared sidebar mounted across chat -> profile -> chat", async ({ page }) => {
    await page.goto("/chat");
    await expect(page.locator(".chat-sidebar__user-name")).toHaveText(CURRENT_USER_NAME);
    let sidebarRequests = 0;
    page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/chat/sidebar") sidebarRequests += 1;
    });

    await page.getByRole("button", { name: "Menu da conta" }).click();
    await page.getByRole("menuitem", { name: "Meu perfil" }).click();
    await expect(page).toHaveURL("/profile");
    await page.goBack();
    await expect(page).toHaveURL("/chat");

    expect(sidebarRequests).toBe(0);
  });

  test("changes avatar via AvatarDialog and it reflects in the sidebar footer without reload", async ({
    page,
  }) => {
    await page.getByRole("button", { name: "Trocar foto" }).click();
    const dialog = page.getByRole("dialog", { name: "Trocar foto" });
    await expect(dialog).toBeVisible();
    await dialog.locator('input[type="file"]').setInputFiles({
      name: "avatar.png",
      mimeType: "image/png",
      buffer: ONE_PIXEL_PNG,
    });
    await dialog.getByRole("button", { name: "Enviar avatar" }).click();
    await expect(dialog).toBeHidden();

    const newSrc = "/media/avatars/e2e-user-uploaded.png";
    await expect(page.locator("img.profile-identity__avatar-img")).toHaveAttribute("src", newSrc);
    await expect(
      page.locator(".chat-sidebar__user-row img.chat-sidebar__avatar-img"),
    ).toHaveAttribute("src", newSrc);
  });

  test("removing the avatar falls back to initials", async ({ page }) => {
    await mockProfileApi(page, { avatar_url: "/media/avatars/e2e-user-existing.png" });
    await page.reload();
    await expect(page.locator("img.profile-identity__avatar-img")).toBeVisible();
    await expect(
      page.locator(".chat-sidebar__user-row img.chat-sidebar__avatar-img"),
    ).toBeVisible();

    await page.getByRole("button", { name: "Trocar foto" }).click();
    const dialog = page.getByRole("dialog", { name: "Trocar foto" });
    await dialog.getByRole("button", { name: "Remover avatar" }).click();
    await expect(dialog).toBeHidden();

    await expect(page.locator("img.profile-identity__avatar-img")).toHaveCount(0);
    await expect(page.locator(".profile-identity__avatar")).toContainText("EU");
    await expect(page.locator(".chat-sidebar__user-row img.chat-sidebar__avatar-img")).toHaveCount(
      0,
    );
  });

  test("navigates all four sections via tabs, and each is a real deep link surviving reload", async ({
    page,
  }) => {
    await mockSessionsApi(page, [makeSession({ id: "current", current: true })]);
    const sections: Array<[string, string]> = [
      ["/profile", "Perfil"],
      ["/profile/notifications", "Notificações"],
      ["/profile/security", "Segurança"],
      ["/profile/sessions", "Sessões"],
    ];
    for (const [path, heading] of sections) {
      await page.goto(path);
      await expect(page).toHaveURL(path);
      await expect(page.getByRole("heading", { level: 2, name: heading })).toBeVisible();
      await page.reload();
      await expect(page).toHaveURL(path);
      await expect(page.getByRole("heading", { level: 2, name: heading })).toBeVisible();
    }
  });

  test("back/forward preserves the active section", async ({ page }) => {
    await mockSessionsApi(page, [makeSession({ id: "current", current: true })]);
    await page.getByRole("tab", { name: "Notificações" }).click();
    await page.getByRole("tab", { name: "Sessões" }).click();
    await page.goBack();
    await expect(page).toHaveURL("/profile/notifications");
    await page.goForward();
    await expect(page).toHaveURL("/profile/sessions");
  });

  test("notifications: sound mode and call ringtone are independent toggles", async ({ page }) => {
    await page.goto("/profile/notifications");
    const mentionsOnly = page.getByRole("radio", { name: "Somente menções" });
    const allMessages = page.getByRole("radio", { name: "Todas as mensagens" });
    const ringtone = page.getByRole("checkbox", { name: "Tocar som para chamadas recebidas" });

    // Real defaults (soundPreference.ts/incomingCallRingtone.ts): "all" mode, ringtone on.
    await expect(allMessages).toBeChecked();
    await expect(ringtone).toBeChecked();

    await mentionsOnly.check();
    await expect(mentionsOnly).toBeChecked();
    await expect(ringtone).toBeChecked(); // untouched by the sound-mode change

    await ringtone.uncheck();
    await expect(ringtone).not.toBeChecked();
    await expect(mentionsOnly).toBeChecked(); // untouched by the ringtone change
  });

  /** The mode select of one conversation, by the accessible name the page gives it. */
  const modeSelect = (page: Page, name: string) =>
    page.getByRole("combobox", { name: `Notificações de ${name}` });

  /**
   * The sidebar's own quick action on a channel row, reached exactly as a person
   * reaches it: open the row's menu, then pick the item.
   *
   * The sidebar is mounted on /profile as well — ProfileSettingsShell is nested
   * inside AppShell and forwards its context — so this is the real shortcut
   * running beside the settings page, which is what makes the convergence
   * assertions below about two surfaces rather than about one.
   *
   * Labels read from conversationActions.ts: "Silenciar notificações" and, once
   * silenced, "Ativar notificações".
   */
  async function useSidebarMuteShortcut(page: Page, channelName: string, item: string) {
    await page.getByRole("button", { name: `Mais opções para canal ${channelName}` }).click();
    await page.getByRole("menuitem", { name: item }).click();
  }

  function conversationFixture() {
    return {
      channels: [
        {
          id: "ch-general",
          slug: "geral",
          display_name: "geral",
          type: "public" as const,
          can_write: true,
          is_general: true,
        },
        {
          id: "ch-infra",
          slug: "infraestrutura",
          display_name: "infraestrutura",
          type: "public" as const,
          can_write: true,
        },
      ],
      dms: [
        { id: "dm-1on1", type: "direct" as const, name: "Juliane" },
        { id: "dm-group", type: "group" as const, name: "Squad" },
      ],
    };
  }

  // Structure first, because it is what the two cards are for: channels and
  // groups are separate blocks, a 1:1 is in neither, and the general channel
  // does not offer the state the server refuses.
  test("notifications: channels and groups are separate blocks, and #geral offers no forbidden state", async ({
    page,
  }) => {
    const store = await mockChatSidebarApi(page, conversationFixture());
    await mockNotificationPreferenceApi(page, store);
    await page.goto("/profile/notifications");

    const channelsCard = page.getByRole("region", { name: "Notificações por canal" });
    const groupsCard = page.getByRole("region", { name: "Notificações por grupos" });

    await expect(
      channelsCard.getByRole("combobox", { name: "Notificações de infraestrutura" }),
    ).toBeVisible();
    await expect(groupsCard.getByRole("combobox", { name: "Notificações de Squad" })).toBeVisible();
    // A 1:1 conversation is not a group, and is in neither block.
    await expect(page.getByRole("combobox", { name: "Notificações de Juliane" })).toHaveCount(0);
    await expect(groupsCard.getByRole("combobox")).toHaveCount(1);
    // The general channel is listed and configurable, but never silenceable —
    // the server refuses that in SQL, so the option is not offered either.
    const general = channelsCard.getByRole("combobox", { name: "Notificações de geral" });
    await expect(general).toBeEnabled();
    await expect(general.locator("option")).toHaveCount(2);
    await expect(general.locator('option[value="muted"]')).toHaveCount(0);
    await expect(channelsCard.getByText(/O canal geral não pode ser silenciado/)).toBeVisible();
    // A group is an ordinary conversation and does offer it.
    await expect(
      groupsCard
        .getByRole("combobox", { name: "Notificações de Squad" })
        .locator('option[value="muted"]'),
    ).toHaveCount(1);
  });

  // Scenario A of issue #136, end to end and in the order the issue states it:
  // the profile narrows a channel, the sidebar silences it, and turning
  // notifications back on returns the profile to the level that was chosen.
  test("notifications: a channel goes all -> mentions_replies -> muted -> the level it had", async ({
    page,
  }) => {
    const store = await mockChatSidebarApi(page, conversationFixture());
    const api = await mockNotificationPreferenceApi(page, store);
    await page.goto("/profile/notifications");

    const infra = modeSelect(page, "infraestrutura");
    await expect(infra).toHaveValue("all");

    // 1. Profile: mentions and replies.
    await infra.selectOption("mentions_replies");
    await expect
      .poll(() => api.requests)
      .toContainEqual({
        method: "PUT",
        pathname: "/api/chat/channels/ch-infra/notification-preference",
        mode: "mentions_replies",
      });
    // 2. A reload keeps it, so what is on screen is what the server holds.
    await page.reload();
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("mentions_replies");

    // 3. The sidebar's shortcut silences it. Its own contract: POST /mute, no
    //    body, and it must not overwrite the level.
    await useSidebarMuteShortcut(page, "infraestrutura", "Silenciar notificações");
    await expect
      .poll(() => api.requests)
      .toContainEqual({ method: "POST", pathname: "/api/chat/channels/ch-infra/mute" });
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("muted");
    await page.reload();
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("muted");
    // The level survived the mute, which is what the restore below rests on.
    expect(api.stored("channel", "ch-infra")).toEqual({ muted: true, level: "mentions_replies" });

    // 4. Turning notifications back on restores the level, never the default.
    await useSidebarMuteShortcut(page, "infraestrutura", "Ativar notificações");
    await expect
      .poll(() => api.requests)
      .toContainEqual({ method: "DELETE", pathname: "/api/chat/channels/ch-infra/mute" });
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("mentions_replies");
    await page.reload();
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("mentions_replies");
  });

  // The same round trip starting from the default, which is the other case the
  // issue names: silencing and restoring must land back on "all" rather than on
  // whatever was last selected somewhere else.
  test("notifications: silencing and restoring a channel left on all returns it to all", async ({
    page,
  }) => {
    const store = await mockChatSidebarApi(page, conversationFixture());
    const api = await mockNotificationPreferenceApi(page, store);
    await page.goto("/profile/notifications");

    await expect(modeSelect(page, "infraestrutura")).toHaveValue("all");

    await useSidebarMuteShortcut(page, "infraestrutura", "Silenciar notificações");
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("muted");
    await useSidebarMuteShortcut(page, "infraestrutura", "Ativar notificações");

    await expect(modeSelect(page, "infraestrutura")).toHaveValue("all");
    await page.reload();
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("all");
    expect(api.stored("channel", "ch-infra")).toEqual({ muted: false, level: "all" });
  });

  // Scenario B: a group proves the same contract on the dm prefix, which is the
  // path a copy-paste gets wrong silently.
  test("notifications: a group follows the same contract under the dm prefix", async ({ page }) => {
    const store = await mockChatSidebarApi(page, conversationFixture());
    const api = await mockNotificationPreferenceApi(page, store);
    await page.goto("/profile/notifications");

    const squad = modeSelect(page, "Squad");
    await expect(squad).toHaveValue("all");

    await squad.selectOption("mentions_replies");
    await expect
      .poll(() => api.requests)
      .toContainEqual({
        method: "PUT",
        pathname: "/api/chat/dm/dm-group/notification-preference",
        mode: "mentions_replies",
      });
    await page.reload();
    await expect(modeSelect(page, "Squad")).toHaveValue("mentions_replies");

    await modeSelect(page, "Squad").selectOption("muted");
    await expect
      .poll(() => api.requests)
      .toContainEqual({
        method: "PUT",
        pathname: "/api/chat/dm/dm-group/notification-preference",
        mode: "muted",
      });
    await page.reload();
    await expect(modeSelect(page, "Squad")).toHaveValue("muted");
    // Selecting "silenced" from the profile preserves the level too, exactly
    // like the sidebar's shortcut does.
    expect(api.stored("dm", "dm-group")).toEqual({ muted: true, level: "mentions_replies" });

    await modeSelect(page, "Squad").selectOption("all");
    await page.reload();
    await expect(modeSelect(page, "Squad")).toHaveValue("all");
    expect(api.stored("dm", "dm-group")).toEqual({ muted: false, level: "all" });
  });

  test("notifications: a refused mutation never leaves the select lying, and can be retried", async ({
    page,
  }) => {
    const store = await mockChatSidebarApi(page, conversationFixture());
    const api = await mockNotificationPreferenceApi(page, store);
    await page.goto("/profile/notifications");

    const infra = modeSelect(page, "infraestrutura");
    await expect(infra).toHaveValue("all");

    api.failOnce();
    await infra.selectOption("muted");

    await expect(page.getByRole("alert")).toContainText(
      "Não foi possível atualizar as notificações de infraestrutura",
    );
    // Rolled back to the persisted value: a refusal must not leave the row
    // showing something the server never accepted.
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("all");
    await page.reload();
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("all");

    // The retry goes through, and the row stops claiming a failure.
    await modeSelect(page, "infraestrutura").selectOption("muted");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("muted");
    await page.reload();
    await expect(modeSelect(page, "infraestrutura")).toHaveValue("muted");
  });

  // One write per conversation, and only for that conversation: a pending write
  // must not freeze the rest of the page.
  test("notifications: one conversation's pending write leaves the others operable", async ({
    page,
  }) => {
    const store = await mockChatSidebarApi(page, conversationFixture());
    const api = await mockNotificationPreferenceApi(page, store);
    // Hold the first canonical write open, so the row can be observed mid-flight.
    let release: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    let firstSeen = false;
    await page.route("**/api/chat/channels/ch-infra/notification-preference", async (route) => {
      if (!firstSeen) {
        firstSeen = true;
        await held;
      }
      await route.fallback();
    });
    await page.goto("/profile/notifications");

    await modeSelect(page, "infraestrutura").selectOption("muted");

    await expect(modeSelect(page, "infraestrutura")).toBeDisabled();
    await expect(page.getByText("Salvando…")).toBeVisible();
    // The other rows never waited on it.
    await expect(modeSelect(page, "Squad")).toBeEnabled();
    await modeSelect(page, "Squad").selectOption("mentions_replies");
    // The group's write reached the server while the channel's was still held
    // open, which is the property: the key is the conversation, so nothing
    // queues behind anything else.
    await expect
      .poll(() => api.requests)
      .toContainEqual({
        method: "PUT",
        pathname: "/api/chat/dm/dm-group/notification-preference",
        mode: "mentions_replies",
      });

    release?.();
    await expect(modeSelect(page, "infraestrutura")).toBeEnabled();
    // And the held one did arrive, once it was let through.
    await expect
      .poll(() => api.requests)
      .toContainEqual({
        method: "PUT",
        pathname: "/api/chat/channels/ch-infra/notification-preference",
        mode: "muted",
      });
  });
  // Phase one of the rollout: the schema and every reader understand the
  // granular model, and the writer is shut. What the page must offer then is the
  // binary control this product has always had — and it must never call the
  // granular endpoint, which would answer 503.
  test("notifications: with the rollout gate shut, the page falls back to the binary control", async ({
    page,
  }) => {
    const fixture = conversationFixture();
    const store = await mockChatSidebarApi(page, {
      ...fixture,
      notificationLevelsEnabled: false,
    });
    const api = await mockNotificationPreferenceApi(page, store);
    await page.goto("/profile/notifications");

    const channelsCard = page.getByRole("region", { name: "Notificações por canal" });
    const groupsCard = page.getByRole("region", { name: "Notificações por grupos" });

    // No select at all, in either card.
    await expect(channelsCard.getByRole("combobox")).toHaveCount(0);
    await expect(groupsCard.getByRole("combobox")).toHaveCount(0);
    const infra = channelsCard.getByRole("checkbox", { name: "Notificações de infraestrutura" });
    await expect(infra).toBeChecked();
    // The general channel stays unavailable, and says why.
    await expect(
      channelsCard.getByRole("checkbox", { name: "Notificações de geral" }),
    ).toBeDisabled();

    // Silencing goes through the mute shortcut, and it really persists.
    await infra.click();
    await expect
      .poll(() => api.requests)
      .toContainEqual({ method: "POST", pathname: "/api/chat/channels/ch-infra/mute" });
    await page.reload();
    await expect(
      channelsCard.getByRole("checkbox", { name: "Notificações de infraestrutura" }),
    ).not.toBeChecked();

    // ...and back on again.
    await channelsCard.getByRole("checkbox", { name: "Notificações de infraestrutura" }).click();
    await expect
      .poll(() => api.requests)
      .toContainEqual({ method: "DELETE", pathname: "/api/chat/channels/ch-infra/mute" });

    // Nothing ever reached the granular endpoint.
    expect(api.requests.filter((request) => request.method === "PUT")).toEqual([]);
  });

  // The sidebar's own shortcut is not gated either: it is the same capability,
  // and phase one depends on it.
  test("notifications: the sidebar mute shortcut works with the gate shut", async ({ page }) => {
    const fixture = conversationFixture();
    const store = await mockChatSidebarApi(page, {
      ...fixture,
      notificationLevelsEnabled: false,
    });
    const api = await mockNotificationPreferenceApi(page, store);
    await page.goto("/profile/notifications");

    await useSidebarMuteShortcut(page, "infraestrutura", "Silenciar notificações");
    await expect
      .poll(() => api.requests)
      .toContainEqual({ method: "POST", pathname: "/api/chat/channels/ch-infra/mute" });
    await expect(
      page.getByRole("checkbox", { name: "Notificações de infraestrutura" }),
    ).not.toBeChecked();

    await useSidebarMuteShortcut(page, "infraestrutura", "Ativar notificações");
    await expect(
      page.getByRole("checkbox", { name: "Notificações de infraestrutura" }),
    ).toBeChecked();
    expect(api.requests.filter((request) => request.method === "PUT")).toEqual([]);
  });
  test("security: no local password/MFA form exists, and the Keycloak link is present when configured", async ({
    page,
  }) => {
    await page.goto("/profile/security");
    await expect(page.getByRole("heading", { level: 2, name: "Segurança" })).toBeVisible();
    await expect(page.getByLabel(/senha/i)).toHaveCount(0);
    await expect(page.getByText(/totp|autenticador|passkey/i)).toHaveCount(0);

    // VITE_KEYCLOAK_ACCOUNT_URL is a build/dev-server-time env var this spec
    // cannot inject via route mocking, so both real states are honoured: the
    // link when the environment has it configured, the honest fallback note
    // otherwise — never a dead link, per SecuritySettingsPage.tsx.
    const manageLink = page.getByRole("link", { name: /gerenciar segurança da conta/i });
    if (await manageLink.count()) {
      await expect(manageLink).toHaveAttribute("target", "_blank");
      await expect(manageLink).toHaveAttribute("rel", /noopener/);
    } else {
      await expect(page.getByText(/não está configurado neste ambiente/i)).toBeVisible();
    }
  });

  test("sessions: identifies current session, revokes a remote one, and revoke-all-others preserves current", async ({
    page,
  }) => {
    await mockSessionsApi(page, [
      makeSession({ id: "current", current: true, user_agent: UA.firefoxLinux }),
      makeSession({ id: "s2", user_agent: UA.chromeWindows }),
      makeSession({ id: "s3", user_agent: UA.safariIphone }),
    ]);
    await page.goto("/profile/sessions");
    await expect(page.getByTestId("session-row")).toHaveCount(3);
    await expect(page.getByText("Sessão atual")).toHaveCount(1);
    const devices = page.getByRole("region", { name: "Dispositivos conectados" });
    await expect(devices.getByTestId("session-row")).toHaveCount(3);
    await expect(sessionRow(page, "Firefox 142")).toContainText("Firefox 142 · LinuxSessão atual");
    await expect(sessionRow(page, "Firefox 142")).toContainText("Ativa agora");
    await expect(sessionRow(page, "Firefox 142").getByRole("button")).toHaveCount(0);
    await expect(sessionRow(page, "Chrome 152")).toContainText("Chrome 152 · Windows 10/11");
    await expect(sessionRow(page, "Chrome 152")).toContainText("Último acesso em");
    await expect(sessionRow(page, "Chrome 152")).toContainText("IP 203.0.*.* (aproximado)");

    await sessionRow(page, "Chrome 152").getByRole("button", { name: "Revogar sessão" }).click();
    const revokeOneDialog = page.getByRole("dialog", { name: "Revogar sessão?" });
    await expect(revokeOneDialog).toBeVisible();
    await revokeOneDialog.getByRole("button", { name: "Revogar sessão" }).click();
    await expect(revokeOneDialog).toBeHidden();
    await expect(page.getByTestId("session-row")).toHaveCount(2);
    await expect(sessionRow(page, "Chrome 152")).toHaveCount(0);

    await page.getByRole("button", { name: "Revogar todas as outras" }).click();
    const revokeAllDialog = page.getByRole("dialog", { name: "Revogar outras sessões?" });
    await expect(revokeAllDialog).toBeVisible();
    await revokeAllDialog.getByRole("button", { name: "Revogar sessões" }).click();
    await expect(revokeAllDialog).toBeHidden();

    await expect(page.getByTestId("session-row")).toHaveCount(1);
    await expect(page.getByText("Sessão atual")).toBeVisible();
    await expect(page.getByRole("button", { name: "Revogar todas as outras" })).toHaveCount(0);
  });

  test("sessions: renders the retry response after refreshing an expired access token", async ({
    page,
  }) => {
    const sequence: string[] = [];
    const currentSession = makeSession({
      id: "session-current",
      current: true,
      user_agent: UA.firefoxLinux,
    });

    await page.route("**/api/auth/refresh", async (route) => {
      if (!sequence.includes("refresh 200")) sequence.push("refresh 200");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          access_token: "refreshed-e2e-at",
          token_type: "Bearer",
          expires_in: 900,
        }),
      });
    });
    await page.route("**/api/auth/me/sessions", async (route) => {
      const authorization = route.request().headers().authorization;
      if (authorization !== "Bearer refreshed-e2e-at") {
        expect(authorization).toBe("Bearer e2e-at");
        if (!sequence.includes("sessions 401")) sequence.push("sessions 401");
        await route.fulfill({
          status: 401,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "token_expired", message: "Token expired" },
          }),
        });
        return;
      }

      if (!sequence.includes("sessions 200")) sequence.push("sessions 200");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            data: [currentSession],
            pagination: { limit: 50, next_cursor: null },
          },
        }),
      });
    });

    await page.goto("/profile/sessions");

    await expect(sessionRow(page, "Firefox 142")).toBeVisible();
    await expect(page.getByText("Sessão atual")).toBeVisible();
    await expect(page.getByText("Não foi possível carregar suas sessões.")).toHaveCount(0);
    expect(sequence).toEqual(["sessions 401", "refresh 200", "sessions 200"]);
  });

  test("a session removed concurrently converges after an idempotent 404 revoke", async ({
    page,
  }) => {
    const sessionState = await mockSessionsApi(page, [
      makeSession({ id: "current", current: true, user_agent: UA.firefoxLinux }),
      makeSession({ id: "stale-session-id", user_agent: UA.edgeWindows }),
    ]);
    await page.goto("/profile/sessions");
    await expect(sessionRow(page, "Microsoft Edge 153")).toBeVisible();

    sessionState.remove("stale-session-id");
    await sessionRow(page, "Microsoft Edge 153")
      .getByRole("button", { name: "Revogar sessão" })
      .click();
    const dialog = page.getByRole("dialog", { name: "Revogar sessão?" });
    await dialog.getByRole("button", { name: "Revogar sessão" }).click();

    await expect(dialog).toBeHidden();
    await expect(sessionRow(page, "Microsoft Edge 153")).toHaveCount(0);
    await expect(page.getByTestId("session-row")).toHaveCount(1);
  });

  test("sessions responsive: rows and actions stay inside the viewport down to 390px", async ({
    page,
  }) => {
    await mockSessionsApi(page, [
      makeSession({ id: "current", current: true, user_agent: UA.firefoxLinux }),
      makeSession({ id: "s2", user_agent: UA.edgeWindows }),
    ]);
    for (const viewport of [
      { width: 1366, height: 768 },
      { width: 768, height: 1024 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/profile/sessions");
      const revoke = sessionRow(page, "Microsoft Edge 153").getByRole("button", {
        name: "Revogar sessão",
      });
      await expect(revoke).toBeVisible();
      const hasOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
      );
      expect(hasOverflow, `${viewport.width}x${viewport.height}`).toBe(false);
      const box = await revoke.boundingBox();
      expect(box !== null && box.x + box.width <= viewport.width, `${viewport.width}px`).toBe(true);
    }
  });

  test("responsive: no horizontal overflow at 1920x1080, 1366x768, 768x1024, 390x844", async ({
    page,
  }) => {
    for (const viewport of [
      { width: 1920, height: 1080 },
      { width: 1366, height: 768 },
      { width: 768, height: 1024 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/profile");
      const hasOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
      );
      expect(hasOverflow, `${viewport.width}x${viewport.height}`).toBe(false);
    }
  });

  test("full keyboard navigation: tabs, edit dialog open via Enter, close via Escape", async ({
    page,
  }) => {
    const perfilTab = page.getByRole("tab", { name: "Perfil", exact: true });
    await perfilTab.focus();
    await expect(perfilTab).toBeFocused();

    for (const name of ["Notificações", "Segurança", "Sessões", "Perfil"]) {
      await page.keyboard.press("ArrowRight");
      await expect(page.getByRole("tab", { name, exact: true })).toBeFocused();
      await expect(page.getByRole("tab", { name, exact: true })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    }

    await page.keyboard.press("Tab");
    const editButton = page.getByRole("button", { name: "Editar" });
    await expect(editButton).toBeFocused();

    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog", { name: "Editar perfil" });
    await expect(dialog).toBeVisible();
    await expect(page.getByLabel("Nome de exibição")).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(editButton).toBeFocused();
  });
});
