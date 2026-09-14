/**
 * ChatMessageArea — central message area rendered for /chat/channel/:id and /chat/dm/:id.
 *
 * A composition root (issue #834): it resolves the conversation, wires the
 * hooks that own each concern, and lays out the column. Everything it composes
 * lives under ./message-area — the header, the timeline and its viewport, the
 * notices, the pins, the dialogs — so reading this file tells you what a
 * conversation is made of, not how any one part of it works.
 *
 * Security invariants:
 * - Message text is rendered as React text nodes (no dangerouslySetInnerHTML).
 * - Line breaks preserved via CSS white-space: pre-wrap, never via HTML injection.
 * - Route :id is decoded via safeDecodeURIComponent before use and re-encoded on navigate.
 * - localStorage stores only allowlisted recent reaction emojis, scoped by user ID.
 * - No token, attachment, or voice recording is ever written to localStorage or
 *   sessionStorage. The one narrow exception (issue #769, reviewed for security):
 *   a draft's text and the id of the message it replies to are mirrored to
 *   sessionStorage, scoped by user, so an unsent draft survives an F5 — see
 *   chatDraftPersistence.ts.
 * - AbortController in useMessages cancels in-flight requests on target change or unmount.
 * - author_id is never sent; sender identity comes from the server-side JWT.
 * - ChatComposer is keyed by `${kind}:${targetId}`, so its TipTap instance, upload
 *   queue and voice recorder never leak between conversations by construction —
 *   but the *content* they hold now survives that remount via useConversationDrafts
 *   (issue #769), the same way #492's viewport anchors already survive it below.
 *
 * WebSocket realtime delivery:
 * Implemented — see useMessages and useChatWebSocket.
 * Auth uses Sec-WebSocket-Protocol to pass the Bearer token (browser WebSocket
 * upgrade cannot set custom headers; token-in-URL is rejected server-side).
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";

import "./ChatMessageArea.css";
import type { Message } from "./chatTypes";
import { fetchAllowedReactionEmojis } from "./chatApi";
import { usePendingReference } from "./usePendingReference";
import { useConversationTarget } from "./useConversationTarget";
import { useEmojiUsage } from "./emoji/useEmojiUsage";
import { useMessages, type SendResult } from "./useMessages";
import { useTypingIndicator } from "./useTypingIndicator";
import type { WSTypingUpdatedEvent } from "./useChatWebSocket";
import { usePins } from "./usePins";
import { selectLatestPin } from "./selectLatestPin";
import { useConversationDetailsPanel } from "./useConversationDetailsPanel";
import { useResourceCallBar } from "./useResourceCallBar";
import ConversationDetailsPanel from "./ConversationDetailsPanel";
import ChatComposer from "./ChatComposer";
import { noopConversationDrafts } from "./useConversationDrafts";
import { senderLabel } from "./messageDisplay";
import ConversationHeader from "./message-area/ConversationHeader";
import ConversationCallBars from "./message-area/ConversationCallBars";
import ConversationNotices from "./message-area/ConversationNotices";
import PinnedBar from "./message-area/PinnedBar";
import ConversationDialogs from "./message-area/dialogs/ConversationDialogs";
import ConversationTimeline from "./message-area/timeline/ConversationTimeline";
import { quickReactionEmojis } from "./message-area/conversationText";
import { directCallBar } from "./message-area/directCallBar";
import { useAuthorDM } from "./message-area/hooks/useAuthorDM";
import { useMessageDialogs } from "./message-area/hooks/useMessageDialogs";
import { useTypingIndicatorLabel } from "./message-area/hooks/useTypingIndicatorLabel";
import { useViewportAnchors } from "./message-area/hooks/useViewportAnchors";

interface ChatMessageAreaProps {
  kind: "channel" | "dm";
}

/** A repeat toggle of the same reaction inside this window is a double-fire. */
const reactionToggleDedupeMs = 300;

/**
 * Whether "conversar com o autor" applies here.
 *
 * Only where a message can be from someone this reader has no DM with yet: a
 * channel, or a group DM. In a 1:1 the author is already the conversation.
 */
function authorDMAction(
  currentUserId: string,
  kind: "channel" | "dm",
  activeDM: { type?: string } | undefined,
  openAuthorDM: (message: Message) => void,
) {
  if (!currentUserId) return undefined;
  if (kind === "channel" || activeDM?.type === "group") return openAuthorDM;
  return undefined;
}

export default function ChatMessageArea({ kind }: ChatMessageAreaProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const target = useConversationTarget(kind);
  const { ctx, targetId, focusMessageId, activeDM, resolvedName } = target;
  // Issue #769: falls back to a no-op store when the outlet context has not
  // reached AppShell's real one yet (mirrors emptyOutletContext), so a
  // screen rendered before that is ready never behaves as if drafts exist.
  const drafts = ctx.drafts ?? noopConversationDrafts;
  const pendingReference = usePendingReference(location.state, ctx.channels, ctx.dms);
  const [allowedReactionEmojis, setAllowedReactionEmojis] = useState<string[]>([]);
  const [editDisabledIds, setEditDisabledIds] = useState<Set<string>>(new Set());
  const lastReactionToggleRef = useRef({ key: "", at: 0 });
  const {
    usage: emojiUsage,
    remember: rememberReaction,
    changeTone: changeEmojiTone,
  } = useEmojiUsage(ctx.currentUserId);

  const dialogs = useMessageDialogs({ kind, targetId, navigate });
  const authorDM = useAuthorDM({
    currentUserId: ctx.currentUserId,
    kind,
    targetId,
    refreshConversations: ctx.refreshConversations,
    navigate,
  });
  const anchors = useViewportAnchors({
    kind,
    targetId,
    currentUserId: ctx.currentUserId,
    channels: ctx.channels,
    dms: ctx.dms,
    markRead: ctx.markRead,
  });

  const recentReactionEmojis = useMemo(
    () => quickReactionEmojis(emojiUsage, allowedReactionEmojis),
    [allowedReactionEmojis, emojiUsage],
  );
  // One object, so the composer's toolbar is not re-rendered by the identity of
  // its own props changing every frame.
  const composerEmoji = useMemo(
    () => ({ usage: emojiUsage, onToneChange: changeEmojiTone, onUsed: rememberReaction }),
    [changeEmojiTone, emojiUsage, rememberReaction],
  );

  const pinTarget = useMemo(() => (targetId ? { kind, id: targetId } : null), [kind, targetId]);
  const { pins, pinnedIds, error: pinError, togglePin, reload: reloadPins } = usePins(pinTarget);
  // One selector, one list, one result: the bar above the conversation and the
  // details panel are handed the same object, so a pin/unpin updates both at
  // once and neither can show a message the other does not.
  const latestPin = useMemo(() => selectLatestPin(pins), [pins]);

  const detailsToggleRef = useRef<HTMLButtonElement>(null);
  const details = useConversationDetailsPanel({
    kind,
    targetId,
    activeDM,
    resolvedName,
    toggleRef: detailsToggleRef,
  });
  const reloadOpenDetails = details.reload;

  // Typing indicator: useTypingIndicator needs sendTyping, which useMessages
  // only produces once called, but useMessages needs an onTypingUpdated
  // callback to hand inbound events to useTypingIndicator. Breaking that cycle
  // is the one job of this ref — a stable callback handed to useMessages that
  // indirects to whatever useTypingIndicator currently returns, kept current by
  // the layout effect below (same "ref holds the latest callback" shape as
  // every onXRef in useMessages itself).
  const typingHandleRemoteEventRef = useRef<(event: WSTypingUpdatedEvent) => void>(() => {});
  const handleTypingUpdatedFromMessages = useCallback((event: WSTypingUpdatedEvent) => {
    typingHandleRemoteEventRef.current(event);
  }, []);

  const {
    state,
    sendMessage,
    retry,
    loadMore,
    selectReply: selectReplyBase,
    cancelReply: cancelReplyBase,
    toggleReaction,
    sendTyping,
    toggleFavorite,
    acknowledgements,
    acknowledgingId,
    acknowledgeError,
    acknowledge,
    reconcileLinkSafety,
    editMessageLocal,
    deleteMessageLocal,
  } = useMessages({
    kind,
    targetId,
    bodyFormat: target.bodyFormat,
    currentUserId: ctx.currentUserId,
    focusMessageId,
    onOwnReactionConfirmed: rememberReaction,
    onPinUpdated: reloadPins,
    onTypingUpdated: handleTypingUpdatedFromMessages,
    // Someone added participants to the open conversation (issue #398). The
    // event names nobody, so the only correct response is to refetch — which is
    // also the same call the local add makes, so the two converge instead of
    // producing two different views of the roster.
    //
    // Passed directly: useMessages holds this callback in a ref, so a new
    // identity each render does not restart the socket or its subscriptions.
    onMembersAdded: reloadOpenDetails,
    // An attachment's malware verdict landed (RF-22). The same treatment as
    // members.added and for the same reason: the event says which row changed,
    // not what the list should now look like, so the panel refetches and the
    // server stays the single authority on every attachment's status.
    //
    // Refetching rather than patching is also what makes the event and the
    // panel's own reconciliation poll safe together. Both end in one
    // `files_ready` carrying a whole list, so two of them arriving at once
    // cannot duplicate a row, and neither can write a status the server did not
    // just report. A missed event costs nothing: the poll, a reopen, or a
    // reload all recover the current state, because the persisted status is the
    // source of truth and this is only a hint that it moved.
    onAttachmentStatus: reloadOpenDetails,
    onMessageRemoved: reloadPins,
  });

  // Issue #769, "REPLY CONTEXT": selectReply/cancelReply already own the
  // *live* reply used for sending — these two wrappers are the only place
  // that also mirrors it into the conversation's draft, so it comes back
  // after a conversation switch the same way the text does.
  const selectReply = useCallback(
    (message: Message) => {
      selectReplyBase(message);
      if (anchors.conversationKey) drafts.setReply(anchors.conversationKey, message.id);
    },
    [selectReplyBase, drafts, anchors.conversationKey],
  );
  const cancelReply = useCallback(() => {
    cancelReplyBase();
    if (anchors.conversationKey) drafts.setReply(anchors.conversationKey, null);
  }, [cancelReplyBase, drafts, anchors.conversationKey]);

  // Issue #769: historyReducer.applyLoaded unconditionally resets replyTo on
  // every initial load, i.e. on every conversation switch (#492 review) —
  // correct for a composer that used to lose its own draft the same way,
  // wrong now that the reply is supposed to survive one. Restoring it here,
  // once the messages a reply target could be found in have actually
  // loaded, keeps that reset (nothing else here needs to know this ever
  // happened) while still bringing the reply back for the reader.
  //
  // A reply whose message is not in the loaded page — deleted, or simply
  // outside it — is dropped rather than guessed at: RF says "não apagar
  // texto", not "restore at any cost", and a dangling replyTo the server
  // would reject on send is worse than none.
  useEffect(() => {
    if (state.status !== "ready" || state.replyTo || !anchors.conversationKey) return;
    const draftReplyId = drafts.getDraft(anchors.conversationKey)?.replyToMessageId;
    if (!draftReplyId) return;
    const message = state.messages.find((m) => m.id === draftReplyId);
    if (message) {
      selectReplyBase(message);
    } else {
      drafts.setReply(anchors.conversationKey, null);
    }
    // Runs once per conversation becoming ready, not on every message-list
    // change (e.g. a realtime append must not re-trigger this).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.status, anchors.conversationKey]);

  const typing = useTypingIndicator({
    kind,
    targetId,
    currentUserId: ctx.currentUserId,
    sendTyping,
    // Never start a typing session while the composer itself would refuse one —
    // matches the disabled prop ChatComposer already receives below.
    disabled: state.status !== "ready",
  });
  useLayoutEffect(() => {
    typingHandleRemoteEventRef.current = typing.handleRemoteEvent;
  });
  // Destructured so the callback below depends on these specific, stable
  // functions rather than the whole `typing` object, whose identity changes
  // every render.
  const { notifyActivity: typingNotifyActivity, stop: typingStop } = typing;
  const typingIndicatorLabel = useTypingIndicatorLabel({
    kind,
    activeDM,
    messages: state.messages,
    typingUserIds: typing.typingUserIds,
    typingDisplayNameByUserId: typing.typingDisplayNameByUserId,
  });

  // A content-changing edit starts/renews typing; the composer being emptied
  // stops it immediately rather than waiting out the inactivity timer.
  const handleComposerActivity = useCallback(
    (hasContent: boolean) => {
      if (hasContent) typingNotifyActivity();
      else typingStop();
    },
    [typingNotifyActivity, typingStop],
  );

  useEffect(() => {
    let active = true;
    fetchAllowedReactionEmojis().then(
      (emojis) => {
        if (active) setAllowedReactionEmojis(emojis);
      },
      () => {
        // Message rendering remains available if optional reaction config fails.
      },
    );
    return () => {
      active = false;
    };
  }, []);

  const handleSend = useCallback(
    async (
      body: string,
      attachmentIds?: string[],
      acknowledgementRequired?: boolean,
    ): Promise<SendResult> => {
      const result = await sendMessage(
        body,
        pendingReference.messageId || undefined,
        attachmentIds,
        acknowledgementRequired,
      );
      if (result.status === "sent") {
        // Sending is itself the clearest possible "stopped typing" signal — do
        // not wait for the composer-cleared activity event or the inactivity
        // timeout to catch up.
        typingStop();
        // Mirrors applySent's own replyTo: null (issue #769) — the reply
        // this message answered is consumed, in the draft as much as in
        // the live reducer state. Only when there actually was one: an
        // unconditional setReply(null) would bump the draft's revision on
        // every single send, even a plain one with no reply — and the
        // send-vs-edit-race guard in ChatComposer (issue #769, "ACK
        // ATRASADO") would then read that as "the reader changed something
        // since submitting" and leave the just-sent text sitting in the
        // editor instead of clearing it.
        if (anchors.conversationKey && drafts.getDraft(anchors.conversationKey)?.replyToMessageId) {
          drafts.setReply(anchors.conversationKey, null);
        }
        navigate(`${location.pathname}${location.search}`, { replace: true, state: null });
      }
      return result;
    },
    [
      anchors.conversationKey,
      drafts,
      location.pathname,
      location.search,
      navigate,
      pendingReference.messageId,
      sendMessage,
      typingStop,
    ],
  );

  const jumpToReference = useCallback(
    (reference: NonNullable<Message["reference"]>) => {
      if (!reference.available) return;
      navigate(
        `/chat/${reference.targetType}/${encodeURIComponent(reference.targetId)}?message=${encodeURIComponent(reference.messageId)}`,
      );
    },
    [navigate],
  );

  const replyPreview = useMemo(
    () =>
      state.replyTo
        ? {
            authorLabel: senderLabel(state.replyTo),
            bodyText: state.replyTo.bodyText,
            bodyFormat: state.replyTo.bodyFormat,
            isRemoved: state.replyTo.isRemoved,
          }
        : null,
    [state.replyTo],
  );

  /**
   * Toggle a reaction, whatever the reader touched to ask for it.
   *
   * Nothing is checked against the emoji catalog here. Every value that reaches
   * this comes from a control this UI drew — the server's quick row, an entry
   * picked out of the loaded catalog, or a reaction the server already returned
   * on the message — and the server validates the sequence again on the toggle.
   * The catalog is loaded lazily with the picker, so asking it was really asking
   * whether the picker had been opened yet: an existing reaction someone else
   * made was refused until it had, and accepted afterwards (issue #496).
   */
  const handleToggleReaction = useCallback(
    (messageId: string, emoji: string) => {
      const key = `${messageId}\u0000${emoji}`;
      const now = Date.now();
      const last = lastReactionToggleRef.current;
      if (last.key === key && now - last.at < reactionToggleDedupeMs) return;
      lastReactionToggleRef.current = { key, at: now };
      toggleReaction(messageId, emoji);
    },
    [toggleReaction],
  );

  const handleEditForbidden = useCallback(
    (messageId: string) => {
      setEditDisabledIds((current) => new Set(current).add(messageId));
      retry();
    },
    [retry],
  );

  const resourceCall = useResourceCallBar({
    kind,
    targetId,
    resolvedName,
    detailsKind: details.detailsKind,
    ctx,
  });

  const clearPendingReference = useCallback(
    () => navigate(`${location.pathname}${location.search}`, { replace: true, state: null }),
    [location.pathname, location.search, navigate],
  );

  // One object rather than a dozen props: the timeline hands every one of these
  // straight down to a message, and none of them means anything on its own here.
  const messageActions = {
    onToggleReaction: handleToggleReaction,
    onReplyMessage: selectReply,
    onReferenceMessage: dialogs.openReference,
    onForwardMessage: dialogs.openForward,
    onReferenceJump: jumpToReference,
    onOpenAuthorDM: authorDMAction(ctx.currentUserId, kind, activeDM, authorDM.openAuthorDM),
    onToggleFavorite: toggleFavorite,
    onReconcileLinkSafety: reconcileLinkSafety,
    onEditMessage: editMessageLocal,
    onEditForbidden: handleEditForbidden,
    onDeleteMessage: deleteMessageLocal,
    onTogglePin: togglePin,
    onAcknowledge: acknowledge,
  };

  const directCallBarProps = directCallBar(kind, ctx.directCallSession, activeDM?.counterpart);

  return (
    <div
      className={`chat-msg-area${details.showDetails ? " chat-msg-area--with-details" : ""}`}
      data-testid="chat-message-area"
    >
      {/*
        The conversation column is always rendered, never conditionally, so the
        panel below is a trailing sibling: adding or removing it leaves this
        entire subtree — message list, composer, scroll container — reconciled in
        place rather than remounted.
      */}
      <div className="chat-msg-area__conversation">
        <ConversationHeader
          kind={kind}
          name={resolvedName}
          counterpart={activeDM?.counterpart}
          presenceTarget={target.presenceTarget}
          // #673: once the direct call bar takes over presentation for this DM,
          // the header suppresses its own call actions — the same pattern #657
          // established for the resource call header action.
          onStartCall={directCallBarProps ? undefined : ctx.startCall}
          resourceCall={resourceCall.headerState}
          details={details}
          detailsToggleRef={detailsToggleRef}
        />

        <ConversationCallBars
          resourceCall={resourceCall.barProps}
          directCall={directCallBarProps}
        />

        <PinnedBar pin={latestPin} onUnpin={togglePin} />

        <ConversationTimeline
          kind={kind}
          targetId={targetId}
          name={resolvedName}
          detailsKind={details.detailsKind}
          mentionTarget={target.mentionTarget}
          state={state}
          currentUserId={ctx.currentUserId}
          actions={messageActions}
          onLoadMore={loadMore}
          onRetry={retry}
          editDisabledIds={editDisabledIds}
          pinnedIds={pinnedIds}
          openingAuthorDMIds={authorDM.openingAuthorDMIds}
          acknowledgements={acknowledgements}
          acknowledgingId={acknowledgingId}
          recentReactionEmojis={recentReactionEmojis}
          emojiUsage={emojiUsage}
          onEmojiToneChange={changeEmojiTone}
          focusMessageId={focusMessageId}
          conversationKey={anchors.conversationKey}
          unreadCountAtOpen={anchors.unreadCountAtOpen}
          initialAnchor={anchors.initialAnchor}
          onCaptureAnchor={anchors.onCaptureAnchor}
          onReachedBottom={anchors.onReachedBottom}
        />

        <ConversationNotices
          sendError={state.sendError}
          realtimeError={state.realtimeError}
          actionError={state.actionError}
          openDMError={authorDM.openDMError}
          pinError={pinError}
          acknowledgeError={acknowledgeError}
          typingLabel={typingIndicatorLabel}
        />

        {/*
        The composer is keyed by the conversation identity so switching targets
        destroys the TipTap instance and mounts a fresh one, rather than one
        editor silently carrying content from channel A into channel B (both
        are bodyFormat "v3", so useEditor would otherwise keep the same
        instance) and the send button posting it to the wrong conversation.
        That isolation is still exactly why the key exists (issue #769
        review) — what changed is that the content is no longer thrown away
        on the way out: `drafts` (an AppShell-level store, keyed the same
        way, unaffected by this remount) is what the fresh instance below
        seeds itself from and writes back into, so the same content comes
        back on returning to this target instead of finding it gone.
      */}
        <ChatComposer
          key={`${kind}:${targetId}`}
          mentionTarget={target.mentionTarget}
          bodyFormat={target.bodyFormat}
          placeholder={target.composerPlaceholder}
          disabled={state.status !== "ready"}
          replyPreview={replyPreview}
          onCancelReply={cancelReply}
          drafts={drafts}
          referencePreview={pendingReference.preview}
          referenceTargetLabel={pendingReference.originLabel}
          onCancelReference={clearPendingReference}
          onSend={handleSend}
          onActivity={handleComposerActivity}
          // The composer's emoji button opens the same picker the reactions use,
          // over the same history — one emoji experience in one product (#496).
          emoji={composerEmoji}
          // RF-32 (issue #458): the same composer serves channels and DMs, so
          // one target prop covers both. It is the route's own kind and id —
          // the very pair the composer is keyed by — so an attachment can never
          // be posted to the destination the user just navigated away from.
          uploadTarget={target.uploadTarget}
          attachmentLimits={ctx.attachmentLimits}
          // An upload adds a file to the destination without creating a message
          // — the message comes later, when the user presses Enviar (RF-32) —
          // so the details panel's file list still has to reconcile here.
          onAttachmentUploaded={reloadOpenDetails}
        />
        <ConversationDialogs
          kind={kind}
          targetId={targetId}
          channels={ctx.channels}
          dms={ctx.dms}
          referenceSource={dialogs.referenceSource}
          forwardSource={dialogs.forwardSource}
          onCloseReference={dialogs.closeReference}
          onSelectReferenceDestination={dialogs.selectReferenceDestination}
          onCloseForward={dialogs.closeForward}
        />
      </div>

      {details.showDetails && (
        <ConversationDetailsPanel
          kind={details.detailsKind ?? "channel"}
          state={details.detailsState}
          currentUserId={ctx.currentUserId}
          latestPin={latestPin}
          onClose={details.close}
        />
      )}
    </div>
  );
}
