import { describe, expect, it } from "vitest";

import type { Message, MessageSecuritySnapshot } from "../chatTypes";
import type { MessageLink } from "../messageLinks";
import { reducer } from "./reducer";
import { initialState, type MessagesState } from "./types";
import { snapshotNeedsReread } from "./useSecurityReconciliation";

/**
 * Snapshot × realtime (issue #807 CQ round 2): an authoritative snapshot is
 * merged per occurrence under the same version order the realtime updates
 * use, so neither side can regress the other, and a redaction the snapshot
 * lifts is recognised as something only a re-read can restore.
 */

const url = "https://example.test/a";
const other = "https://other.test/b";

function link(overrides: Partial<MessageLink> = {}): MessageLink {
  return {
    ordinal: 0,
    targetKey: "key-a",
    text: url,
    url,
    hostname: "example.test",
    safety: "safe",
    click: "direct",
    href: url,
    updatedAt: "2026-08-18T12:00:00Z",
    ...overrides,
  };
}
const pending = (overrides: Partial<MessageLink> = {}) =>
  link({ safety: "pending", click: "none", href: "", ...overrides });
const blocked = (overrides: Partial<MessageLink> = {}) =>
  link({
    text: "",
    url: "",
    hostname: "",
    safety: "malicious",
    click: "none",
    href: "",
    ...overrides,
  });

function message(overrides: Partial<Message> = {}): Message {
  return {
    id: "m1",
    senderId: "u1",
    senderDisplayName: "Ana",
    senderEmail: "ana@example.test",
    kind: "user",
    bodyText: `veja ${url}`,
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    linkSafetyState: "safe",
    deletedAt: null,
    createdAt: "2026-08-18T11:00:00Z",
    updatedAt: "2026-08-18T11:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    links: [link()],
    ...overrides,
  };
}

function snapshot(
  overrides: Partial<Extract<MessageSecuritySnapshot, { available: true }>> = {},
): MessageSecuritySnapshot {
  return {
    messageId: "m1",
    available: true,
    status: "active",
    linkSafetyState: "safe",
    updatedAt: "2026-08-18T11:00:00Z",
    links: [link()],
    ...overrides,
  };
}

function stateWith(...messages: Message[]): MessagesState {
  return { ...initialState, messages };
}

function refreshed(state: MessagesState, ...snapshots: MessageSecuritySnapshot[]) {
  return reducer(state, { type: "security_snapshots_refreshed", snapshots });
}

describe("security snapshot × realtime ordering", () => {
  it("order 1: an old pending snapshot does not undo a newer realtime safe", () => {
    // Drawn: the realtime update landed at 12:05 while the snapshot (read at
    // the message's own 11:00 version) still said pending.
    const state = stateWith(message({ links: [link({ updatedAt: "2026-08-18T12:05:00Z" })] }));
    const next = refreshed(state, snapshot({ links: [pending()] }));
    expect(next.messages[0].links?.[0].safety).toBe("safe");
    expect(next.messages[0].links?.[0].href).toBe(url);
  });

  it("order 2: an old safe snapshot does not undo a newer realtime condemnation", () => {
    const state = stateWith(
      message({
        bodyText: "veja ￼",
        linkSafetyState: "malicious",
        links: [blocked({ updatedAt: "2026-08-18T12:05:00Z" })],
      }),
    );
    const next = refreshed(state, snapshot({ links: [link()] }));
    expect(next.messages[0].links?.[0].safety).toBe("malicious");
    expect(next.messages[0].bodyText).toBe("veja ￼");
  });

  it("order 3: an old malicious snapshot does not regress a newer realtime release", () => {
    const state = stateWith(message({ links: [link({ updatedAt: "2026-08-18T12:05:00Z" })] }));
    const next = refreshed(state, snapshot({ linkSafetyState: "malicious", links: [blocked()] }));
    expect(next.messages[0].links?.[0].safety).toBe("safe");
  });

  it("order 4: a genuinely newer snapshot wins over an older realtime state", () => {
    const state = stateWith(message({ links: [pending({ updatedAt: "2026-08-18T12:00:00Z" })] }));
    const next = refreshed(
      state,
      snapshot({ links: [link({ updatedAt: "2026-08-18T12:00:00.9Z" })] }),
    );
    expect(next.messages[0].links?.[0].safety).toBe("safe");
    // Precision is chronological, not lexical: "…00Z" is older than "…00.9Z".
    const back = refreshed(
      next,
      snapshot({ links: [pending({ updatedAt: "2026-08-18T12:00:00Z" })] }),
    );
    expect(back.messages[0].links?.[0].safety).toBe("safe");
  });

  it("order 5: a newer snapshot that no longer lists an occurrence removes it", () => {
    const state = stateWith(
      message({
        links: [
          link(),
          link({ ordinal: 1, targetKey: "key-b", text: other, url: other, href: other }),
        ],
      }),
    );
    const edited = snapshot({ updatedAt: "2026-08-18T11:30:00Z", links: [link()] });
    const next = refreshed(state, edited);
    expect(next.messages[0].links).toHaveLength(1);
    expect(next.messages[0].links?.[0].targetKey).toBe("key-a");
    // Whereas an *older* snapshot listing fewer occurrences removes nothing:
    // the drawn message is a later revision than the snapshot describes.
    const stale = snapshot({ updatedAt: "2026-08-18T10:00:00Z", links: [link()] });
    expect(refreshed(state, stale).messages[0].links).toHaveLength(2);
  });

  it("a newer snapshot with no links at all clears them; an older one keeps them", () => {
    const state = stateWith(message());
    expect(
      refreshed(state, snapshot({ updatedAt: "2026-08-18T11:30:00Z", links: undefined }))
        .messages[0].links,
    ).toBeUndefined();
    expect(
      refreshed(state, snapshot({ updatedAt: "2026-08-18T10:00:00Z", links: undefined }))
        .messages[0].links,
    ).toHaveLength(1);
  });

  it("an older snapshot still upgrades an occurrence whose target is newer", () => {
    // The message was edited after the snapshot was taken (drawn version is
    // newer), but the snapshot carries a newer verdict for a target the edit
    // kept: target state is body-independent, so it applies.
    const state = stateWith(message({ updatedAt: "2026-08-18T11:30:00Z", links: [pending()] }));
    const next = refreshed(
      state,
      snapshot({
        updatedAt: "2026-08-18T11:00:00Z",
        links: [link({ updatedAt: "2026-08-18T12:05:00Z" })],
      }),
    );
    expect(next.messages[0].links?.[0].safety).toBe("safe");
  });
});

describe("snapshotNeedsReread", () => {
  it("re-reads when a snapshot lifts a redaction this client holds", () => {
    const current = message({
      bodyText: "veja ￼",
      linkSafetyState: "malicious",
      links: [blocked()],
    });
    expect(
      snapshotNeedsReread(
        current,
        snapshot({ links: [link({ updatedAt: "2026-08-18T12:05:00Z" })] }),
      ),
    ).toBe(true);
    expect(
      snapshotNeedsReread(
        current,
        snapshot({
          links: [
            link({
              safety: "unknown",
              click: "interstitial",
              href: "",
              updatedAt: "2026-08-18T12:05:00Z",
            }),
          ],
        }),
      ),
    ).toBe(true);
  });

  it("re-reads when a snapshot condemns a link this client still draws", () => {
    expect(
      snapshotNeedsReread(
        message(),
        snapshot({
          linkSafetyState: "malicious",
          links: [blocked({ updatedAt: "2026-08-18T12:05:00Z" })],
        }),
      ),
    ).toBe(true);
  });

  it("re-reads when the quote's withheld excerpt is cleared", () => {
    const current = message({
      quoted: {
        id: "q1",
        authorId: "u2",
        bodyText: "",
        bodyFormat: "v2",
        deletedAt: null,
        createdAt: "2026-08-18T10:00:00Z",
        updatedAt: "2026-08-18T10:30:00Z",
        isRemoved: false,
        linkSafetyState: "malicious",
      },
    });
    const cleared = snapshot({
      quoted: {
        messageId: "q1",
        status: "active",
        linkSafetyState: "safe",
        updatedAt: "2026-08-18T12:05:00Z",
      },
    });
    expect(snapshotNeedsReread(current, cleared)).toBe(true);
    const stillBlocked = snapshot({
      quoted: {
        messageId: "q1",
        status: "active",
        linkSafetyState: "malicious",
        updatedAt: "2026-08-18T12:05:00Z",
      },
    });
    expect(snapshotNeedsReread(current, stillBlocked)).toBe(false);
  });

  it("costs no request when nothing withheld changes", () => {
    // safe -> safe
    expect(snapshotNeedsReread(message(), snapshot())).toBe(false);
    // pending -> safe: a patch is enough, nothing was withheld
    expect(
      snapshotNeedsReread(
        message({ links: [pending()] }),
        snapshot({ links: [link({ updatedAt: "2026-08-18T12:05:00Z" })] }),
      ),
    ).toBe(false);
    // malicious -> malicious: already redacted
    const blockedMessage = message({
      bodyText: "veja ￼",
      linkSafetyState: "malicious",
      links: [blocked()],
    });
    expect(
      snapshotNeedsReread(
        blockedMessage,
        snapshot({ linkSafetyState: "malicious", links: [blocked()] }),
      ),
    ).toBe(false);
    // a stale release (older than the condemnation) is not a release
    expect(
      snapshotNeedsReread(
        blockedMessage,
        snapshot({ links: [link({ updatedAt: "2026-08-18T11:00:00Z" })] }),
      ),
    ).toBe(false);
    // unavailable, or a message this client does not draw
    expect(snapshotNeedsReread(message(), { messageId: "m1", available: false })).toBe(false);
    expect(snapshotNeedsReread(undefined, snapshot())).toBe(false);
  });

  it("re-reads when a newer snapshot no longer lists a redacted occurrence (edited away offline)", () => {
    // Case 1. The body still carries the marker the condemnation left; the
    // occurrence is gone from the server, and what replaced it lives only on
    // the read. The reducer drops the occurrence; the read brings the body.
    const current = message({
      bodyText: "veja ￼",
      linkSafetyState: "malicious",
      links: [blocked()],
    });
    const edited = snapshot({
      linkSafetyState: "",
      updatedAt: "2026-08-18T12:05:00Z",
      links: undefined,
    });
    expect(snapshotNeedsReread(current, edited)).toBe(true);
    expect(refreshed(stateWith(current), edited).messages[0].links).toBeUndefined();
    // The same when other occurrences remain and only the redacted one went.
    const partial = snapshot({
      updatedAt: "2026-08-18T12:05:00Z",
      links: [link({ ordinal: 0, targetKey: "key-b", text: other, url: other, href: other })],
    });
    expect(snapshotNeedsReread(current, partial)).toBe(true);
  });

  it("does not read a removal from a stale snapshot", () => {
    // Case 2. The snapshot predates the drawn message: its silence about the
    // occurrence is age, not an edit — and the reducer keeps the occurrence too.
    const current = message({
      bodyText: "veja ￼",
      linkSafetyState: "malicious",
      updatedAt: "2026-08-18T12:00:00Z",
      links: [blocked()],
    });
    const stale = snapshot({
      linkSafetyState: "",
      updatedAt: "2026-08-18T10:00:00Z",
      links: undefined,
    });
    expect(snapshotNeedsReread(current, stale)).toBe(false);
    expect(refreshed(stateWith(current), stale).messages[0].links).toHaveLength(1);
  });

  it("does not re-read for the removal of an occurrence nothing was withheld for", () => {
    // Cases 5 and 6: safe or pending occurrences edited away carry no redacted
    // text; the snapshot applies on its own.
    for (const drawn of [link(), pending()]) {
      const current = message({ links: [drawn] });
      const edited = snapshot({
        linkSafetyState: "",
        updatedAt: "2026-08-18T12:05:00Z",
        links: undefined,
      });
      expect(snapshotNeedsReread(current, edited)).toBe(false);
      expect(refreshed(stateWith(current), edited).messages[0].links).toBeUndefined();
    }
  });

  it("re-reads when the quoted message was edited so its condemned occurrence is gone", () => {
    // Case 8: the quote's excerpt was withheld; the snapshot of the quoted
    // message no longer condemns anything (no target left), so the read must
    // bring the excerpt back.
    const current = message({
      quoted: {
        id: "q1",
        authorId: "u2",
        bodyText: "",
        bodyFormat: "v2",
        deletedAt: null,
        createdAt: "2026-08-18T10:00:00Z",
        updatedAt: "2026-08-18T10:30:00Z",
        isRemoved: false,
        linkSafetyState: "malicious",
      },
    });
    const edited = snapshot({
      quoted: {
        messageId: "q1",
        status: "active",
        linkSafetyState: "",
        updatedAt: "2026-08-18T12:05:00Z",
      },
    });
    expect(snapshotNeedsReread(current, edited)).toBe(true);
    const staleQuote = snapshot({
      quoted: {
        messageId: "q1",
        status: "active",
        linkSafetyState: "",
        updatedAt: "2026-08-18T09:00:00Z",
      },
    });
    expect(snapshotNeedsReread(current, staleQuote)).toBe(false);
  });
});

/**
 * The aggregate marker is a compatibility projection (issue #807 CQ round 4).
 * A message that carries links[] has a body the server already redacted span
 * by span; the aggregate `malicious` must never withhold that body wholesale.
 * A legacy message without links[] keeps the historical whole-body withholding.
 */
describe("aggregate marker × per-link body", () => {
  const redactedBody = "veja ￼ agora";
  const modernBlocked = () =>
    message({ bodyText: redactedBody, linkSafetyState: "malicious", links: [blocked()] });

  it("keeps the span-level body when a modern message receives the aggregate condemnation", () => {
    const state = stateWith(
      message({ bodyText: redactedBody, linkSafetyState: "", links: [blocked()] }),
    );
    const next = reducer(state, {
      type: "link_safety_changed",
      messageId: "m1",
      state: "malicious",
      updatedAt: "2026-08-18T12:05:00Z",
    });
    expect(next.messages[0].linkSafetyState).toBe("malicious");
    expect(next.messages[0].bodyText).toBe(redactedBody);
    // Applied once; a repeat changes nothing.
    const again = reducer(next, {
      type: "link_safety_changed",
      messageId: "m1",
      state: "malicious",
      updatedAt: "2026-08-18T12:05:00Z",
    });
    expect(again.messages[0].bodyText).toBe(redactedBody);
  });

  it("keeps the span-level body on a reconnect that says malicious -> malicious, with no re-read", () => {
    const current = modernBlocked();
    const same = snapshot({
      linkSafetyState: "malicious",
      updatedAt: "2026-08-18T12:30:00Z",
      links: [blocked({ updatedAt: "2026-08-18T12:30:00Z" })],
    });
    expect(snapshotNeedsReread(current, same)).toBe(false);
    const next = refreshed(stateWith(current), same);
    expect(next.messages[0].bodyText).toBe(redactedBody);
    expect(next.messages[0].links?.[0].safety).toBe("malicious");
  });

  it("keeps the span-level body when the aggregate arrives through a retained correction", () => {
    // The correction was retained before the message arrived; applying it to
    // the created payload must not undo the server's projection either.
    const corrected = reducer(stateWith(), {
      type: "link_safety_changed",
      messageId: "m1",
      state: "malicious",
      updatedAt: "2026-08-18T12:05:00Z",
    });
    const next = reducer(corrected, {
      type: "message_snapshot",
      message: message({ bodyText: redactedBody, linkSafetyState: "", links: [blocked()] }),
      insertIfMissing: true,
    });
    expect(next.messages[0].bodyText).toBe(redactedBody);
  });

  it("still withholds the whole body of a legacy message without links[]", () => {
    const legacy = message({ bodyText: `veja ${url}`, linkSafetyState: "", links: undefined });
    const next = reducer(stateWith(legacy), {
      type: "link_safety_changed",
      messageId: "m1",
      state: "malicious",
      updatedAt: "2026-08-18T12:05:00Z",
    });
    expect(next.messages[0].bodyText).toBe("");
    const viaSnapshot = refreshed(
      stateWith(legacy),
      snapshot({
        linkSafetyState: "malicious",
        updatedAt: "2026-08-18T12:05:00Z",
        links: undefined,
      }),
    );
    expect(viaSnapshot.messages[0].bodyText).toBe("");
  });

  it("leaves a safe modern message alone", () => {
    const state = stateWith(message());
    const next = reducer(state, {
      type: "link_safety_changed",
      messageId: "m1",
      state: "safe",
      updatedAt: "2026-08-18T12:05:00Z",
    });
    expect(next.messages[0].bodyText).toBe(`veja ${url}`);
    expect(next.messages[0].links?.[0].href).toBe(url);
  });

  it("keeps the legacy whole-excerpt withholding for a quote, which has no per-link projection", () => {
    // The server projects a quoted excerpt wholesale — quotes carry no
    // links[] — so the aggregate is still the quote's authority.
    const withQuote = message({
      bodyText: "concordo",
      linkSafetyState: "",
      links: undefined,
      quoted: {
        id: "q1",
        authorId: "u2",
        bodyText: `original com ${url}`,
        bodyFormat: "v2",
        deletedAt: null,
        createdAt: "2026-08-18T10:00:00Z",
        updatedAt: "2026-08-18T10:30:00Z",
        isRemoved: false,
        linkSafetyState: "",
      },
    });
    const next = reducer(stateWith(withQuote), {
      type: "link_safety_changed",
      messageId: "q1",
      state: "malicious",
      updatedAt: "2026-08-18T12:05:00Z",
    });
    expect(next.messages[0].quoted?.bodyText).toBe("");
    expect(next.messages[0].bodyText).toBe("concordo");
  });
});
