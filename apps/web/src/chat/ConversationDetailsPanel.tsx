/**
 * ConversationDetailsPanel — the side panel for a channel ("Detalhes do canal",
 * issue #435) and for an ad-hoc group ("Detalhes do grupo", issue #441).
 *
 * One shell, two vocabularies. The frame — heading, close button, loading and
 * error states, the pinned-message section and the recent-files section — is
 * identical for both, because those concepts are identical for both. What
 * differs is the aggregate being described: a channel has visibility, a
 * creation date and members; a group has a name, a creation date and
 * participants, and no visibility at all. Those two sections are therefore
 * separate components selected by the `kind` tag, not one component with a
 * pile of optional props.
 *
 * Security invariants:
 * - Every server-supplied string (channel name, member names, file names, pin
 *   body) is rendered as a React text node or through RichTextRenderer. There is
 *   no dangerouslySetInnerHTML here and no URL is ever built from a filename.
 * - The authenticated user is identified by ID, never by display name, so two
 *   members with the same name can never both be marked "Você".
 * - Avatar URLs arrive already filtered by chatApi's same-origin rule; a
 *   rejected one is simply absent and the initials fallback renders.
 * - Attachments are metadata only. Nothing here links to content: the download
 *   endpoint needs an Authorization header and refuses anything the scan has
 *   not cleared, so the panel does not pretend a file is retrievable by URL.
 *
 * The panel is a layout sibling of the conversation, never a modal and never a
 * route: it renders beside the messages so opening it cannot unmount the
 * message list, the composer or the WebSocket subscription.
 */

import { useEffect, useRef } from "react";

import "./ConversationDetailsPanel.css";
import RichTextRenderer from "./RichTextRenderer";
import type {
  ChannelAttachment,
  ChannelDetails,
  ChannelMemberProfile,
  GroupDetails,
  GroupParticipantProfile,
  PinnedItem,
} from "./chatTypes";
import {
  avatarColorFor,
  formatDayLabel,
  formatLongDate,
  formatTime,
  initialsFrom,
  senderLabel,
} from "./messageDisplay";
import type { ConversationDetailsState } from "./useConversationDetails";
import {
  conversationDetailsPanelId,
  conversationDetailsTitleId,
  formatFileSize,
} from "./conversationDetailsDisplay";

/** Material symbol name for a file, chosen from the *detected* type only. */
function fileIconFor(contentType: string): string {
  if (contentType.startsWith("image/")) return "image";
  if (contentType.startsWith("video/")) return "movie";
  if (contentType.startsWith("audio/")) return "graphic_eq";
  if (contentType === "application/pdf") return "picture_as_pdf";
  if (contentType.startsWith("text/")) return "description";
  return "draft";
}

const attachmentStatusLabel: Record<ChannelAttachment["status"], string> = {
  pending_scan: "Em análise",
  clean: "Verificado",
  rejected: "Reprovado",
};

const presenceLabel = {
  online: "Online",
  away: "Ausente",
  offline: "Offline",
} as const;

interface SectionMessageProps {
  children: React.ReactNode;
  /** Loading rows announce themselves; empty and error states are static text. */
  role?: "status" | "alert";
}

function SectionMessage({ children, role }: SectionMessageProps) {
  return (
    <p className="chat-details__note" role={role}>
      {children}
    </p>
  );
}

/**
 * An action whose flow does not exist yet.
 *
 * It is rendered as a real disabled button rather than hidden or faked: the
 * affordance stays visible and its unavailability is announced (aria-disabled
 * plus a described reason) instead of the click silently doing nothing or, far
 * worse, showing a success that never happened.
 */
function UnavailableAction({
  label,
  icon,
  reasonId,
  className,
}: {
  label: string;
  icon?: string;
  reasonId: string;
  className: string;
}) {
  return (
    <button type="button" className={className} disabled aria-describedby={reasonId}>
      {icon && (
        <span className="material-symbols-outlined" aria-hidden="true">
          {icon}
        </span>
      )}
      {label}
    </button>
  );
}

/**
 * A person row, shared by the channel's members and the group's participants.
 *
 * `subtitle` is what the two surfaces disagree about — a channel shows the
 * channel role, a group has no role to show — so it is passed in rather than
 * derived here from a union.
 */
interface MemberRowProps {
  member: ChannelMemberProfile | GroupParticipantProfile;
  subtitle: string;
  isCurrentUser: boolean;
}

function MemberRow({ member, subtitle, isCurrentUser }: MemberRowProps) {
  const color = avatarColorFor(member.userId);
  return (
    <li className="chat-details__member">
      <span
        className={`chat-details__avatar chat-details__avatar--${color}`}
        aria-hidden="true"
        data-testid="chat-details-member-avatar"
      >
        {member.avatarUrl ? (
          <img
            className="chat-details__avatar-img"
            src={member.avatarUrl}
            alt=""
            referrerPolicy="no-referrer"
          />
        ) : (
          initialsFrom(member.displayName)
        )}
        {member.presence && (
          <span
            className={`chat-details__presence chat-details__presence--${member.presence}`}
            data-testid="chat-details-presence"
          />
        )}
      </span>
      <span className="chat-details__member-text">
        <span className="chat-details__member-name">
          {member.displayName}
          {isCurrentUser && <span className="chat-details__badge">Você</span>}
        </span>
        <span className="chat-details__member-role">
          {subtitle}
          {member.presence && ` · ${presenceLabel[member.presence]}`}
        </span>
      </span>
    </li>
  );
}

/**
 * The "Sobre" section of a channel: description placeholder, creation date,
 * visibility and member total.
 */
function ChannelAboutSection({ details }: { details: ChannelDetails }) {
  return (
    <>
      {/*
        chat.channels has no description column, so there is nothing to render
        here yet. The empty state is the honest outcome, not a placeholder for
        data the server withheld.
      */}
      <p className="chat-details__empty" data-testid="chat-details-description">
        Este canal ainda não tem descrição.
      </p>
      <p className="chat-details__meta">
        <span className="material-symbols-outlined" aria-hidden="true">
          calendar_today
        </span>
        {details.createdAt
          ? `Criado em ${formatLongDate(details.createdAt)}`
          : "Data de criação indisponível"}
      </p>
      <p className="chat-details__meta">
        <span className="material-symbols-outlined" aria-hidden="true">
          {details.type === "private" ? "lock" : "public"}
        </span>
        {details.type === "private" ? "Canal privado" : "Canal público"}
        {" · "}
        {details.memberCount === 1 ? "1 membro" : `${details.memberCount} membros`}
      </p>
    </>
  );
}

/**
 * The "Sobre" section of a group: name, creation date and participant total.
 *
 * Deliberately without visibility: a group is not public or private, it is a
 * closed conversation between the people in it, and rendering a channel's
 * vocabulary here would state something the domain never says.
 */
function GroupAboutSection({ details }: { details: GroupDetails }) {
  return (
    <>
      <p className="chat-details__group-name" data-testid="chat-details-group-name">
        {details.name || "Grupo sem nome"}
      </p>
      <p className="chat-details__meta">
        <span className="material-symbols-outlined" aria-hidden="true">
          calendar_today
        </span>
        {details.createdAt
          ? `Criado em ${formatLongDate(details.createdAt)}`
          : "Data de criação indisponível"}
      </p>
      <p className="chat-details__meta">
        <span className="material-symbols-outlined" aria-hidden="true">
          group
        </span>
        {details.participantCount === 1
          ? "1 participante"
          : `${details.participantCount} participantes`}
      </p>
    </>
  );
}

/** The channel's online-members list (issue #435): presence-filtered server-side. */
function ChannelMembersSection({
  details,
  currentUserId,
}: {
  details: ChannelDetails;
  currentUserId: string;
}) {
  if (details.onlineMembers.length === 0) {
    // "ninguem online agora", never "este canal nao tem membros" — the
    // channel's size is reported separately and is unaffected.
    return <SectionMessage>Nenhum membro online no momento.</SectionMessage>;
  }
  return (
    <ul className="chat-details__members" aria-label="Membros online do canal">
      {details.onlineMembers.map((member) => (
        <MemberRow
          key={member.userId}
          member={member}
          subtitle={member.role === "moderator" ? "Moderador" : "Membro"}
          isCurrentUser={Boolean(currentUserId) && member.userId === currentUserId}
        />
      ))}
    </ul>
  );
}

/**
 * The group's participant list (issue #441).
 *
 * Every active participant appears, online or not: presence is shown beside a
 * participant, never used to decide whether they are shown.
 */
function GroupParticipantsSection({
  details,
  currentUserId,
}: {
  details: GroupDetails;
  currentUserId: string;
}) {
  if (details.participants.length === 0) {
    return <SectionMessage>Nenhum participante para exibir.</SectionMessage>;
  }
  return (
    <ul className="chat-details__members" aria-label="Participantes do grupo">
      {details.participants.map((participant) => (
        <MemberRow
          key={participant.userId}
          member={participant}
          subtitle="Participante"
          isCurrentUser={Boolean(currentUserId) && participant.userId === currentUserId}
        />
      ))}
    </ul>
  );
}

/** Per-kind wording, in one table so a missing case is a type error. */
const panelCopy = {
  channel: {
    title: "Detalhes do canal",
    closeLabel: "Fechar detalhes do canal",
    peopleHeading: "Membros online",
    peopleUnavailable: "A gestão de membros do canal ainda não está disponível nesta versão.",
    addAction: "Adicionar membros",
    pinEmpty: "Nenhuma mensagem fixada neste canal.",
    filesEmpty: "Nenhum arquivo enviado neste canal.",
    filesUnavailable: "A central de arquivos do canal ainda não está disponível nesta versão.",
  },
  group: {
    title: "Detalhes do grupo",
    closeLabel: "Fechar detalhes do grupo",
    peopleHeading: "Participantes",
    peopleUnavailable: "A gestão de participantes do grupo ainda não está disponível nesta versão.",
    addAction: "Adicionar participantes",
    pinEmpty: "Nenhuma mensagem fixada neste grupo.",
    filesEmpty: "Nenhum arquivo enviado neste grupo.",
    filesUnavailable: "A central de arquivos do grupo ainda não está disponível nesta versão.",
  },
} as const;

interface ConversationDetailsPanelProps {
  /**
   * Which vocabulary the frame uses. It is the caller's domain discriminant,
   * resolved from the conversation record — never from the route or the name —
   * and it is available before the data loads, so the heading is correct while
   * the sections are still fetching.
   */
  kind: "channel" | "group";
  state: ConversationDetailsState;
  /** Identifies the viewer by ID; a display name would be ambiguous. */
  currentUserId: string;
  /**
   * The result of the one pin selector, shared with the bar above the
   * conversation. Passing the selected item (rather than the list) is what makes
   * "the bar and the panel show the same message" structural.
   */
  latestPin: PinnedItem | null;
  onClose: () => void;
}

export default function ConversationDetailsPanel({
  kind,
  state,
  currentUserId,
  latestPin,
  onClose,
}: ConversationDetailsPanelProps) {
  const copy = panelCopy[kind];
  const closeButtonRef = useRef<HTMLButtonElement>(null);

  // Focus moves into the panel once, on open, so a keyboard user lands on it
  // instead of continuing from the header button. Deliberately not re-run on
  // data changes: a refetch (channel switch, pin update) must never steal focus
  // from wherever the user has since moved it.
  useEffect(() => {
    closeButtonRef.current?.focus();
  }, []);

  const details = state.details;
  const files = state.files;

  return (
    <aside
      id={conversationDetailsPanelId}
      className="chat-details"
      aria-labelledby={conversationDetailsTitleId}
      data-testid="chat-conversation-details"
      data-conversation-kind={kind}
    >
      <div className="chat-details__head">
        <h2 id={conversationDetailsTitleId} className="chat-details__title">
          {copy.title}
        </h2>
        <button
          ref={closeButtonRef}
          type="button"
          className="chat-details__close"
          aria-label={copy.closeLabel}
          onClick={onClose}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            close
          </span>
        </button>
      </div>

      <div className="chat-details__body">
        {/* ── Sobre ─────────────────────────────────────────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-details-about">
          <h3 id="chat-details-about" className="chat-details__label">
            Sobre
          </h3>
          {details.status === "loading" && (
            <SectionMessage role="status">
              {kind === "channel"
                ? "Carregando informações do canal…"
                : "Carregando informações do grupo…"}
            </SectionMessage>
          )}
          {details.status === "error" && (
            <SectionMessage role="alert">
              {kind === "channel"
                ? "Não foi possível carregar as informações do canal."
                : "Não foi possível carregar as informações do grupo."}
            </SectionMessage>
          )}
          {details.status === "ready" &&
            (details.data.kind === "channel" ? (
              <ChannelAboutSection details={details.data} />
            ) : (
              <GroupAboutSection details={details.data} />
            ))}
        </section>

        {/* ── Pessoas (membros online / participantes) ──────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-details-people">
          <div className="chat-details__section-head">
            {/*
              The count is the server's total for this section, never the length
              of the rendered list: both lists are capped previews. For a channel
              that total is how many members are online; for a group it is how
              many participants there are.
            */}
            <h3 id="chat-details-people" className="chat-details__label">
              {copy.peopleHeading}
              {details.status === "ready" &&
                ` (${
                  details.data.kind === "channel"
                    ? details.data.onlineCount
                    : details.data.participantCount
                })`}
            </h3>
            <UnavailableAction
              label="Ver todos"
              reasonId="chat-details-people-unavailable"
              className="chat-details__link-action"
            />
          </div>
          {details.status === "loading" && (
            <SectionMessage role="status">
              {kind === "channel" ? "Carregando membros…" : "Carregando participantes…"}
            </SectionMessage>
          )}
          {details.status === "error" && (
            <SectionMessage role="alert">
              {kind === "channel"
                ? "Não foi possível carregar os membros."
                : "Não foi possível carregar os participantes."}
            </SectionMessage>
          )}
          {details.status === "ready" &&
            (details.data.kind === "channel" ? (
              <ChannelMembersSection details={details.data} currentUserId={currentUserId} />
            ) : (
              <GroupParticipantsSection details={details.data} currentUserId={currentUserId} />
            ))}
          <UnavailableAction
            label={copy.addAction}
            icon="person_add"
            reasonId="chat-details-people-unavailable"
            className="chat-details__wide-action"
          />
          <p id="chat-details-people-unavailable" className="chat-details__note">
            {copy.peopleUnavailable}
          </p>
        </section>

        {/* ── Mensagem fixada ───────────────────────────────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-details-pin">
          <h3 id="chat-details-pin" className="chat-details__label">
            Mensagem fixada
          </h3>
          {latestPin === null ? (
            <p className="chat-details__empty" data-testid="chat-details-pin-empty">
              {copy.pinEmpty}
            </p>
          ) : (
            <div className="chat-details__pin" data-testid="chat-details-pin">
              <span className="material-symbols-outlined chat-details__pin-icon" aria-hidden="true">
                push_pin
              </span>
              <div className="chat-details__pin-text">
                <div className="chat-details__pin-body">
                  {latestPin.message.isRemoved ? (
                    <em>Mensagem removida.</em>
                  ) : (
                    <RichTextRenderer
                      text={latestPin.message.bodyText}
                      bodyFormat={latestPin.message.bodyFormat}
                    />
                  )}
                </div>
                <div className="chat-details__pin-by">
                  {senderLabel(latestPin.message)}
                  {latestPin.pinnedAt &&
                    ` · ${formatDayLabel(latestPin.pinnedAt)}, ${formatTime(latestPin.pinnedAt)}`}
                </div>
              </div>
            </div>
          )}
        </section>

        {/* ── Arquivos recentes ─────────────────────────────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-details-files">
          <div className="chat-details__section-head">
            <h3 id="chat-details-files" className="chat-details__label">
              Arquivos recentes
            </h3>
            <UnavailableAction
              label="Ver todos"
              reasonId="chat-details-files-unavailable"
              className="chat-details__link-action"
            />
          </div>
          {files.status === "loading" && (
            <SectionMessage role="status">Carregando arquivos…</SectionMessage>
          )}
          {files.status === "error" && (
            <SectionMessage role="alert">Não foi possível carregar os arquivos.</SectionMessage>
          )}
          {files.status === "ready" &&
            (files.data.length === 0 ? (
              <p className="chat-details__empty" data-testid="chat-details-files-empty">
                {copy.filesEmpty}
              </p>
            ) : (
              <ul className="chat-details__files" aria-label="Arquivos recentes">
                {files.data.map((file) => (
                  <li key={file.id} className="chat-details__file">
                    <span className="chat-details__file-icon" aria-hidden="true">
                      <span className="material-symbols-outlined">
                        {fileIconFor(file.contentType)}
                      </span>
                    </span>
                    <span className="chat-details__file-text">
                      {/* A filename is text. It is never a URL and never markup. */}
                      <span className="chat-details__file-name">{file.filename}</span>
                      <span className="chat-details__file-meta">
                        {file.createdAt &&
                          `${formatDayLabel(file.createdAt)}, ${formatTime(file.createdAt)} · `}
                        {formatFileSize(file.size)}
                        {file.status !== "clean" && (
                          <span
                            className={`chat-details__file-status chat-details__file-status--${file.status}`}
                          >
                            {attachmentStatusLabel[file.status]}
                          </span>
                        )}
                      </span>
                    </span>
                  </li>
                ))}
              </ul>
            ))}
          <p id="chat-details-files-unavailable" className="chat-details__note">
            {copy.filesUnavailable}
          </p>
        </section>
      </div>
    </aside>
  );
}
