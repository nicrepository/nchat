/**
 * Editing and deleting one message, as state the bubble can render.
 *
 * Split out of MessageBubble (issue #496, CQ follow-up): whether the editor is
 * open, whether a delete is in flight, and whether either action is offered at
 * all are one subject with one lifetime, and they were interleaved with layout
 * and link-safety rendering in a single component.
 */

import { useCallback, useState } from "react";

import type { Message } from "./chatTypes";
import type { CodecFormat } from "./tiptapSerializer";

export interface MessageEditingOptions {
  message: Message;
  /** True when the reader wrote this message: only an author edits or deletes. */
  isMine: boolean;
  /** RF-04: the edit window has closed, or the server refused a previous edit. */
  editDisabled: boolean;
  onEditMessage: (
    messageId: string,
    body: string,
    bodyFormat: Message["bodyFormat"],
  ) => Promise<Message>;
  onEditForbidden: (messageId: string) => void;
  onDeleteMessage: (messageId: string) => Promise<void>;
  /** Closing the toolbar as the editor opens keeps the two from overlapping. */
  onHideToolbar: () => void;
}

export interface MessageEditingState {
  editing: boolean;
  deleting: boolean;
  saveEdit: (body: string, format: CodecFormat) => Promise<Message>;
  cancelEdit: () => void;
  handleForbidden: () => void;
  /** Undefined when this reader may not edit right now, which hides the button. */
  startEdit: (() => void) | undefined;
  /** Undefined when this reader may not delete right now. */
  requestDelete: (() => void) | undefined;
}

export function useMessageEditing({
  message,
  isMine,
  editDisabled,
  onEditMessage,
  onEditForbidden,
  onDeleteMessage,
  onHideToolbar,
}: MessageEditingOptions): MessageEditingState {
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const saveEdit = useCallback(
    async (body: string, format: CodecFormat) => {
      const updated = await onEditMessage(message.id, body, format);
      setEditing(false);
      return updated;
    },
    [message.id, onEditMessage],
  );

  const cancelEdit = useCallback(() => setEditing(false), []);

  const handleForbidden = useCallback(() => {
    setEditing(false);
    onEditForbidden(message.id);
  }, [message.id, onEditForbidden]);

  const beginEdit = useCallback(() => {
    setEditing(true);
    onHideToolbar();
  }, [onHideToolbar]);

  const runDelete = useCallback(async () => {
    if (deleting || !window.confirm("Excluir esta mensagem?")) return;
    setDeleting(true);
    try {
      await onDeleteMessage(message.id);
    } catch {
      // The message is still there, so the button has to come back: leaving it
      // busy would strand the reader with an action they cannot retry.
      setDeleting(false);
    }
  }, [deleting, message.id, onDeleteMessage]);

  const canEdit = isMine && !editDisabled && !editing;
  // A system event is something that happened *to* the conversation; there is
  // nobody whose message it is to delete.
  const canDelete = isMine && message.kind === "user" && !editing;

  return {
    editing,
    deleting,
    saveEdit,
    cancelEdit,
    handleForbidden,
    startEdit: canEdit ? beginEdit : undefined,
    requestDelete: canDelete ? () => void runDelete() : undefined,
  };
}
