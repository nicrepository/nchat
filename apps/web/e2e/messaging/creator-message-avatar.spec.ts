import {
  type Browser,
  type BrowserContext,
  expect,
  type Page,
  test,
  type TestInfo,
} from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  type MessagingScenario,
  OTHER_USER_NAME,
  SECOND_CANDIDATE_ID,
  SECOND_CANDIDATE_NAME,
  THIRD_CANDIDATE_ID,
  THIRD_CANDIDATE_NAME,
  createScenario,
  emitConversationAvailable,
  fillComposer,
  grantConversationAccess,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  messageCreatedEvent,
  sidebarResponseBody,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #1104 — whoever creates a group or channel shows their avatar on their
 * messages to everybody else, like any other participant.
 *
 * The server records conversation_created with the creator as sender. It draws
 * no bubble, but the timeline used to count it as the start of the creator's
 * run, so the creator's first message right after it was "grouped" and its
 * avatar hidden (opacity 0) for every reader.
 *
 * Two people in independent browser contexts — own /api/auth/me, own identity,
 * own avatar, own socket — and one simulated server shared by both, the
 * pattern of #1103 in conversation-creation.spec.ts. The creator goes through
 * the real "Nova conversa" dialog and the real composer; the other person reads
 * the result from history, from realtime and after a reload.
 */

interface Person {
  userId: string;
  name: string;
  photo: string;
}

const A: Person = {
  userId: CURRENT_USER_ID,
  name: CURRENT_USER_NAME,
  photo: "/api/auth/avatars/e2e-1104-a.svg",
};
const B: Person = {
  userId: SECOND_CANDIDATE_ID,
  name: SECOND_CANDIDATE_NAME,
  photo: "/api/auth/avatars/e2e-1104-b.svg",
};

/**
 * A group needs at least two invitees, so C is invited too. C never opens a
 * browser: the two people observed are A and B.
 */
const C = { userId: THIRD_CANDIDATE_ID, name: THIRD_CANDIDATE_NAME };

type Kind = "group" | "channel";
type Message = ReturnType<typeof makeMessage>;

interface Session {
  person: Person;
  context: BrowserContext;
  page: Page;
  scenario: MessagingScenario;
}

interface Room {
  kind: Kind;
  id: string;
  name: string;
  /** The conversation's single message log, shared by every member. */
  log: Message[];
}

const wsKind = (room: Room) => (room.kind === "channel" ? "channel" : "dm");
const roomUrl = (room: Room) => `/chat/${wsKind(room)}/${room.id}`;
const messagesPath = (room: Room) =>
  room.kind === "channel"
    ? `/api/chat/channels/${room.id}/messages`
    : `/api/chat/dm/${room.id}/messages`;

/**
 * The simulated server's clock: one second per row of the log. The creation
 * event and the first messages land in the same minute every run, which is
 * exactly the sequence that used to group the creator's first message.
 */
const CLOCK_START = Date.parse("2026-07-15T12:00:00.000Z");
const serverTime = (room: Room) => new Date(CLOCK_START + room.log.length * 1000).toISOString();

function photoSvg(color: string) {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1" fill="${color}"/></svg>`;
}

async function openUserSession(
  browser: Browser,
  testInfo: TestInfo,
  person: Person,
  other: Person,
): Promise<Session> {
  const context = await browser.newContext();
  const page = await context.newPage();
  const homeId = uniqueId(testInfo, `home-${person.userId}`);
  const scenario = createScenario({
    kind: "dm",
    targetId: homeId,
    targetName: OTHER_USER_NAME,
    messages: [],
    dmCandidates: [other, C].map(({ userId, name }) => ({ userId, displayName: name })),
  });
  await installMessagingMocks(page, scenario);
  // Registered after the defaults, so they win: this session is person.userId.
  await page.route("**/api/auth/me", (route) =>
    route.fulfill({
      json: { data: { id: person.userId, display_name: person.name, avatar_url: person.photo } },
    }),
  );
  await page.route("**/api/chat/sidebar", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: sidebarResponseBody(scenario, person.userId),
    }),
  );
  await page.route("**/api/auth/avatars/*", (route) =>
    route.fulfill({
      contentType: "image/svg+xml",
      body: photoSvg(route.request().url().endsWith(A.photo) ? "#2864b4" : "#b42864"),
    }),
  );
  await page.goto(`/chat/dm/${homeId}`);
  await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
  return { person, context, page, scenario };
}

/** What the server commits for a new conversation, for each of its members. */
async function commitRoom(room: Room, members: Session[]) {
  for (const { scenario, page } of members) {
    if (room.kind === "channel") {
      scenario.sidebarChannels.push({
        id: room.id,
        slug: room.id,
        display_name: room.name,
        type: "private",
        can_write: true,
        unread_count: 0,
      });
    } else {
      scenario.sidebarDMs.push({ id: room.id, type: "group", name: room.name, unread_count: 0 });
    }
    scenario.messagesByTarget.set(`${wsKind(room)}:${room.id}`, room.log);
    await grantConversationAccess(page, { kind: wsKind(room), targetId: room.id });
  }
}

/**
 * The creation endpoint, on the creator's page: the conversation, its
 * conversation_created event authored by the creator, both memberships, then
 * conversation.available to the invitee — the order the chat-service follows.
 */
async function installCreateEndpoint(room: Room, creator: Session, member: Session) {
  const pattern = room.kind === "channel" ? "**/api/chat/channels" : "**/api/chat/dms/group";
  await creator.page.route(pattern, async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    room.log.push(
      makeMessage({
        id: `${room.id}-created`,
        kind: "system",
        sender_id: creator.person.userId,
        sender_display_name: creator.person.name,
        event_type: "conversation_created",
        created_at: serverTime(room),
      }),
    );
    await commitRoom(room, [creator, member]);
    const data =
      room.kind === "channel"
        ? { id: room.id, slug: room.id, display_name: room.name, type: "private" }
        : { conversation_id: room.id };
    await route.fulfill({ status: 201, json: { data } });
    await emitConversationAvailable(member.page, { kind: wsKind(room), targetId: room.id });
  });
}

/**
 * The send endpoint, on every member's page: the author's identity and avatar
 * come from the server's view of that session, never from the request, and the
 * message is fanned out to the other member's socket.
 */
async function installSendEndpoint(room: Room, author: Session, others: Session[]) {
  await author.page.route(`**${messagesPath(room)}`, async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    const { body_text: bodyText = "" } = route.request().postDataJSON() as { body_text?: string };
    const message = makeMessage({
      id: `${room.id}-msg-${room.log.length}`,
      sender_id: author.person.userId,
      sender_display_name: author.person.name,
      sender_avatar_url: author.person.photo,
      body_text: bodyText,
      body_format: "v2",
      created_at: serverTime(room),
    });
    room.log.push(message);
    const event = messageCreatedEvent({ kind: wsKind(room), targetId: room.id, message });
    for (const other of others) {
      await other.page.evaluate((frame) => {
        (
          window as unknown as { __e2eEmitMessageCreated: (event: typeof frame) => void }
        ).__e2eEmitMessageCreated(frame);
      }, event);
    }
    await route.fulfill({ json: { data: message } });
  });
}

async function installSharedServer(room: Room, creator: Session, member: Session) {
  await installCreateEndpoint(room, creator, member);
  await installSendEndpoint(room, creator, [member]);
  await installSendEndpoint(room, member, [creator]);
}

async function createGroupAs(creator: Session, room: Room, invitee: Person) {
  const { page } = creator;
  await page.getByRole("button", { name: "Nova conversa" }).click();
  const dialog = page.getByRole("dialog", { name: "Nova conversa" });
  await dialog.getByRole("radio", { name: "Grupo" }).check();
  for (const { name } of [invitee, C]) {
    await dialog.getByLabel("Pesquisar pessoa").fill(name);
    await dialog.getByRole("button", { name }).click();
  }
  await dialog.getByRole("button", { name: "Continuar" }).click();
  await dialog.getByLabel("Nome do grupo (opcional)").fill(room.name);
  await dialog.getByRole("button", { name: "Criar grupo" }).click();
  await expect(dialog).toBeHidden();
}

async function createChannelAs(creator: Session, room: Room, invitee: Person) {
  const { page } = creator;
  await page.getByRole("button", { name: "Nova conversa" }).click();
  const dialog = page.getByRole("dialog", { name: "Nova conversa" });
  await dialog.getByRole("radio", { name: "Canal" }).check();
  await dialog.getByLabel("Nome do canal").fill(room.name);
  await dialog.getByRole("radio", { name: "Privado" }).check();
  await dialog.getByRole("searchbox", { name: "Pesquisar pessoa" }).fill(invitee.name);
  await dialog
    .getByRole("list", { name: "Pessoas encontradas" })
    .getByRole("button", { name: invitee.name })
    .click();
  await dialog.getByRole("button", { name: "Criar canal" }).click();
  await expect(dialog).toBeHidden();
}

async function sendAs(session: Session, room: Room, text: string) {
  const { page } = session;
  await fillComposer(page, text);
  const posted = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === messagesPath(room) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Enviar mensagem" }).click();
  await posted;
}

function roomRow(page: Page, room: Room) {
  const region = room.kind === "channel" ? "Canais" : "Grupos";
  return page
    .getByRole("region", { name: region })
    .getByRole("option", { name: new RegExp(room.name) });
}

async function openRoom(session: Session, room: Room) {
  await roomRow(session.page, room).click();
  await expect(session.page).toHaveURL(new RegExp(`${roomUrl(room)}$`));
}

async function waitSubscribed(session: Session, room: Room) {
  await session.page.waitForFunction(
    ({ kind, id }) =>
      (
        window as unknown as { __e2eHasSubscription: (kind: string, id: string) => boolean }
      ).__e2eHasSubscription(kind, id),
    { kind: wsKind(room), id: room.id },
  );
}

/** A message that opens its author's run: name, and their own avatar, shown. */
async function assertAuthorAvatarVisible(page: Page, messageId: string, author: Person) {
  const bubble = messageBubble(page, messageId);
  await expect(bubble).toBeVisible();
  await expect(bubble).not.toHaveClass(/chat-msg-area__msg--grouped/);
  await expect(bubble).toContainText(author.name);
  const avatar = bubble.locator(".chat-msg-area__msg-avatar");
  await expect(avatar).toHaveCSS("opacity", "1");
  await expect(avatar.locator("img")).toHaveAttribute("src", author.photo);
}

/** History as the server holds it: conversation_created, then the creator's first words. */
async function assertCreatorOpensRun(page: Page, room: Room, creator: Person) {
  const createdId = `${room.id}-created`;
  await expect(
    page.locator(`[data-testid="chat-system-message"][data-message-id="${createdId}"]`),
  ).toBeVisible();
  await expect(messageBubble(page, createdId)).toHaveCount(0);
  await assertAuthorAvatarVisible(page, `${room.id}-msg-1`, creator);
}

async function creatorScenario(
  browser: Browser,
  testInfo: TestInfo,
  kind: Kind,
  roles: { creator: Person; member: Person },
) {
  const creator = await openUserSession(browser, testInfo, roles.creator, roles.member);
  const member = await openUserSession(browser, testInfo, roles.member, roles.creator);
  const room: Room = {
    kind,
    id: `e2e-1104-${kind}-${roles.creator.userId}`,
    name: `Criado por ${roles.creator.name}`,
    log: [],
  };
  await installSharedServer(room, creator, member);

  // 1. The creator creates through the product's dialog and speaks first.
  await (kind === "channel" ? createChannelAs : createGroupAs)(creator, room, roles.member);
  await expect(creator.page).toHaveURL(new RegExp(`${roomUrl(room)}$`));
  await sendAs(creator, room, "primeira do criador");
  expect(room.log.map((m) => [m.kind, m.sender_id])).toEqual([
    ["system", roles.creator.userId],
    ["user", roles.creator.userId],
  ]);

  // 2. History: the other member opens it and sees the creator's avatar.
  await openRoom(member, room);
  await assertCreatorOpensRun(member.page, room, roles.creator);

  // 3. Realtime: the creator again, over the member's socket. Same author in
  // the same minute, so this one may be grouped by design — identity is what
  // is checked, not the visibility of a continuation avatar.
  await waitSubscribed(member, room);
  await sendAs(creator, room, "segunda do criador");
  const second = messageBubble(member.page, `${room.id}-msg-2`);
  await expect(second).toContainText("segunda do criador");
  await expect(second.locator(".chat-msg-area__msg-avatar img")).toHaveAttribute(
    "src",
    roles.creator.photo,
  );

  // 4. The ordinary member answers; the creator sees the member's own avatar.
  await waitSubscribed(creator, room);
  await sendAs(member, room, "resposta do membro");
  await assertAuthorAvatarVisible(creator.page, `${room.id}-msg-3`, roles.member);

  // 5. Reload: the same history still opens with the creator's avatar.
  await member.page.reload();
  await assertCreatorOpensRun(member.page, room, roles.creator);

  await creator.context.close();
  await member.context.close();
}

for (const kind of ["group", "channel"] as const) {
  test.describe(`avatar do criador em ${kind === "group" ? "grupo" : "canal"} (#1104)`, () => {
    test("A cria; B vê o avatar de A no histórico, em tempo real e após reload", async ({
      browser,
    }, testInfo) => {
      await creatorScenario(browser, testInfo, kind, { creator: A, member: B });
    });

    test("B cria; A vê o avatar de B no histórico, em tempo real e após reload", async ({
      browser,
    }, testInfo) => {
      await creatorScenario(browser, testInfo, kind, { creator: B, member: A });
    });
  });
}
