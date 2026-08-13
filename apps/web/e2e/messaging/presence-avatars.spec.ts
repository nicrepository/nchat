import { expect, test, type Page } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_CHANNEL_ID,
  OTHER_CHANNEL_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  SECOND_CANDIDATE_ID,
  SECOND_CANDIDATE_NAME,
  channelDetailsFixture,
  createScenario,
  groupDetailsFixture,
  dropWebSocket,
  emitPresence,
  installMessagingMocks,
  makeMessage,
  setPresenceRoster,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * RF-58 — presence on avatars, in real time.
 *
 * What these specs are for: the observer's side. A second person's state
 * changes on the server and this tab has to follow it without a reload, say so
 * in words as well as in colour, and never claim "offline" for someone it has
 * simply not been told about yet.
 *
 * What they deliberately do not attempt: proving that a dropped TCP connection
 * eventually becomes offline, or that closing one of two devices leaves a user
 * present. Those are decisions the server makes from its own connection table,
 * and asserting them through a mocked socket would only be asserting the mock.
 * They are covered where they live, in the chat-service presence tests.
 */

const ONLINE_AT = "2026-08-11T10:00:00.000Z";
const AWAY_AT = "2026-08-11T10:05:00.000Z";
const OFFLINE_AT = "2026-08-11T10:10:00.000Z";

function dmRow(page: Page) {
  return page.getByRole("option", { name: new RegExp(`Mensagem direta com ${OTHER_USER_NAME}`) });
}

function dot(page: Page) {
  return dmRow(page).getByTestId("presence-dot");
}

async function openDM(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
  const targetId = uniqueId(testInfo, "dm");
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: OTHER_USER_NAME,
    messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
  });
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/dm/${targetId}`);
  await expect(dmRow(page)).toBeVisible();
  return { scenario, targetId };
}

test.describe("presença nos avatares (RF-58)", () => {
  test("lê ausência no snapshot como offline, depois de o servidor responder", async ({
    page,
  }, testInfo) => {
    // Nobody is connected in this scenario, so the snapshot that answers the
    // subscribe is empty — which is still an answer. Only then does absence
    // mean offline; the window before it is where the indicator stays silent,
    // and that is asserted in presence.test.tsx where the socket can be held
    // open without answering.
    await openDM(page, testInfo);

    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Offline`,
    );
    await expect(dot(page)).toHaveAttribute("data-presence", "offline");
  });

  test("observa o outro usuário ficar online, ausente e offline sem recarregar", async ({
    page,
  }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);

    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "online", updated_at: ONLINE_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "online");
    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Online`,
    );

    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "away", updated_at: AWAY_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "away");
    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Ausente`,
    );

    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "offline", updated_at: OFFLINE_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "offline");
    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Offline`,
    );

    // The page was never reloaded: the message that was on screen at the start
    // still is.
    await expect(page.getByText("olá")).toBeVisible();
  });

  test("descarta um evento mais antigo que o já aplicado", async ({ page }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);

    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "offline", updated_at: OFFLINE_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "offline");

    // A late delivery of an earlier transition. The server stated when each
    // happened, so the client can refuse this one instead of flickering back.
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "online", updated_at: ONLINE_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "offline");
  });

  test("reconexão restaura a presença a partir do snapshot", async ({ page }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);

    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "online", updated_at: ONLINE_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "online");

    // The world moves while this tab is disconnected. No event for it is
    // coming: the server does not replay, so the snapshot after the
    // resubscribe is the only thing that can correct the view.
    await setPresenceRoster(page, { kind: "dm", targetId }, [
      { user_id: OTHER_USER_ID, state: "away", updated_at: AWAY_AT },
    ]);
    await dropWebSocket(page);

    await expect(dot(page)).toHaveAttribute("data-presence", "away");
    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Ausente`,
    );
  });

  test("o estado é legível por mouse, por teclado e por leitor de tela", async ({
    page,
  }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "away", updated_at: AWAY_AT },
    });

    // Mouse: the indicator carries the word as its tooltip.
    await expect(dot(page)).toHaveAttribute("title", "Ausente");

    // Screen reader and keyboard: the row's own accessible name states it, so
    // the information does not depend on hovering a nine-pixel circle, and the
    // avatar did not have to become a focusable control to expose it.
    await dmRow(page).focus();
    await expect(dmRow(page)).toBeFocused();
    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Ausente`,
    );
  });

  test("mostra a presença do próprio usuário autenticado no rodapé", async ({ page }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: CURRENT_USER_ID, state: "online", updated_at: ONLINE_AT },
    });

    const profile = page.getByRole("link", {
      name: new RegExp(`Meu perfil de ${CURRENT_USER_NAME}`),
    });
    await expect(profile).toHaveAccessibleName(`Meu perfil de ${CURRENT_USER_NAME}, Online`);
    await expect(profile.getByTestId("presence-dot")).toHaveAttribute("data-presence", "online");
  });
});

test.describe("assinatura compartilhada e acessibilidade (#444)", () => {
  // A conversa aberta e a sidebar observam o mesmo alvo pela mesma conexão. Sair
  // da conversa não pode cancelar a assinatura que a sidebar ainda usa — era
  // exatamente isso que fazia a linha da sidebar parar de receber presença de
  // uma conversa da qual o usuário apenas tinha navegado para longe.
  test("mantém o alvo assinado pela sidebar ao trocar de conversa", async ({ page }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "online", updated_at: ONLINE_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "online");

    // Sai da DM para um canal. A sidebar continua mostrando a DM.
    await page.getByRole("option", { name: new RegExp(OTHER_CHANNEL_NAME) }).click();
    await expect(page.getByTestId("chat-msg-header")).toContainText(OTHER_CHANNEL_NAME);

    // Nenhum unsubscribe do alvo que a sidebar ainda observa.
    const frames = await page.evaluate(() =>
      (
        window as unknown as { __e2eWebSocketMessages: () => Array<Record<string, unknown>> }
      ).__e2eWebSocketMessages(),
    );
    expect(
      frames.filter(
        (frame) =>
          frame["type"] === "unsubscribe" &&
          frame["target_type"] === "dm" &&
          frame["target_id"] === targetId,
      ),
    ).toHaveLength(0);
    // E um único subscribe por alvo, mesmo com dois consumidores.
    expect(
      frames.filter(
        (frame) =>
          frame["type"] === "subscribe" &&
          frame["target_type"] === "dm" &&
          frame["target_id"] === targetId,
      ),
    ).toHaveLength(1);

    // A prova que interessa: a sidebar continua recebendo atualizações do alvo.
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "away", updated_at: AWAY_AT },
    });
    await expect(dot(page)).toHaveAttribute("data-presence", "away");
  });

  // O ponto ao lado do avatar da mensagem é decorativo (aria-hidden), então sem
  // um equivalente textual o estado do remetente dependia só da cor.
  test("anuncia o estado do remetente na mensagem, não apenas pela cor", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [
        makeMessage({
          id: `${targetId}-msg`,
          body_text: "olá",
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
        }),
      ],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-msg-bubble")).toBeVisible();

    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "online", updated_at: ONLINE_AT },
    });

    const bubble = page.getByTestId("chat-msg-bubble");
    await expect(bubble.getByTestId("chat-msg-sender")).toHaveText(OTHER_USER_NAME);
    await expect(bubble.getByTestId("chat-msg-sender-presence")).toHaveText("Status: Online");
    await expect(bubble.getByTestId("presence-dot")).toHaveAttribute("aria-hidden", "true");
  });
});

test.describe("presença por conversa (RF-58)", () => {
  // O roster é a resposta a "quem está presente *nesta* conversa". Um mock que
  // respondesse a lista inteira da fixture para qualquer alvo não conseguiria
  // falhar quando o cliente vazasse alguém de outra conversa — que é
  // exatamente o erro que estes specs existem para pegar.
  test("cada alvo recebe somente o seu próprio roster", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });

    // Canal com A e B; grupo com A e C. Ninguém aparece nos dois.
    scenario.channelDetails.set(
      OTHER_CHANNEL_ID,
      channelDetailsFixture(
        {
          id: OTHER_CHANNEL_ID,
          slug: "e2e-canal-secundario",
          display_name: OTHER_CHANNEL_NAME,
          type: "public",
        },
        [
          {
            user_id: CURRENT_USER_ID,
            display_name: CURRENT_USER_NAME,
            role: "member",
            presence: "online",
          },
          {
            user_id: OTHER_USER_ID,
            display_name: OTHER_USER_NAME,
            role: "member",
            presence: "online",
          },
        ],
      ),
    );
    scenario.groupDetails.set(
      GROUP_DM_ID,
      groupDetailsFixture({ id: GROUP_DM_ID, name: GROUP_DM_NAME }, [
        { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
        { user_id: SECOND_CANDIDATE_ID, display_name: SECOND_CANDIDATE_NAME, presence: "online" },
      ]),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(dmRow(page)).toBeVisible();

    const rosters = await snapshotsByTarget(page);

    const channelRoster = rosters[`channel:${OTHER_CHANNEL_ID}`] ?? [];
    expect(channelRoster).toContain(OTHER_USER_ID);
    // C existe na fixture, mas não neste canal.
    expect(channelRoster).not.toContain(SECOND_CANDIDATE_ID);

    const groupRoster = rosters[`dm:${GROUP_DM_ID}`] ?? [];
    expect(groupRoster).toContain(SECOND_CANDIDATE_ID);
    // B existe na fixture, mas não neste grupo.
    expect(groupRoster).not.toContain(OTHER_USER_ID);
  });
});

/**
 * Os snapshots que o cliente recebeu, por alvo. Lê os frames entregues em vez de
 * esperar um tempo arbitrário: a asserção acontece quando eles chegaram.
 */
async function snapshotsByTarget(page: Page): Promise<Record<string, string[]>> {
  await page.waitForFunction(
    () =>
      (window as unknown as { __e2eReceivedSnapshots?: () => unknown[] }).__e2eReceivedSnapshots?.()
        ?.length !== undefined,
  );
  await expect
    .poll(async () =>
      page.evaluate(
        () =>
          (
            window as unknown as { __e2eReceivedSnapshots: () => unknown[] }
          ).__e2eReceivedSnapshots().length,
      ),
    )
    .toBeGreaterThan(1);

  return page.evaluate(() => {
    const frames = (
      window as unknown as {
        __e2eReceivedSnapshots: () => Array<{
          target_type: string;
          target_id: string;
          users: Array<{ user_id: string }>;
        }>;
      }
    ).__e2eReceivedSnapshots();
    const byTarget: Record<string, string[]> = {};
    for (const frame of frames) {
      byTarget[`${frame.target_type}:${frame.target_id}`] = frame.users.map((u) => u.user_id);
    }
    return byTarget;
  });
}
