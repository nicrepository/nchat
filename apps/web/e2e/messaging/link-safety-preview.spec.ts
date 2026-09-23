import { expect, test, type Page } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  emitLinkUpdated,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  uniqueId,
  type RawLink,
} from "../helpers/messagingApi";

/**
 * Link Safety per URL and rich previews (issue #807), through the real
 * message area against a mocked server.
 *
 * The server is the authority for every link: the specs feed the client the
 * entities it would receive and assert what the reader gets — an anchor only
 * where an href was sent, an interstitial for an unverified link, a blocked
 * chip with the rest of the text preserved, a card only after a clearance, and
 * a chat that keeps working while the provider or the preview worker is down.
 * No spec reaches a real host: the card image is served by the mocked
 * chat-service route, and nothing else is fetched.
 */

const SAFE_URL = "https://docs.example.test/guia";
const OTHER_URL = "https://blog.example.test/post";
const UNKNOWN_URL = "https://desconhecido.example.test/x";
const IMAGE_ID = "11111111-1111-4111-8111-111111111111";

// A 1×1 JPEG, the smallest derived thumbnail the mocked route can serve.
const THUMBNAIL = Buffer.from(
  "/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////wgALCAABAAEBAREA/8QAFBABAAAAAAAAAAAAAAAAAAAAAP/aAAgBAQABPxA=",
  "base64",
);

/** A deterministic stand-in for the server's target key: 32 hex chars per URL. */
function targetKey(url: string): string {
  let hash = 0;
  for (const char of url) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return hash.toString(16).padStart(8, "0").repeat(4);
}

function safeLink(url: string, ordinal = 0, extra: Partial<RawLink> = {}): RawLink {
  return {
    ordinal,
    target_key: targetKey(url),
    text: url,
    url,
    hostname: new URL(url).host,
    safety: "safe",
    click: "direct",
    href: url,
    updated_at: "2026-07-15T12:00:00.000Z",
    ...extra,
  };
}

function readyPreview(url: string, title: string, withImage = false): RawLink["preview"] {
  return {
    state: "ready",
    hostname: new URL(url).host,
    site_name: "Example",
    title,
    description: "Uma descrição curta da página.",
    ...(withImage ? { image_id: IMAGE_ID, image_width: 480, image_height: 240 } : {}),
  };
}

const blockedLink = (ordinal = 0, url = "https://blocked.example/"): RawLink => ({
  ordinal,
  target_key: targetKey(url),
  safety: "malicious",
  click: "none",
  updated_at: "2026-07-15T12:00:00.000Z",
});

const unknownLink = (url: string, ordinal = 0): RawLink => ({
  ordinal,
  target_key: targetKey(url),
  text: url,
  url,
  hostname: new URL(url).host,
  safety: "unknown",
  click: "interstitial",
  updated_at: "2026-07-15T12:00:00.000Z",
});

const pendingLink = (url: string, ordinal = 0): RawLink => ({
  ordinal,
  target_key: targetKey(url),
  text: url,
  url,
  hostname: new URL(url).host,
  safety: "pending",
  click: "none",
  updated_at: "2026-07-15T12:00:00.000Z",
});

async function mockThumbnail(page: Page, hits: { count: number }) {
  await page.route("**/api/chat/link-previews/*/image", async (route) => {
    hits.count += 1;
    await route.fulfill({ status: 200, contentType: "image/jpeg", body: THUMBNAIL });
  });
}

const anchorFor = (page: Page, messageId: string, url: string) =>
  messageBubble(page, messageId).locator(`a.rtr-link[href="${url}"]`);

test.describe("links por alvo e previews (issue #807)", () => {
  test("DM: link safe é clicável e ganha um rich card com imagem derivada", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const message = makeMessage({
      id: `${targetId}-safe`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: `Confira isso: ${SAFE_URL}`,
      body_format: "v2",
      link_safety_state: "safe",
      links: [safeLink(SAFE_URL, 0, { preview: readyPreview(SAFE_URL, "Guia do NChat", true) })],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    const hits = { count: 0 };
    await installMessagingMocks(page, scenario);
    await mockThumbnail(page, hits);
    await page.goto(`/chat/dm/${targetId}`);

    const anchor = anchorFor(page, message.id, SAFE_URL);
    await expect(anchor).toHaveAttribute("target", "_blank");
    await expect(anchor).toHaveAttribute("rel", /noopener/);

    const card = messageBubble(page, message.id).getByTestId("chat-link-card");
    await expect(card).toContainText("docs.example.test");
    await expect(card).toContainText("Guia do NChat");
    await expect(
      card.getByRole("link", { name: "Guia do NChat — docs.example.test" }),
    ).toHaveAttribute("href", SAFE_URL);
    const image = card.getByRole("img");
    await expect(image).toHaveAttribute("src", /^blob:/);
    // Served by chat-service, and only by it: no element on the page points the
    // browser at the remote host.
    expect(hits.count).toBeGreaterThanOrEqual(1);
    expect(await page.locator(`img[src^="http"]`).count()).toBe(0);
  });

  test("grupo: link safe clicável", async ({ page }, testInfo) => {
    const groupId = uniqueId(testInfo, "group");
    const message = makeMessage({
      id: `${groupId}-safe`,
      body_text: `veja ${SAFE_URL}`,
      body_format: "v2",
      links: [safeLink(SAFE_URL)],
    });
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId: groupId,
      targetName: "Grupo #807",
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${groupId}`);
    await expect(anchorFor(page, message.id, SAFE_URL)).toBeVisible();
  });

  test("canal privado: link safe clicável", async ({ page }, testInfo) => {
    const channelId = uniqueId(testInfo, "private");
    const channelMessage = makeMessage({
      id: `${channelId}-safe`,
      body_text: `canal ${SAFE_URL}`,
      body_format: "v3",
      links: [safeLink(SAFE_URL)],
    });
    const channelScenario = createScenario({
      kind: "channel",
      targetId: channelId,
      targetName: "Privado #807",
      messages: [channelMessage],
    });
    await installMessagingMocks(page, channelScenario);
    await page.goto(`/chat/channel/${channelId}`);
    await expect(anchorFor(page, channelMessage.id, SAFE_URL)).toBeVisible();
  });

  test("malicious + texto legítimo: chip bloqueado, texto preservado, link safe ao lado continua", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const message = makeMessage({
      id: `${targetId}-mixed`,
      sender_id: OTHER_USER_ID,
      body_text: `Olha ￼ e também ${SAFE_URL} obrigado`,
      body_format: "v2",
      link_safety_state: "malicious",
      links: [blockedLink(0), safeLink(SAFE_URL, 1)],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = messageBubble(page, message.id);
    await expect(bubble.locator("[data-link-safety='malicious']")).toHaveText(
      /Link bloqueado por segurança/,
    );
    await expect(bubble).toContainText("Olha");
    await expect(bubble).toContainText("obrigado");
    await expect(anchorFor(page, message.id, SAFE_URL)).toBeVisible();
    expect(await bubble.locator("a").count()).toBe(1);
    await expect(bubble.getByTestId("chat-link-card")).toHaveCount(0);
  });

  test("provider fora do ar: mensagem publica, link fica pendente e o chat continua", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [],
      postLinks: (body) => (body.includes(OTHER_URL) ? [pendingLink(OTHER_URL)] : undefined),
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await fillComposer(page, `veja ${OTHER_URL}`);
    await page.getByRole("button", { name: "Enviar mensagem" }).click();

    const bubble = page.locator("[data-testid='chat-msg-bubble']").filter({ hasText: OTHER_URL });
    await expect(bubble).toBeVisible();
    await expect(bubble.getByRole("status")).toHaveText(/Verificando segurança do link…/);
    expect(await bubble.locator("a, button.rtr-link--unverified").count()).toBe(0);

    // The chat is not stuck: a second message goes through while the first
    // link waits.
    await fillComposer(page, "segunda mensagem");
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await expect(page.getByText("segunda mensagem")).toBeVisible();

    // The verdict lands later, over realtime: the link becomes an anchor.
    const messageId = scenario.requests.dmPosts.length ? `${targetId}-reply-1` : "";
    await emitLinkUpdated(page, {
      kind: "dm",
      targetId,
      messageId,
      link: safeLink(OTHER_URL, 0, { updated_at: "2026-07-15T12:10:00.000Z" }),
    });
    await expect(anchorFor(page, messageId, OTHER_URL)).toBeVisible();
  });

  test("queda total dos dois providers: pendente vira interstitial, não fica verificando para sempre", async ({
    page,
  }, testInfo) => {
    // The journey issue #928 made worth asserting on its own. With a primary
    // and a fallback, "nobody could answer" is the state both being down
    // produces — and the backend converges it at the target's deadline rather
    // than leaving it pending. From the reader's side that is one transition:
    // the "checking" status becomes an interstitial, and the spinner is gone.
    //
    // Neither provider is reachable from this spec, and neither is mocked
    // either: the server is the authority for link state, so what the client
    // receives is the terminal `unknown` it would receive from a deadline
    // sweep. Which provider failed is not observable here by construction.
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [],
      postLinks: (body) => (body.includes(UNKNOWN_URL) ? [pendingLink(UNKNOWN_URL)] : undefined),
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await fillComposer(page, `veja ${UNKNOWN_URL}`);
    await page.getByRole("button", { name: "Enviar mensagem" }).click();

    const bubble = page.locator("[data-testid='chat-msg-bubble']").filter({ hasText: UNKNOWN_URL });
    await expect(bubble).toBeVisible();
    await expect(bubble.getByRole("status")).toHaveText(/Verificando segurança do link…/);
    expect(await bubble.locator("a").count()).toBe(0);

    // The deadline elapses server-side with no verdict from either provider.
    const messageId = `${targetId}-reply-1`;
    await emitLinkUpdated(page, {
      kind: "dm",
      targetId,
      messageId,
      link: unknownLink(UNKNOWN_URL),
    });

    // The reader is out of the waiting state and gets a decision to make, with
    // the real host shown. Never an automatic anchor: an outage is not a
    // clearance.
    const button = bubble.getByRole("button", {
      name: `${UNKNOWN_URL} — Link não verificado`,
    });
    await expect(button).toBeVisible();
    await expect(bubble.getByRole("status")).toHaveCount(0);
    expect(await bubble.locator("a.rtr-link").count()).toBe(0);
    await expect(bubble.getByTestId("chat-link-card")).toHaveCount(0);
  });

  test("preview indisponível não quebra o link", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const message = makeMessage({
      id: `${targetId}-fetching`,
      body_text: `veja ${SAFE_URL}`,
      body_format: "v2",
      links: [
        safeLink(SAFE_URL, 0, { preview: { state: "fetching", hostname: "docs.example.test" } }),
      ],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = messageBubble(page, message.id);
    await expect(bubble.getByTestId("chat-link-card-placeholder")).toContainText(
      "Preparando visualização…",
    );
    await expect(anchorFor(page, message.id, SAFE_URL)).toBeVisible();

    await emitLinkUpdated(page, {
      kind: "dm",
      targetId,
      messageId: message.id,
      link: safeLink(SAFE_URL, 0, {
        updated_at: "2026-07-15T12:10:00.000Z",
        preview: { state: "failed", hostname: "docs.example.test" },
      }),
    });
    await expect(bubble.getByTestId("chat-link-card-placeholder")).toHaveCount(0);
    await expect(bubble.getByTestId("chat-link-card")).toHaveCount(0);
    await expect(anchorFor(page, message.id, SAFE_URL)).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("unknown: interstitial mostra o host real, fecha com Escape e devolve o foco", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const message = makeMessage({
      id: `${targetId}-unknown`,
      sender_id: OTHER_USER_ID,
      body_text: `abra ${UNKNOWN_URL}`,
      body_format: "v2",
      link_safety_state: "inconclusive",
      links: [unknownLink(UNKNOWN_URL)],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = messageBubble(page, message.id);
    expect(await bubble.locator("a").count()).toBe(0);
    const button = bubble.getByRole("button", { name: `${UNKNOWN_URL} — Link não verificado` });
    await button.focus();
    await page.keyboard.press("Enter");

    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Não foi possível verificar este link");
    await expect(page.getByTestId("chat-link-interstitial-host")).toHaveText(
      "desconhecido.example.test",
    );
    await expect(page.getByRole("button", { name: "Cancelar" })).toBeFocused();
    const open = page.getByRole("link", { name: "Abrir mesmo assim" });
    await expect(open).toHaveAttribute("href", UNKNOWN_URL);
    await expect(open).toHaveAttribute("rel", /noopener/);
    await expect(bubble.getByTestId("chat-link-card")).toHaveCount(0);

    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(button).toBeFocused();
  });

  test("recheck safe -> malicious revoga a navegação em tempo real", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const message = makeMessage({
      id: `${targetId}-revoked`,
      sender_id: OTHER_USER_ID,
      body_text: `veja ${OTHER_URL} agora`,
      body_format: "v2",
      links: [safeLink(OTHER_URL, 0, { preview: readyPreview(OTHER_URL, "Um post") })],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    // The authoritative re-read after a condemnation: the redacted body.
    await page.route(`**/api/chat/dm/${targetId}/messages/${message.id}`, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: makeMessage({
            ...message,
            body_text: "veja ￼ agora",
            link_safety_state: "malicious",
            updated_at: "2026-07-15T12:20:00.000Z",
            links: [blockedLink(0, OTHER_URL)],
          }),
        }),
      });
    });
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = messageBubble(page, message.id);
    await expect(anchorFor(page, message.id, OTHER_URL)).toBeVisible();
    await expect(bubble.getByTestId("chat-link-card")).toBeVisible();

    await emitLinkUpdated(page, {
      kind: "dm",
      targetId,
      messageId: message.id,
      link: { ...blockedLink(0, OTHER_URL), updated_at: "2026-07-15T12:20:00.000Z" },
    });

    await expect(bubble.locator("[data-link-safety='malicious']")).toHaveText(
      /Link bloqueado por segurança/,
    );
    await expect(anchorFor(page, message.id, OTHER_URL)).toHaveCount(0);
    await expect(bubble.getByTestId("chat-link-card")).toHaveCount(0);
    await expect(bubble).not.toContainText(OTHER_URL);
    await expect(bubble).toContainText("agora");
  });

  test("recheck malicious -> safe devolve o link em tempo real, sem recarregar", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const message = makeMessage({
      id: `${targetId}-released`,
      sender_id: OTHER_USER_ID,
      body_text: "veja ￼ agora",
      body_format: "v2",
      link_safety_state: "malicious",
      links: [blockedLink(0, OTHER_URL)],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    // The occurrence's text was withheld by the server; only the authoritative
    // re-read can bring it back. The conversation itself is never reloaded.
    let rereads = 0;
    let listLoads = 0;
    const listURL = new RegExp(`/api/chat/dm/${targetId}/messages(\\?|$)`);
    page.on("request", (request) => {
      if (listURL.test(request.url())) listLoads += 1;
    });
    await page.route(`**/api/chat/dm/${targetId}/messages/${message.id}`, async (route) => {
      rereads += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: makeMessage({
            ...message,
            body_text: `veja ${OTHER_URL} agora`,
            link_safety_state: "safe",
            updated_at: "2026-07-15T12:30:00.000Z",
            links: [safeLink(OTHER_URL, 0, { updated_at: "2026-07-15T12:30:00.000Z" })],
          }),
        }),
      });
    });
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = messageBubble(page, message.id);
    await expect(bubble.locator("[data-link-safety='malicious']")).toBeVisible();
    await expect(bubble).not.toContainText(OTHER_URL);
    expect(listLoads).toBeGreaterThanOrEqual(1);
    const listLoadsBefore = listLoads;

    await emitLinkUpdated(page, {
      kind: "dm",
      targetId,
      messageId: message.id,
      link: safeLink(OTHER_URL, 0, { updated_at: "2026-07-15T12:30:00.000Z" }),
    });

    await expect(anchorFor(page, message.id, OTHER_URL)).toBeVisible();
    await expect(bubble.locator("[data-link-safety='malicious']")).toHaveCount(0);
    await expect(bubble).toContainText("veja");
    await expect(bubble).toContainText("agora");
    expect(rereads).toBeGreaterThanOrEqual(1);
    expect(listLoads).toBe(listLoadsBefore);
  });

  test("preview desligado no servidor: links safe continuam clicáveis, sem card", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    // With CHAT_LINK_PREVIEW_ENABLED=false the server sends no preview at all.
    const message = makeMessage({
      id: `${targetId}-nopreview`,
      body_text: `${SAFE_URL} e ${OTHER_URL}`,
      body_format: "v2",
      links: [safeLink(SAFE_URL, 0), safeLink(OTHER_URL, 1)],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await expect(anchorFor(page, message.id, SAFE_URL)).toBeVisible();
    await expect(anchorFor(page, message.id, OTHER_URL)).toBeVisible();
    await expect(messageBubble(page, message.id).getByTestId("chat-link-card")).toHaveCount(0);
    await expect(
      messageBubble(page, message.id).getByTestId("chat-link-card-placeholder"),
    ).toHaveCount(0);
  });

  test("no máximo dois cards, target repetido conta uma vez, e ocultar mantém o link", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const third = "https://c.example.test/3";
    const message = makeMessage({
      id: `${targetId}-cards`,
      sender_id: CURRENT_USER_ID,
      body_text: `${SAFE_URL} ${SAFE_URL} ${OTHER_URL} ${third}`,
      body_format: "v2",
      links: [
        safeLink(SAFE_URL, 0, { preview: readyPreview(SAFE_URL, "Primeiro") }),
        safeLink(SAFE_URL, 1, { preview: readyPreview(SAFE_URL, "Primeiro") }),
        safeLink(OTHER_URL, 2, { preview: readyPreview(OTHER_URL, "Segundo") }),
        safeLink(third, 3, { preview: readyPreview(third, "Terceiro") }),
      ],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = messageBubble(page, message.id);
    await expect(bubble.getByTestId("chat-link-card")).toHaveCount(2);
    await expect(bubble.getByTestId("chat-link-card").nth(0)).toContainText("Primeiro");
    await expect(bubble.getByTestId("chat-link-card").nth(1)).toContainText("Segundo");
    expect(await bubble.locator("a.rtr-link").count()).toBe(4);

    await bubble
      .getByRole("button", { name: "Opções da visualização de docs.example.test" })
      .click();
    await expect(bubble.getByRole("menuitem", { name: /reportar/i })).toHaveCount(0);
    await bubble.getByRole("menuitem", { name: "Ocultar visualização" }).click();
    await expect(bubble.getByTestId("chat-link-card")).toHaveCount(1);
    expect(await bubble.locator("a.rtr-link").count()).toBe(4);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });
});
