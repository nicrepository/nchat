/**
 * ConversationNameField — the conversation's name in the details panel, and
 * the inline rename it offers when the server said this caller may (issue #893).
 *
 * One control with two states rather than a modal: reading, the name and a
 * pencil; editing, a field with an explicit confirm and cancel. The dialog the
 * sidebar opens (RenameChannelDialog, #527) stays exactly as it was — this is
 * an additional affordance over the same operation, and the rules both obey
 * live in conversationRename.ts rather than in either of them.
 *
 * What this component does *not* own is the name. `name` is the persisted
 * value from the details payload and is the only thing ever rendered as the
 * conversation's name; the draft is editor state and never leaves this
 * component except as the argument of one confirmed request. So a rename that
 * is refused, abandoned, or still in flight cannot make any surface — this
 * panel, the header or the sidebar — show something the server did not accept.
 *
 * It does not reconcile either, and deliberately (CQ-893-03). A confirmed
 * rename refreshes the canonical sidebar list, useReloadOnRename sees the name
 * move under the same target, and the panel refetches its own projection. This
 * component used to *also* ask the panel to reload on success, which meant one
 * local rename cost two GET /details — and did nothing at all for a rename by
 * someone else. One trigger, "the canonical name of this target moved", now
 * covers a local rename, a remote one and a reconnect alike.
 *
 * Security invariants:
 * - `onRename` being absent is the whole reason a control is missing, and it
 *   is never a security boundary: the host omits it for a 1:1, for the general
 *   channel and for a channel the server did not authorize, and PATCH
 *   re-derives all three from the session regardless.
 * - The name is a React text node, typed and rendered. No markup, no URL.
 * - A refusal is rendered from its status code only; the server's message is
 *   never surfaced and nothing is logged.
 */

import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";

import {
  type ConversationRenameAction,
  type ConversationRenameKind,
  conversationRenameCopy,
  renameErrorMessage,
  renameNameRefusal,
} from "./conversationRename";

interface ConversationNameFieldProps {
  kind: ConversationRenameKind;
  /** The persisted name. Never a draft, and never written to from here. */
  name: string;
  /** Present only when this caller may rename this target; see the module note. */
  onRename?: ConversationRenameAction;
}

export default function ConversationNameField({
  kind,
  name,
  onRename,
}: ConversationNameFieldProps) {
  const copy = conversationRenameCopy[kind];
  // Unique per instance: two panels can be mounted at once — the one the
  // header opens and the one a sidebar row opens — and a shared id would make
  // aria-describedby point at the other one's message.
  const fieldId = useId();
  const errorId = `${fieldId}-error`;
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const editButtonRef = useRef<HTMLButtonElement>(null);
  // `pending` lands a render later, so it cannot stop a second submit fired in
  // the same tick — a double click, or Enter and a click together. This ref is
  // what actually makes submit single, as it does in RenameChannelDialog.
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);
  // Only a close the user asked for restores focus; the first render must not
  // steal it from wherever the panel put it.
  const restoreFocusRef = useRef(false);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (editing) {
      // The common gesture is replacing the name; the next is editing it.
      inputRef.current?.focus();
      inputRef.current?.select();
      return;
    }
    if (!restoreFocusRef.current) return;
    restoreFocusRef.current = false;
    editButtonRef.current?.focus();
  }, [editing]);

  function beginEdit() {
    setDraft(name);
    setError("");
    setEditing(true);
  }

  function leaveEdit() {
    restoreFocusRef.current = true;
    setEditing(false);
    setError("");
  }

  function cancelEdit() {
    // Leaving mid-write would hide the outcome of a request already sent.
    if (submittingRef.current) return;
    leaveEdit();
  }

  async function runRename(trimmed: string, rename: ConversationRenameAction) {
    submittingRef.current = true;
    setPending(true);
    setError("");
    try {
      await rename(trimmed);
      // A switch of conversation unmounts this editor, and the mutation for
      // the previous target legitimately completes afterwards. Nothing it
      // returns may touch the editor the next conversation now owns.
      if (!mountedRef.current) return;
      // Closing is all this does. The panel converges on its own once the
      // canonical name moves — see the module note — so the typed value is
      // never adopted here and nothing is refetched twice.
      leaveEdit();
    } catch (failure) {
      // Recoverable: the editor stays open with the typed name intact, and the
      // persisted name keeps being what every other surface shows.
      if (!mountedRef.current) return;
      setError(renameErrorMessage(kind, failure));
      inputRef.current?.focus();
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  function submit() {
    if (submittingRef.current || !onRename) return;
    const trimmed = draft.trim();
    // The verdicts the user can reach without the server: nothing typed, or
    // past the domain's cap — counted in code points, the unit the backend
    // counts in. The typed value is never altered to fit; it stays in the
    // field with the reason beside it. Every other rule is still the server's.
    const refusal = renameNameRefusal(kind, trimmed);
    if (refusal) {
      setError(refusal);
      inputRef.current?.focus();
      return;
    }
    // Nothing to persist: a request that would rewrite the same name is a
    // write with no change, so the editor simply closes.
    if (trimmed === name) {
      leaveEdit();
      return;
    }
    void runRename(trimmed, onRename);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      // The panel closes on Escape too, and this one is about the editor.
      event.stopPropagation();
      cancelEdit();
      return;
    }
    if (event.key !== "Enter") return;
    // A held key repeats, and an IME's Enter commits a candidate rather than
    // the name — neither is a second request.
    if (event.repeat || event.nativeEvent.isComposing) return;
    event.preventDefault();
    submit();
  }

  if (!editing) {
    return (
      <div className="chat-details__name-row">
        <p className="chat-details__name" data-testid={copy.testId}>
          {name || copy.unnamed}
        </p>
        {onRename && (
          <button
            ref={editButtonRef}
            type="button"
            className="chat-details__name-action"
            aria-label={copy.editAction}
            title={copy.editAction}
            onClick={beginEdit}
            data-testid="chat-details-rename"
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              edit
            </span>
          </button>
        )}
      </div>
    );
  }

  return (
    <div className="chat-details__name-edit">
      <div className="chat-details__name-row">
        <input
          ref={inputRef}
          id={fieldId}
          className="chat-details__name-input"
          type="text"
          autoComplete="off"
          aria-label={copy.field}
          /*
            No `maxLength`: it counts UTF-16 code units and the domain counts
            code points, so any number here would refuse names the backend
            accepts — a 100-emoji channel name is 200 units and perfectly
            valid. The cap is enforced on submit instead, in the right unit.
          */
          value={draft}
          /*
            readOnly rather than disabled: a disabled input leaves the tab
            order, which would drop focus to <body> the moment the user
            confirms — mid-write, with nothing to come back to. readOnly keeps
            the field focused and announced while refusing the edit.
          */
          readOnly={pending}
          aria-busy={pending || undefined}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errorId : undefined}
          onChange={(event) => {
            setDraft(event.target.value);
            setError("");
          }}
          onKeyDown={handleKeyDown}
        />
        {/*
          Confirm is disabled while a write is in flight and cancel is not: the
          guard that makes submit single is the ref above, and leaving a user
          unable to dismiss an editor whose request has stalled would be the
          worse failure. cancelEdit refuses while submitting, so the control is
          reachable and announced without ever producing an ambiguous state.
        */}
        <button
          type="button"
          className="chat-details__name-action chat-details__name-action--confirm"
          aria-label={copy.confirm}
          title={copy.confirm}
          disabled={pending}
          aria-busy={pending || undefined}
          onClick={submit}
          data-testid="chat-details-rename-confirm"
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            check
          </span>
        </button>
        <button
          type="button"
          className="chat-details__name-action"
          aria-label={copy.cancel}
          title={copy.cancel}
          onClick={cancelEdit}
          data-testid="chat-details-rename-cancel"
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            close
          </span>
        </button>
      </div>
      {error && (
        <p id={errorId} className="chat-details__name-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
