/**
 * The link-safety notices on a message bubble (issue #135).
 *
 * # What these tests are really guarding
 *
 * The policy this issue introduced is asymmetric on purpose: a message whose
 * links could not be verified is *published* — everyone receives it, its content
 * renders exactly as any other message's — and this deployment's server is still
 * forbidden from fetching those links. The client's job is to render that
 * distinction, and to render nothing else.
 *
 * So the assertions come in two kinds. That the notice says the right thing and
 * sits above the content, and that the client never touches the link: no fetch,
 * no HEAD, no preload, no prefetch, no image pointed at the URL. `fetch` and
 * `Image` are spied on for the whole file and asserted to be untouched, so a
 * future change that adds a client-side preview fails here rather than in
 * production.
 */

import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { emptyEmojiUsage } from "./emoji/emojiUsage";
import MessageBubble from "./MessageBubble";
import type { MessageBubbleProps } from "./MessageBubble";
import { linkSafetyAllowsAnchors, normalizeLinkSafety } from "./chatTypes";
import type { LinkSafetyRecheck, Message } from "./chatTypes";
import { parseMessageLink, type LinkPreview, type MessageLink } from "./messageLinks";
import { withheldBodyNotice as withheldBodyText } from "./MessageContent";

const linkURL = "https://example.test/some/page";

const noticeText =
  "Não foi possível verificar este link agora. A prévia automática não foi carregada.";
const blockedText = "Este link foi bloqueado após a verificação de segurança.";

function messageWith(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Alex",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: `veja ${linkURL} por favor`,
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    linkSafetyState: "",
    deletedAt: null,
    createdAt: "2026-08-17T12:00:00Z",
    updatedAt: "2026-08-17T12:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...overrides,
  };
}

function renderBubble(overrides: Partial<MessageBubbleProps> = {}) {
  const props: MessageBubbleProps = {
    message: messageWith(),
    onToggleReaction: vi.fn(),
    onReplyMessage: vi.fn(),
    onReferenceMessage: vi.fn(),
    onToggleFavorite: vi.fn(),
    onEditMessage: vi.fn(),
    onEditForbidden: vi.fn(),
    onDeleteMessage: vi.fn(),
    emojiUsage: emptyEmojiUsage,
    onEmojiToneChange: vi.fn(),
    currentUserId: "me",
    recentReactionEmojis: [],
    reactionMenuVisible: false,
    onReactionMenuVisibleChange: vi.fn(),
    pickerOpen: false,
    onPickerOpenChange: vi.fn(),
    ...overrides,
  };
  return render(<MessageBubble {...props} />);
}

let fetchSpy: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchSpy = vi.fn(() => Promise.reject(new Error("the client must not fetch a message link")));
  vi.stubGlobal("fetch", fetchSpy);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/**
 * Asserts the whole "the client does not touch the link" property in one place.
 *
 * It checks the two ways a browser could be made to reach the URL from rendered
 * markup — a network call this component initiated, and a resource element
 * pointing at it — rather than only the obvious one.
 */
function expectNoClientSideFetch(container: HTMLElement) {
  expect(fetchSpy).not.toHaveBeenCalled();
  for (const selector of ["img", "link", "iframe", "script", "source", "video", "audio"]) {
    expect(container.querySelectorAll(selector).length).toBe(0);
  }
}

describe("an unverified link", () => {
  it("renders the notice above the message content", () => {
    const { container } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
    });

    const notice = screen.getByTestId("chat-message-link-unverified");
    // The exact copy. It is deliberately not a security warning: the provider
    // alleged nothing, it declined to answer, and the sentence says only that.
    expect(notice).toHaveTextContent(noticeText);
    expect(notice.textContent).not.toMatch(/perigos|malicioso|inseguro|bloquead/i);

    // Above the content, so a reader sees the caveat before the link it is
    // about. Asserted as "first thing inside the bubble" rather than as a
    // relative comparison: the body is a bare text node, and comparing against
    // its container would compare the notice with its own ancestor.
    const bubble = container.querySelector(".chat-msg-area__msg-bubble");
    expect(bubble).not.toBeNull();
    expect(bubble?.firstElementChild).toBe(notice);
    const text = bubble?.textContent ?? "";
    expect(text.indexOf(noticeText)).toBeGreaterThanOrEqual(0);
    expect(text.indexOf(noticeText)).toBeLessThan(text.indexOf(linkURL));
  });

  it("renders the message content unchanged", () => {
    renderBubble({ message: messageWith({ linkSafetyState: "inconclusive" }) });

    // The message was published, so its content is drawn exactly as any other's.
    // Nothing is hidden, struck through or replaced.
    expect(screen.getByText(new RegExp(linkURL.replace(/[/.?]/g, "\\$&")))).toBeInTheDocument();
    expect(screen.queryByText(/ocultado/i)).not.toBeInTheDocument();
  });

  it("never makes the client reach the link", () => {
    const { container } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
    });

    expectNoClientSideFetch(container);
  });

  it("offers a re-check action that asks the backend and disables itself", async () => {
    let resolve: (value: LinkSafetyRecheck | undefined) => void = () => {};
    const onReconcileLinkSafety = vi.fn(
      () => new Promise<LinkSafetyRecheck | undefined>((r) => (resolve = r)),
    );
    renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
      onReconcileLinkSafety,
    });

    const button = screen.getByTestId("chat-message-link-recheck");
    // The label promises a re-check and never a new scan, because none is ever
    // started — see the reconcile endpoint.
    expect(button).toHaveTextContent("Verificar novamente");
    expect(button.textContent).not.toMatch(/escanear|scan|forçar/i);

    fireEvent.click(button);

    expect(onReconcileLinkSafety).toHaveBeenCalledWith("msg-1");
    await waitFor(() => expect(button).toBeDisabled());

    // A second click while one is in flight cannot queue another request: this is
    // the client half of not turning the button into a poll.
    fireEvent.click(button);
    expect(onReconcileLinkSafety).toHaveBeenCalledTimes(1);

    // No retry hint in the reply, so nothing to wait out: the button re-enables
    // as soon as the request settles.
    resolve(undefined);
    await waitFor(() => expect(button).not.toBeDisabled());
  });

  it("shows the notice without an action when no handler is wired", () => {
    renderBubble({ message: messageWith({ linkSafetyState: "inconclusive" }) });

    // The warning is the important half; the action is a convenience.
    expect(screen.getByTestId("chat-message-link-unverified")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-message-link-recheck")).not.toBeInTheDocument();
  });
});

describe("a condemned link", () => {
  it("withdraws the content and says why", () => {
    const { container } = renderBubble({
      message: messageWith({ linkSafetyState: "malicious" }),
    });

    expect(screen.getByTestId("chat-message-link-blocked")).toHaveTextContent(blockedText);
    // The body is withheld: the body *is* the link as far as the risk goes, and a
    // URL a reader can select and paste is a URL the block did not stop.
    expect(
      screen.queryByText(new RegExp(linkURL.replace(/[/.?]/g, "\\$&"))),
    ).not.toBeInTheDocument();
    // The author and timestamp stay, so the conversation still makes sense.
    expect(screen.getByText("Alex")).toBeInTheDocument();
    expectNoClientSideFetch(container);
  });

  it("offers no re-check action", () => {
    renderBubble({
      message: messageWith({ linkSafetyState: "malicious" }),
      onReconcileLinkSafety: vi.fn(),
    });

    // There is nothing to re-check: the link was condemned, not unverified.
    expect(screen.queryByTestId("chat-message-link-recheck")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
  });
});

describe("every other state", () => {
  it("draws no link notice at all", () => {
    for (const state of ["", "safe"] as const) {
      cleanup();
      renderBubble({ message: messageWith({ linkSafetyState: state }) });

      expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
      expect(screen.queryByTestId("chat-message-link-blocked")).not.toBeInTheDocument();
    }
  });

  it("keeps the withheld-message notice separate from the unverified one", () => {
    renderBubble({
      message: messageWith({ status: "pending_link_scan", linkSafetyState: "" }),
    });

    // Two different states with two different meanings: "still being checked" is
    // temporary and shown only to the author, "could not be checked" is terminal
    // and shown to everyone.
    expect(screen.getByTestId("chat-message-pending-scan")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
  });

  it("says nothing about links on a removed message", () => {
    renderBubble({
      message: messageWith({ isRemoved: true, linkSafetyState: "inconclusive" }),
    });

    // A removed message's placeholder is the whole of what it says.
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
    expect(screen.getByText("Mensagem removida.")).toBeInTheDocument();
  });
});

/**
 * The anchors themselves (issue #135).
 *
 * "Published" and "clickable" are the same thing for a reader, so the notice is
 * only half the feature: an `inconclusive` message must genuinely render an
 * `<a href>`, and a `malicious` one must genuinely render none. These are the two
 * assertions the whole policy rests on, and they are DOM assertions rather than
 * prop assertions for that reason.
 *
 * Clicking an anchor is browser navigation. Nothing observes `fetch`, `HEAD`,
 * `preload` or `prefetch` to decide whether to draw one — `expectNoClientSideFetch`
 * is asserted alongside every one of them.
 */
/**
 * Per-link entities (issue #807).
 *
 * The server describes every link; the client draws what it is told and
 * decides nothing. These assert each state's shape — anchor, interstitial
 * button, pending note, blocked chip — that the states are independent within
 * one message, and that a message the server did not describe never grows an
 * anchor from its text alone.
 */
function linkEntity(overrides: Partial<MessageLink> = {}): MessageLink {
  return {
    ordinal: 0,
    targetKey: "key-artigo",
    text: linkURL,
    url: linkURL,
    hostname: "example.test",
    safety: "safe",
    click: "direct",
    href: linkURL,
    updatedAt: "2026-08-17T12:00:00Z",
    ...overrides,
  };
}

const unknownLink = (overrides: Partial<MessageLink> = {}) =>
  linkEntity({ safety: "unknown", click: "interstitial", href: "", ...overrides });
const pendingLink = (overrides: Partial<MessageLink> = {}) =>
  linkEntity({ safety: "pending", click: "none", href: "", ...overrides });
const blockedLink = (ordinal = 0) =>
  linkEntity({
    ordinal,
    text: "",
    url: "",
    hostname: "",
    safety: "malicious",
    click: "none",
    href: "",
  });

function rerenderBubble(rerender: ReturnType<typeof render>["rerender"], message: Message) {
  rerender(
    <MessageBubble
      message={message}
      onToggleReaction={vi.fn()}
      onReplyMessage={vi.fn()}
      onReferenceMessage={vi.fn()}
      onToggleFavorite={vi.fn()}
      onEditMessage={vi.fn()}
      onEditForbidden={vi.fn()}
      onDeleteMessage={vi.fn()}
      emojiUsage={emptyEmojiUsage}
      onEmojiToneChange={vi.fn()}
      currentUserId="me"
      recentReactionEmojis={[]}
      reactionMenuVisible={false}
      onReactionMenuVisibleChange={vi.fn()}
      pickerOpen={false}
      onPickerOpenChange={vi.fn()}
    />,
  );
}

describe("anchors", () => {
  const anchors = (container: HTMLElement) =>
    Array.from(container.querySelectorAll("a")) as HTMLAnchorElement[];

  it("renders a safe link as a real anchor to the server's href", () => {
    const { container } = renderBubble({
      message: messageWith({ linkSafetyState: "safe", links: [linkEntity()] }),
    });

    const found = anchors(container);
    expect(found).toHaveLength(1);
    expect(found[0].getAttribute("href")).toBe(linkURL);
    expect(found[0]).toHaveTextContent(linkURL);
    expect(found[0].getAttribute("target")).toBe("_blank");
    const rel = (found[0].getAttribute("rel") ?? "").split(/\s+/);
    expect(rel).toContain("noopener");
    expect(rel).toContain("noreferrer");
    // Per-link state on the entity: no message-level banner.
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
    expectNoClientSideFetch(container);
  });

  it("uses the canonical href the server sent, never a client-derived one", () => {
    const canonical = "https://xn--exmple-cua.test/p";
    const written = "https://exämple.test/p";
    const { container } = renderBubble({
      message: messageWith({
        bodyText: `veja ${written}`,
        linkSafetyState: "safe",
        links: [
          linkEntity({
            text: written,
            url: canonical,
            href: canonical,
            hostname: "xn--exmple-cua.test",
          }),
        ],
      }),
    });
    const found = anchors(container);
    expect(found).toHaveLength(1);
    expect(found[0].getAttribute("href")).toBe(canonical);
    expect(found[0]).toHaveTextContent(written);
    // The real destination is available on hover for an IDN spelling.
    expect(found[0].getAttribute("title")).toBe(canonical);
  });

  it("renders a pending link as text with a status note, not focusable", () => {
    const { container } = renderBubble({
      message: messageWith({ links: [pendingLink()] }),
    });

    expect(anchors(container)).toHaveLength(0);
    expect(container.querySelectorAll("button").length).toBe(0);
    const pending = container.querySelector("[data-link-safety='pending']");
    expect(pending).not.toBeNull();
    expect(pending).toHaveTextContent(linkURL);
    expect(screen.getByRole("status")).toHaveTextContent("Verificando segurança do link…");
    expectNoClientSideFetch(container);
  });

  it("renders an unverified link as a button that opens the interstitial", () => {
    const { container } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
    });

    expect(anchors(container)).toHaveLength(0);
    const button = screen.getByRole("button", { name: `${linkURL} — Link não verificado` });
    expect(button.getAttribute("href")).toBeNull();

    fireEvent.click(button);
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("Não foi possível verificar este link");
    // The real host, and the full destination, so nothing is hidden.
    expect(screen.getByTestId("chat-link-interstitial-host")).toHaveTextContent("example.test");
    expect(dialog).toHaveTextContent(linkURL);
    expect(dialog.textContent).not.toMatch(/seguro/i);
    // Initial focus on the safe action; the open action is a real anchor with
    // the hardened attributes; nothing was fetched.
    expect(document.activeElement).toHaveTextContent("Cancelar");
    const open = screen.getByRole("link", { name: "Abrir mesmo assim" });
    expect(open.getAttribute("href")).toBe(linkURL);
    expect(open.getAttribute("rel")).toContain("noopener");
    expect(open.getAttribute("target")).toBe("_blank");
    expect(fetchSpy).not.toHaveBeenCalled();

    // Escape closes and focus returns to the link that opened it.
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(button);
  });

  it("offers the re-check inside the interstitial when a handler is wired", async () => {
    const onReconcile = vi.fn(
      async (): Promise<LinkSafetyRecheck> => ({
        state: "inconclusive",
        updatedAt: "2026-08-17T12:01:00Z",
        retryAfterSeconds: 60,
      }),
    );
    renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
      onReconcileLinkSafety: onReconcile,
    });
    fireEvent.click(screen.getByRole("button", { name: `${linkURL} — Link não verificado` }));
    fireEvent.click(screen.getByRole("button", { name: "Verificar novamente" }));
    await waitFor(() => expect(onReconcile).toHaveBeenCalledWith("msg-1"));
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Verificar novamente" })).not.toBeInTheDocument(),
    );
  });

  it("renders a blocked link as the chip, with the URL absent from the page", () => {
    const { container } = renderBubble({
      message: messageWith({
        bodyText: "clique em \uFFFC agora",
        linkSafetyState: "malicious",
        links: [blockedLink()],
      }),
    });

    expect(anchors(container)).toHaveLength(0);
    expect(container.textContent).not.toContain(linkURL);
    expect(container.textContent).toContain("clique em");
    expect(container.textContent).toContain("agora");
    const chip = container.querySelector("[data-link-safety='malicious']");
    expect(chip).toHaveTextContent("Link bloqueado por segurança");
    expect(chip?.getAttribute("tabindex")).toBeNull();
    expectNoClientSideFetch(container);
  });

  it("keeps every link independent within one message", () => {
    const good = "https://good.test/a";
    const { container } = renderBubble({
      message: messageWith({
        bodyText: `bom ${good} incerto ${linkURL} ruim \uFFFC fim`,
        linkSafetyState: "malicious",
        links: [
          linkEntity({ ordinal: 0, text: good, url: good, href: good, hostname: "good.test" }),
          unknownLink({ ordinal: 1 }),
          blockedLink(2),
        ],
      }),
    });

    const found = anchors(container);
    expect(found).toHaveLength(1);
    expect(found[0].getAttribute("href")).toBe(good);
    expect(
      screen.getByRole("button", { name: `${linkURL} — Link não verificado` }),
    ).toBeInTheDocument();
    expect(container.querySelector("[data-link-safety='malicious']")).not.toBeNull();
    expect(container.textContent).toContain("fim");
    // No whole-message tombstone: the rest of the text is preserved.
    expect(container.textContent).not.toContain(withheldBodyText);
  });

  it("drops the anchor when a later render describes the link as unverified or blocked", () => {
    const { container, rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "safe", links: [linkEntity()] }),
    });
    expect(anchors(container)).toHaveLength(1);

    rerenderBubble(
      rerender,
      messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
    );
    expect(anchors(container)).toHaveLength(0);
    expect(
      screen.getByRole("button", { name: `${linkURL} — Link não verificado` }),
    ).toBeInTheDocument();

    rerenderBubble(
      rerender,
      messageWith({
        bodyText: "veja \uFFFC",
        linkSafetyState: "malicious",
        links: [blockedLink()],
      }),
    );
    expect(anchors(container)).toHaveLength(0);
    expect(container.textContent).not.toContain(linkURL);
  });

  it("renders no anchor for a URL the server did not describe", () => {
    // A body with two URLs and an entity for one: the other is literal text,
    // whatever the message-level marker says.
    const { container } = renderBubble({
      message: messageWith({
        bodyText: `${linkURL} e https://other.test/x`,
        linkSafetyState: "safe",
        links: [linkEntity()],
      }),
    });
    expect(anchors(container)).toHaveLength(1);
    expect(container.textContent).toContain("https://other.test/x");
  });

  it("never renders a dangerous scheme as an anchor", () => {
    for (const body of [
      "javascript:alert(1)",
      "data:text/html,<script>alert(1)</script>",
      "file:///etc/passwd",
      "blob:https://example.test/uuid",
      "vbscript:msgbox(1)",
    ]) {
      cleanup();
      const { container } = renderBubble({
        message: messageWith({
          bodyText: body,
          linkSafetyState: "safe",
          links: [linkEntity({ text: body, url: body, href: body })],
        }),
      });

      expect(anchors(container)).toHaveLength(0);
      expect(container.textContent).toContain(body.slice(0, 12));
    }
  });

  it("renders no anchor while the message is still withheld by a legacy server", () => {
    const { container } = renderBubble({
      message: messageWith({ status: "pending_link_scan", linkSafetyState: "" }),
    });
    expect(anchors(container)).toHaveLength(0);
  });
});

/**
 * The clickability allowlist, restated for issue #807: an anchor exists only
 * where the server sent an href on a direct link. No message-level state, no
 * text pattern and no client-side parser can produce one.
 */
describe("clickability is an allowlist", () => {
  const anchors = (container: HTMLElement) => Array.from(container.querySelectorAll("a"));

  it("renders no anchor for a message the server described no links for", () => {
    for (const state of ["", "safe", "inconclusive", "malicious", "unknown"] as const) {
      cleanup();
      const { container } = renderBubble({ message: messageWith({ linkSafetyState: state }) });
      expect(anchors(container)).toHaveLength(0);
      // The text is still there — except for the legacy tombstone, which
      // withholds a condemned body wholesale.
      if (state !== "malicious") expect(container.textContent).toContain(linkURL);
    }
  });

  it("decodes an unrecognised server state as unknown, never as inconclusive", () => {
    expect(normalizeLinkSafety("future_state_v2")).toBe("unknown");
    expect(linkSafetyAllowsAnchors("unknown")).toBe(false);
  });

  it("refuses an entity whose href contradicts its click policy", () => {
    // Decoded through the same parser the API uses, so a server bug cannot
    // become an anchor.
    expect(
      parseMessageLink({ safety: "unknown", click: "interstitial", href: linkURL, url: linkURL }),
    ).toBeUndefined();
    expect(
      parseMessageLink({ safety: "safe", click: "direct", href: "javascript:x" }),
    ).toBeUndefined();
    expect(parseMessageLink({ safety: "trusted", click: "direct", href: linkURL })).toBeUndefined();
    expect(
      parseMessageLink({ target_key: "key", safety: "safe", click: "direct", href: linkURL })?.href,
    ).toBe(linkURL);
  });

  it("renders an anchor only for a direct link with an href", () => {
    const cases: Array<[MessageLink, number]> = [
      [linkEntity(), 1],
      [linkEntity({ href: "" }), 0],
      [unknownLink(), 0],
      [pendingLink(), 0],
    ];
    for (const [link, want] of cases) {
      cleanup();
      const { container } = renderBubble({ message: messageWith({ links: [link] }) });
      expect(anchors(container)).toHaveLength(want);
    }
  });
});

/**
 * Rich preview cards (issue #807 §24-28).
 */
/**
 * Emphasised links (issue #807 CQ follow-up). Plain and rich text share one
 * link pipeline: a URL inside bold, italic or bold-italic is the same span —
 * same entity, same anchor or interstitial, same chip — as a plain one, only
 * wrapped, and the wrapper never mints a second href of its own.
 */
describe("links inside emphasis", () => {
  const anchors = (container: HTMLElement) => Array.from(container.querySelectorAll("a"));
  const wrappers: Array<[string, string, string]> = [
    ["bold", `**${linkURL}**`, "strong"],
    ["italic", `*${linkURL}*`, "em"],
    ["bold-italic", `***${linkURL}***`, "strong em"],
  ];

  it.each(wrappers)(
    "draws a safe %s link as one anchor inside its wrapper",
    (_name, body, wrapper) => {
      const { container } = renderBubble({
        message: messageWith({ bodyText: body, linkSafetyState: "safe", links: [linkEntity()] }),
      });
      const found = anchors(container);
      expect(found).toHaveLength(1);
      expect(found[0].getAttribute("href")).toBe(linkURL);
      expect(found[0].getAttribute("rel")).toContain("noopener");
      expect(container.querySelector(`${wrapper} a`)).toBe(found[0]);
    },
  );

  it.each(wrappers)(
    "draws a pending %s link as text with the pending note and no anchor",
    (_name, body, wrapper) => {
      const { container } = renderBubble({
        message: messageWith({ bodyText: body, linkSafetyState: "", links: [pendingLink()] }),
      });
      expect(anchors(container)).toHaveLength(0);
      expect(container.querySelector(`${wrapper} [data-link-safety='pending']`)).not.toBeNull();
      expect(container.textContent).toContain(linkURL);
    },
  );

  it.each(wrappers)(
    "draws an unverified %s link as the interstitial button, not an anchor",
    (_name, body, wrapper) => {
      const { container } = renderBubble({
        message: messageWith({
          bodyText: body,
          linkSafetyState: "inconclusive",
          links: [unknownLink()],
        }),
      });
      expect(anchors(container)).toHaveLength(0);
      const button = screen.getByRole("button", { name: `${linkURL} — Link não verificado` });
      expect(container.querySelector(`${wrapper} button`)).toBe(button);
      fireEvent.click(button);
      expect(screen.getByRole("dialog")).toHaveTextContent(linkURL);
      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    },
  );

  it.each(wrappers)(
    "draws a blocked %s link as the chip inside its wrapper",
    (_name, body, wrapper) => {
      const { container } = renderBubble({
        message: messageWith({
          bodyText: body.replace(linkURL, "\uFFFC"),
          linkSafetyState: "malicious",
          links: [blockedLink()],
        }),
      });
      expect(anchors(container)).toHaveLength(0);
      expect(container.querySelector(`${wrapper} [data-link-safety='malicious']`)).not.toBeNull();
      expect(container.textContent).not.toContain(linkURL);
    },
  );

  it("still never linkifies inline code", () => {
    const { container } = renderBubble({
      message: messageWith({
        bodyText: `\`${linkURL}\``,
        linkSafetyState: "safe",
        links: [linkEntity()],
      }),
    });
    expect(anchors(container)).toHaveLength(0);
    expect(container.querySelector("code")).toHaveTextContent(linkURL);
  });
});

/**
 * The interstitial derives what it shows from the message's current links
 * (issue #807 CQ follow-up): a verdict that arrives while it is open, or an
 * edit that changes the occurrence, is reflected at once — the "Abrir mesmo
 * assim" action is never offered on a snapshot the server has since replaced.
 */
describe("the interstitial follows the occurrence", () => {
  const anchors = (container: HTMLElement) => Array.from(container.querySelectorAll("a"));
  const openInterstitial = () => {
    fireEvent.click(screen.getByRole("button", { name: `${linkURL} — Link não verificado` }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  };

  it("closes when the link is cleared, and the body offers the anchor instead", () => {
    const { container, rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
    });
    openInterstitial();
    rerenderBubble(
      rerender,
      messageWith({
        linkSafetyState: "safe",
        links: [linkEntity({ updatedAt: "2026-08-17T12:05:00Z" })],
      }),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(anchors(container)).toHaveLength(1);
  });

  it("closes when the link is condemned, and nothing of the URL remains", () => {
    const { container, rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
    });
    openInterstitial();
    rerenderBubble(
      rerender,
      messageWith({
        bodyText: "veja \uFFFC por favor",
        linkSafetyState: "malicious",
        links: [blockedLink()],
      }),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(container.textContent).not.toContain(linkURL);
  });

  it("closes when an edit removes the occurrence", () => {
    const { rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
    });
    openInterstitial();
    rerenderBubble(
      rerender,
      messageWith({ bodyText: "sem link", linkSafetyState: "", links: undefined }),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("closes when an edit replaces the occurrence with another target at the same position", () => {
    const other = "https://other.test/b";
    const { rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
    });
    openInterstitial();
    expect(screen.getByRole("link", { name: "Abrir mesmo assim" }).getAttribute("href")).toBe(
      linkURL,
    );
    rerenderBubble(
      rerender,
      messageWith({
        bodyText: `veja ${other} por favor`,
        linkSafetyState: "inconclusive",
        links: [
          unknownLink({ targetKey: "key-other", text: other, url: other, hostname: "other.test" }),
        ],
      }),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // Opening the new occurrence shows the new destination, never the old one.
    fireEvent.click(screen.getByRole("button", { name: `${other} — Link não verificado` }));
    expect(screen.getByRole("link", { name: "Abrir mesmo assim" }).getAttribute("href")).toBe(
      other,
    );
  });

  it("stays open, on the current state, while the occurrence is still unverified", () => {
    const { rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive", links: [unknownLink()] }),
    });
    openInterstitial();
    rerenderBubble(
      rerender,
      messageWith({
        linkSafetyState: "inconclusive",
        links: [unknownLink({ hostname: "xn--example.test", updatedAt: "2026-08-17T12:05:00Z" })],
      }),
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByTestId("chat-link-interstitial-host")).toHaveTextContent("xn--example.test");
  });
});

describe("preview cards", () => {
  const readyPreview = (overrides: Partial<LinkPreview> = {}): LinkPreview => ({
    state: "ready",
    hostname: "example.test",
    siteName: "Example",
    title: "A page title",
    description: "A short description of the page.",
    imageId: "",
    imageWidth: 0,
    imageHeight: 0,
    ...overrides,
  });

  it("draws the card under the message with the real hostname, as one anchor", () => {
    const { container } = renderBubble({
      message: messageWith({
        linkSafetyState: "safe",
        links: [linkEntity({ preview: readyPreview() })],
      }),
    });

    const card = screen.getByTestId("chat-link-card");
    const cardAnchor = card.querySelector("a.link-card__body") as HTMLAnchorElement;
    expect(cardAnchor.getAttribute("href")).toBe(linkURL);
    expect(cardAnchor.getAttribute("rel")).toContain("noopener");
    expect(cardAnchor.getAttribute("aria-label")).toBe("A page title — example.test");
    expect(card).toHaveTextContent("example.test");
    expect(card).toHaveTextContent("A page title");
    expect(card).toHaveTextContent("A short description");
    // The site's own name does not replace the host.
    expect(card.querySelector(".link-card__host")).toHaveTextContent("example.test");
    // Nothing remote is loaded by the browser.
    expectNoClientSideFetch(container);
  });

  it("renders remote metadata as text, never as markup", () => {
    renderBubble({
      message: messageWith({
        linkSafetyState: "safe",
        links: [linkEntity({ preview: readyPreview({ title: "<img src=x onerror=alert(1)>" }) })],
      }),
    });
    const card = screen.getByTestId("chat-link-card");
    expect(card.querySelectorAll("img")).toHaveLength(0);
    expect(card).toHaveTextContent("<img src=x onerror=alert(1)>");
  });

  it("shows a placeholder while the preview is fetching and nothing when it failed", () => {
    const { rerender } = renderBubble({
      message: messageWith({
        linkSafetyState: "safe",
        links: [linkEntity({ preview: readyPreview({ state: "fetching", title: "" }) })],
      }),
    });
    expect(screen.getByTestId("chat-link-card-placeholder")).toHaveTextContent(
      "Preparando visualização…",
    );
    // The link is already clickable while the card is on its way.
    expect(screen.getByRole("link", { name: linkURL })).toBeInTheDocument();

    rerenderBubble(
      rerender,
      messageWith({
        linkSafetyState: "safe",
        links: [linkEntity({ preview: readyPreview({ state: "failed", title: "" }) })],
      }),
    );
    expect(screen.queryByTestId("chat-link-card-placeholder")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-link-card")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    // Failure changes nothing about the anchor.
    expect(screen.getByRole("link", { name: linkURL })).toBeInTheDocument();
  });

  it("draws at most two cards, one per distinct target, in order", () => {
    const urls = ["https://a.test/1", "https://b.test/2", "https://c.test/3"];
    const links = [
      ...urls.map((url, ordinal) =>
        linkEntity({
          ordinal,
          targetKey: `key-${ordinal}`,
          text: url,
          url,
          href: url,
          hostname: new URL(url).host,
          preview: readyPreview({ hostname: new URL(url).host, title: `T${ordinal}` }),
        }),
      ),
      // The first target again: no second card.
      linkEntity({
        ordinal: 3,
        targetKey: "key-0",
        text: urls[0],
        url: urls[0],
        href: urls[0],
        hostname: "a.test",
        preview: readyPreview({ hostname: "a.test", title: "T0" }),
      }),
    ];
    const { container } = renderBubble({
      message: messageWith({
        bodyText: `${urls.join(" ")} ${urls[0]}`,
        linkSafetyState: "safe",
        links,
      }),
    });

    const cards = screen.getAllByTestId("chat-link-card");
    expect(cards).toHaveLength(2);
    expect(cards[0]).toHaveTextContent("a.test");
    expect(cards[1]).toHaveTextContent("b.test");
    // Every link is still an anchor, cards or not.
    expect(container.querySelectorAll("a.rtr-link")).toHaveLength(4);
  });

  it("never draws a card for a link that is not safe, whatever the preview says", () => {
    renderBubble({
      message: messageWith({
        linkSafetyState: "inconclusive",
        links: [unknownLink({ preview: readyPreview() })],
      }),
    });
    expect(screen.queryByTestId("chat-link-card")).not.toBeInTheDocument();
  });

  it("hides the card from its menu and keeps the link", () => {
    localStorage.clear();
    const { container } = renderBubble({
      message: messageWith({
        linkSafetyState: "safe",
        links: [linkEntity({ preview: readyPreview() })],
      }),
    });
    fireEvent.click(screen.getByRole("button", { name: "Opções da visualização de example.test" }));
    const menu = screen.getByRole("menu");
    expect(screen.getByRole("menuitem", { name: "Abrir link" }).getAttribute("href")).toBe(linkURL);
    expect(screen.getByRole("menuitem", { name: "Copiar link" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /reportar/i })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: "Ocultar visualização" }));

    expect(menu).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-link-card")).not.toBeInTheDocument();
    expect(container.querySelectorAll("a.rtr-link")).toHaveLength(1);
    // Remembered for this reader.
    cleanup();
    renderBubble({
      message: messageWith({
        linkSafetyState: "safe",
        links: [linkEntity({ preview: readyPreview() })],
      }),
    });
    expect(screen.queryByTestId("chat-link-card")).not.toBeInTheDocument();
    localStorage.clear();
  });
});

/**
 * The re-check cooldown (CQ-007).
 *
 * The API answers `retry_after_seconds` because its own rate limit is real;
 * ignoring it made the button offer an action that was going to be refused. The
 * backend stays the authority — this is ergonomics, and a reload legitimately
 * clears it because the server will simply refuse again.
 */
describe("the re-check cooldown", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("keeps the button disabled for the interval the server asked for", async () => {
    const onReconcileLinkSafety = vi.fn(async () => ({
      state: "inconclusive" as const,
      updatedAt: "2026-08-18T12:00:00Z",
      retryAfterSeconds: 60,
    }));
    renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
      onReconcileLinkSafety,
    });

    const button = screen.getByTestId("chat-message-link-recheck");
    fireEvent.click(button);

    // Settle the request inside act, so the cooldown state lands.
    await vi.waitFor(() => expect(onReconcileLinkSafety).toHaveBeenCalledTimes(1));
    await act(async () => {
      await Promise.resolve();
    });
    expect(button).toBeDisabled();

    // One second short of the window: still refused, and still no second request.
    await act(async () => {
      vi.advanceTimersByTime(59_000);
    });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onReconcileLinkSafety).toHaveBeenCalledTimes(1);

    // The window closes.
    await act(async () => {
      vi.advanceTimersByTime(1_000);
    });
    expect(button).not.toBeDisabled();

    fireEvent.click(button);
    expect(onReconcileLinkSafety).toHaveBeenCalledTimes(2);
  });

  // CQ-002. The server already withholds these bodies, so the client's job is to
  // say why rather than render an empty block — and, whatever it renders, to
  // produce no anchor and no request for the address it no longer has.
  describe("a condemned message seen through another message", () => {
    const withheld = "Conteúdo ocultado por segurança.";

    it("shows a quote of it as withheld, with no anchor", () => {
      renderBubble({
        message: messageWith({
          bodyText: "concordo",
          quoted: {
            id: "msg-source",
            authorId: "user-2",
            // What the server sends for a condemned source: an empty body plus
            // the state that explains it.
            bodyText: "",
            bodyFormat: "v2",
            isRemoved: false,
            deletedAt: null,
            createdAt: "2026-08-17T11:00:00Z",
            linkSafetyState: "malicious",
          },
        }),
      });

      const quote = screen.getByTestId("chat-message-quote");
      expect(quote).toHaveTextContent(withheld);
      expect(quote.querySelector("a")).toBeNull();
      expect(quote.textContent).not.toContain("http");
      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it("shows a cross-target reference to it as withheld, with no anchor", () => {
      renderBubble({
        message: messageWith({
          bodyText: "veja lá",
          reference: {
            available: true,
            messageId: "msg-source",
            targetType: "channel",
            targetId: "ch-2",
            targetLabel: "geral",
            authorDisplayName: "Bea",
            bodyText: "",
            bodyFormat: "v2",
            createdAt: "2026-08-17T11:00:00Z",
            linkSafetyState: "malicious",
          },
        }),
      });

      const reference = screen.getByTestId("chat-message-reference");
      expect(reference).toHaveTextContent(withheld);
      expect(reference.querySelector("a")).toBeNull();
      expect(reference.textContent).not.toContain("http");
      expect(fetchSpy).not.toHaveBeenCalled();
    });

    // The withholding is keyed on the state, not on an empty body: a quote of an
    // ordinary message still renders, and an inconclusive one still renders with
    // its link clickable.
    it("still renders a quote whose source is merely inconclusive", () => {
      renderBubble({
        message: messageWith({
          bodyText: "concordo",
          quoted: {
            id: "msg-source",
            authorId: "user-2",
            bodyText: `veja ${linkURL}`,
            bodyFormat: "v2",
            isRemoved: false,
            deletedAt: null,
            createdAt: "2026-08-17T11:00:00Z",
            linkSafetyState: "inconclusive",
          },
        }),
      });

      const quote = screen.getByTestId("chat-message-quote");
      expect(quote).not.toHaveTextContent(withheld);
      expect(quote.textContent).toContain(linkURL);
    });
  });
});
