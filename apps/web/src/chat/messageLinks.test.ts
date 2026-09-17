import { describe, expect, it } from "vitest";

import {
  applyLinkUpdate,
  findLinkOccurrence,
  hasBlockedLink,
  isStaleLinkUpdate,
  linkForText,
  linkUpdateNeedsSnapshot,
  linkVersion,
  parseMessageLink,
  parseMessageLinks,
  previewCards,
  type MessageLink,
} from "./messageLinks";

const wire = (overrides: Record<string, unknown> = {}) => ({
  ordinal: 0,
  target_key: "a1b2c3d4e5f60718293a4b5c6d7e8f90",
  text: "https://Example.test/a",
  url: "https://example.test/a",
  hostname: "example.test",
  safety: "safe",
  click: "direct",
  href: "https://example.test/a",
  updated_at: "2026-08-18T12:00:00Z",
  ...overrides,
});

function entity(overrides: Partial<MessageLink> = {}): MessageLink {
  return {
    ordinal: 0,
    targetKey: "a1b2c3d4e5f60718293a4b5c6d7e8f90",
    text: "https://Example.test/a",
    url: "https://example.test/a",
    hostname: "example.test",
    safety: "safe",
    click: "direct",
    href: "https://example.test/a",
    updatedAt: "2026-08-18T12:00:00Z",
    ...overrides,
  };
}

describe("parseMessageLink", () => {
  it("decodes the contract field for field", () => {
    expect(
      parseMessageLink(
        wire({
          preview: {
            state: "ready",
            hostname: "example.test",
            title: "T",
            image_id: "img-1",
            image_width: 480,
            image_height: 240,
          },
        }),
      ),
    ).toEqual({
      ...entity(),
      preview: {
        state: "ready",
        hostname: "example.test",
        siteName: "",
        title: "T",
        description: "",
        imageId: "img-1",
        imageWidth: 480,
        imageHeight: 240,
      },
    });
  });

  it("refuses everything outside the closed sets, so nothing unknown becomes an anchor", () => {
    expect(parseMessageLink(wire({ safety: "trusted" }))).toBeUndefined();
    expect(parseMessageLink(wire({ click: "open" }))).toBeUndefined();
    expect(parseMessageLink(wire({ safety: "unknown", click: "interstitial" }))).toBeUndefined();
    expect(parseMessageLink(wire({ href: "javascript:alert(1)" }))).toBeUndefined();
    expect(parseMessageLink(wire({ href: "ftp://example.test/" }))).toBeUndefined();
    expect(parseMessageLink("not an object")).toBeUndefined();
    expect(parseMessageLink(null)).toBeUndefined();
  });

  it("drops a preview it does not understand and keeps the link", () => {
    expect(parseMessageLink(wire({ preview: { state: "rendered" } }))?.preview).toBeUndefined();
    expect(parseMessageLink(wire({ preview: "x" }))?.preview).toBeUndefined();
  });

  it("tolerates a blocked entity with no text or url, which still carries its identity", () => {
    const blocked = parseMessageLink({
      ordinal: 1,
      target_key: "a1b2c3d4e5f60718293a4b5c6d7e8f90",
      safety: "malicious",
      click: "none",
      updated_at: "t",
    });
    expect(blocked).toEqual({
      ...entity({
        ordinal: 1,
        text: "",
        url: "",
        hostname: "",
        safety: "malicious",
        click: "none",
        href: "",
        updatedAt: "t",
      }),
    });
  });

  it("refuses an entity without a target key: it could never be matched to an update", () => {
    expect(parseMessageLink(wire({ target_key: "" }))).toBeUndefined();
    expect(parseMessageLink(wire({ target_key: undefined }))).toBeUndefined();
  });
});

describe("linkVersion", () => {
  it("orders RFC 3339 timestamps of any precision as instants, not as strings", () => {
    const at = (fraction: string) => linkVersion(`2026-08-18T12:00:00${fraction}Z`)!;
    expect(at("")).toEqual([Date.parse("2026-08-18T12:00:00.000Z"), 0]);
    expect(at(".1")).toEqual([Date.parse("2026-08-18T12:00:00.100Z"), 0]);
    expect(at(".01")).toEqual([Date.parse("2026-08-18T12:00:00.010Z"), 0]);
    expect(at(".001")).toEqual([Date.parse("2026-08-18T12:00:00.001Z"), 0]);
    expect(at(".9")).toEqual([Date.parse("2026-08-18T12:00:00.900Z"), 0]);
    expect(at(".900000001")).toEqual([Date.parse("2026-08-18T12:00:00.900Z"), 1]);
    // Lexicographically "…00.9Z" > "…00Z" and "…00.10Z" < "…00.9Z"; as instants
    // the whole second is the earliest and .9 is the latest of these.
    expect(at("")[0]).toBeLessThan(at(".9")[0]);
    expect(at(".10")[0]).toBeLessThan(at(".9")[0]);
    expect(linkVersion("2026-08-18T12:00:00.5-03:00")).toEqual([
      Date.parse("2026-08-18T15:00:00.500Z"),
      0,
    ]);
  });

  it("is undefined for anything that is not a timestamp", () => {
    expect(linkVersion("")).toBeUndefined();
    expect(linkVersion("t")).toBeUndefined();
    expect(linkVersion("2026-08-18")).toBeUndefined();
    expect(linkVersion("2026-13-40T99:00:00Z")).toBeUndefined();
  });
});

describe("isStaleLinkUpdate", () => {
  const drawn = (updatedAt: string) => entity({ updatedAt });
  const update = (updatedAt: string) =>
    entity({ safety: "unknown", click: "interstitial", href: "", updatedAt });

  it("compares chronologically across precisions", () => {
    expect(isStaleLinkUpdate(update("2026-08-18T12:00:00.9Z"), drawn("2026-08-18T12:00:00Z"))).toBe(
      false,
    );
    expect(isStaleLinkUpdate(update("2026-08-18T12:00:00Z"), drawn("2026-08-18T12:00:00.9Z"))).toBe(
      true,
    );
    expect(
      isStaleLinkUpdate(update("2026-08-18T12:00:00.10Z"), drawn("2026-08-18T12:00:00.9Z")),
    ).toBe(true);
    expect(
      isStaleLinkUpdate(
        update("2026-08-18T12:00:00.000000002Z"),
        drawn("2026-08-18T12:00:00.000000001Z"),
      ),
    ).toBe(false);
    expect(
      isStaleLinkUpdate(
        update("2026-08-18T12:00:00.000000001Z"),
        drawn("2026-08-18T12:00:00.000000002Z"),
      ),
    ).toBe(true);
  });

  it("treats an equal version as not stale, so a repeat is idempotent", () => {
    expect(
      isStaleLinkUpdate(update("2026-08-18T12:00:00Z"), drawn("2026-08-18T12:00:00.000Z")),
    ).toBe(false);
  });

  it("is explicit about what cannot be ordered: an unreadable update is stale, an unreadable drawn version is not defended", () => {
    expect(isStaleLinkUpdate(update("t"), drawn("2026-08-18T12:00:00Z"))).toBe(true);
    expect(isStaleLinkUpdate(update(""), drawn("2026-08-18T12:00:00Z"))).toBe(true);
    expect(isStaleLinkUpdate(update("2026-08-18T12:00:00Z"), drawn("t"))).toBe(false);
    expect(isStaleLinkUpdate(update("2026-08-18T12:00:00Z"), drawn(""))).toBe(false);
  });
});

describe("parseMessageLinks", () => {
  it("returns undefined for absent, malformed or empty input", () => {
    expect(parseMessageLinks(undefined)).toBeUndefined();
    expect(parseMessageLinks("x")).toBeUndefined();
    expect(parseMessageLinks([])).toBeUndefined();
    expect(parseMessageLinks([wire({ safety: "future" })])).toBeUndefined();
  });

  it("keeps the well-formed entities and drops the rest", () => {
    expect(parseMessageLinks([wire(), wire({ click: "later" })])).toHaveLength(1);
  });
});

describe("linkForText", () => {
  it("matches the exact text as written and never a blocked entity", () => {
    const links = [
      entity(),
      entity({ ordinal: 1, text: "", url: "", safety: "malicious", click: "none", href: "" }),
    ];
    expect(linkForText(links, "https://Example.test/a")).toBe(links[0]);
    expect(linkForText(links, "https://example.test/a")).toBeUndefined();
    expect(linkForText(links, "")).toBeUndefined();
    expect(linkForText(undefined, "x")).toBeUndefined();
  });
});

describe("applyLinkUpdate", () => {
  const otherKey = "0000000000000000000000000000ffff";
  const twice = [
    entity(),
    entity({ ordinal: 1, text: "https://example.test/a#x" }),
    entity({
      ordinal: 2,
      targetKey: otherKey,
      url: "https://other.test/",
      text: "https://other.test/",
    }),
  ];

  it("patches every occurrence of the target and keeps ordinal and text", () => {
    const update = entity({
      safety: "unknown",
      click: "interstitial",
      href: "",
      updatedAt: "2026-08-18T12:05:00Z",
    });
    const next = applyLinkUpdate(twice, update)!;
    expect(next[0]).toEqual({ ...update, ordinal: 0, text: twice[0].text });
    expect(next[1]).toEqual({ ...update, ordinal: 1, text: twice[1].text });
    expect(next[2]).toBe(twice[2]);
  });

  it("matches by identity, never by the visible url", () => {
    // The same URL under another key is another target; a blocked occurrence
    // with no URL is still the target its key names.
    const sameURLOtherTarget = entity({ targetKey: otherKey, updatedAt: "2026-08-18T12:05:00Z" });
    expect(applyLinkUpdate(twice, sameURLOtherTarget)![0]).toBe(twice[0]);
    const blocked = [
      entity({ text: "", url: "", hostname: "", safety: "malicious", click: "none", href: "" }),
    ];
    const released = entity({ safety: "safe", updatedAt: "2026-08-18T12:05:00Z" });
    expect(applyLinkUpdate(blocked, released)![0]).toEqual({ ...released, ordinal: 0, text: "" });
  });

  it("ignores an update older than what is drawn, whatever precision either side carries", () => {
    const stale = entity({
      safety: "pending",
      click: "none",
      href: "",
      updatedAt: "2026-08-18T11:00:00Z",
    });
    expect(applyLinkUpdate(twice, stale)).toEqual(twice);
    const drawnFine = [entity({ updatedAt: "2026-08-18T12:00:00.9Z" })];
    const wholeSecond = entity({
      safety: "unknown",
      click: "interstitial",
      href: "",
      updatedAt: "2026-08-18T12:00:00Z",
    });
    expect(applyLinkUpdate(drawnFine, wholeSecond)).toEqual(drawnFine);
    const laterFine = entity({
      safety: "unknown",
      click: "interstitial",
      href: "",
      updatedAt: "2026-08-18T12:00:00.95Z",
    });
    expect(applyLinkUpdate(drawnFine, laterFine)![0].safety).toBe("unknown");
  });

  it("changes nothing for a target the message no longer names, or an update without identity", () => {
    expect(applyLinkUpdate(twice, entity({ targetKey: "gone" }))).toEqual(twice);
    expect(applyLinkUpdate(twice, entity({ targetKey: "" }))).toEqual(twice);
    expect(applyLinkUpdate(undefined, entity())).toBeUndefined();
  });
});

describe("linkUpdateNeedsSnapshot", () => {
  const blocked = entity({
    text: "",
    url: "",
    hostname: "",
    safety: "malicious",
    click: "none",
    href: "",
  });

  it("re-reads for a condemnation and for the release of a blocked occurrence", () => {
    expect(
      linkUpdateNeedsSnapshot([entity()], entity({ safety: "malicious", click: "none", href: "" })),
    ).toBe(true);
    expect(linkUpdateNeedsSnapshot([blocked], entity({ updatedAt: "2026-08-18T12:05:00Z" }))).toBe(
      true,
    );
    expect(
      linkUpdateNeedsSnapshot(undefined, entity({ safety: "malicious", click: "none", href: "" })),
    ).toBe(true);
  });

  it("patches in place otherwise, and never for a stale release of a blocked occurrence", () => {
    expect(
      linkUpdateNeedsSnapshot(
        [entity()],
        entity({ safety: "unknown", click: "interstitial", href: "" }),
      ),
    ).toBe(false);
    expect(linkUpdateNeedsSnapshot(undefined, entity())).toBe(false);
    expect(linkUpdateNeedsSnapshot([blocked], entity({ updatedAt: "2026-08-18T11:00:00Z" }))).toBe(
      false,
    );
  });
});

describe("findLinkOccurrence", () => {
  it("resolves by identity and position, and is undefined once the occurrence is gone", () => {
    const links = [entity(), entity({ ordinal: 1 })];
    expect(findLinkOccurrence(links, { targetKey: links[1].targetKey, ordinal: 1 })).toBe(links[1]);
    expect(
      findLinkOccurrence(links, { targetKey: links[1].targetKey, ordinal: 2 }),
    ).toBeUndefined();
    expect(findLinkOccurrence(links, { targetKey: "other", ordinal: 0 })).toBeUndefined();
    expect(
      findLinkOccurrence(undefined, { targetKey: links[0].targetKey, ordinal: 0 }),
    ).toBeUndefined();
  });
});

describe("previewCards", () => {
  const ready = {
    state: "ready" as const,
    hostname: "h",
    siteName: "",
    title: "",
    description: "",
    imageId: "",
    imageWidth: 0,
    imageHeight: 0,
  };

  it("draws at most two, one per target, only for safe links with a live preview", () => {
    const links = [
      entity({ ordinal: 0, targetKey: "a", url: "https://a.test/", preview: ready }),
      entity({ ordinal: 1, targetKey: "a", url: "https://a.test/", preview: ready }),
      entity({
        ordinal: 2,
        targetKey: "b",
        url: "https://b.test/",
        preview: { ...ready, state: "failed" },
      }),
      entity({
        ordinal: 3,
        targetKey: "c",
        url: "https://c.test/",
        preview: { ...ready, state: "queued" },
      }),
      entity({ ordinal: 4, targetKey: "d", url: "https://d.test/", preview: ready }),
      entity({
        ordinal: 5,
        targetKey: "e",
        url: "https://e.test/",
        safety: "unknown",
        click: "interstitial",
        href: "",
        preview: ready,
      }),
    ];
    expect(previewCards(links).map((link) => link.url)).toEqual([
      "https://a.test/",
      "https://c.test/",
    ]);
    expect(previewCards(undefined)).toEqual([]);
  });

  it("keeps a third target's preview as data and draws it once a slot frees up", () => {
    // The server hydrates every safe preview; the two-card limit is only here.
    const third = entity({ ordinal: 2, targetKey: "c", url: "https://c.test/", preview: ready });
    const links = [
      entity({ ordinal: 0, targetKey: "a", url: "https://a.test/", preview: ready }),
      entity({ ordinal: 1, targetKey: "b", url: "https://b.test/", preview: ready }),
      third,
    ];
    expect(previewCards(links).map((link) => link.targetKey)).toEqual(["a", "b"]);

    // A realtime update on the third target updates the state but still draws
    // no third card while the first two hold their slots.
    const updated = applyLinkUpdate(
      links,
      entity({
        targetKey: "c",
        url: "https://c.test/",
        updatedAt: "2026-08-18T12:05:00Z",
        preview: { ...ready, title: "C" },
      }),
    )!;
    expect(updated[2].preview?.title).toBe("C");
    expect(previewCards(updated).map((link) => link.targetKey)).toEqual(["a", "b"]);

    // The first card stops being eligible: the third target takes the slot
    // without any new data from the server.
    const revoked = applyLinkUpdate(
      updated,
      entity({
        targetKey: "a",
        url: "https://a.test/",
        safety: "unknown",
        click: "interstitial",
        href: "",
        updatedAt: "2026-08-18T12:06:00Z",
      }),
    )!;
    expect(previewCards(revoked).map((link) => link.targetKey)).toEqual(["b", "c"]);
    expect(previewCards(revoked)[1].preview?.title).toBe("C");
  });
});

describe("hasBlockedLink", () => {
  it("reports a condemned entity", () => {
    expect(hasBlockedLink([entity()])).toBe(false);
    expect(hasBlockedLink([entity(), entity({ safety: "malicious" })])).toBe(true);
    expect(hasBlockedLink(undefined)).toBe(false);
  });
});
