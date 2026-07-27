import { expect, type Locator, type Page, type Route, type TestInfo } from "@playwright/test";

export const CURRENT_USER_ID = "e2e-author";
export const CURRENT_USER_NAME = "E2E Autor";
export const OTHER_USER_ID = "e2e-participant";
export const OTHER_USER_NAME = "E2E Participante";
export const GROUP_DM_ID = "e2e-dm-group";
export const GROUP_DM_NAME = "E2E Grupo";
export const OTHER_CHANNEL_ID = "e2e-channel-other";
export const OTHER_CHANNEL_NAME = "Canal E2E";

type TargetKind = "channel" | "dm";

interface RawMessage {
  id: string;
  sender_id: string;
  sender_display_name: string;
  sender_email: string;
  kind: "user" | "system";
  body_text?: string;
  body_format: "v1" | "v2" | "v3";
  status: "active" | "deleted";
  is_removed: boolean;
  created_at: string;
  updated_at: string;
  edited_at?: string | null;
  edit_count: number;
  is_edited: boolean;
  deleted_at?: string | null;
  reactions: Array<{ emoji: string; count: number; reacted_by_me: boolean }>;
  is_favorited: boolean;
  is_forwarded: boolean;
  quoted?: RawQuote;
  reference?: RawReference;
}

interface RawReference {
  available: boolean;
  message_id?: string;
  target_type?: TargetKind;
  target_id?: string;
  target_label?: string;
  author_display_name?: string;
  body?: string;
  body_format?: "v1" | "v2" | "v3";
  created_at?: string;
}

interface RawQuote {
  id: string;
  author_id: string;
  body: string;
  body_format: "v1" | "v2" | "v3";
  is_removed: boolean;
  deleted_at?: string | null;
  created_at: string;
}

interface MessagingScenarioOptions {
  kind: TargetKind;
  targetId: string;
  targetName: string;
  messages: RawMessage[];
  editWindowExpiredIds?: string[];
}

interface PatchRequest {
  messageId: string;
  method: string;
  endpoint: string;
  body: unknown;
  body_format: unknown;
  raw: Record<string, unknown>;
}

export interface MessagingScenario {
  kind: TargetKind;
  targetId: string;
  targetName: string;
  messagesByTarget: Map<string, RawMessage[]>;
  requests: {
    channelPosts: Array<{
      body_text?: string;
      parent_message_id?: string;
      referenced_message_id?: string;
    }>;
    dmPosts: Array<{
      body_text?: string;
      parent_message_id?: string;
      referenced_message_id?: string;
    }>;
    forwards: Array<{
      destinationChannelId: string;
      sourceMessageId?: string;
      idempotencyKey?: string;
      raw: Record<string, unknown>;
    }>;
    patches: PatchRequest[];
    deletes: string[];
  };
  forwardedByIdempotencyKey: Map<
    string,
    { destinationChannelId: string; sourceMessageId: string; message: RawMessage }
  >;
}

export function uniqueId(testInfo: TestInfo, suffix: string): string {
  const stable = `${testInfo.project.name}-${testInfo.titlePath.join("-")}-${suffix}`
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 80);
  return `e2e-${stable}`;
}

export function makeMessage(overrides: Partial<RawMessage> = {}): RawMessage {
  const now = overrides.created_at ?? "2026-07-15T12:00:00.000Z";
  const isRemoved = overrides.is_removed ?? overrides.status === "deleted";
  return {
    id: overrides.id ?? "msg-e2e",
    sender_id: overrides.sender_id ?? CURRENT_USER_ID,
    sender_display_name: overrides.sender_display_name ?? CURRENT_USER_NAME,
    sender_email: overrides.sender_email ?? "author@example.test",
    kind: overrides.kind ?? "user",
    body_text: isRemoved ? undefined : (overrides.body_text ?? "Mensagem E2E"),
    body_format: overrides.body_format ?? "v3",
    status: isRemoved ? "deleted" : (overrides.status ?? "active"),
    is_removed: isRemoved,
    created_at: now,
    updated_at: overrides.updated_at ?? now,
    edited_at: overrides.edited_at ?? null,
    edit_count: overrides.edit_count ?? 0,
    is_edited: overrides.is_edited ?? (overrides.edit_count ?? 0) > 0,
    deleted_at: overrides.deleted_at ?? null,
    reactions: overrides.reactions ?? [],
    is_favorited: overrides.is_favorited ?? false,
    is_forwarded: overrides.is_forwarded ?? false,
    quoted: overrides.quoted,
    reference: overrides.reference,
  };
}

export function quoteFrom(message: RawMessage): RawQuote {
  return {
    id: message.id,
    author_id: message.sender_id,
    body: message.is_removed ? "" : (message.body_text ?? ""),
    body_format: message.body_format,
    is_removed: message.is_removed,
    deleted_at: message.deleted_at ?? null,
    created_at: message.created_at,
  };
}

export function createScenario(options: MessagingScenarioOptions): MessagingScenario {
  const messagesByTarget = new Map<string, RawMessage[]>();
  messagesByTarget.set(targetKey(options.kind, options.targetId), [...options.messages]);
  messagesByTarget.set(targetKey("channel", OTHER_CHANNEL_ID), []);
  messagesByTarget.set(targetKey("dm", "e2e-dm-other"), []);
  messagesByTarget.set(targetKey("dm", GROUP_DM_ID), []);

  return {
    kind: options.kind,
    targetId: options.targetId,
    targetName: options.targetName,
    messagesByTarget,
    requests: { channelPosts: [], dmPosts: [], forwards: [], patches: [], deletes: [] },
    forwardedByIdempotencyKey: new Map(),
  };
}

export function messagesFor(
  scenario: MessagingScenario,
  kind: TargetKind = scenario.kind,
  targetId: string = scenario.targetId,
): RawMessage[] {
  const key = targetKey(kind, targetId);
  const messages = scenario.messagesByTarget.get(key);
  if (messages) {
    return messages;
  }
  const emptyMessages: RawMessage[] = [];
  scenario.messagesByTarget.set(key, emptyMessages);
  return emptyMessages;
}

export async function installMessagingMocks(
  page: Page,
  scenario: MessagingScenario,
  options: { editWindowExpiredIds?: string[] } = {},
) {
  const expired = new Set(options.editWindowExpiredIds ?? []);
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: "http://localhost:5173",
  });

  await page.addInitScript((accessToken) => {
    sessionStorage.setItem("nchat_at", accessToken);

    class StableWebSocket {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSING = 2;
      static readonly CLOSED = 3;
      readonly readyState = StableWebSocket.OPEN;
      onopen: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      onclose: ((event: CloseEvent) => void) | null = null;

      constructor() {
        setTimeout(() => this.onopen?.(new Event("open")), 0);
      }

      send() {}
      close() {
        this.onclose?.(new CloseEvent("close"));
      }
    }

    window.WebSocket = StableWebSocket as unknown as typeof WebSocket;
  }, `e2e-at-${scenario.targetId}`);

  await page.route("**/api/chat/sidebar", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          current_user_id: CURRENT_USER_ID,
          channels:
            scenario.kind === "channel"
              ? [
                  {
                    id: scenario.targetId,
                    slug: "e2e-canal",
                    display_name: scenario.targetName,
                    type: "public",
                    can_write: true,
                    unread_count: 0,
                  },
                  {
                    id: OTHER_CHANNEL_ID,
                    slug: "e2e-canal-secundario",
                    display_name: OTHER_CHANNEL_NAME,
                    type: "public",
                    can_write: true,
                    unread_count: 0,
                  },
                ]
              : [
                  {
                    id: OTHER_CHANNEL_ID,
                    slug: "e2e-canal",
                    display_name: OTHER_CHANNEL_NAME,
                    type: "public",
                    can_write: true,
                    unread_count: 0,
                  },
                ],
          dm_conversations: [
            {
              id: scenario.kind === "dm" ? scenario.targetId : "e2e-dm-other",
              type: "direct",
              name: scenario.kind === "dm" ? scenario.targetName : OTHER_USER_NAME,
              unread_count: 0,
            },
            // An ad-hoc group is always present so the sidebar fixture covers
            // all three product categories (ISSUE #396).
            {
              id: GROUP_DM_ID,
              type: "group",
              name: GROUP_DM_NAME,
              unread_count: 0,
            },
          ],
        },
      }),
    }),
  );

  await page.route("**/api/chat/reactions/allowed-emojis", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { emojis: ["👍", "🎉"] } }),
    }),
  );

  await page.route("**/api/chat/**/pins", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { pins: [], total_count: 0 } }),
    }),
  );

  await page.route("**/api/chat/messages/*/history?*", (route) => {
    const messageId = decodeMessageId(route.request().url(), 2);
    const message = messageId ? findMessageLocation(scenario, messageId)?.message : undefined;
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          history:
            message && message.is_edited
              ? [
                  {
                    body: "Texto original antes da edição",
                    body_format: message.body_format,
                    versioned_at: message.created_at,
                  },
                ]
              : [],
          offset: 0,
        },
      }),
    });
  });

  await page.route("**/api/chat/messages/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const messageId = decodeMessageId(request.url(), 1);
    if (!messageId) {
      await route.fulfill({ status: 404 });
      return;
    }
    const location = findMessageLocation(scenario, messageId);

    if (request.method() === "PATCH") {
      const raw = (await request.postDataJSON()) as Record<string, unknown>;
      scenario.requests.patches.push({
        messageId,
        method: request.method(),
        endpoint: url.pathname,
        body: raw.body,
        body_format: raw.body_format,
        raw,
      });

      if (!location) {
        await route.fulfill({ status: 404 });
        return;
      }

      if (
        typeof raw.body !== "string" ||
        raw.body.trim() === "" ||
        !isBodyFormat(raw.body_format)
      ) {
        await route.fulfill({
          status: 400,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "invalid_message_payload", message: "invalid message payload" },
          }),
        });
        return;
      }

      if (expired.has(messageId)) {
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "edit_window_expired", message: "edit window expired" },
          }),
        });
        return;
      }

      const previous = location.message;
      const updated = makeMessage({
        ...previous,
        body_text: raw.body,
        body_format: raw.body_format,
        is_edited: true,
        edit_count: previous.edit_count + 1,
        edited_at: "2026-07-15T12:05:00.000Z",
        updated_at: "2026-07-15T12:05:00.000Z",
      });
      location.messages[location.index] = updated;
      await fulfillMessage(route, updated);
      return;
    }

    if (request.method() === "DELETE") {
      scenario.requests.deletes.push(messageId);
      if (!location) {
        await route.fulfill({ status: 404 });
        return;
      }
      const previous = location.message;
      const deleted = makeMessage({
        ...previous,
        body_text: undefined,
        status: "deleted",
        is_removed: true,
        deleted_at: "2026-07-15T12:10:00.000Z",
        updated_at: "2026-07-15T12:10:00.000Z",
        reactions: [],
        quoted: undefined,
      });
      location.messages[location.index] = deleted;
      await fulfillMessage(route, deleted);
      return;
    }

    await route.fallback();
  });

  await page.route("**/api/chat/channels/*/messages/*", async (route) => {
    await handleSingleTargetMessageRoute(route, scenario, "channel");
  });

  await page.route("**/api/chat/dm/*/messages/*", async (route) => {
    await handleSingleTargetMessageRoute(route, scenario, "dm");
  });

  await page.route("**/api/chat/channels/*/messages", async (route) => {
    await handleTargetMessagesRoute(route, scenario, "channel");
  });

  await page.route("**/api/chat/dm/*/messages", async (route) => {
    await handleTargetMessagesRoute(route, scenario, "dm");
  });

  await page.route("**/api/chat/channels/*/messages/forward", async (route) => {
    const request = route.request();
    const target = parseMessagesTarget(request.url(), "channel");
    const raw = (await request.postDataJSON()) as Record<string, unknown>;
    const sourceMessageId =
      typeof raw.source_message_id === "string" ? raw.source_message_id : undefined;
    const idempotencyKey = request.headers()["idempotency-key"];
    if (request.method() !== "POST" || !target) {
      await route.fulfill({ status: 404 });
      return;
    }
    scenario.requests.forwards.push({
      destinationChannelId: target.targetId,
      sourceMessageId,
      idempotencyKey,
      raw,
    });
    const source = sourceMessageId ? findMessageLocation(scenario, sourceMessageId) : undefined;
    if (!source || source.message.is_removed) {
      await route.fulfill({ status: 404 });
      return;
    }
    if (idempotencyKey) {
      const replay = scenario.forwardedByIdempotencyKey.get(idempotencyKey);
      if (replay) {
        if (
          replay.destinationChannelId !== target.targetId ||
          replay.sourceMessageId !== sourceMessageId
        ) {
          await route.fulfill({ status: 409 });
          return;
        }
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: replay.message }),
        });
        return;
      }
    }
    const destination = messagesFor(scenario, "channel", target.targetId);
    const created = makeMessage({
      id: `${target.targetId}-forward-${destination.length + 1}`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: source.message.body_text,
      body_format: source.message.body_format,
      created_at: "2026-07-15T12:04:00.000Z",
      updated_at: "2026-07-15T12:04:00.000Z",
      reactions: [],
      is_favorited: false,
      is_forwarded: true,
      quoted: undefined,
      reference: undefined,
    });
    destination.push(created);
    if (idempotencyKey && sourceMessageId) {
      scenario.forwardedByIdempotencyKey.set(idempotencyKey, {
        destinationChannelId: target.targetId,
        sourceMessageId,
        message: created,
      });
    }
    await fulfillMessage(route, created, 201);
  });

  await page.route("**/api/chat/**/message-references", async (route) => {
    const body = (await route.request().postDataJSON()) as { message_ids?: string[] };
    const references = (body.message_ids ?? []).map((messageId) => ({
      message_id: messageId,
      reference: findMessageLocation(scenario, messageId)?.message.reference ?? {
        available: false,
      },
    }));
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { references } }),
    });
  });
}

async function handleSingleTargetMessageRoute(
  route: Route,
  scenario: MessagingScenario,
  routeKind: TargetKind,
) {
  const target = parseMessagesTarget(route.request().url(), routeKind);
  const messageId = decodeMessageId(route.request().url(), 1);
  const location = messageId ? findMessageLocation(scenario, messageId) : undefined;
  if (
    route.request().method() !== "GET" ||
    !target ||
    !location ||
    location.kind !== routeKind ||
    location.targetId !== target.targetId
  ) {
    await route.fulfill({ status: 404 });
    return;
  }
  await fulfillMessage(route, location.message);
}

async function handleTargetMessagesRoute(
  route: Route,
  scenario: MessagingScenario,
  routeKind: TargetKind,
) {
  const request = route.request();
  const target = parseMessagesTarget(request.url(), routeKind);
  if (!target) {
    await route.fulfill({ status: 404 });
    return;
  }
  const messages = messagesFor(scenario, routeKind, target.targetId);

  if (request.method() === "GET") {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { messages, next_cursor: "" } }),
    });
    return;
  }

  if (request.method() === "POST") {
    const body = (await request.postDataJSON()) as {
      body_text?: string;
      body_format?: RawMessage["body_format"];
      parent_message_id?: string;
      referenced_message_id?: string;
    };
    const requests =
      routeKind === "channel" ? scenario.requests.channelPosts : scenario.requests.dmPosts;
    requests.push({
      body_text: body.body_text,
      parent_message_id: body.parent_message_id,
      referenced_message_id: body.referenced_message_id,
    });

    const parent = messages.find((message) => message.id === body.parent_message_id);
    const source = body.referenced_message_id
      ? findMessageLocation(scenario, body.referenced_message_id)
      : undefined;
    const created = makeMessage({
      id: `${target.targetId}-reply-${messages.length + 1}`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: body.body_text ?? "",
      body_format: body.body_format ?? (routeKind === "channel" ? "v3" : "v2"),
      created_at: "2026-07-15T12:03:00.000Z",
      updated_at: "2026-07-15T12:03:00.000Z",
      quoted: parent ? quoteFrom(parent) : undefined,
      reference: source
        ? {
            available: true,
            message_id: source.message.id,
            target_type: source.kind,
            target_id: source.targetId,
            target_label:
              source.targetId === scenario.targetId
                ? scenario.targetName
                : source.kind === "channel"
                  ? "Canal E2E"
                  : OTHER_USER_NAME,
            author_display_name: source.message.sender_display_name,
            body: source.message.body_text ?? "",
            body_format: source.message.body_format,
            created_at: source.message.created_at,
          }
        : undefined,
    });
    messages.push(created);
    await fulfillMessage(route, created);
    return;
  }

  await route.fallback();
}

async function fulfillMessage(route: Route, message: RawMessage, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify({ data: message }),
  });
}

function targetKey(kind: TargetKind, targetId: string): string {
  return `${kind}:${targetId}`;
}

function parseTargetKey(key: string): { kind: TargetKind; targetId: string } | undefined {
  const [kind, ...targetParts] = key.split(":");
  if ((kind !== "channel" && kind !== "dm") || targetParts.length === 0) {
    return undefined;
  }
  return { kind, targetId: targetParts.join(":") };
}

function parseMessagesTarget(
  url: string,
  expectedKind: TargetKind,
): { kind: TargetKind; targetId: string } | undefined {
  const path = new URL(url).pathname.split("/").filter(Boolean);
  const collection = expectedKind === "channel" ? "channels" : "dm";
  const collectionIndex = path.indexOf(collection);
  if (collectionIndex === -1 || path[collectionIndex + 2] !== "messages") {
    return undefined;
  }
  const targetId = path[collectionIndex + 1];
  return targetId ? { kind: expectedKind, targetId: decodeURIComponent(targetId) } : undefined;
}

function decodeMessageId(url: string, trailingSegments: number): string | undefined {
  const path = new URL(url).pathname.split("/").filter(Boolean);
  const messageIndex = path.indexOf("messages");
  const idIndex = messageIndex + 1;
  if (messageIndex === -1 || path.length < idIndex + trailingSegments) {
    return undefined;
  }
  return decodeURIComponent(path[idIndex]);
}

function findMessageLocation(
  scenario: MessagingScenario,
  messageId: string,
):
  | {
      kind: TargetKind;
      targetId: string;
      messages: RawMessage[];
      index: number;
      message: RawMessage;
    }
  | undefined {
  for (const [key, messages] of scenario.messagesByTarget.entries()) {
    const parsed = parseTargetKey(key);
    if (!parsed) {
      continue;
    }
    const index = messages.findIndex((message) => message.id === messageId);
    if (index >= 0) {
      return { ...parsed, messages, index, message: messages[index] };
    }
  }
  return undefined;
}

function isBodyFormat(value: unknown): value is RawMessage["body_format"] {
  return value === "v1" || value === "v2" || value === "v3";
}

export function messageBubble(page: Page, messageId: string): Locator {
  return page.locator(`[data-testid="chat-msg-bubble"][data-message-id="${messageId}"]`);
}

export async function revealActions(page: Page, messageId: string): Promise<Locator> {
  const bubble = messageBubble(page, messageId);
  await expect(bubble).toBeVisible();
  await bubble.hover();
  return bubble;
}

export async function fillComposer(page: Page, text: string) {
  const input = page.getByTestId("chat-composer-input");
  await expect(input).toBeVisible();
  await input.click();
  await page.keyboard.insertText(text);
  await expect(input).toContainText(text);
}

export async function replaceEditorText(page: Page, editor: Locator, text: string) {
  await expect(editor).toBeVisible();
  await editor.click();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+A" : "Control+A");
  await page.keyboard.press("Backspace");
  await page.keyboard.type(text);
  await expect(editor).toHaveText(text);
}
