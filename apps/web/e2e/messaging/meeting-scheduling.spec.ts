import { expect, test, type Page } from "@playwright/test";
import {
  createScenario,
  installMessagingMocks,
  makeMessage,
  OTHER_USER_ID,
  CURRENT_USER_ID,
  sidebarResponseBody,
} from "../helpers/messagingApi";

const conversationId = "10550000-0000-4000-8000-000000000001";
const meetingId = "10550000-0000-4000-8000-000000000002";
const secondId = "10550000-0000-4000-8000-000000000003";
const ana = { user_id: OTHER_USER_ID, display_name: "Ana" };
const bia = { user_id: secondId, display_name: "Bia" };
const result = {
  id: meetingId,
  status: "scheduled",
  version: 1,
  starts_at: "2026-11-03T13:00:00Z",
  ends_at: "2026-11-03T13:30:00Z",
  timezone: "America/Sao_Paulo",
};

async function fixture(
  page: Page,
  kind: "dm" | "group" | "channel" = "channel",
  options: {
    bulkFailure?: boolean;
    createStatus?: number;
    loseResponse?: boolean;
    gate?: Promise<void>;
    large?: boolean;
  } = {},
) {
  const apiKind = kind === "channel" ? "channel" : "dm";
  const scenario = createScenario({
    kind: apiKind,
    conversationType: kind === "group" ? "group" : "direct",
    targetId: conversationId,
    targetName: "Equipe",
    messages: Array.from({ length: 35 }, (_, i) =>
      makeMessage({ id: `meeting-message-${i}`, body_text: `Mensagem preservada ${i}` }),
    ),
  });
  await installMessagingMocks(page, scenario);
  const requests: { key: string; body: Record<string, unknown> }[] = [];
  const candidateRequests: string[] = [];
  const bulkRequests: string[] = [];
  await page.route("**/api/chat/meeting-candidates/**", async (route) => {
    const url = new URL(route.request().url());
    candidateRequests.push(url.toString());
    const q = url.searchParams.get("q");
    const cursor = url.searchParams.get("cursor");
    const all = options.large
      ? Array.from({ length: 650 }, (_, index) => ({
          user_id: `10550000-0000-4000-8000-${String(index + 10).padStart(12, "0")}`,
          display_name: `Pessoa ${index}`,
        }))
      : [ana, bia];
    const matches = q
      ? all.filter((person) => person.display_name.toLowerCase().startsWith(q.toLowerCase()))
      : all;
    const start = cursor ? matches.findIndex((person) => person.user_id === cursor) + 1 : 0;
    const limit = options.large ? 50 : 1;
    const items = matches.slice(start, start + limit);
    const next_cursor =
      start + limit < matches.length && kind !== "dm" ? items.at(-1)?.user_id : undefined;
    await route.fulfill({
      json: {
        data: { items, next_cursor },
      },
    });
  });
  await page.route("**/api/chat/meetings", async (route) => {
    expect(route.request().method()).toBe("POST");
    requests.push({
      key: route.request().headers()["idempotency-key"],
      body: route.request().postDataJSON(),
    });
    if (options.gate) await options.gate;
    if (options.loseResponse && requests.length === 1) {
      await route.abort("failed");
      return;
    }
    if (options.createStatus && requests.length === 1) {
      await route.fulfill({
        status: options.createStatus,
        json: { error: { code: "rate_limited", message: "private" } },
      });
      return;
    }
    await route.fulfill({ status: requests.length > 1 ? 200 : 201, json: { data: result } });
  });
  await page.route("**/api/chat/meetings/*/participants/invite-all", async (route) => {
    bulkRequests.push(route.request().url());
    expect(route.request().postDataJSON()).toEqual({});
    if (options.bulkFailure && bulkRequests.length === 1) {
      await route.fulfill({
        status: 503,
        json: { error: { code: "unavailable", message: "private" } },
      });
      return;
    }
    await route.fulfill({ json: { data: { added: 2 } } });
  });
  await page.goto(`/chat/${apiKind}/${conversationId}`);
  await expect(page.getByTestId("chat-composer-input")).toBeVisible();
  return { requests, candidateRequests, bulkRequests, scenario };
}
const dialog = (page: Page) => page.getByRole("dialog", { name: "Agendar reunião", exact: true });
async function open(page: Page) {
  await page.getByRole("button", { name: "Agendar reunião", exact: true }).click();
  await expect(dialog(page).getByRole("checkbox", { name: "Ana", exact: true })).toBeVisible();
}
async function submit(page: Page) {
  await dialog(page).getByRole("button", { name: "Agendar reunião", exact: true }).click();
}

test.describe("meeting scheduling #1055", () => {
  for (const kind of ["dm", "group", "channel"] as const) {
    test(`schedules from the ${kind} header with contextual selection`, async ({ page }) => {
      const api = await fixture(page, kind);
      await open(page);
      await expect(dialog(page).getByLabel("Título (obrigatório)")).toHaveValue(
        kind === "dm" ? "Reunião com Equipe" : "Equipe",
      );
      const candidate = dialog(page).getByRole("checkbox", { name: "Ana", exact: true });
      if (kind === "dm") await expect(candidate).toBeChecked();
      else {
        await expect(candidate).not.toBeChecked();
        await candidate.check();
      }
      await submit(page);
      await expect(dialog(page).getByRole("status")).toContainText("Reunião criada com sucesso");
      expect(api.requests).toHaveLength(1);
      expect(api.requests[0].key).toMatch(/^[\w.:-]{1,128}$/);
      expect(api.requests[0].body).toMatchObject({
        conversation: { kind: kind === "channel" ? "channel" : "dm", id: conversationId },
        participants: [{ user_id: OTHER_USER_ID, role: "attendee" }],
        join_policy: "invited_only",
      });
      expect(api.bulkRequests).toEqual([]);
    });
  }
  test("opens the same flow from the conversation menu without navigation", async ({ page }) => {
    await fixture(page);
    const path = page.url();
    await page.getByRole("button", { name: "Mais opções para canal Equipe" }).click();
    await page.getByRole("menuitem", { name: "Agendar reunião" }).click();
    await expect(dialog(page)).toBeVisible();
    expect(page.url()).toBe(path);
    await dialog(page).getByRole("button", { name: "Fechar agendamento" }).click();
    await expect(page.getByRole("button", { name: "Mais opções para canal Equipe" })).toBeFocused();
  });
  test("restores focus to mobile navigation when the sidebar opener becomes hidden", async ({
    page,
  }) => {
    await fixture(page);
    await page.getByRole("button", { name: "Mais opções para canal Equipe" }).click();
    await page.getByRole("menuitem", { name: "Agendar reunião" }).click();
    await expect(dialog(page)).toBeVisible();
    await page.setViewportSize({ width: 375, height: 667 });
    await dialog(page).getByRole("button", { name: "Fechar agendamento" }).click();
    await expect(page.getByTestId("chat-nav-toggle")).toBeFocused();
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();
  });
  test("preserves composer, viewport, timeline identity and socket lifecycle when closing and reopening", async ({
    page,
  }) => {
    await fixture(page);
    const composer = page.getByTestId("chat-composer-input");
    await composer.fill("Rascunho da mensagem com conteúdo");
    const list = page.locator(".chat-msg-area__list");
    await expect.poll(() => list.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);
    await list.evaluate((el) => {
      el.scrollTop = 90;
    });
    await expect.poll(() => list.evaluate((el) => Math.round(el.scrollTop))).toBe(90);
    const before = await page.evaluate(() => {
      const scope = window as unknown as {
        __e2eWebSocketLifecycle: () => unknown;
        __meetingTimeline?: Element | null;
        __meetingComposer?: Element | null;
      };
      scope.__meetingTimeline = document.querySelector(".chat-msg-area__list");
      scope.__meetingComposer = document.querySelector('[data-testid="chat-composer-input"]');
      return scope.__e2eWebSocketLifecycle();
    });
    await open(page);
    await dialog(page).getByLabel("Título (obrigatório)").fill("Continuar depois");
    await page.keyboard.press("Escape");
    await expect(dialog(page)).toBeHidden();
    await expect(composer).toHaveText("Rascunho da mensagem com conteúdo");
    await expect.poll(() => list.evaluate((el) => Math.round(el.scrollTop))).toBe(90);
    expect(
      await page.evaluate(() => {
        const scope = window as unknown as {
          __e2eWebSocketLifecycle: () => unknown;
          __meetingTimeline?: Element;
          __meetingComposer?: Element;
        };
        return {
          socket: scope.__e2eWebSocketLifecycle(),
          sameTimeline: scope.__meetingTimeline === document.querySelector(".chat-msg-area__list"),
          sameComposer:
            scope.__meetingComposer ===
            document.querySelector('[data-testid="chat-composer-input"]'),
        };
      }),
    ).toEqual({ socket: before, sameTimeline: true, sameComposer: true });
    await open(page);
    await expect(dialog(page).getByLabel("Título (obrigatório)")).toHaveValue("Continuar depois");
  });
  test("loads candidates by page, retains previous selections and never implicitly invites all", async ({
    page,
  }) => {
    const api = await fixture(page);
    await open(page);
    expect(api.candidateRequests).toHaveLength(1);
    await expect(
      dialog(page).getByRole("checkbox", { name: "Convidar todos os membros elegíveis" }),
    ).not.toBeChecked();
    await dialog(page).getByRole("checkbox", { name: "Ana", exact: true }).check();
    await dialog(page).getByRole("button", { name: "Próxima página de participantes" }).click();
    await dialog(page).getByRole("checkbox", { name: "Bia", exact: true }).check();
    await dialog(page).getByLabel("Buscar participantes pelo início do nome").fill("Ana");
    await expect(dialog(page).getByRole("checkbox", { name: "Ana", exact: true })).toBeChecked();
    await expect(
      dialog(page).getByRole("list", { name: "Participantes selecionados" }),
    ).toContainText("Bia");
    await submit(page);
    await expect(dialog(page).getByRole("status")).toContainText("Reunião criada");
    expect(api.requests[0].body.participants).toHaveLength(2);
    expect(api.bulkRequests).toHaveLength(0);
    expect(new URL(api.candidateRequests[2]).searchParams.has("cursor")).toBe(false);
  });
  test("keeps a 650-member channel bounded and unselected", async ({ page }) => {
    const api = await fixture(page, "channel", { large: true });
    await page.getByRole("button", { name: "Agendar reunião", exact: true }).click();
    const candidates = dialog(page).getByRole("list", { name: "Participantes elegíveis" });
    await expect(candidates.getByRole("checkbox")).toHaveCount(50);
    await expect(candidates.locator("input:checked")).toHaveCount(0);
    expect(api.candidateRequests).toHaveLength(1);
    await dialog(page).getByRole("button", { name: "Próxima página de participantes" }).click();
    await expect(candidates).toContainText("Pessoa 50");
    await expect(candidates.getByRole("checkbox")).toHaveCount(50);
    await submit(page);
    await expect(dialog(page)).toContainText("Reunião criada com sucesso");
    expect(api.requests[0].body.participants).toEqual([]);
    expect(api.bulkRequests).toHaveLength(0);
  });
  test("application navigation shortcuts do not leave an open modal", async ({ page }) => {
    await fixture(page);
    await open(page);
    const before = page.url();
    await dialog(page).getByLabel("Título (obrigatório)").focus();
    await page.keyboard.press("Alt+ArrowDown");
    await page.keyboard.press("Control+k");
    expect(page.url()).toBe(before);
    await expect(dialog(page)).toBeVisible();
  });
  test("explicit bulk failure preserves the persisted ID and retries only invite-all", async ({
    page,
  }) => {
    const api = await fixture(page, "channel", { bulkFailure: true });
    await open(page);
    await dialog(page)
      .getByRole("checkbox", { name: "Convidar todos os membros elegíveis" })
      .check();
    await submit(page);
    await expect(dialog(page).getByRole("alert")).toContainText("A reunião foi criada");
    await expect(dialog(page)).toContainText(meetingId);
    expect(api.requests).toHaveLength(1);
    await dialog(page).getByRole("button", { name: "Tentar convite coletivo novamente" }).click();
    await expect(dialog(page)).toContainText("Convite coletivo concluído");
    expect(api.requests).toHaveLength(1);
    expect(api.bulkRequests).toHaveLength(2);
    expect(api.bulkRequests[0]).toBe(api.bulkRequests[1]);
  });
  test("blocks double click and Escape while a create is pending", async ({ page }) => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const api = await fixture(page, "channel", { gate });
    await open(page);
    await dialog(page).getByRole("button", { name: "Agendar reunião", exact: true }).dblclick();
    await expect.poll(() => api.requests.length).toBe(1);
    await page.keyboard.press("Escape");
    await expect(dialog(page)).toBeVisible();
    release();
    await expect(dialog(page)).toContainText("Reunião criada com sucesso");
    expect(api.requests).toHaveLength(1);
  });
  test("validates inline, focuses the field and keeps all data", async ({ page }) => {
    const api = await fixture(page);
    await open(page);
    const title = dialog(page).getByLabel("Título (obrigatório)");
    await title.fill("  ");
    await submit(page);
    await expect(title).toBeFocused();
    await expect(title).toHaveAttribute("aria-invalid", "true");
    expect(api.requests).toHaveLength(0);
    await title.fill("Corrigido");
    await dialog(page).getByLabel("Duração", { exact: true }).selectOption("custom");
    await dialog(page).getByLabel("Duração personalizada em minutos").fill("0");
    await submit(page);
    await expect(dialog(page)).toContainText("duração positiva");
  });
  test("recovers from 429 with a stable key", async ({ page }) => {
    const api = await fixture(page, "group", { createStatus: 429 });
    await open(page);
    await submit(page);
    await expect(dialog(page).getByRole("alert")).toContainText("Aguarde um minuto");
    await submit(page);
    await expect(dialog(page)).toContainText("já registrada");
    expect(api.requests).toHaveLength(2);
    expect(api.requests[0]).toEqual(api.requests[1]);
  });
  test("recovers an unconfirmed create using the same payload and key", async ({ page }) => {
    const api = await fixture(page, "dm", { loseResponse: true });
    await open(page);
    await submit(page);
    await expect(dialog(page).getByRole("alert")).toContainText("Resposta não confirmada");
    await expect(dialog(page).getByLabel("Título (obrigatório)")).toBeDisabled();
    await submit(page);
    await expect(dialog(page)).toContainText("já registrada");
    expect(api.requests[0]).toEqual(api.requests[1]);
  });
  test("a 401 refresh preserves the same identity and never automatically replays the write", async ({
    page,
  }) => {
    const api = await fixture(page, "channel", { createStatus: 401 });
    await page.route("**/api/auth/refresh", (route) =>
      route.fulfill({
        json: { access_token: "rotated-meeting-token", token_type: "Bearer", expires_in: 300 },
      }),
    );
    await open(page);
    await dialog(page).getByLabel("Título (obrigatório)").fill("Rascunho após refresh");
    await submit(page);
    await expect(dialog(page).getByRole("alert")).toContainText("confirmação da sessão");
    expect(api.requests).toHaveLength(1);
    await expect(dialog(page).getByLabel("Título (obrigatório)")).toHaveValue(
      "Rascunho após refresh",
    );
    await submit(page);
    await expect(dialog(page)).toContainText("já registrada");
    expect(api.requests[0]).toEqual(api.requests[1]);
  });
  test("a refresh resolving another account clears the previous scheduling draft", async ({
    page,
  }) => {
    const api = await fixture(page, "channel", { createStatus: 401 });
    let rotated = false;
    await page.route("**/api/auth/refresh", async (route) => {
      rotated = true;
      await route.fulfill({
        json: { access_token: "other-meeting-account", token_type: "Bearer", expires_in: 300 },
      });
    });
    await page.route("**/api/chat/sidebar", (route) =>
      route.fulfill({
        contentType: "application/json",
        body: sidebarResponseBody(api.scenario, rotated ? "another-account" : CURRENT_USER_ID),
      }),
    );
    await open(page);
    await dialog(page).getByLabel("Título (obrigatório)").fill("Conteúdo privado anterior");
    await submit(page);
    await expect(dialog(page)).toBeHidden();
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();
    await open(page);
    await expect(dialog(page).getByLabel("Título (obrigatório)")).toHaveValue("Equipe");
    expect(api.requests).toHaveLength(1);
  });
  test("completes with keyboard and keeps focus inside the native modal", async ({ page }) => {
    await fixture(page);
    const trigger = page.getByRole("button", { name: "Agendar reunião", exact: true });
    await trigger.focus();
    await page.keyboard.press("Enter");
    await expect(dialog(page).getByRole("heading", { name: "Agendar reunião" })).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(
      dialog(page).getByRole("button", { name: "Agendar reunião", exact: true }),
    ).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(dialog(page).getByRole("button", { name: "Fechar agendamento" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(trigger).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(dialog(page).getByRole("checkbox", { name: "Ana", exact: true })).toBeVisible();
    await dialog(page).getByRole("button", { name: "Agendar reunião", exact: true }).focus();
    await page.keyboard.press("Enter");
    await expect(dialog(page)).toContainText("Reunião criada com sucesso");
  });
  for (const viewport of [
    { width: 375, height: 667 },
    { width: 667, height: 375 },
  ]) {
    test(`mobile ${viewport.width}x${viewport.height} scrolls fields with reachable actions`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await fixture(page, "dm");
      await open(page);
      const modal = dialog(page);
      await expect(modal).toBeInViewport();
      await expect(page.locator("body")).toHaveJSProperty("scrollWidth", viewport.width);
      await modal.getByLabel("Descrição (opcional)").fill("Pauta mobile");
      await submit(page);
      await expect(modal).toContainText("Reunião criada com sucesso");
      await modal.getByRole("button", { name: "Concluir", exact: true }).click();
      await expect(page.getByTestId("chat-composer-input")).toBeVisible();
    });
  }
});
