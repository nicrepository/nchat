import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_CHANNEL_ID,
  SECOND_CANDIDATE_ID,
  SECOND_CANDIDATE_NAME,
  createScenario,
  installMessagingMocks,
  makeMessage,
  messagesFor,
  uniqueId,
} from "../helpers/messagingApi";

test.describe("auto-add por menção", () => {
  test("reconcilia leituras concorrentes do evento sem mostrar o banner de realtime", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "mention-auto-add");
    const event = makeMessage({
      id: `${targetId}-member-added`,
      kind: "system",
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: "",
      event_type: "conversation_member_added",
      event_payload: {
        target_users: [{ user_id: SECOND_CANDIDATE_ID, display_name: SECOND_CANDIDATE_NAME }],
      },
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal auto-add E2E",
      messages: [],
      dmCandidates: [{ userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME }],
    });

    await installMessagingMocks(page, scenario);

    await page.route(`**/api/chat/channels/${targetId}/mentions**`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            users: [
              {
                type: "user",
                id: SECOND_CANDIDATE_ID,
                label: SECOND_CANDIDATE_NAME,
                will_be_added: true,
              },
            ],
            channels: [],
          },
        }),
      }),
    );

    let eventReads = 0;
    let announceConversationEvent!: () => void;
    const conversationEventAnnounced = new Promise<void>((resolve) => {
      announceConversationEvent = resolve;
    });
    let announceSecondRead!: () => void;
    const secondReadStarted = new Promise<void>((resolve) => {
      announceSecondRead = resolve;
    });
    await page.route(`**/api/chat/channels/${targetId}/messages/${event.id}`, async (route) => {
      eventReads += 1;
      if (eventReads === 1) {
        announceConversationEvent();
        // Keep the socket-triggered read in flight until the POST response
        // starts a second reconciliation for the same event id.
        await secondReadStarted;
      } else {
        announceSecondRead();
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: event }),
      });
    });

    await page.route(`**/api/chat/channels/${targetId}/messages`, async (route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }
      const request = (await route.request().postDataJSON()) as {
        body_text?: string;
        body_format?: "v1" | "v2" | "v3";
      };
      const expectedMention = `@[${SECOND_CANDIDATE_NAME}](mention:user:${SECOND_CANDIDATE_ID})`;
      if (!request.body_text?.includes(expectedMention)) {
        await route.fulfill({ status: 400, contentType: "application/json", body: "{}" });
        return;
      }
      const sent = makeMessage({
        id: `${targetId}-mention-message`,
        body_text: request.body_text,
        body_format: request.body_format ?? "v3",
      });
      messagesFor(scenario).push(sent);
      await page.evaluate(
        ({ channelId, messageId }) => {
          (
            window as unknown as {
              __e2eEmitWebSocketEvent: (event: Record<string, unknown>) => void;
            }
          ).__e2eEmitWebSocketEvent({
            type: "conversation.event",
            target_type: "channel",
            target_id: channelId,
            message_id: messageId,
          });
        },
        { channelId: targetId, messageId: event.id },
      );
      await conversationEventAnnounced;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { ...sent, created_conversation_event_id: event.id },
        }),
      });
    });

    await page.goto(`/chat/channel/${targetId}`);
    await page.waitForFunction(
      ({ kind, id }) =>
        (
          window as unknown as {
            __e2eHasSubscription?: (targetKind: string, targetId: string) => boolean;
          }
        ).__e2eHasSubscription?.(kind, id) === true,
      { kind: "channel", id: targetId },
    );
    await page.evaluate(() => {
      (window as unknown as { __sawRealtimeError: boolean }).__sawRealtimeError = false;
      new MutationObserver(() => {
        if (document.querySelector('[data-testid="chat-realtime-error"]')) {
          (window as unknown as { __sawRealtimeError: boolean }).__sawRealtimeError = true;
        }
      }).observe(document.body, { childList: true, subtree: true });
    });

    // A failed sidebar subscription shares the socket with the open timeline,
    // but is not a failure of the conversation the reader is looking at.
    await page.evaluate(
      ({ targetId: secondaryTargetId }) => {
        (
          window as unknown as {
            __e2eEmitWebSocketEvent: (event: Record<string, unknown>) => void;
          }
        ).__e2eEmitWebSocketEvent({
          type: "error",
          operation: "subscribe",
          code: "room_access_denied",
          target_type: "channel",
          target_id: secondaryTargetId,
        });
      },
      { targetId: OTHER_CHANNEL_ID },
    );

    const composer = page.getByTestId("chat-composer-input");
    await composer.click();
    await page.keyboard.type("@E2E");
    const candidate = page.getByRole("option", {
      name: new RegExp(`${SECOND_CANDIDATE_NAME}.*Será adicionada ao enviar`),
    });
    await expect(candidate).toBeVisible();
    await candidate.click();
    await page.getByRole("button", { name: "Enviar mensagem" }).click();

    const timeline = page.getByRole("log", { name: "Mensagens" });
    await expect(
      timeline.getByText(`Você adicionou ${SECOND_CANDIDATE_NAME} ao canal`),
    ).toBeVisible();
    expect(eventReads).toBeGreaterThanOrEqual(2);
    await expect(page.getByTestId("chat-realtime-error")).toHaveCount(0);
    expect(
      await page.evaluate(
        () => (window as unknown as { __sawRealtimeError: boolean }).__sawRealtimeError,
      ),
    ).toBe(false);
  });
});
