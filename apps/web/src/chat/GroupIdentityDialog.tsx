/**
 * GroupIdentityDialog — "Alterar identidade" for an existing group (#1026).
 *
 * Opened from the group row's action menu. The shell (portal, backdrop,
 * role="dialog", Escape, focus trap, single submit) mirrors RenameChannelDialog
 * rather than introducing another modal system; the body is the same
 * GroupAvatarPicker the creation flow uses.
 *
 * Choosing Automático and saving removes the persisted emoji — an explicit
 * DELETE, never a blank value. Nothing is written until "Salvar", and the
 * sidebar shows the new identity only after the server confirmed it and the
 * canonical list was refetched. Authority and the emoji's validity are the
 * server's: a refusal is shown, never assumed away.
 */

import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./RenameChannelDialog.css";
import { ApiRequestError } from "../lib/api";
import GroupAvatarPicker from "./GroupAvatarPicker";
import {
  type GroupIdentity,
  identityIncomplete,
  identityOf,
  persistedEmoji,
} from "./groupIdentity";

const titleId = "chat-group-identity-title";
const errorId = "chat-group-identity-error";
const IDENTITY_ERRORS: Readonly<Record<number, string>> = {
  400: "Escolha um emoji válido.",
  403: "Você não pode alterar a identidade deste grupo.",
  404: "Este grupo não está mais disponível.",
  429: "Muitas alterações em sequência. Aguarde e tente novamente.",
  0: "Sem conexão. Verifique sua rede e tente novamente.",
};

function identityErrorMessage(error: unknown): string {
  const known = error instanceof ApiRequestError ? IDENTITY_ERRORS[error.status] : undefined;
  return known ?? "Não foi possível alterar a identidade. Tente novamente.";
}

/**
 * Whether Tab can land on this element. Enabled is not enough: a radio group
 * is a single stop — its checked radio — and the emoji grid is a roving widget
 * that exposes only its tabIndex=0 cell.
 */
function isTabStop(element: HTMLElement): boolean {
  if (!element.matches("button, input, [tabindex]") || element.matches(":disabled")) return false;
  if (element.tabIndex < 0) return false;
  return !(element instanceof HTMLInputElement && element.type === "radio" && !element.checked);
}

/** The dialog's Tab stops in document order — the order the browser tabs in. */
function tabStops(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>("*")).filter(isTabStop);
}

/** Where Tab must wrap to when focus sits on the edge it would leave by. */
function wrapTarget(
  stops: HTMLElement[],
  active: Element | null,
  backwards: boolean,
): HTMLElement | undefined {
  const edge = backwards ? stops[0] : stops[stops.length - 1];
  if (!edge || active !== edge) return undefined;
  return backwards ? stops[stops.length - 1] : stops[0];
}

interface GroupIdentityDialogProps {
  groupId: string;
  name: string;
  avatarEmoji: string | undefined;
  currentUserId: string;
  onClose: () => void;
  /** Resolves once persisted; `undefined` returns the group to Automático. */
  onSave: (groupId: string, emoji: string | undefined) => Promise<void>;
}

export default function GroupIdentityDialog({
  groupId,
  name,
  avatarEmoji,
  currentUserId,
  onClose,
  onSave,
}: GroupIdentityDialogProps) {
  const [identity, setIdentity] = useState<GroupIdentity>(() => identityOf(avatarEmoji));
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  // `pending` cannot stop a second submit in the same tick; this ref does.
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);
  const unchanged = persistedEmoji(identity) === avatarEmoji;

  useEffect(() => {
    mountedRef.current = true;
    dialogRef.current?.querySelector<HTMLInputElement>("input:checked")?.focus();
    return () => {
      mountedRef.current = false;
    };
  }, []);

  function requestClose() {
    if (!submittingRef.current) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab" || !dialogRef.current) return;
    const target = wrapTarget(tabStops(dialogRef.current), document.activeElement, event.shiftKey);
    if (!target) return;
    event.preventDefault();
    target.focus();
  }

  async function save() {
    if (submittingRef.current) return;
    submittingRef.current = true;
    setPending(true);
    setError("");
    try {
      await onSave(groupId, persistedEmoji(identity));
      if (mountedRef.current) onClose();
    } catch (failure) {
      if (mountedRef.current) setError(identityErrorMessage(failure));
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  return createPortal(
    <div className="rename-channel__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="rename-channel group-identity-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={error ? errorId : undefined}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="rename-channel__title">
          Identidade do grupo
        </h2>
        <GroupAvatarPicker
          name={name}
          identity={identity}
          currentUserId={currentUserId}
          disabled={pending}
          onChange={(next) => {
            setIdentity(next);
            setError("");
          }}
        />
        {error && (
          <p id={errorId} className="rename-channel__error" role="alert">
            {error}
          </p>
        )}
        <div className="rename-channel__actions">
          <button
            type="button"
            className="rename-channel__cancel"
            disabled={pending}
            onClick={requestClose}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="rename-channel__submit"
            disabled={pending || unchanged || identityIncomplete(identity)}
            aria-busy={pending}
            onClick={() => void save()}
          >
            {pending ? "Salvando…" : "Salvar"}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
