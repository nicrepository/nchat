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
  type DMCandidateFixture,
  type MessagingScenario,
  OTHER_USER_ID,
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
 * Objetivo: criar DM 1:1 e grupo ad-hoc de ponta a ponta pelo diálogo "Nova
 * conversa" — busca de pessoas, seleção, submit, navegação para a nova
 * conversa e atualização da sidebar via retry() (ChatSidebar.handleDMOpened /
 * handleChannelCreated).
 */
test.describe("criação de conversas — DM 1:1 e grupo ad-hoc", () => {
  test("cria uma DM 1:1 a partir da busca e a sidebar reflete a nova conversa", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
      dmCandidates: [{ userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME }],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await expect(dialog).toBeVisible();

    await dialog.getByLabel("Pesquisar pessoa").fill(SECOND_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: SECOND_CANDIDATE_NAME }).click();

    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/e2e-dm-with-${SECOND_CANDIDATE_ID}$`));

    // Sidebar refetch (retry()) placed the new 1:1 under "Mensagens diretas".
    await expect(
      page
        .getByRole("region", { name: "Mensagens diretas" })
        .getByRole("option", { name: `Mensagem direta com ${SECOND_CANDIDATE_NAME}` }),
    ).toBeVisible();

    expect(scenario.requests.dmCreates).toEqual([{ otherUserId: SECOND_CANDIDATE_ID }]);
  });

  test("cria um grupo ad-hoc com título e a sidebar reflete o novo grupo", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
      dmCandidates: [
        { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
        { userId: THIRD_CANDIDATE_ID, displayName: THIRD_CANDIDATE_NAME },
      ],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await dialog.getByRole("radio", { name: "Grupo" }).check();

    await dialog.getByLabel("Pesquisar pessoa").fill(SECOND_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: SECOND_CANDIDATE_NAME }).click();
    await dialog.getByLabel("Pesquisar pessoa").fill(THIRD_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: THIRD_CANDIDATE_NAME }).click();
    await dialog.getByRole("button", { name: "Continuar" }).click();
    await dialog.getByLabel("Nome do grupo (opcional)").fill("Infraestrutura E2E");

    await dialog.getByRole("button", { name: "Criar grupo" }).click();

    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(/\/chat\/dm\/e2e-group-1$/);
    await expect(
      page
        .getByRole("region", { name: "Grupos" })
        .getByRole("option", { name: "Grupo Infraestrutura E2E" }),
    ).toBeVisible();

    expect(scenario.requests.groupCreates).toEqual([
      {
        participantUserIds: [SECOND_CANDIDATE_ID, THIRD_CANDIDATE_ID],
        title: "Infraestrutura E2E",
      },
    ]);
  });

  test("pessoa indisponível: mostra erro genérico, mantém o diálogo aberto e não cria conversa", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
      // O candidato aparece na busca mas o servidor recusa a criação (404):
      // simula alguém que saiu do workspace entre a busca e o clique.
      dmCandidates: [],
    });
    await installMessagingMocks(page, scenario);
    // A busca é servida separadamente do candidato "oficial": injeta um
    // candidato só na resposta de busca para forçar a divergência.
    await page.route("**/api/chat/dm-candidates**", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { candidates: [{ user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME }] },
        }),
      }),
    );
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await dialog.getByLabel("Pesquisar pessoa").fill(OTHER_USER_NAME);
    await dialog.getByRole("button", { name: OTHER_USER_NAME }).click();

    await expect(dialog.getByRole("alert")).toHaveText(
      "Esta pessoa não está disponível para mensagens.",
    );
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
    expect(scenario.requests.dmCreates).toEqual([{ otherUserId: OTHER_USER_ID }]);
  });

  /**
   * Issue #721: o outro lado. Quem *recebe* uma DM inédita não está inscrito
   * numa conversa que não existia, então nenhum evento de sala o alcança — nem
   * o message.created da primeira mensagem. O servidor publica
   * conversation.available para ele, e a sidebar se reconcilia sozinha.
   *
   * Sem polling e sem espera fixa: a asserção é o próprio locator do Playwright,
   * e o marcador de reload prova que a conversa não apareceu por recarga.
   */
  test("destinatário: uma DM inédita aparece na sidebar em tempo real, sem recarregar", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    // Sobrevive a qualquer navegação/recarga da aba, então some se a sidebar
    // só se corrigir por reload.
    await page.evaluate(() => {
      (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload = true;
    });

    const incomingId = `${targetId}-incoming`;
    const incomingRow = page
      .getByRole("region", { name: "Mensagens diretas" })
      .getByRole("option", { name: new RegExp(`^Mensagem direta com ${SECOND_CANDIDATE_NAME}`) });
    await expect(incomingRow).toHaveCount(0);

    // O servidor já persistiu a DM e a primeira mensagem: a partir daqui
    // GET /api/chat/sidebar devolve a conversa, e um subscribe a ela é aceito.
    scenario.sidebarDMs.push({
      id: incomingId,
      type: "direct",
      name: SECOND_CANDIDATE_NAME,
      unread_count: 1,
      counterpart: { user_id: SECOND_CANDIDATE_ID, display_name: SECOND_CANDIDATE_NAME },
      last_message_at: "2026-06-01T12:00:00.000Z",
    });
    await grantConversationAccess(page, { kind: "dm", targetId: incomingId });

    await emitConversationAvailable(page, { kind: "dm", targetId: incomingId });

    await expect(incomingRow).toBeVisible();
    expect(
      await page.evaluate(
        () => (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload === true,
      ),
    ).toBe(true);
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
  });
});

/**
 * Issue #1103: um grupo recém-criado, de ponta a ponta no cliente, com três
 * sessões independentes — cada uma com seu próprio contexto de navegador, sua
 * identidade e seu socket. A cria o grupo pelo diálogo real; o "servidor" é um
 * só estado compartilhado pelas três, que faz o que o chat-service faz: commita
 * o grupo para os membros e, só depois, anuncia conversation.available a quem
 * não o criou.
 *
 * A janela crítica é controlada sem espera fixa: o refetch que a descoberta
 * dispara em B e C fica retido com a resposta do instante em que chegou, e é
 * nesse intervalo — B e C ainda sem assinatura — que A envia a primeira
 * mensagem. O message.created dela não alcança ninguém; quem tem que trazê-la é
 * a reconciliação que segue a assinatura, e o histórico ao abrir o grupo.
 */
test.describe("descoberta em tempo real de um grupo recém-criado (#1103)", () => {
  const groupId = "e2e-group-1103";
  const groupName = "Plantão E2E";

  type Message = ReturnType<typeof makeMessage>;

  interface Barrier {
    arrived: Promise<void>;
    arrive: () => void;
    released: Promise<void>;
    release: () => void;
  }

  function barrier(): Barrier {
    let arrive!: () => void;
    let release!: () => void;
    const arrived = new Promise<void>((resolve) => (arrive = resolve));
    const released = new Promise<void>((resolve) => (release = resolve));
    return { arrived, arrive, released, release };
  }

  interface Session {
    userId: string;
    name: string;
    context: BrowserContext;
    page: Page;
    scenario: MessagingScenario;
    /** Armed by the server when it announces; holds the next sidebar read. */
    hold?: Barrier;
  }

  async function openSession(
    browser: Browser,
    testInfo: TestInfo,
    who: { userId: string; name: string; candidates?: DMCandidateFixture[] },
  ): Promise<Session> {
    const context = await browser.newContext();
    const page = await context.newPage();
    const targetId = uniqueId(testInfo, `dm-${who.userId}`);
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
      dmCandidates: who.candidates,
    });
    await installMessagingMocks(page, scenario);
    const session: Session = { userId: who.userId, name: who.name, context, page, scenario };

    // Registered after the defaults, so they win: this session is who.userId.
    await page.route("**/api/auth/me", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: { id: who.userId, display_name: who.name } }),
      }),
    );
    await page.route("**/api/chat/sidebar", async (route) => {
      // The answer is what the server knew when the request arrived, even if
      // it is delivered after something else has been committed.
      const body = sidebarResponseBody(scenario, who.userId);
      const hold = session.hold;
      session.hold = undefined;
      if (hold) {
        hold.arrive();
        await hold.released;
      }
      await route.fulfill({ status: 200, contentType: "application/json", body });
    });

    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
    await page.evaluate(() => {
      (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload = true;
    });
    return session;
  }

  const groupRow = (page: Page) =>
    page
      .getByRole("region", { name: "Grupos" })
      .getByRole("option", { name: new RegExp(`^Grupo ${groupName}`) });

  const isSubscribed = (id: string) =>
    (
      window as unknown as {
        __e2eHasSubscription: (kind: string, targetId: string) => boolean;
      }
    ).__e2eHasSubscription("dm", id);

  const neverReloaded = (page: Page) =>
    page.evaluate(() => (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload === true);

  /**
   * The shared server, installed on A's page because A is the one who calls it.
   * Returns what it observed: who was announced to, and — per message — which
   * remote sessions held a live subscription when it was fanned out.
   */
  async function installSharedServer(creator: Session, remotes: Session[]) {
    const messages: Message[] = [];
    const announced: string[] = [];
    const liveAtSend: Array<Record<string, boolean>> = [];
    const holds = new Map(remotes.map((session) => [session.userId, barrier()]));

    await creator.page.route("**/api/chat/dms/group", async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      const body = route.request().postDataJSON() as {
        participant_user_ids: string[];
        title?: string;
      };
      const invited = remotes.filter((s) => body.participant_user_ids.includes(s.userId));
      // Commit: the group and its single message log exist for every member.
      for (const member of [creator, ...invited]) {
        member.scenario.sidebarDMs.push({
          id: groupId,
          type: "group",
          name: body.title ?? "",
          unread_count: 0,
        });
        member.scenario.messagesByTarget.set(`dm:${groupId}`, messages);
        if (member !== creator) {
          await grantConversationAccess(member.page, { kind: "dm", targetId: groupId });
        }
      }
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({ data: { conversation_id: groupId } }),
      });
      // After the commit, and to the invitees only — never to the creator.
      for (const member of invited) {
        member.hold = holds.get(member.userId);
        announced.push(member.userId);
        await emitConversationAvailable(member.page, { kind: "dm", targetId: groupId });
      }
    });

    await creator.page.route(`**/api/chat/dm/${groupId}/messages`, async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      const { body_text: bodyText = "" } = route.request().postDataJSON() as {
        body_text?: string;
      };
      const message = makeMessage({
        id: `${groupId}-msg-${messages.length + 1}`,
        sender_id: creator.userId,
        sender_display_name: creator.name,
        body_text: bodyText,
        body_format: "v2",
        created_at: new Date().toISOString(),
      });
      messages.push(message);
      // The room fan-out: every member's sockets, delivered only where that
      // target is subscribed — exactly the gap #1103 is about.
      const event = messageCreatedEvent({ kind: "dm", targetId: groupId, message });
      const live: Record<string, boolean> = {};
      for (const member of remotes) {
        const row = member.scenario.sidebarDMs.find((dm) => dm.id === groupId);
        if (row) {
          row.unread_count = (row.unread_count ?? 0) + 1;
          row.last_message_at = message.created_at;
        }
        live[member.userId] = await member.page.evaluate(isSubscribed, groupId);
        await member.page.evaluate((frame) => {
          (
            window as unknown as { __e2eEmitMessageCreated: (event: typeof frame) => void }
          ).__e2eEmitMessageCreated(frame);
        }, event);
      }
      liveAtSend.push(live);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: message }),
      });
    });

    return { announced, liveAtSend, holds };
  }

  async function sendAsCreator(page: Page, text: string) {
    await fillComposer(page, text);
    const posted = page.waitForResponse(
      (response) =>
        response.url().endsWith(`/api/chat/dm/${groupId}/messages`) &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await posted;
  }

  test("A cria com B e C; ambos descobrem o grupo e a primeira mensagem sem recarregar", async ({
    browser,
  }, testInfo) => {
    const a = await openSession(browser, testInfo, {
      userId: CURRENT_USER_ID,
      name: CURRENT_USER_NAME,
      candidates: [
        { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
        { userId: THIRD_CANDIDATE_ID, displayName: THIRD_CANDIDATE_NAME },
      ],
    });
    const b = await openSession(browser, testInfo, {
      userId: SECOND_CANDIDATE_ID,
      name: SECOND_CANDIDATE_NAME,
    });
    const c = await openSession(browser, testInfo, {
      userId: THIRD_CANDIDATE_ID,
      name: THIRD_CANDIDATE_NAME,
    });
    const remotes = [b, c];
    for (const session of remotes) await expect(groupRow(session.page)).toHaveCount(0);
    const server = await installSharedServer(a, remotes);

    // 1. A cria o grupo pelo diálogo do produto.
    await a.page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = a.page.getByRole("dialog", { name: "Nova conversa" });
    await dialog.getByRole("radio", { name: "Grupo" }).check();
    for (const name of [SECOND_CANDIDATE_NAME, THIRD_CANDIDATE_NAME]) {
      await dialog.getByLabel("Pesquisar pessoa").fill(name);
      await dialog.getByRole("button", { name }).click();
    }
    await dialog.getByRole("button", { name: "Continuar" }).click();
    await dialog.getByLabel("Nome do grupo (opcional)").fill(groupName);
    await dialog.getByRole("button", { name: "Criar grupo" }).click();
    await expect(a.page).toHaveURL(new RegExp(`/chat/dm/${groupId}$`));
    await expect(groupRow(a.page)).toHaveCount(1);

    // 2–3. A descoberta chegou a B e C, e o refetch de cada um está em voo.
    await Promise.all(remotes.map((s) => server.holds.get(s.userId)?.arrived));
    expect(server.announced).toEqual([b.userId, c.userId]);

    // 4. Antes de B/C terminarem o refresh e assinarem, A manda a primeira.
    await sendAsCreator(a.page, "primeira mensagem do grupo");
    expect(server.liveAtSend).toEqual([{ [b.userId]: false, [c.userId]: false }]);

    // 5. Os refetches terminam — com o estado de antes da mensagem.
    for (const session of remotes) server.holds.get(session.userId)?.release();

    for (const session of remotes) {
      const row = groupRow(session.page);
      await expect(row).toHaveCount(1);
      // 6. Assinatura confirmada pelo fluxo normal de subscriptions.
      await session.page.waitForFunction(isSubscribed, groupId);
      // 7. Só a reconciliação que segue a assinatura conhece a mensagem: a
      // resposta retida dizia zero.
      await expect(row.getByLabel("1 não lidas")).toBeVisible();

      // 8–9. Ao abrir, o histórico traz a primeira mensagem, de A.
      await row.click();
      await expect(session.page).toHaveURL(new RegExp(`/chat/dm/${groupId}$`));
      const first = messageBubble(session.page, `${groupId}-msg-1`);
      await expect(first).toContainText("primeira mensagem do grupo");
      await expect(first).toContainText(CURRENT_USER_NAME);
    }

    // 10. A seguinte chega pelo realtime normal, sem nova leitura do histórico.
    const historyReads = new Map(remotes.map((s) => [s.userId, 0]));
    for (const session of remotes) {
      session.page.on("request", (request) => {
        if (
          request.method() === "GET" &&
          new URL(request.url()).pathname === `/api/chat/dm/${groupId}/messages`
        ) {
          historyReads.set(session.userId, (historyReads.get(session.userId) ?? 0) + 1);
        }
      });
    }
    await sendAsCreator(a.page, "segunda mensagem do grupo");
    expect(server.liveAtSend[1]).toEqual({ [b.userId]: true, [c.userId]: true });
    for (const session of remotes) {
      await expect(messageBubble(session.page, `${groupId}-msg-2`)).toContainText(
        "segunda mensagem do grupo",
      );
      expect(historyReads.get(session.userId)).toBe(0);
    }

    // Uma linha só em cada sidebar, e ninguém recarregou.
    for (const session of [a, ...remotes]) {
      await expect(groupRow(session.page)).toHaveCount(1);
      expect(await neverReloaded(session.page)).toBe(true);
      await session.context.close();
    }
  });
});

/**
 * Issue #1023: Pessoa, Grupo e Canal são fluxos próprios dentro do mesmo
 * diálogo. Aqui só o que precisa de navegador real — foco, teclado, layout e
 * a volta do foco ao acionador; as combinações de draft ficam no Vitest.
 */
async function openWithTwoCandidates(page: Page, testInfo: TestInfo) {
  const targetId = uniqueId(testInfo, "dm");
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: OTHER_USER_NAME,
    messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    dmCandidates: [
      { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
      { userId: THIRD_CANDIDATE_ID, displayName: THIRD_CANDIDATE_NAME },
    ],
  });
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/dm/${targetId}`);
  return scenario;
}

test.describe("nova conversa — fluxos Pessoa, Grupo e Canal (#1023)", () => {
  test("teclado: drafts de Grupo e Canal sobrevivem à troca de modo e o foco volta ao acionador", async ({
    page,
  }, testInfo) => {
    const scenario = await openWithTwoCandidates(page, testInfo);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
    const trigger = page.getByRole("button", { name: "Nova conversa" });
    await trigger.focus();
    await page.keyboard.press("Enter");

    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    const search = dialog.getByRole("searchbox", { name: "Pesquisar pessoa" });
    await expect(search).toBeFocused();

    // Pessoa → Grupo pelas setas do seletor de modo; o foco fica no seletor.
    await page.keyboard.press("Shift+Tab");
    await expect(dialog.getByRole("radio", { name: "Pessoa" })).toBeFocused();
    await page.keyboard.press("ArrowRight");
    const groupRadio = dialog.getByRole("radio", { name: "Grupo" });
    await expect(groupRadio).toBeChecked();
    await expect(groupRadio).toBeFocused();

    // Draft de Grupo.
    await page.keyboard.press("Tab");
    await expect(search).toBeFocused();
    await page.keyboard.type(SECOND_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: SECOND_CANDIDATE_NAME }).press("Enter");
    await dialog.getByRole("button", { name: THIRD_CANDIDATE_NAME }).press("Enter");
    // Participantes → Identidade: focus lands on the name (issue #1026).
    await dialog.getByRole("button", { name: "Continuar" }).press("Enter");
    const groupName = dialog.getByLabel("Nome do grupo (opcional)");
    await expect(groupName).toBeFocused();
    await page.keyboard.type("Infra 🚀");

    // Grupo → Canal: o formulário não rouba o foco do seletor.
    await groupRadio.focus();
    await page.keyboard.press("ArrowRight");
    const channelRadio = dialog.getByRole("radio", { name: "Canal" });
    await expect(channelRadio).toBeFocused();
    const channelName = dialog.getByLabel("Nome do canal");
    await channelName.focus();
    await page.keyboard.type("Operações");
    await dialog.getByRole("radio", { name: "Privado" }).focus();
    await page.keyboard.press("Space");

    // Canal → Grupo: o draft de Grupo está intacto.
    await channelRadio.focus();
    await page.keyboard.press("ArrowLeft");
    await expect(groupRadio).toBeChecked();
    await expect(groupName).toHaveValue("Infra 🚀");
    await expect(dialog.getByRole("button", { name: "Criar grupo" })).toBeEnabled();
    // Voltar returns to Participantes with the selection intact.
    await dialog.getByRole("button", { name: "Voltar" }).press("Enter");
    await expect(search).toBeFocused();
    const chips = dialog.getByRole("list", { name: "Pessoas selecionadas" });
    await expect(chips).toContainText(SECOND_CANDIDATE_NAME);
    await expect(chips).toContainText(THIRD_CANDIDATE_NAME);

    // Grupo → Canal: o draft de Canal também.
    await groupRadio.focus();
    await page.keyboard.press("ArrowRight");
    await expect(channelName).toHaveValue("Operações");
    await expect(dialog.getByLabel("Identificador")).toHaveValue("operacoes");
    await expect(dialog.getByRole("radio", { name: "Privado" })).toBeChecked();

    // Escape fecha sem criar nada e devolve o foco ao botão "Nova conversa".
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(scenario.requests.groupCreates).toEqual([]);
    expect(scenario.requests.dmCreates).toEqual([]);
  });

  test("celular: modos sem overflow horizontal e com a ação de criar alcançável", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const scenario = await openWithTwoCandidates(page, testInfo);
    // No celular a navegação é uma gaveta: "Nova conversa" mora nela.
    await page.getByTestId("chat-nav-toggle").click();
    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });

    const overflow = () =>
      page.evaluate(() => {
        const root = document.documentElement;
        const modal = document.querySelector<HTMLElement>("[role='dialog']");
        return {
          page: root.scrollWidth - root.clientWidth,
          dialog: modal ? modal.scrollWidth - modal.clientWidth : -1,
        };
      });

    await dialog.getByRole("radio", { name: "Canal" }).check();
    const createChannel = dialog.getByRole("button", { name: "Criar canal" });
    await createChannel.scrollIntoViewIfNeeded();
    await expect(createChannel).toBeInViewport();
    expect(await overflow()).toEqual({ page: 0, dialog: 0 });

    await dialog.getByRole("radio", { name: "Grupo" }).check();
    await dialog.getByRole("searchbox", { name: "Pesquisar pessoa" }).fill(SECOND_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: SECOND_CANDIDATE_NAME }).click();
    await dialog.getByRole("button", { name: THIRD_CANDIDATE_NAME }).click();
    expect(await overflow()).toEqual({ page: 0, dialog: 0 });
    await dialog.getByRole("button", { name: "Continuar" }).click();
    // Identidade, with the emoji grid open: still no horizontal overflow.
    await dialog.getByRole("radio", { name: "Emoji" }).check();
    await expect(dialog.getByRole("searchbox", { name: "Buscar emoji" })).toBeVisible();
    expect(await overflow()).toEqual({ page: 0, dialog: 0 });
    await dialog.getByRole("radio", { name: "Automático" }).check();

    const createGroup = dialog.getByRole("button", { name: "Criar grupo" });
    await createGroup.scrollIntoViewIfNeeded();
    await expect(createGroup).toBeInViewport();
    await createGroup.click();

    await expect(dialog).toBeHidden();
    expect(scenario.requests.groupCreates).toEqual([
      { participantUserIds: [SECOND_CANDIDATE_ID, THIRD_CANDIDATE_ID], title: "" },
    ]);
  });
});

/**
 * Issue #1026: identidade de grupo — Automático (iniciais sobre fundo neutro) e
 * Emoji — da criação à sidebar, atravessando rename e o retorno a Automático.
 */
test.describe("identidade de grupo (#1026)", () => {
  async function openIdentityStep(page: Page) {
    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await dialog.getByRole("radio", { name: "Grupo" }).check();
    await dialog.getByRole("searchbox", { name: "Pesquisar pessoa" }).fill(SECOND_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: SECOND_CANDIDATE_NAME }).click();
    await dialog.getByRole("button", { name: THIRD_CANDIDATE_NAME }).click();
    await dialog.getByRole("button", { name: "Continuar" }).click();
    return dialog;
  }

  const groupRow = (page: Page, name: string) =>
    page.getByRole("region", { name: "Grupos" }).getByRole("option", { name: `Grupo ${name}` });

  test("Automático: iniciais do nome no preview e na sidebar, sem identidade no request", async ({
    page,
  }, testInfo) => {
    const scenario = await openWithTwoCandidates(page, testInfo);
    const dialog = await openIdentityStep(page);

    await expect(dialog.getByRole("radio", { name: "Automático" })).toBeChecked();
    await dialog.getByLabel("Nome do grupo (opcional)").fill("Infra Plataforma");
    await expect(
      dialog.getByRole("img", { name: "Prévia da identidade: iniciais IP" }),
    ).toBeVisible();
    // Double submit: one group.
    await dialog.getByRole("button", { name: "Criar grupo" }).dblclick();

    await expect(dialog).toBeHidden();
    const avatar = groupRow(page, "Infra Plataforma").locator(".group-avatar");
    await expect(avatar).toHaveText("IP");
    await expect(avatar).toHaveAttribute("data-mode", "auto");
    expect(scenario.requests.groupCreates).toEqual([
      { participantUserIds: [SECOND_CANDIDATE_ID, THIRD_CANDIDATE_ID], title: "Infra Plataforma" },
    ]);
    expect(scenario.requests.groupCreates[0]).not.toHaveProperty("avatarEmoji");
  });

  test("Emoji: sobrevive ao rename e volta a Automático com as iniciais do nome atual", async ({
    page,
  }, testInfo) => {
    const scenario = await openWithTwoCandidates(page, testInfo);
    const dialog = await openIdentityStep(page);

    await dialog.getByLabel("Nome do grupo (opcional)").fill("Lançamento");
    await dialog.getByRole("radio", { name: "Emoji" }).check();
    await expect(dialog.getByRole("button", { name: "Criar grupo" })).toBeDisabled();
    await dialog.getByRole("searchbox", { name: "Buscar emoji" }).fill("foguete");
    await dialog.getByRole("button", { name: "foguete", exact: true }).click();
    await expect(dialog.getByRole("img", { name: "Prévia da identidade: emoji 🚀" })).toBeVisible();
    await dialog.getByRole("button", { name: "Criar grupo" }).click();

    await expect(dialog).toBeHidden();
    await expect(groupRow(page, "Lançamento").locator(".group-avatar")).toHaveText("🚀");
    expect(scenario.requests.groupCreates).toEqual([
      {
        participantUserIds: [SECOND_CANDIDATE_ID, THIRD_CANDIDATE_ID],
        title: "Lançamento",
        avatarEmoji: "🚀",
      },
    ]);

    // Rename keeps the emoji.
    await page.getByRole("button", { name: "Mais opções para grupo Lançamento" }).click();
    await page.getByRole("menuitem", { name: "Renomear grupo" }).click();
    const rename = page.getByRole("dialog", { name: "Renomear grupo" });
    await rename.getByLabel("Nome do grupo").fill("Novo Nome");
    await rename.getByRole("button", { name: "Salvar" }).click();
    await expect(rename).toBeHidden();
    await expect(groupRow(page, "Novo Nome").locator(".group-avatar")).toHaveText("🚀");

    // Back to Automático: the emoji is removed, initials come from the current name.
    await page.getByRole("button", { name: "Mais opções para grupo Novo Nome" }).click();
    await page.getByRole("menuitem", { name: "Alterar identidade" }).click();
    const identity = page.getByRole("dialog", { name: "Identidade do grupo" });
    await expect(identity.getByRole("radio", { name: "Emoji" })).toBeChecked();
    await identity.getByRole("radio", { name: "Automático" }).check();
    await expect(
      identity.getByRole("img", { name: "Prévia da identidade: iniciais NN" }),
    ).toBeVisible();
    await identity.getByRole("button", { name: "Salvar" }).click();
    await expect(identity).toBeHidden();
    // Saving returns focus to the row's menu button, like dismissing does.
    await expect(
      page.getByRole("button", { name: "Mais opções para grupo Novo Nome" }),
    ).toBeFocused();

    const avatar = groupRow(page, "Novo Nome").locator(".group-avatar");
    await expect(avatar).toHaveText("NN");
    await expect(avatar).toHaveAttribute("data-mode", "auto");
    expect(scenario.requests.groupAvatars).toEqual([
      { conversationId: "e2e-group-1", emoji: null },
    ]);
  });

  test("teclado: Escape fecha o diálogo de identidade sem gravar nada", async ({
    page,
  }, testInfo) => {
    const scenario = await openWithTwoCandidates(page, testInfo);
    const dialog = await openIdentityStep(page);
    await dialog.getByRole("button", { name: "Criar grupo" }).click();
    await expect(dialog).toBeHidden();

    const actions = page.getByRole("button", { name: "Mais opções para grupo Grupo sem nome" });
    await actions.click();
    await page.getByRole("menuitem", { name: "Alterar identidade" }).click();
    const identity = page.getByRole("dialog", { name: "Identidade do grupo" });
    await expect(identity.getByRole("radio", { name: "Automático" })).toBeFocused();
    await page.keyboard.press("ArrowRight");
    await expect(identity.getByRole("radio", { name: "Emoji" })).toBeChecked();
    await page.keyboard.press("Escape");
    await expect(identity).toBeHidden();
    expect(scenario.requests.groupAvatars).toEqual([]);
  });

  /**
   * The modal keeps focus with real keyboard events: a radio group is one Tab
   * stop (its checked radio), so with Emoji selected the unchecked Automático
   * is not a stop and must not be treated as the dialog's first one. Focus
   * comes back to the row's "Mais opções" button, the control that opened the
   * menu the dialog was chosen from.
   */
  test("foco: Tab e Shift+Tab ficam no diálogo com Emoji, e Escape devolve o foco ao acionador", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });
    scenario.sidebarDMs.push({
      id: "e2e-group-emoji",
      type: "group",
      name: "Projeto",
      unread_count: 0,
      avatar_emoji: "🚀",
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const trigger = page.getByRole("button", { name: "Mais opções para grupo Projeto" });
    await trigger.click();
    await page.getByRole("menuitem", { name: "Alterar identidade" }).click();
    const dialog = page.getByRole("dialog", { name: "Identidade do grupo" });
    const emojiRadio = dialog.getByRole("radio", { name: "Emoji" });
    await expect(emojiRadio).toBeChecked();
    await expect(dialog.getByRole("searchbox", { name: "Buscar emoji" })).toBeVisible();
    const focusInside = () =>
      dialog.evaluate((element) => element.contains(document.activeElement));

    // Shift+Tab from the checked radio wraps to the last stop, never out.
    await emojiRadio.focus();
    await page.keyboard.press("Shift+Tab");
    await expect(dialog.getByRole("button", { name: "Cancelar" })).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(emojiRadio).toBeFocused();

    // A full lap in each direction stays inside.
    for (const key of ["Tab", "Shift+Tab"]) {
      for (let press = 0; press < 25; press += 1) {
        await page.keyboard.press(key);
        expect(await focusInside()).toBe(true);
      }
    }

    await emojiRadio.focus();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(scenario.requests.groupAvatars).toEqual([]);
  });

  /**
   * The skin-tone palette is portalled to <body>, outside the dialog's DOM. It
   * is a sub-overlay of the modal: Tab and Shift+Tab stay on it, Escape closes
   * only it and hands focus back to its emoji, choosing a tone does the same,
   * and only then does Escape close the dialog — back to the row's trigger.
   */
  test("foco: a paleta de tons, portalled, não deixa o foco escapar do modal", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });
    scenario.sidebarDMs.push({
      id: "e2e-group-tones",
      type: "group",
      name: "Projeto",
      unread_count: 0,
      avatar_emoji: "🚀",
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const trigger = page.getByRole("button", { name: "Mais opções para grupo Projeto" });
    await trigger.focus();
    await page.keyboard.press("Enter");
    await page.getByRole("menuitem", { name: "Alterar identidade" }).focus();
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog", { name: "Identidade do grupo" });
    await expect(dialog.getByRole("radio", { name: "Emoji" })).toBeChecked();

    const search = dialog.getByRole("searchbox", { name: "Buscar emoji" });
    await search.focus();
    await page.keyboard.type("polegar");
    const thumbs = dialog.getByRole("button", { name: "polegar para cima", exact: true });
    await thumbs.focus();
    await page.keyboard.press("Enter");

    const palette = page.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
    await expect(palette).toBeVisible();
    const focusInPalette = () =>
      palette.evaluate((element) => element.contains(document.activeElement));
    expect(await focusInPalette()).toBe(true);

    for (const key of ["Tab", "Shift+Tab"]) {
      for (let press = 0; press < 8; press += 1) {
        await page.keyboard.press(key);
        expect(await focusInPalette()).toBe(true);
      }
    }

    // Escape closes the palette only, back to its emoji; the dialog stays.
    await page.keyboard.press("Escape");
    await expect(palette).toBeHidden();
    await expect(dialog).toBeVisible();
    await expect(thumbs).toBeFocused();

    // Choosing a tone closes the palette the same way.
    await page.keyboard.press("Enter");
    await expect(palette).toBeVisible();
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("Enter");
    await expect(palette).toBeHidden();
    await expect(thumbs).toBeFocused();
    await expect(dialog.getByRole("img", { name: /Prévia da identidade: emoji 👍/ })).toBeVisible();

    // Then Escape closes the dialog, back to the row's trigger.
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(scenario.requests.groupAvatars).toEqual([]);
  });
});
