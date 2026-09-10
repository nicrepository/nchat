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

async function mockChatSidebarApi(page: Page) {
  await page.route("**/api/chat/sidebar", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          current_user_id: CURRENT_USER_ID,
          workspace: { id: "e2e-workspace", name: "E2E", slug: "e2e" },
          channels: [],
          dm_conversations: [],
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
    ip_address: "203.0.113.10",
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

function sessionRow(page: Page, userAgent: string) {
  return page.getByTestId("session-row").filter({ hasText: userAgent });
}

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
      makeSession({ id: "current", current: true, user_agent: "Firefox on Linux" }),
      makeSession({ id: "s2", user_agent: "Chrome on Windows" }),
      makeSession({ id: "s3", user_agent: "Safari on iPhone" }),
    ]);
    await page.goto("/profile/sessions");
    await expect(page.getByTestId("session-row")).toHaveCount(3);
    await expect(page.getByText("Sessão atual")).toHaveCount(1);

    await sessionRow(page, "Chrome on Windows")
      .getByRole("button", { name: "Revogar sessão" })
      .click();
    const revokeOneDialog = page.getByRole("dialog", { name: "Revogar sessão?" });
    await expect(revokeOneDialog).toBeVisible();
    await revokeOneDialog.getByRole("button", { name: "Revogar sessão" }).click();
    await expect(revokeOneDialog).toBeHidden();
    await expect(page.getByTestId("session-row")).toHaveCount(2);
    await expect(sessionRow(page, "Chrome on Windows")).toHaveCount(0);

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
      user_agent: "Firefox after refresh",
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

    await expect(sessionRow(page, "Firefox after refresh")).toBeVisible();
    await expect(page.getByText("Sessão atual")).toBeVisible();
    await expect(page.getByText("Não foi possível carregar suas sessões.")).toHaveCount(0);
    expect(sequence).toEqual(["sessions 401", "refresh 200", "sessions 200"]);
  });

  test("a session removed concurrently converges after an idempotent 404 revoke", async ({
    page,
  }) => {
    const sessionState = await mockSessionsApi(page, [
      makeSession({ id: "current", current: true, user_agent: "Firefox on Linux" }),
      makeSession({ id: "stale-session-id", user_agent: "Dispositivo antigo" }),
    ]);
    await page.goto("/profile/sessions");
    await expect(sessionRow(page, "Dispositivo antigo")).toBeVisible();

    sessionState.remove("stale-session-id");
    await sessionRow(page, "Dispositivo antigo")
      .getByRole("button", { name: "Revogar sessão" })
      .click();
    const dialog = page.getByRole("dialog", { name: "Revogar sessão?" });
    await dialog.getByRole("button", { name: "Revogar sessão" }).click();

    await expect(dialog).toBeHidden();
    await expect(sessionRow(page, "Dispositivo antigo")).toHaveCount(0);
    await expect(page.getByTestId("session-row")).toHaveCount(1);
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
