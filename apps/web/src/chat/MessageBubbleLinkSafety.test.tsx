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

import MessageBubble from "./MessageBubble";
import type { MessageBubbleProps } from "./MessageBubble";
import { linkSafetyAllowsAnchors, normalizeLinkSafety } from "./chatTypes";
import type { LinkSafetyRecheck, Message } from "./chatTypes";

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
    allowedReactionEmojis: [],
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
describe("anchors", () => {
  const anchors = (container: HTMLElement) =>
    Array.from(container.querySelectorAll("a")) as HTMLAnchorElement[];

  it("renders a safe message's URL as a real anchor", () => {
    const { container } = renderBubble({ message: messageWith({ linkSafetyState: "safe" }) });

    const found = anchors(container);
    expect(found).toHaveLength(1);
    expect(found[0].getAttribute("href")).toBe(linkURL);
    expect(found[0]).toHaveTextContent(linkURL);
    expectNoClientSideFetch(container);
  });

  // The proof the whole issue turns on: unverified is *published and clickable*,
  // with the notice above it — not blocked, and not stripped of its link.
  it("renders an unverified message's URL as a real anchor, under the notice", () => {
    const { container } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
    });

    const found = anchors(container);
    expect(found).toHaveLength(1);
    expect(found[0].getAttribute("href")).toBe(linkURL);

    const notice = screen.getByTestId("chat-message-link-unverified");
    expect(
      notice.compareDocumentPosition(found[0]) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expectNoClientSideFetch(container);
  });

  it("gives every anchor a permitted protocol and safe rel/target", () => {
    for (const state of ["safe", "inconclusive"] as const) {
      cleanup();
      const { container } = renderBubble({ message: messageWith({ linkSafetyState: state }) });

      for (const anchor of anchors(container)) {
        const href = anchor.getAttribute("href") ?? "";
        expect(href).toMatch(/^https?:\/\//i);
        expect(new URL(href).protocol).toMatch(/^https?:$/);
        // A new tab must not hand the opened page a handle back to this one, nor
        // leak the workspace URL — which names a channel or a conversation — as a
        // referrer.
        expect(anchor.getAttribute("target")).toBe("_blank");
        const rel = (anchor.getAttribute("rel") ?? "").split(/\s+/);
        expect(rel).toContain("noopener");
        expect(rel).toContain("noreferrer");
      }
    }
  });

  // The other half of the proof.
  it("renders no anchor at all for a condemned message", () => {
    const { container } = renderBubble({ message: messageWith({ linkSafetyState: "malicious" }) });

    expect(anchors(container)).toHaveLength(0);
    expect(container.textContent).not.toContain(linkURL);
    expectNoClientSideFetch(container);
  });

  // A message still being checked was never published, so there is no public link
  // to offer — even to its own author, who is the only person who can see it.
  it("renders no anchor while the message is still being checked", () => {
    const { container } = renderBubble({
      message: messageWith({ status: "pending_link_scan", linkSafetyState: "" }),
    });

    expect(anchors(container)).toHaveLength(0);
  });

  // The realtime transition, as the DOM sees it: the same component, re-rendered
  // with the corrected state, loses its anchor.
  it("drops the anchor when a published message is later condemned", () => {
    const { container, rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
    });
    expect(anchors(container)).toHaveLength(1);

    rerender(
      <MessageBubble
        message={messageWith({ linkSafetyState: "malicious" })}
        onToggleReaction={vi.fn()}
        onReplyMessage={vi.fn()}
        onReferenceMessage={vi.fn()}
        onToggleFavorite={vi.fn()}
        onEditMessage={vi.fn()}
        onEditForbidden={vi.fn()}
        onDeleteMessage={vi.fn()}
        allowedReactionEmojis={[]}
        recentReactionEmojis={[]}
        reactionMenuVisible={false}
        onReactionMenuVisibleChange={vi.fn()}
        pickerOpen={false}
        onPickerOpenChange={vi.fn()}
      />,
    );

    expect(anchors(container)).toHaveLength(0);
    expect(screen.getByTestId("chat-message-link-blocked")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
  });

  // And the benign direction: the notice goes, the anchor stays.
  it("keeps the anchor and drops the notice when a verdict finally clears it", () => {
    const { container, rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
    });
    expect(screen.getByTestId("chat-message-link-unverified")).toBeInTheDocument();

    rerender(
      <MessageBubble
        message={messageWith({ linkSafetyState: "safe" })}
        onToggleReaction={vi.fn()}
        onReplyMessage={vi.fn()}
        onReferenceMessage={vi.fn()}
        onToggleFavorite={vi.fn()}
        onEditMessage={vi.fn()}
        onEditForbidden={vi.fn()}
        onDeleteMessage={vi.fn()}
        allowedReactionEmojis={[]}
        recentReactionEmojis={[]}
        reactionMenuVisible={false}
        onReactionMenuVisibleChange={vi.fn()}
        pickerOpen={false}
        onPickerOpenChange={vi.fn()}
      />,
    );

    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
    const found = anchors(container);
    expect(found).toHaveLength(1);
    expect(found[0].getAttribute("href")).toBe(linkURL);
    // One message, not two: a correction is not a creation.
    expect(container.querySelectorAll("[data-testid='chat-msg-bubble']")).toHaveLength(1);
  });

  // A message aggregated as malicious because *one* of its links was condemned
  // must not leave the others clickable. With the aggregate model that is
  // structural — nothing is rendered as an anchor at all.
  it("leaves no anchor clickable when one of several links is condemned", () => {
    const { container } = renderBubble({
      message: messageWith({
        bodyText: `bom https://good.test/a e ruim ${linkURL}`,
        linkSafetyState: "malicious",
      }),
    });

    expect(anchors(container)).toHaveLength(0);
    expect(container.textContent).not.toContain("https://good.test/a");
  });

  // Dangerous schemes, end to end through the real renderer rather than only
  // through the scanner's unit tests.
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
        message: messageWith({ bodyText: body, linkSafetyState: "safe" }),
      });

      expect(anchors(container)).toHaveLength(0);
      // The text is still shown — it is what the sender wrote — it is simply not
      // a destination.
      expect(container.textContent).toContain(body.slice(0, 12));
    }
  });

  it("leaves text that merely resembles a URL as text", () => {
    const { container } = renderBubble({
      message: messageWith({
        bodyText: "example.test/a e www.example.test e hxxps://example.test/a",
        linkSafetyState: "safe",
      }),
    });

    expect(anchors(container)).toHaveLength(0);
  });
});

/**
 * The clickability allowlist (CQ-004).
 *
 * Migration 000027 gave every pre-existing message `link_safety_state = ''`, and
 * a deployment with link scanning switched off produces nothing else. Those
 * messages have never been checked by anything, so "not known bad" must not be
 * read as "good enough to link" — otherwise turning the feature on retroactively
 * linkifies the entire message history on no evidence.
 */
describe("clickability is an allowlist", () => {
  const anchors = (container: HTMLElement) => Array.from(container.querySelectorAll("a"));

  it("renders no anchor for a legacy message with no link-safety state", () => {
    const { container } = renderBubble({
      message: messageWith({ status: "active", linkSafetyState: "" }),
    });

    expect(anchors(container)).toHaveLength(0);
    // The text is still shown in full — nothing is withheld, it is simply not a
    // destination.
    expect(container.textContent).toContain(linkURL);
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-message-link-blocked")).not.toBeInTheDocument();
  });

  it("renders no anchor when the field is absent altogether", () => {
    // A pre-#135 server sends no field at all; the mapper leaves it undefined.
    const message = messageWith();
    delete (message as { linkSafetyState?: unknown }).linkSafetyState;
    const { container } = renderBubble({ message });

    expect(anchors(container)).toHaveLength(0);
    expect(container.textContent).toContain(linkURL);
  });

  // CQ-004: "the provider produced no usable verdict" and "this build does not
  // recognise what the server said" are different facts. The decoder maps the
  // second to `unknown`, which authorises nothing — mapping it to `inconclusive`
  // would have granted anchors on a state nobody here has reasoned about.
  it("renders no anchor for a state this client does not understand", () => {
    for (const raw of ["future_state_v2", "probably_fine", "SAFE", "unknown"]) {
      cleanup();
      const { container } = renderBubble({
        message: messageWith({ linkSafetyState: normalizeLinkSafety(raw) }),
      });

      expect(anchors(container), `state ${raw} produced an anchor`).toHaveLength(0);
      // The content is still shown in full — nothing is withheld, it is simply not
      // a destination.
      expect(container.textContent).toContain(linkURL);
    }
  });

  it("decodes an unrecognised server state as unknown, never as inconclusive", () => {
    expect(normalizeLinkSafety("future_state_v2")).toBe("unknown");
    expect(normalizeLinkSafety("inconclusive")).toBe("inconclusive");
    expect(normalizeLinkSafety("")).toBe("");
    expect(normalizeLinkSafety(undefined)).toBe("");
    expect(linkSafetyAllowsAnchors("unknown")).toBe(false);
    expect(linkSafetyAllowsAnchors("")).toBe(false);
    expect(linkSafetyAllowsAnchors("malicious")).toBe(false);
    expect(linkSafetyAllowsAnchors("safe")).toBe(true);
    expect(linkSafetyAllowsAnchors("inconclusive")).toBe(true);
  });

  // A message that was clickable must stop being clickable when a realtime
  // correction names a state this build does not understand.
  it("drops the anchor when a correction names an unknown state", () => {
    const { container, rerender } = renderBubble({
      message: messageWith({ linkSafetyState: "inconclusive" }),
    });
    expect(anchors(container)).toHaveLength(1);

    rerender(
      <MessageBubble
        message={messageWith({ linkSafetyState: normalizeLinkSafety("future_state_v2") })}
        onToggleReaction={vi.fn()}
        onReplyMessage={vi.fn()}
        onReferenceMessage={vi.fn()}
        onToggleFavorite={vi.fn()}
        onEditMessage={vi.fn()}
        onEditForbidden={vi.fn()}
        onDeleteMessage={vi.fn()}
        allowedReactionEmojis={[]}
        recentReactionEmojis={[]}
        reactionMenuVisible={false}
        onReactionMenuVisibleChange={vi.fn()}
        pickerOpen={false}
        onPickerOpenChange={vi.fn()}
      />,
    );

    expect(anchors(container)).toHaveLength(0);
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
  });

  it("renders an anchor only for the two checked states", () => {
    for (const state of ["safe", "inconclusive"] as const) {
      cleanup();
      const { container } = renderBubble({ message: messageWith({ linkSafetyState: state }) });
      expect(anchors(container)).toHaveLength(1);
    }
    for (const state of ["", "malicious", "unknown"] as const) {
      cleanup();
      const { container } = renderBubble({ message: messageWith({ linkSafetyState: state }) });
      expect(anchors(container)).toHaveLength(0);
    }
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
