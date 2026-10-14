/**
 * PinnedMessagesSection — the details panel's "Mensagens fixadas" collection
 * (issue #896).
 *
 * A projection of the one pin collection ChatMessageArea's usePins holds: it
 * fetches nothing, keeps no copy of the list and applies no order of its own —
 * the server's order (newest pin first) is the order drawn. Expansion, the
 * compact cap and the loading/error/empty states are the shared primitive's
 * (issue #892); what is left here is the row, and where focus goes when a row
 * the reader just unpinned disappears.
 *
 * Navigating and unpinning are callbacks from the composition root, so this
 * file builds no URL and calls no API. Neither is a security boundary: the
 * server re-checks read access on the target for every pin request.
 *
 * Security: the body goes through RichTextRenderer without link entities or
 * mention interaction, so it is text and inert markup only — never an anchor,
 * never a nested control, never raw HTML.
 */

import { useCallback, useId, useLayoutEffect, useRef, type ReactNode, type Ref } from "react";

import type { PinnedItem } from "./chatTypes";
import ExpandableDetailsSection, {
  SectionMessage,
  type ExpandableSectionContent,
} from "./ExpandableDetailsSection";
import { formatDayLabel, formatTime, senderLabel } from "./messageDisplay";
import RichTextRenderer from "./RichTextRenderer";
import type { PinMutationOutcome, PinsCollection } from "./usePins";

/** Everything the section is handed by the conversation's composition root. */
export interface PinnedMessages {
  /** The conversation the collection belongs to; the panel keys the section by it. */
  conversationKey: string;
  collection: PinsCollection;
  /** Messages with an unpin (or pin) in flight. */
  pendingIds: ReadonlySet<string>;
  /** Opens the message in the conversation, through the `?message=` deep link. */
  onNavigate: (messageId: string) => void;
  /** Unpins, and says how that write ended once the list has been reconciled. */
  onUnpin: (messageId: string) => Promise<PinMutationOutcome>;
  /** Retries a failed load of the collection. */
  onRetry: () => void;
}

/**
 * "Juliane Lino · Hoje, 12:30"; the date is dropped when it is not a usable one.
 * A sender with no name and no e-mail is named in words, never by an id: the
 * caption is also every control's accessible name.
 */
function pinCaption(pin: PinnedItem): string {
  const author = senderLabel(pin.message, "Autor não identificado");
  const day = formatDayLabel(pin.pinnedAt);
  return day ? `${author} · ${day}, ${formatTime(pin.pinnedAt)}` : author;
}

interface PinnedMessageRowProps {
  pin: PinnedItem;
  pending: boolean;
  onNavigate: (messageId: string) => void;
  onUnpin: (messageId: string) => void;
  /** This row is being removed while focus is inside it. Must be stable. */
  onRemovedWithFocus: (messageId: string) => void;
  /** Hands the row's navigation control to the section, for focus after a removal. */
  openRef: Ref<HTMLButtonElement>;
}

/**
 * One pin: a navigation control and an unpin control, as siblings.
 *
 * The body can hold block content (lists, code) that a <button> may not
 * contain, so the navigation button carries the caption and stretches over the
 * whole row through CSS, with the body as its description. The unpin button
 * sits above that stretch, so its click never reaches navigation — by
 * structure, not by stopPropagation.
 *
 * Pending is `aria-disabled`, not `disabled`: a disabled control drops focus
 * out from under the person who just pressed it, and a failed unpin must leave
 * them where they were. The repeat is refused here and, authoritatively, by
 * usePins' per-message lock.
 *
 * Whether focus goes down with the row is observed where it is decided: React
 * runs a layout effect's cleanup while the row is still in the document, just
 * before removing it. Focus inside the row at that instant is focus this
 * removal takes away — nothing is inferred from a blur afterwards.
 */
function PinnedMessageRow({
  pin,
  pending,
  onNavigate,
  onUnpin,
  onRemovedWithFocus,
  openRef,
}: PinnedMessageRowProps) {
  const bodyId = useId();
  const rowRef = useRef<HTMLLIElement>(null);
  const caption = pinCaption(pin);
  const messageId = pin.message.id;
  useLayoutEffect(() => {
    const row = rowRef.current;
    return () => {
      if (row?.contains(document.activeElement)) onRemovedWithFocus(messageId);
    };
  }, [messageId, onRemovedWithFocus]);
  return (
    <li ref={rowRef} className="chat-details__pin">
      <span className="material-symbols-outlined chat-details__pin-icon" aria-hidden="true">
        push_pin
      </span>
      <div className="chat-details__pin-text">
        <div id={bodyId} className="chat-details__pin-body">
          {pin.message.isRemoved ? (
            <em>Mensagem removida.</em>
          ) : (
            <RichTextRenderer text={pin.message.bodyText} bodyFormat={pin.message.bodyFormat} />
          )}
        </div>
        <button
          ref={openRef}
          type="button"
          className="chat-details__pin-open"
          aria-label={`Ir para a mensagem de ${caption}`}
          aria-describedby={bodyId}
          onClick={() => onNavigate(messageId)}
        >
          {caption}
        </button>
      </div>
      <button
        type="button"
        className="chat-details__pin-unpin"
        title="Desafixar"
        aria-label={
          pending ? `Desafixando mensagem de ${caption}…` : `Desafixar mensagem de ${caption}`
        }
        aria-disabled={pending || undefined}
        onClick={() => {
          if (!pending) onUnpin(messageId);
        }}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          {pending ? "progress_activity" : "keep_off"}
        </span>
      </button>
    </li>
  );
}

/**
 * The row focus should move to once `removedId` is gone: the nearest one after
 * it in the list as it was, else the nearest before it — by identity, among
 * the rows actually rendered now. Positions are not identities: rows may have
 * been added, removed or reordered while the unpin was in flight.
 */
function focusSuccessor(
  before: readonly PinnedItem[],
  removedId: string,
  isRendered: (messageId: string) => boolean,
): string | undefined {
  const ids = before.map((pin) => pin.message.id);
  const at = ids.indexOf(removedId);
  const after = ids.slice(at + 1);
  const prior = ids.slice(0, Math.max(at, 0)).reverse();
  return [...after, ...prior].find(isRendered);
}

/**
 * Whether focus that went down with `owner`'s row is the section's to put
 * back: the reader asked to unpin that very row, and it has really left the
 * list — not merely scrolled out of a collapsed preview.
 */
function isRecoverable(
  owner: string | null,
  requested: ReadonlySet<string>,
  present: ReadonlySet<string>,
): owner is string {
  return owner !== null && requested.has(owner) && !present.has(owner);
}

/** Drops the unpin requests whose rows have left the list: they are settled. */
function forgetAbsent(requested: Set<string>, present: ReadonlySet<string>): void {
  for (const id of requested) {
    if (!present.has(id)) requested.delete(id);
  }
}

function refreshFailed(collection: PinsCollection): boolean {
  return collection.status === "ready" && collection.refreshFailed === true;
}

function sectionContent(
  pins: PinnedMessages,
  rows: (pin: PinnedItem) => ReactNode,
  empty: ReactNode,
): ExpandableSectionContent {
  const { collection } = pins;
  if (collection.status === "loading") {
    return { status: "loading", message: "Carregando mensagens fixadas…" };
  }
  if (collection.status === "error") {
    return { status: "error", message: "Não foi possível carregar as mensagens fixadas." };
  }
  return { status: "ready", items: collection.pins.map(rows), empty };
}

/**
 * The section. Its caller keys it by conversation, so expansion and the focus
 * bookkeeping below end with the conversation they were about.
 */
export default function PinnedMessagesSection({
  pins,
  emptyText,
}: {
  pins: PinnedMessages;
  emptyText: string;
}) {
  const openButtons = useRef(new Map<string, HTMLButtonElement>());
  const emptyRef = useRef<HTMLParagraphElement>(null);
  // Rows the reader asked to unpin, by message id — any number at once; each
  // leaves the set when it leaves the list. Asking is not owning focus.
  const requestedUnpins = useRef(new Set<string>());
  // The row that held focus when it was removed in the current commit, if any:
  // at most one row can. Written by that row's cleanup, read and cleared below.
  const removedWithFocus = useRef<string | null>(null);
  // The list as last rendered, to find what came after a row that is now gone.
  const previousPins = useRef<readonly PinnedItem[] | null>(null);
  const loaded = pins.collection.status === "ready" ? pins.collection.pins : null;

  const reportRemovedWithFocus = useCallback((messageId: string) => {
    removedWithFocus.current = messageId;
  }, []);

  // After every commit: if the row that took focus with it was one the reader
  // asked to unpin, and it really left the list, focus moves to its successor,
  // or to the empty state when nothing is left. Focus the reader had already
  // taken elsewhere — another control, or a click on plain text — was not in
  // the row when it went, so there is nothing to recover. The section is keyed
  // by conversation, so none of this outlives the conversation it is about.
  useLayoutEffect(() => {
    const owner = removedWithFocus.current;
    removedWithFocus.current = null;
    const before = previousPins.current;
    previousPins.current = loaded;
    if (!loaded || !before || loaded === before) return;
    const present = new Set(loaded.map((pin) => pin.message.id));
    const recover = isRecoverable(owner, requestedUnpins.current, present);
    forgetAbsent(requestedUnpins.current, present);
    if (!recover) return;
    const successor = focusSuccessor(before, owner, (id) => openButtons.current.has(id));
    const target = successor ? openButtons.current.get(successor) : emptyRef.current;
    target?.focus();
  });

  // A refused write ends its request: the row may still leave the list later,
  // but not because of this unpin, so that removal is not the section's to
  // follow with focus. An accepted write keeps it — even when the read meant
  // to confirm it failed, the unpin may well have happened. The outcome
  // settles before any further press can reach here, so a retry always
  // registers after the refused attempt has been cleared.
  function unpin(messageId: string) {
    requestedUnpins.current.add(messageId);
    void pins.onUnpin(messageId).then((outcome) => {
      if (outcome === "rejected") requestedUnpins.current.delete(messageId);
    });
  }

  const content = sectionContent(
    pins,
    (pin) => (
      <PinnedMessageRow
        key={pin.message.id}
        pin={pin}
        pending={pins.pendingIds.has(pin.message.id)}
        onNavigate={pins.onNavigate}
        onUnpin={unpin}
        onRemovedWithFocus={reportRemovedWithFocus}
        openRef={(element) => {
          if (element) openButtons.current.set(pin.message.id, element);
          else openButtons.current.delete(pin.message.id);
        }}
      />
    ),
    <p
      ref={emptyRef}
      className="chat-details__empty"
      tabIndex={-1}
      data-testid="chat-details-pin-empty"
    >
      {emptyText}
    </p>,
  );

  return (
    <ExpandableDetailsSection
      title="Mensagens fixadas"
      listLabel="Mensagens fixadas"
      content={content}
    >
      {/* A list is still shown, but the read meant to confirm it failed: said
          as an update failure, never as "no pins" and never as a failed unpin
          — the write may well have happened. */}
      {refreshFailed(pins.collection) && (
        <SectionMessage role="alert">
          Não foi possível atualizar as mensagens fixadas.
        </SectionMessage>
      )}
      {(pins.collection.status === "error" || refreshFailed(pins.collection)) && (
        <button type="button" className="chat-details__wide-action" onClick={pins.onRetry}>
          <span className="material-symbols-outlined" aria-hidden="true">
            refresh
          </span>
          Tentar novamente
        </button>
      )}
    </ExpandableDetailsSection>
  );
}
