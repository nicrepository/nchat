import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ApiRequestError } from "../lib/api";
import { randomId } from "../lib/randomId";
import {
  assignConversationRole,
  leaveOwnedConversation,
  transferConversationOwnership,
  type OwnershipDetails,
  type OwnershipMember,
} from "./ownershipApi";
import { type OwnershipAction as Action, roleLabels } from "./ownershipPresentation";

export interface OwnershipDialogContext {
  kind: "channel" | "group";
  id: string;
  ownership: OwnershipDetails;
  currentUserId: string;
  reload: () => void;
  onCommitted?: (message: string) => void;
}
type Props = OwnershipDialogContext;

function ownershipError(error: unknown): string {
  if (error instanceof ApiRequestError && error.status === 409)
    return "A propriedade mudou ou não há sucessor elegível. Atualize os detalhes e tente novamente.";
  return "Não foi possível concluir. Atualize os detalhes e tente novamente.";
}

type TransferSelection = { target: string; actorRole: "admin" | "member"; leave: boolean };

async function performOwnershipAction(
  action: Action,
  props: Props,
  selection: TransferSelection,
  request: { body: string; key: string },
) {
  switch (action.type) {
    case "role":
      return assignConversationRole(props.kind, props.id, action.member.userId, action.role);
    case "leave":
      return leaveOwnedConversation(props.kind, props.id);
    case "transfer": {
      const { target, actorRole, leave } = selection;
      const body = JSON.stringify([target, actorRole, leave]);
      if (request.body !== body) {
        request.body = body;
        request.key = randomId();
      }
      return transferConversationOwnership(
        props.kind,
        props.id,
        target,
        actorRole,
        leave,
        request.key,
      );
    }
  }
}

function ownershipActionTitle(action: Action): string {
  switch (action.type) {
    case "transfer":
      return "Transferir minha propriedade";
    case "leave":
      return "Sair da conversa";
    case "role":
      return `Tornar ${roleLabels[action.role].toLowerCase()}`;
  }
}

function OwnershipTransferFields({
  busy,
  candidates,
  target,
  setTarget,
  actorRole,
  setActorRole,
  leave,
  setLeave,
}: TransferSelection & {
  busy: boolean;
  candidates: OwnershipMember[];
  setTarget: (value: string) => void;
  setActorRole: (value: "admin" | "member") => void;
  setLeave: (value: boolean) => void;
}) {
  return (
    <>
      <p>O participante escolhido se torna proprietário. Escolha seu papel após a transferência.</p>
      <label>
        Novo proprietário
        <select value={target} onChange={(event) => setTarget(event.target.value)} disabled={busy}>
          <option value="">Escolha um participante</option>
          {candidates.map((member) => (
            <option key={member.userId} value={member.userId}>
              {member.displayName}
            </option>
          ))}
        </select>
      </label>
      <label>
        Meu papel
        <select
          value={actorRole}
          disabled={busy}
          onChange={(event) => setActorRole(event.target.value === "admin" ? "admin" : "member")}
        >
          <option value="member">Membro</option>
          <option value="admin">Administrador</option>
        </select>
      </label>
      <label className="ownership-dialog__checkbox">
        <input
          type="checkbox"
          checked={leave}
          disabled={busy}
          onChange={(event) => setLeave(event.target.checked)}
        />
        Sair após transferir
      </label>
    </>
  );
}

function OwnershipLeaveWarning({
  lastOwner,
  blocked,
  successor,
}: {
  lastOwner: boolean;
  blocked: boolean;
  successor?: OwnershipMember;
}) {
  return (
    <>
      <p>Você perderá acesso à conversa e ao histórico até ser adicionado novamente.</p>
      {lastOwner && (
        <p>
          {successor
            ? `${successor.displayName} assumirá a propriedade automaticamente.`
            : blocked
              ? "Não há sucessor automático elegível. Escolha um proprietário antes de sair."
              : "Você é o último participante."}
        </p>
      )}
    </>
  );
}

export default function OwnershipActionDialog({
  action,
  props,
  onClose,
}: {
  action: Action;
  props: Props;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const pending = useRef(false);
  const mounted = useRef(true);
  useLayoutEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const request = useRef<{ body: string; key: string }>({ body: "", key: "" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [target, setTarget] = useState("");
  const [actorRole, setActorRole] = useState<"admin" | "member">("member");
  const [leave, setLeave] = useState(false);
  const candidates = props.ownership.members.filter(
    (member) => member.actions.transfer === true && member.userId !== props.currentUserId,
  );
  const successor = props.ownership.members.find(
    (member) => member.userId === props.ownership.leavePreview.successorUserId,
  );
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => element?.close();
  }, []);
  async function submit() {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError("");
    try {
      await performOwnershipAction(action, props, { target, actorRole, leave }, request.current);
      if (mounted.current) {
        props.onCommitted?.("Alteração concluída.");
        props.reload();
        onClose();
      }
    } catch (failure) {
      if (mounted.current) {
        setError(ownershipError(failure));
      }
    } finally {
      pending.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  const title = ownershipActionTitle(action);
  const blocked = action.type === "leave" && props.ownership.leavePreview.blocked;
  return createPortal(
    <dialog
      ref={dialog}
      className="ownership-dialog chat-theme"
      aria-labelledby="ownership-dialog-title"
      aria-describedby="ownership-dialog-description"
      aria-modal="true"
      onCancel={(event) => {
        event.preventDefault();
        if (!busy) onClose();
      }}
      onKeyDown={(event) => event.stopPropagation()}
    >
      <div className="ownership-dialog__heading">
        <span className="ownership-dialog__icon material-symbols-outlined" aria-hidden="true">
          {ownershipActionIcon(action)}
        </span>
        <h3 id="ownership-dialog-title">{title}</h3>
      </div>
      <div id="ownership-dialog-description">
        <OwnershipRoleDescription action={action} />
        {action.type === "transfer" && (
          <OwnershipTransferFields
            busy={busy}
            candidates={candidates}
            target={target}
            setTarget={setTarget}
            actorRole={actorRole}
            setActorRole={setActorRole}
            leave={leave}
            setLeave={setLeave}
          />
        )}
        {action.type === "leave" && (
          <OwnershipLeaveWarning
            lastOwner={props.ownership.leavePreview.lastOwner}
            blocked={blocked}
            successor={successor}
          />
        )}
      </div>
      {error && (
        <p className="ownership-dialog__error" role="alert">
          {error}
        </p>
      )}
      <div className="ownership-dialog__buttons">
        <button type="button" autoFocus disabled={busy} onClick={onClose}>
          Cancelar
        </button>
        <button
          type="button"
          className={
            action.type === "leave"
              ? "ownership-dialog__confirm ownership-dialog__confirm--danger"
              : "ownership-dialog__confirm"
          }
          disabled={busy || blocked || (action.type === "transfer" && target === "")}
          onClick={() => void submit()}
        >
          {busy ? "Confirmando…" : "Confirmar"}
        </button>
      </div>
    </dialog>,
    document.body,
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
