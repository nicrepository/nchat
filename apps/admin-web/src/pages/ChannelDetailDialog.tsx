import { useCallback, useEffect, useRef, useState } from "react";

import {
  addChannelMembers,
  getChannel,
  listMemberCandidates,
  removeChannelMember,
  type ChannelMember,
} from "../api/managementApi";
import AdminUserSearchSelect, { type UserOption } from "../components/AdminUserSearchSelect";
import ConfirmDialog from "../components/ConfirmDialog";
import QueryStates from "../components/QueryStates";
import { formatDateTime } from "../lib/units";
import { classify, useAdminQuery } from "../lib/useAdminQuery";
import { useAdminSession } from "../session/useAdminSession";

interface ChannelDetailDialogProps {
  channelID: string;
  onClose: () => void;
}

/** A member whose removal is waiting to be confirmed. */
type PendingRemoval = { member: ChannelMember };

/**
 * Adding and removing are two operations with two different backend rules, so
 * they get two predicates. A single "can manage membership" boolean collapsed
 * them and hid the removal an archived channel still allows.
 *
 * Adding requires an active channel: the shared eligibility rule in
 * libs/go/platform/channelmembership requires `c.status = 'active'`, so an
 * archived channel admits nobody. `#geral` *does* accept additions — guests are
 * not enrolled in it automatically, so adding one is a real operation.
 *
 * Both of these are UX. The API decides, and still would if these returned true
 * for everything.
 */
function canAddMember(channel: { status: string }): boolean {
  return channel.status !== "archived";
}

/**
 * Removing has nothing to do with the channel being archived — the backend's
 * RemoveChannelMember does not read `status` at all, matching chat-service.
 * Taking somebody out of a channel nobody uses is not an operation that needs
 * the channel to be live.
 *
 * `#geral` is the one refusal: every workspace member belongs to it by
 * construction, and the API answers 403.
 */
function canRemoveMember(channel: { isGeneral: boolean }): boolean {
  return !channel.isGeneral;
}

/**
 * What the operator is told when one of the two is unavailable.
 *
 * Never "membership cannot be changed": on an archived channel that is false,
 * and a message that overstates the restriction is as unhelpful as a control
 * that does not work.
 */
function membershipNotice(channel: { isGeneral: boolean; status: string }): string | null {
  if (channel.isGeneral) {
    return "O canal #geral acompanha a membership do workspace: é possível adicionar alguém que ainda não participe, mas não remover.";
  }
  if (channel.status === "archived") {
    return "O canal está arquivado. Novos membros não podem ser adicionados, mas membros existentes ainda podem ser removidos.";
  }
  return null;
}

/**
 * One channel's record: where it lives, how big it is, and who administers it.
 *
 * The two lists of people are kept apart on the screen exactly as they are in
 * the payload. A channel moderator governs this channel; a workspace owner or
 * admin governs the workspace it belongs to. Showing them as one list of
 * "admins" would teach the operator a model this platform deliberately does not
 * have.
 *
 * The message count is a volume, not a listing — it is what an operator
 * deciding whether to archive a channel needs, and no message is reachable from
 * here.
 */
export default function ChannelDetailDialog({ channelID, onClose }: ChannelDetailDialogProps) {
  const { can } = useAdminSession();
  const canManage = can("admin.channels.manage");

  const closeRef = useRef<HTMLButtonElement>(null);
  const opener = useRef<Element | null>(null);
  // A chosen person, not typed text. The add button is enabled by this being
  // set, so a half-typed name can never become a request.
  const [candidate, setCandidate] = useState<UserOption | null>(null);
  const [pending, setPending] = useState(false);
  const [feedback, setFeedback] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [confirming, setConfirming] = useState<PendingRemoval | null>(null);

  useEffect(() => {
    opener.current = document.activeElement;
    closeRef.current?.focus();
    return () => {
      if (opener.current instanceof HTMLElement) opener.current.focus();
    };
  }, []);

  const load = useCallback((signal: AbortSignal) => getChannel(channelID, signal), [channelID]);
  const query = useAdminQuery(load);
  const detail = query.data;

  // One guard for both mutations. The `pending` flag is the guard, not only the
  // disabled attribute: a second click that lands before React re-renders finds
  // it already set.
  async function runMembership(action: () => Promise<string>) {
    if (pending) return;
    setPending(true);
    setFeedback(null);
    try {
      setFeedback({ tone: "ok", text: await action() });
      query.reload();
    } catch (error) {
      setFeedback({ tone: "error", text: classify(error).message });
    } finally {
      setPending(false);
      setConfirming(null);
    }
  }

  // Stable so the picker's effect does not restart on every render of the
  // dialog around it.
  const searchCandidates = useCallback(
    async (term: string, signal: AbortSignal): Promise<UserOption[]> => {
      const found = await listMemberCandidates(channelID, term, signal);
      return found.map((person) => ({
        id: person.userId,
        displayName: person.fullName || person.displayName,
        secondary: person.email,
        avatarUrl: person.avatarUrl || undefined,
        hint: person.workspaceRole,
      }));
    },
    [channelID],
  );

  const addMember = () => {
    if (candidate === null) return;
    const chosen = candidate;
    void runMembership(async () => {
      const result = await addChannelMembers(channelID, [chosen.id]);
      setCandidate(null);
      return result.added > 0
        ? `${chosen.displayName} foi adicionado. O canal tem ${result.memberCount} membro(s).`
        : `${chosen.displayName} já era membro do canal. Nada foi alterado.`;
    });
  };

  const removeMember = () => {
    if (confirming === null) return;
    const { member } = confirming;
    void runMembership(async () => {
      const result = await removeChannelMember(channelID, member.userId);
      return result.removed
        ? `${member.displayName} foi removido. O canal tem ${result.memberCount} membro(s).`
        : `${member.displayName} já não era membro do canal.`;
    });
  };

  return (
    <div className="admin-dialog-backdrop">
      <div
        className="admin-dialog admin-dialog--wide"
        role="dialog"
        aria-modal="true"
        aria-labelledby="admin-channel-detail-title"
        onKeyDown={(event) => {
          // While a confirmation is open it owns Escape; closing the record
          // underneath it would leave the operator with neither.
          if (event.key === "Escape" && confirming === null && !pending) onClose();
        }}
      >
        <div className="admin-dialog__header">
          <h2 id="admin-channel-detail-title">
            {detail === null ? "Detalhes do canal" : detail.displayName}
          </h2>
          <button
            type="button"
            className="admin-button admin-button--ghost"
            ref={closeRef}
            onClick={onClose}
          >
            Fechar
          </button>
        </div>

        <QueryStates
          status={query.status}
          message={query.message}
          empty="Canal indisponível."
          isEmpty={false}
          onRetry={query.reload}
        />

        {query.status === "ready" && detail !== null && (
          <>
            <dl className="admin-definitions">
              <dt>Identificador</dt>
              <dd>#{detail.slug}</dd>
              <dt>Workspace</dt>
              <dd>{detail.workspaceName}</dd>
              <dt>Categoria</dt>
              <dd>{detail.categoryName || "—"}</dd>
              <dt>Visibilidade</dt>
              <dd>{detail.type === "private" ? "Privado" : "Público"}</dd>
              <dt>Situação</dt>
              <dd>{detail.status === "archived" ? "Arquivado" : "Ativo"}</dd>
              <dt>Membros</dt>
              <dd>{detail.memberCount}</dd>
              <dt>Volume de mensagens</dt>
              <dd>{detail.messageCount}</dd>
              <dt>Criado em</dt>
              <dd>{formatDateTime(detail.createdAt)}</dd>
              <dt>Última atividade</dt>
              <dd>{formatDateTime(detail.lastActivityAt)}</dd>
            </dl>

            <h3>Moderadores do canal</h3>
            <PeopleTable
              caption="Moderadores do canal"
              people={detail.moderators}
              empty="Este canal não tem moderadores próprios."
            />

            <h3>Administradores do workspace</h3>
            <p className="admin-table__muted">
              Autoridade sobre o workspace, não sobre este canal em particular.
            </p>
            <PeopleTable
              caption="Administradores do workspace"
              people={detail.workspaceAdmins}
              empty="Nenhum owner ou admin ativo neste workspace."
            />

            <h3>Membros</h3>
            <p className="admin-table__muted">
              Mostrando até {detail.members.length} de {detail.memberCount} membro(s). Adicionar ou
              remover altera apenas a participação: não concede papel algum e não dá acesso ao
              conteúdo do canal a quem administra.
            </p>

            {feedback !== null && (
              <p
                className={feedback.tone === "ok" ? "admin-notice" : "admin-alert"}
                role={feedback.tone === "ok" ? "status" : "alert"}
                data-testid="admin-membership-feedback"
              >
                {feedback.text}
              </p>
            )}

            {canManage && canAddMember(detail) && (
              <form
                className="admin-filters"
                onSubmit={(event) => {
                  event.preventDefault();
                  addMember();
                }}
              >
                <AdminUserSearchSelect
                  label="Adicionar membro"
                  placeholder="Busque por nome ou e-mail"
                  search={searchCandidates}
                  selected={candidate}
                  onSelect={setCandidate}
                  disabled={pending}
                  emptyLabel="Ninguém do workspace corresponde — quem já é membro não aparece aqui."
                  help="Somente pessoas do workspace deste canal que ainda não são membros."
                />
                <button
                  type="submit"
                  className="admin-button"
                  disabled={pending || candidate === null}
                >
                  {pending ? "Aplicando…" : "Adicionar"}
                </button>
              </form>
            )}
            {canManage && membershipNotice(detail) !== null && (
              <p className="admin-notice">{membershipNotice(detail)}</p>
            )}

            {detail.members.length === 0 ? (
              <p className="admin-empty">Este canal não tem membros.</p>
            ) : (
              <div className="admin-table-scroll">
                <table className="admin-table">
                  <caption className="admin-visually-hidden">Membros do canal</caption>
                  <thead>
                    <tr>
                      <th scope="col">Pessoa</th>
                      <th scope="col">E-mail</th>
                      <th scope="col">Papel no canal</th>
                      {canManage && canRemoveMember(detail) && <th scope="col">Ações</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {detail.members.map((member) => (
                      <tr key={member.userId}>
                        <th scope="row">{member.displayName}</th>
                        <td>{member.email}</td>
                        <td>{member.role}</td>

                        {canManage && canRemoveMember(detail) && (
                          <td>
                            <button
                              type="button"
                              className="admin-button admin-button--ghost"
                              disabled={pending}
                              onClick={() => setConfirming({ member })}
                            >
                              Remover
                            </button>
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}

        {confirming !== null && (
          <ConfirmDialog
            title="Remover esta pessoa do canal?"
            description={`${confirming.member.displayName} deixará de participar deste canal.`}
            impact="A membership do workspace e o histórico do canal não são alterados. A pessoa pode ser adicionada de novo."
            confirmLabel="Remover"
            pending={pending}
            onConfirm={removeMember}
            onCancel={() => setConfirming(null)}
          />
        )}
      </div>
    </div>
  );
}

interface PeopleTableProps {
  caption: string;
  people: { userId: string; displayName: string; email: string; role: string }[];
  empty: string;
}

function PeopleTable({ caption, people, empty }: PeopleTableProps) {
  if (people.length === 0) return <p className="admin-empty">{empty}</p>;
  return (
    <div className="admin-table-scroll">
      <table className="admin-table">
        <caption className="admin-visually-hidden">{caption}</caption>
        <thead>
          <tr>
            <th scope="col">Pessoa</th>
            <th scope="col">E-mail</th>
            <th scope="col">Papel</th>
          </tr>
        </thead>
        <tbody>
          {people.map((person) => (
            <tr key={person.userId}>
              <th scope="row">{person.displayName}</th>
              <td>{person.email}</td>
              <td>{person.role}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
