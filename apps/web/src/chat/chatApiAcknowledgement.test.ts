/**
 * The acknowledgement wire contract (issue #824).
 *
 * What the client sends and what it makes of what comes back. The decoders are
 * defensive on purpose: a state this build does not recognise must never be
 * read as `pending`, because `pending` is the one state that offers an action.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

const { mockAuthFetch } = vi.hoisted(() => ({ mockAuthFetch: vi.fn() }));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import {
  acknowledgeMessage,
  fetchChannelMessages,
  fetchMessageAcknowledgement,
  fetchMessageAcknowledgements,
  postChannelMessage,
} from "./chatApi";

const channelId = "11111111-1111-4111-8111-111111111111";
const messageId = "22222222-2222-4222-8222-222222222222";

function messagePayload(overrides: Record<string, unknown> = {}) {
  return {
    id: messageId,
    sender_id: "user-1",
    kind: "user",
    body_text: "olá",
    body_format: "v3",
    status: "active",
    created_at: "2026-09-11T12:00:00Z",
    updated_at: "2026-09-11T12:00:00Z",
    ...overrides,
  };
}

function summaryPayload(overrides: Record<string, unknown> = {}) {
  return {
    message_id: messageId,
    required: true,
    total: 5,
    pending: 2,
    acknowledged: 3,
    responded: 0,
    expired: 0,
    cancelled: 0,
    viewer_state: "pending",
    ...overrides,
  };
}

function lastRequest() {
  return mockAuthFetch.mock.calls.at(-1) as [string, { method: string; body?: string }];
}

beforeEach(() => mockAuthFetch.mockReset());

describe("acknowledgement on the message contract", () => {
  it("asks for confirmation only when the composer did", async () => {
    mockAuthFetch.mockResolvedValue({ data: messagePayload() });
    await postChannelMessage(channelId, "olá", { acknowledgementRequired: true });
    expect(JSON.parse(lastRequest()[1].body!)).toMatchObject({ acknowledgement_required: true });
  });

  // Backward compatibility: a send that asks for nothing is byte-for-byte the
  // payload it always was, so a pre-#824 server sees no new field.
  it("omits the field entirely from an ordinary send", async () => {
    mockAuthFetch.mockResolvedValue({ data: messagePayload() });
    await postChannelMessage(channelId, "olá");
    expect(JSON.parse(lastRequest()[1].body!)).not.toHaveProperty("acknowledgement_required");

    await postChannelMessage(channelId, "olá", { acknowledgementRequired: false });
    expect(JSON.parse(lastRequest()[1].body!)).not.toHaveProperty("acknowledgement_required");
  });

  it("reads the flag off a listed message", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { messages: [messagePayload({ acknowledgement_required: true })] },
    });
    const page = await fetchChannelMessages(channelId);
    expect(page.messages[0].acknowledgementRequired).toBe(true);
  });

  // A server that does not send the field asked nobody: absence can never
  // invent a confirmation request.
  it("treats an absent or unrecognised flag as asking nobody", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { messages: [messagePayload(), messagePayload({ acknowledgement_required: "yes" })] },
    });
    const page = await fetchChannelMessages(channelId);
    expect(page.messages[0].acknowledgementRequired).toBe(false);
    expect(page.messages[1].acknowledgementRequired).toBe(false);
  });
});

describe("the acknowledgement endpoints", () => {
  it("reads a summary without sending a body", async () => {
    mockAuthFetch.mockResolvedValue({ data: summaryPayload() });
    const summary = await fetchMessageAcknowledgement(messageId);

    const [url, init] = lastRequest();
    expect(url).toContain(`/messages/${messageId}/acknowledgement`);
    expect(init.method).toBe("GET");
    expect(init.body).toBeUndefined();
    expect(summary).toMatchObject({ required: true, total: 5, acknowledged: 3, pending: 2 });
    expect(summary.viewerState).toBe("pending");
  });

  // The action names no recipient, because there is nothing to name: the server
  // takes it from the session.
  it("confirms with no payload at all", async () => {
    mockAuthFetch.mockResolvedValue({
      data: summaryPayload({ viewer_state: "acknowledged", acknowledged: 4, pending: 1 }),
    });
    const summary = await acknowledgeMessage(messageId);

    const [url, init] = lastRequest();
    expect(url).toContain(`/messages/${messageId}/acknowledgement`);
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(summary.viewerState).toBe("acknowledged");
  });

  // The sender's detail is rendered when the server sends it, and simply absent
  // when it does not. The client never decides which.
  it("keeps the per-recipient detail the server chose to send", async () => {
    mockAuthFetch.mockResolvedValue({
      data: summaryPayload({
        viewer_state: "",
        recipients: [
          { recipient_id: "u-1", state: "acknowledged", resolved_at: "2026-09-11T13:00:00Z" },
          { recipient_id: "u-2", state: "pending", resolved_at: null },
        ],
      }),
    });
    const summary = await fetchMessageAcknowledgement(messageId);
    expect(summary.viewerState).toBeUndefined();
    expect(summary.recipients).toEqual([
      { recipientId: "u-1", state: "acknowledged", resolvedAt: "2026-09-11T13:00:00Z" },
      { recipientId: "u-2", state: "pending", resolvedAt: undefined },
    ]);
  });

  it("reports no detail when the server withheld it", async () => {
    mockAuthFetch.mockResolvedValue({ data: summaryPayload() });
    expect((await fetchMessageAcknowledgement(messageId)).recipients).toBeUndefined();
  });

  // A state this build does not know is not pending. Falling back to pending
  // would offer an action against a request that may already be resolved.
  it("refuses to read an unknown state as pending", async () => {
    mockAuthFetch.mockResolvedValue({
      data: summaryPayload({
        viewer_state: "snoozed",
        recipients: [{ recipient_id: "u-1", state: "snoozed" }],
      }),
    });
    const summary = await fetchMessageAcknowledgement(messageId);
    expect(summary.viewerState).toBeUndefined();
    expect(summary.recipients).toEqual([]);
  });

  it("reads a nonsense count as zero rather than NaN", async () => {
    mockAuthFetch.mockResolvedValue({
      data: summaryPayload({ total: "many", pending: -3, acknowledged: null }),
    });
    const summary = await fetchMessageAcknowledgement(messageId);
    expect(summary).toMatchObject({ total: 0, pending: 0, acknowledged: 0 });
  });
});

// ── the page batch (issue #824) ──────────────────────────────────────────────

describe("the acknowledgement page batch", () => {
  const other = "33333333-3333-4333-8333-333333333333";

  it("asks about a whole page in one request, keyed by message", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        acknowledgements: {
          [messageId]: summaryPayload({ viewer_state: "pending" }),
          [other]: summaryPayload({ total: 2, pending: 2, acknowledged: 0, viewer_state: "" }),
        },
      },
    });

    const summaries = await fetchMessageAcknowledgements([messageId, other]);
    const [url, init] = lastRequest();
    expect(url).toContain("/messages/acknowledgements");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body!)).toEqual({ message_ids: [messageId, other] });
    expect(mockAuthFetch).toHaveBeenCalledOnce();

    expect(summaries[messageId].viewerState).toBe("pending");
    expect(summaries[messageId].messageId).toBe(messageId);
    expect(summaries[other].viewerState).toBeUndefined();
    expect(summaries[other].total).toBe(2);
  });

  // An empty page costs nothing at all — no request leaves the client.
  it("issues no request for an empty page", async () => {
    expect(await fetchMessageAcknowledgements([])).toEqual({});
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  // A message the server withheld has no key, and the client invents none.
  it("keeps only the entries the server returned", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { acknowledgements: { [messageId]: summaryPayload() } },
    });
    const summaries = await fetchMessageAcknowledgements([messageId, other]);
    expect(Object.keys(summaries)).toEqual([messageId]);
  });

  it("tolerates a server that answered with nothing", async () => {
    mockAuthFetch.mockResolvedValue({ data: {} });
    expect(await fetchMessageAcknowledgements([messageId])).toEqual({});
  });

  // The page view carries summaries only; the recipient list stays on the
  // message-scoped read where the server decides who may have it.
  it("applies the same defensive decoding as the single read", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        acknowledgements: {
          [messageId]: summaryPayload({ viewer_state: "snoozed", total: "many" }),
        },
      },
    });
    const summaries = await fetchMessageAcknowledgements([messageId]);
    expect(summaries[messageId].viewerState).toBeUndefined();
    expect(summaries[messageId].total).toBe(0);
  });
});
