/**
 * What a message says, and the notices that qualify it.
 *
 * Split out of MessageBubble (issue #496, CQ follow-up). The bubble was deciding
 * layout, link-safety presentation, quote and reference previews, the editor
 * swap and the toolbar all in one function; this file owns the first four, which
 * are all answers to "what goes inside the bubble".
 */

import { useCallback, useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";

import { linkSafetyAllowsAnchors } from "./chatTypes";
import type { LinkSafetyRecheck, Message } from "./chatTypes";
import InlineMessageEditor from "./InlineMessageEditor";
import MessageAttachments from "./MessageAttachments";
import RichTextRenderer from "./RichTextRenderer";
import type { CodecFormat } from "./tiptapSerializer";

/**
 * The exact notice shown above a message whose links could not be verified
 * (issue #135).
 *
 * The wording is deliberate and is not a security warning. Cloudflare's real
 * production answer for this case was an operational refusal — the hostname had
 * been scanned too recently — which is evidence of nothing about the link. So
 * this says what is actually true: the check did not complete, and the automatic
 * preview was therefore not loaded. It does not call the link dangerous, and it
 * does not call it safe.
 */
const linkUnverifiedNotice =
  "Não foi possível verificar este link agora. A prévia automática não foi carregada.";

/**
 * What a reader is told about a link the provider condemned after the message
 * had already been delivered (issue #135).
 *
 * This one *is* a security statement, and it is the only one in this file, which
 * is why it reads nothing like the notice above.
 */
const linkBlockedNotice = "Este link foi bloqueado após a verificação de segurança.";

/**
 * What a quote, a cross-target reference, or an edit-history entry shows in
 * place of a body whose links this deployment condemned (issue #135, CQ-002).
 *
 * The server already sends an empty body for those, so this is presentation
 * only — it exists so the reader is told the content was withheld rather than
 * being shown an empty block that reads like a rendering bug.
 */
export const withheldBodyNotice = "Conteúdo ocultado por segurança.";

interface QuoteBlockProps {
  quote: NonNullable<Message["quoted"]>;
  authorLabel: string;
  canJump: boolean;
  onJump?: (messageId: string) => void;
}

function QuoteBlock({ quote, authorLabel, canJump, onJump }: QuoteBlockProps) {
  const jump = () => {
    if (canJump) onJump?.(quote.id);
  };
  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (!canJump || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    jump();
  };

  return (
    <div
      className={`chat-msg-area__quote${canJump ? " chat-msg-area__quote--clickable" : " chat-msg-area__quote--disabled"}`}
      role={canJump ? "button" : undefined}
      tabIndex={canJump ? 0 : undefined}
      aria-disabled={canJump ? undefined : true}
      aria-label={canJump ? `Ir para mensagem original de ${authorLabel}` : undefined}
      onClick={canJump ? jump : undefined}
      onKeyDown={handleKeyDown}
      data-testid="chat-message-quote"
    >
      <div className="chat-msg-area__quote-author">{authorLabel}</div>
      <div className="chat-msg-area__quote-excerpt">
        {quote.isRemoved ? (
          "Mensagem original indisponível."
        ) : quote.linkSafetyState === "malicious" ? (
          <span className="chat-msg-area__link-blocked-body">{withheldBodyNotice}</span>
        ) : (
          <RichTextRenderer text={quote.bodyText} bodyFormat={quote.bodyFormat} />
        )}
      </div>
    </div>
  );
}

type AvailableReference = Extract<NonNullable<Message["reference"]>, { available: true }>;

function referenceTargetLabel(reference: AvailableReference): string {
  if (reference.targetLabel) {
    return `${reference.targetType === "channel" ? "#" : ""}${reference.targetLabel}`;
  }
  return reference.targetType === "channel" ? "Canal" : "Conversa";
}

function ReferenceBlock({
  reference,
  onJump,
}: {
  reference: NonNullable<Message["reference"]>;
  onJump?: (reference: NonNullable<Message["reference"]>) => void;
}) {
  if (!reference.available) {
    return (
      <div
        className="chat-msg-area__reference chat-msg-area__reference--unavailable"
        aria-disabled="true"
        data-testid="chat-message-reference"
      >
        citação indisponível
      </div>
    );
  }
  return (
    <div
      className="chat-msg-area__reference chat-msg-area__reference--clickable"
      data-testid="chat-message-reference"
      role="link"
      tabIndex={0}
      aria-label="Ir para mensagem citada"
      onClick={() => onJump?.(reference)}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onJump?.(reference);
        }
      }}
    >
      <span className="chat-msg-area__reference-origin">{referenceTargetLabel(reference)}</span>
      <div className="chat-msg-area__reference-author">
        {reference.authorDisplayName || "Usuário"}
      </div>
      <div className="chat-msg-area__reference-body">
        {reference.linkSafetyState === "malicious" ? (
          <span className="chat-msg-area__link-blocked-body">{withheldBodyNotice}</span>
        ) : (
          <RichTextRenderer text={reference.bodyText} bodyFormat={reference.bodyFormat} />
        )}
      </div>
    </div>
  );
}

/**
 * The "could not verify this link" notice, and its optional re-check action
 * (issue #135).
 *
 * # Why it renders above the message and not instead of it
 *
 * The message was published. `inconclusive` means this server has no clearance
 * to fetch the URL on its own behalf — it is a statement about our authority, not
 * about the link — so the content is drawn exactly as any other message's and the
 * notice sits above it as context.
 *
 * # What this component does not do
 *
 * It does not touch the link. There is no fetch, no HEAD, no `<link rel=preload>`,
 * no prefetch and no image whose src is the message's URL anywhere in this file
 * or in the renderer it wraps. RichTextRenderer may produce an anchor that only
 * navigates after the reader clicks it; it produces no automatic fetch or
 * subresource. The reader's own browser is free to do whatever they ask of it
 * when they copy or click; that is their browser, not our server, and the
 * distinction is the whole point of the state.
 *
 * The button asks the backend to re-read a verdict it may already have. It does
 * not start a scan, and it deliberately says "Verificar novamente" rather than
 * anything that would promise one. It disables itself while in flight and for the
 * interval the server suggests, so it cannot become a poll.
 */
function LinkSafetyNotice({
  messageId,
  onReconcile,
}: {
  messageId: string;
  onReconcile?: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
}) {
  const [checking, setChecking] = useState(false);
  // When the server's cooldown lifts, as an epoch millisecond. Null means "not
  // waiting", and the timer below is what clears it. Kept in component state
  // only: the backend is the authority on the rate limit and re-applies it on
  // every request, so a reload simply asks again and is refused there. This is
  // ergonomics — it stops the button offering an action that will be rejected.
  const [blockedUntil, setBlockedUntil] = useState<number | null>(null);
  // Guards a setState after the bubble is gone — a message can be deleted, or the
  // conversation switched, while the request is in flight.
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // One timer, armed only while a cooldown is actually running, so an idle notice
  // costs nothing. The clearing happens in the timer callback rather than in the
  // effect body: an effect that calls setState synchronously is a cascading
  // render, and a zero delay is enough to make an already-elapsed cooldown lift
  // on the next tick.
  useEffect(() => {
    if (blockedUntil === null) return;
    const remaining = Math.max(0, blockedUntil - Date.now());
    const timer = window.setTimeout(() => {
      if (mounted.current) setBlockedUntil(null);
    }, remaining);
    return () => window.clearTimeout(timer);
  }, [blockedUntil]);

  const disabled = checking || blockedUntil !== null;

  const recheck = useCallback(async () => {
    if (!onReconcile || checking || blockedUntil !== null) return;
    setChecking(true);
    try {
      const result = await onReconcile(messageId);
      // The server tells us how long its own cooldown runs for. Honouring it is
      // what keeps the button from becoming a poll: asking again sooner would be
      // refused, and offering an action that will be refused is a lie about what
      // the control does.
      if (mounted.current && result?.retryAfterSeconds) {
        setBlockedUntil(Date.now() + result.retryAfterSeconds * 1000);
      }
    } finally {
      // Re-enabled either way. A state that actually changed removes this notice
      // entirely, so the only case where the button comes back is the one where
      // nothing was learned.
      if (mounted.current) setChecking(false);
    }
  }, [blockedUntil, checking, messageId, onReconcile]);

  return (
    <div
      className="chat-msg-area__link-notice"
      data-testid="chat-message-link-unverified"
      role="note"
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        info
      </span>
      <span className="chat-msg-area__link-notice-text">{linkUnverifiedNotice}</span>
      {onReconcile && (
        <button
          type="button"
          className="chat-msg-area__link-notice-action"
          data-testid="chat-message-link-recheck"
          onClick={() => void recheck()}
          disabled={disabled}
        >
          {checking ? "Verificando…" : "Verificar novamente"}
        </button>
      )}
    </div>
  );
}

/**
 * RF-21 (issue #135). Above the content, so a reader sees the caveat before the
 * link it is about. The two link-safety states are deliberately different in
 * tone and in consequence:
 *
 * - inconclusive: the check did not complete. The message renders in full,
 *   unchanged, and this is context rather than a warning. It is not a claim
 *   about the link, and the copy must never become one;
 * - malicious: the provider condemned a link after the message had already been
 *   delivered. The body is withheld the same way a removed message's is, so the
 *   URL cannot be read off the screen, copied or acted on — the message, its
 *   author and its timestamp stay, so the conversation still makes sense.
 *
 * The pending-scan notice above them is the sender's alone: the backend
 * withholds such a message from every other reader, so this is what stops the
 * composer from claiming a delivery that has not happened. The state is
 * rendered, never decided.
 */
function MessageNotices({
  message,
  onReconcile,
}: {
  message: Message;
  onReconcile?: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
}) {
  return (
    <>
      {message.status === "pending_link_scan" && (
        <div className="chat-msg-area__pending-scan" data-testid="chat-message-pending-scan">
          <span className="material-symbols-outlined" aria-hidden="true">
            shield
          </span>
          Verificando segurança dos links…
        </div>
      )}
      <LinkSafetyBanner message={message} onReconcile={onReconcile} />
      {message.isForwarded && !message.isRemoved && (
        <div className="chat-msg-area__forwarded" data-testid="chat-message-forwarded">
          <span className="material-symbols-outlined" aria-hidden="true">
            forward
          </span>
          Mensagem encaminhada
        </div>
      )}
    </>
  );
}

function LinkSafetyBanner({
  message,
  onReconcile,
}: {
  message: Message;
  onReconcile?: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
}) {
  if (message.isRemoved) return null;
  if (message.linkSafetyState === "inconclusive") {
    return <LinkSafetyNotice messageId={message.id} onReconcile={onReconcile} />;
  }
  if (message.linkSafetyState === "malicious") {
    return (
      <div className="chat-msg-area__link-blocked" data-testid="chat-message-link-blocked">
        <span className="material-symbols-outlined" aria-hidden="true">
          gpp_maybe
        </span>
        {linkBlockedNotice}
      </div>
    );
  }
  return null;
}

/**
 * The reference and quote previews. Both are hidden on a removed message so no
 * residual context is mixed with the removal placeholder.
 */
function MessageContextBlocks({
  message,
  quoteAuthorLabel,
  canJumpToQuote = false,
  onQuoteJump,
  onReferenceJump,
}: Pick<MessageContentProps, "message" | "quoteAuthorLabel" | "canJumpToQuote"> & {
  onQuoteJump?: (messageId: string) => void;
  onReferenceJump?: (reference: NonNullable<Message["reference"]>) => void;
}) {
  if (message.isRemoved) return null;
  return (
    <>
      {message.reference && (
        <ReferenceBlock reference={message.reference} onJump={onReferenceJump} />
      )}
      {message.quoted && (
        <QuoteBlock
          quote={message.quoted}
          authorLabel={quoteAuthorLabel ?? "Usuário desconhecido"}
          canJump={canJumpToQuote}
          onJump={onQuoteJump}
        />
      )}
    </>
  );
}

/**
 * The body itself, in the one of four forms the message's state calls for.
 *
 * A condemned body is withheld rather than rendered with the link struck
 * through: the body *is* the link, as far as the risk goes, and a URL a reader
 * can select and paste is a URL the block did not stop. Nothing there is
 * clickable and nothing is fetched.
 */
function MessageBodyContent({
  message,
  channelId,
  editing,
  onSaveEdit,
  onCancelEdit,
  onEditForbidden,
}: Pick<
  MessageContentProps,
  "message" | "channelId" | "editing" | "onSaveEdit" | "onCancelEdit" | "onEditForbidden"
>) {
  if (message.isRemoved) return "Mensagem removida.";
  if (message.linkSafetyState === "malicious") {
    return <span className="chat-msg-area__link-blocked-body">{withheldBodyNotice}</span>;
  }
  if (editing) {
    return (
      <InlineMessageEditor
        message={message}
        channelId={channelId}
        onSave={onSaveEdit}
        onCancel={onCancelEdit}
        onForbidden={onEditForbidden}
      />
    );
  }
  // The state test is linkSafetyAllowsAnchors, an allowlist of exactly the two
  // states representing a *completed* check. Everything else — the legacy empty
  // state, and `unknown`, which is what the decoder produces for a server value
  // this build does not recognise — renders as literal text.
  //
  // `status === "active"` is still required, because a withheld message was
  // never published and has no public link, but it is never sufficient on its
  // own: since #135 a published message is not a verified one.
  const linksClickable =
    message.status === "active" && linkSafetyAllowsAnchors(message.linkSafetyState ?? "");
  return (
    <RichTextRenderer
      text={message.bodyText}
      bodyFormat={message.bodyFormat}
      linksClickable={linksClickable}
    />
  );
}

export interface MessageContentProps {
  message: Message;
  channelId?: string;
  editing: boolean;
  onSaveEdit: (body: string, format: CodecFormat) => Promise<Message>;
  onCancelEdit: () => void;
  onEditForbidden: () => void;
  quoteAuthorLabel?: string;
  canJumpToQuote?: boolean;
  onQuoteJump?: (messageId: string) => void;
  onReferenceJump?: (reference: NonNullable<Message["reference"]>) => void;
  onReconcileLinkSafety?: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
}

export default function MessageContent(props: MessageContentProps) {
  const { message } = props;
  return (
    <>
      <MessageNotices message={message} onReconcile={props.onReconcileLinkSafety} />
      <MessageContextBlocks
        message={message}
        quoteAuthorLabel={props.quoteAuthorLabel}
        canJumpToQuote={props.canJumpToQuote}
        onQuoteJump={props.onQuoteJump}
        onReferenceJump={props.onReferenceJump}
      />
      <MessageBodyContent {...props} />
      {/* RF-32. Below the body, so an attachment sent with text reads as part of
          the same message, and hidden for a removed one along with everything
          else the placeholder replaces. Editing does not touch attachments, so
          they stay visible while the body is being edited. */}
      {!message.isRemoved && <MessageAttachments attachments={message.attachments} />}
    </>
  );
}
