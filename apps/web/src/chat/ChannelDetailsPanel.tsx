/**
 * ChannelDetailsPanel — the "Detalhes do canal" side panel (issue #435).
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

import "./ChannelDetailsPanel.css";
import RichTextRenderer from "./RichTextRenderer";
import type { ChannelAttachment, ChannelMemberProfile, PinnedItem } from "./chatTypes";
import {
  avatarColorFor,
  formatDayLabel,
  formatLongDate,
  formatTime,
  initialsFrom,
  senderLabel,
} from "./messageDisplay";
import type { ChannelDetailsState } from "./useChannelDetails";
import {
  channelDetailsPanelId,
  channelDetailsTitleId,
  formatFileSize,
} from "./channelDetailsDisplay";

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

interface MemberRowProps {
  member: ChannelMemberProfile;
  isCurrentUser: boolean;
}

function MemberRow({ member, isCurrentUser }: MemberRowProps) {
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
          {member.role === "moderator" ? "Moderador" : "Membro"}
          {member.presence && ` · ${presenceLabel[member.presence]}`}
        </span>
      </span>
    </li>
  );
}

interface ChannelDetailsPanelProps {
  state: ChannelDetailsState;
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

export default function ChannelDetailsPanel({
  state,
  currentUserId,
  latestPin,
  onClose,
}: ChannelDetailsPanelProps) {
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
      id={channelDetailsPanelId}
      className="chat-details"
      aria-labelledby={channelDetailsTitleId}
      data-testid="chat-channel-details"
    >
      <div className="chat-details__head">
        <h2 id={channelDetailsTitleId} className="chat-details__title">
          Detalhes do canal
        </h2>
        <button
          ref={closeButtonRef}
          type="button"
          className="chat-details__close"
          aria-label="Fechar detalhes do canal"
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
            <SectionMessage role="status">Carregando informações do canal…</SectionMessage>
          )}
          {details.status === "error" && (
            <SectionMessage role="alert">
              Não foi possível carregar as informações do canal.
            </SectionMessage>
          )}
          {details.status === "ready" && (
            <>
              {/*
                chat.channels has no description column, so there is nothing to
                render here yet. The empty state is the honest outcome, not a
                placeholder for data the server withheld.
              */}
              <p className="chat-details__empty" data-testid="chat-details-description">
                Este canal ainda não tem descrição.
              </p>
              <p className="chat-details__meta">
                <span className="material-symbols-outlined" aria-hidden="true">
                  calendar_today
                </span>
                {details.data.createdAt
                  ? `Criado em ${formatLongDate(details.data.createdAt)}`
                  : "Data de criação indisponível"}
              </p>
              <p className="chat-details__meta">
                <span className="material-symbols-outlined" aria-hidden="true">
                  {details.data.type === "private" ? "lock" : "public"}
                </span>
                {details.data.type === "private" ? "Canal privado" : "Canal público"}
                {" · "}
                {details.data.memberCount === 1
                  ? "1 membro"
                  : `${details.data.memberCount} membros`}
              </p>
            </>
          )}
        </section>

        {/* ── Membros online ────────────────────────────────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-details-members">
          <div className="chat-details__section-head">
            {/*
              The heading counts onlineCount, not the rendered list: the list is
              a capped preview, so showing its length would under-report a
              channel with more online members than fit. The channel's total size
              lives in "Sobre" above and is a different number entirely.
            */}
            <h3 id="chat-details-members" className="chat-details__label">
              Membros online
              {details.status === "ready" && ` (${details.data.onlineCount})`}
            </h3>
            <UnavailableAction
              label="Ver todos"
              reasonId="chat-details-members-unavailable"
              className="chat-details__link-action"
            />
          </div>
          {details.status === "loading" && (
            <SectionMessage role="status">Carregando membros…</SectionMessage>
          )}
          {details.status === "error" && (
            <SectionMessage role="alert">Não foi possível carregar os membros.</SectionMessage>
          )}
          {details.status === "ready" &&
            (details.data.onlineMembers.length === 0 ? (
              // "ninguém online agora", never "este canal não tem membros" —
              // the channel's size is reported separately and is unaffected.
              <SectionMessage>Nenhum membro online no momento.</SectionMessage>
            ) : (
              <ul className="chat-details__members" aria-label="Membros online do canal">
                {details.data.onlineMembers.map((member) => (
                  <MemberRow
                    key={member.userId}
                    member={member}
                    isCurrentUser={Boolean(currentUserId) && member.userId === currentUserId}
                  />
                ))}
              </ul>
            ))}
          <UnavailableAction
            label="Adicionar membros"
            icon="person_add"
            reasonId="chat-details-members-unavailable"
            className="chat-details__wide-action"
          />
          <p id="chat-details-members-unavailable" className="chat-details__note">
            A gestão de membros do canal ainda não está disponível nesta versão.
          </p>
        </section>

        {/* ── Mensagem fixada ───────────────────────────────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-details-pin">
          <h3 id="chat-details-pin" className="chat-details__label">
            Mensagem fixada
          </h3>
          {latestPin === null ? (
            <p className="chat-details__empty" data-testid="chat-details-pin-empty">
              Nenhuma mensagem fixada neste canal.
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
                Nenhum arquivo enviado neste canal.
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
            A central de arquivos do canal ainda não está disponível nesta versão.
          </p>
        </section>
      </div>
    </aside>
  );
}
