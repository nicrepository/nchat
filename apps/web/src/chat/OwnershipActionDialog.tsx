import { useEffect, useId, useRef, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import type { OwnershipDetails } from "./ownershipApi";
import { type OwnershipAction as Action, roleLabels } from "./ownershipPresentation";
import { ownershipCopy as copy } from "./ownershipDialogState";
import { useOwnershipSubmit } from "./useOwnershipSubmit";
import OwnershipTargetPicker, { OwnershipPerson } from "./OwnershipTargetPicker";
import { useOwnershipSelection } from "./useOwnershipSelection";
import type { OwnershipSubmitState } from "./ownershipDialogState";
import "./OwnershipRoster.css";

export interface OwnershipDialogContext {
  kind: "channel" | "group";
  id: string;
  ownership: OwnershipDetails;
  currentUserId: string;
  workspaceId?: string;
  projectionStatus?: "ready" | "error";
  reload: () => void;
  onCommitted?: (message: string) => void;
}

function title(action: Action) {
  if (action.type === "role") return `Tornar ${roleLabels[action.role].toLowerCase()}`;
  return action.type === "transfer" ? "Transferir minha propriedade" : copy.leave;
}

function confirmLabel(action: Action, ownership: OwnershipDetails) {
  if (action.type === "role") return "Confirmar";
  if (action.type === "transfer") return copy.transfer;
  return ownership.leavePreview.lastOwner && ownership.members.length > 1
    ? copy.leaveTransfer
    : copy.leave;
}

export default function OwnershipActionDialog({
  action,
  props,
  onClose,
}: {
  action: Action;
  props: OwnershipDialogContext;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const ids = useId();
  function finishClose() {
    dialog.current?.close();
    onClose();
  }
  const flow = useOwnershipSubmit(action, props, finishClose);
  const selection = useOwnershipSelection(action, props, flow.state, flow.newIntent);
  const { busy, refreshing, canSubmit, needsTarget, target, actorRole } = selection;
  const errorId = "error" in flow.state ? `${ids}-error` : undefined;
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    cancel.current?.focus({ preventScroll: true });
    return () => element?.close();
  }, []);
  function close() {
    if (!flow.pending.current) finishClose();
  }
  function submit() {
    if (!canSubmit) return;
    void flow.submit({ target: needsTarget ? target : "", actorRole });
  }
  return createPortal(
    <dialog
      ref={dialog}
      className="ownership-dialog chat-theme"
      data-action={action.type}
      aria-labelledby={`${ids}-title`}
      aria-describedby={`${ids}-description`}
      aria-modal="true"
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      onKeyDown={trapDialogFocus}
    >
      <div className="ownership-dialog__heading">
        <span className="ownership-dialog__icon material-symbols-outlined" aria-hidden="true">
          {ownershipActionIcon(action)}
        </span>
        <div>
          <h3 id={`${ids}-title`}>{title(action)}</h3>
          <span className="ownership-dialog__subtitle">Responsabilidade da conversa</span>
        </div>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
        aria-describedby={errorId}
        aria-busy={busy || refreshing}
      >
        <OwnershipFields action={action} props={props} selection={selection} ids={ids} />
        <OwnershipNotices
          state={flow.state}
          unavailable={selection.unavailable}
          ids={ids}
          reload={props.reload}
        />
        <div className="ownership-dialog__buttons">
          <button ref={cancel} type="button" autoFocus disabled={busy} onClick={close}>
            {copy.cancel}
          </button>
          <button
            type="submit"
            className={`ownership-dialog__confirm${action.type === "leave" ? " ownership-dialog__confirm--danger" : ""}`}
            disabled={!canSubmit}
            aria-describedby={errorId}
          >
            {busy ? copy.submitting : confirmLabel(action, props.ownership)}
          </button>
        </div>
      </form>
    </dialog>,
    document.body,
  );
}

function OwnershipFields({
  action,
  props,
  selection,
  ids,
}: {
  action: Action;
  props: OwnershipDialogContext;
  selection: ReturnType<typeof useOwnershipSelection>;
  ids: string;
}) {
  const workspaceId = props.workspaceId ?? "";
  return (
    <>
      <div id={`${ids}-description`}>
        <OwnershipRoleDescription action={action} />
        {action.type === "transfer" && (
          <p>
            O participante escolhido se torna proprietário. Você deixa de ser proprietário e assume
            o papel escolhido.
          </p>
        )}
        {action.type === "leave" && (
          <OwnershipLeaveFields
            ownership={props.ownership}
            successor={selection.successor}
            manual={selection.manual}
            selected={selection.selected}
            workspaceId={workspaceId}
          />
        )}
      </div>
      <OwnershipManualChoice action={action} props={props} selection={selection} />
      {selection.needsTarget && (
        <OwnershipTargetPicker
          candidates={selection.candidates}
          target={selection.selected?.userId ?? ""}
          onChange={selection.changeTarget}
          disabled={selection.fieldsDisabled}
          workspaceId={workspaceId}
        />
      )}
      {action.type === "transfer" && <OwnershipActorRole ids={ids} selection={selection} />}
    </>
  );
}
function OwnershipManualChoice({
  action,
  props,
  selection,
}: {
  action: Action;
  props: OwnershipDialogContext;
  selection: ReturnType<typeof useOwnershipSelection>;
}) {
  if (
    action.type !== "leave" ||
    !props.ownership.leavePreview.lastOwner ||
    props.ownership.members.length <= 1
  )
    return null;
  return (
    <button
      className="ownership-dialog__secondary"
      type="button"
      disabled={selection.fieldsDisabled}
      onClick={selection.toggleManual}
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        swap_horiz
      </span>
      {selection.manual ? copy.automatic : copy.chooseAnother}
    </button>
  );
}
function OwnershipActorRole({
  ids,
  selection,
}: {
  ids: string;
  selection: ReturnType<typeof useOwnershipSelection>;
}) {
  return (
    <fieldset className="ownership-dialog__roles" disabled={selection.fieldsDisabled}>
      <legend>{copy.actorRole}</legend>
      {(["admin", "member"] as const).map((role) => (
        <label key={role}>
          <input
            type="radio"
            aria-label={roleLabels[role]}
            aria-describedby={`${ids}-role-${role}`}
            name={`${ids}-role`}
            checked={selection.actorRole === role}
            onChange={() => selection.changeRole(role)}
          />
          <span>
            <strong>{roleLabels[role]}</strong>
            <small id={`${ids}-role-${role}`}>{actorRoleHints[role]}</small>
          </span>
        </label>
      ))}
    </fieldset>
  );
}
function OwnershipNotices({
  state,
  unavailable,
  ids,
  reload,
}: {
  state: OwnershipSubmitState;
  unavailable: boolean;
  ids: string;
  reload: () => void;
}) {
  const error = "error" in state ? state.error : "";
  const refreshing = state.phase === "conflict";
  return (
    <>
      {error && (
        <p id={`${ids}-error`} className="ownership-dialog__error" role="alert">
          {error}
        </p>
      )}
      {refreshing && !unavailable && <p role="status">{copy.refreshing}</p>}
      {unavailable && <p role="alert">{copy.refreshError}</p>}
      {(refreshing || unavailable) && (
        <button className="ownership-dialog__secondary" type="button" onClick={reload}>
          {copy.refresh}
        </button>
      )}
    </>
  );
}

function OwnershipLeaveFields({
  ownership,
  successor,
  manual,
  selected,
  workspaceId,
}: {
  ownership: OwnershipDetails;
  successor?: OwnershipDetails["members"][number];
  manual: boolean;
  selected?: OwnershipDetails["members"][number];
  workspaceId: string;
}) {
  const preview = ownership.leavePreview;
  if (!preview.lastOwner) return <p>Você deixará de participar da conversa.</p>;
  if (ownership.members.length === 1 && !preview.blocked) return <p>{copy.empty}</p>;
  const person = manual ? selected : successor;
  return (
    <>
      <p className="ownership-dialog__consequence">{copy.lastOwner}</p>
      {manual ? (
        <p>O proprietário escolhido assumirá a responsabilidade quando você sair.</p>
      ) : (
        <p>
          {successor
            ? `Se sair, ${successor.displayName} será promovido automaticamente.`
            : copy.blocked}
        </p>
      )}
      {person && (
        <div className="ownership-dialog__successor">
          <span>{copy.target}</span>
          <OwnershipPerson member={person} workspaceId={workspaceId} />
          <span className="ownership-dialog__successor-note">
            <span className="material-symbols-outlined" aria-hidden="true">
              key
            </span>
            Assumirá a propriedade quando você sair
          </span>
        </div>
      )}
    </>
  );
}

const roleDescriptions = {
  owner:
    "Poderá gerenciar papéis, remover participantes e transferir a propriedade da conversa. Você mantém seu papel atual.",
  admin:
    "Poderá editar o nome e remover membros comuns. Não poderá alterar papéis nem administrar outros administradores ou proprietários.",
  member:
    "Poderá participar da conversa e adicionar pessoas quando permitido. Não poderá editar o nome, remover participantes ou alterar papéis.",
};

const actorRoleHints = {
  admin: "Edite o nome e gerencie membros comuns.",
  member: "Continue participando da conversa.",
};

function ownershipActionIcon(action: Action) {
  return action.type === "leave" ? "logout" : "key";
}

function OwnershipRoleDescription({ action }: { action: Action }) {
  if (action.type !== "role") return null;
  return (
    <>
      <div className="ownership-dialog__person">
        <strong>{action.member.displayName}</strong>
        <span>
          {roleLabels[action.member.role]} → {roleLabels[action.role]}
        </span>
      </div>
      <p>{roleDescriptions[action.role]}</p>
    </>
  );
}

// Same boundary handling as the existing AddMembersDialog, including inputs.
function trapDialogFocus(event: KeyboardEvent<HTMLDialogElement>) {
  event.stopPropagation();
  if (event.key !== "Tab") return;
  const controls = event.currentTarget.querySelectorAll<HTMLElement>(
    "button:enabled, input:enabled",
  );
  if (!controls.length) {
    event.preventDefault();
    return;
  }
  const first = controls[0];
  const last = controls[controls.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}
