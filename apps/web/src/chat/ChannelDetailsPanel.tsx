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

import { useCallback, useEffect, useRef, useState } from "react";

import "./ChannelDetailsPanel.css";
import AddMembersDialog from "./AddMembersDialog";
import RichTextRenderer from "./RichTextRenderer";
import type {
  AddMembersResult,
  ChannelAttachment,
  ChannelMemberProfile,
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
  const addMembersButtonRef = useRef<HTMLButtonElement>(null);
  /**
   * The channel the picker was opened for, or null.
   *
   * Storing the identity instead of a boolean is the whole protection against
   * confirming a selection into the wrong channel. The panel is deliberately
   * not remounted on a channel switch (issue #435), so a boolean would survive
   * one: the dialog would stay open, keep the people picked in channel A, and
   * its confirm would post them to channel B.
   *
   * Comparing against the channel currently rendered closes that during render,
   * with no effect and no second mechanism: the dialog unmounts, its
   * AbortController cancels any in-flight search or submit, and the selection
   * goes with it. Nothing is mutated on the way out.
   */
  const [pickerFor, setPickerFor] = useState<string | null>(null);
  // The notice carries the channel it describes rather than being reset when the
  // channel changes. The panel is deliberately not remounted on a channel switch
  // (issue #435), so a bare string would outlive its channel and report "2
  // pessoas adicionadas" under one nobody was added to. Tying it to an identity
  // makes that unrepresentable, and needs no effect to enforce.
  const [addedNotice, setAddedNotice] = useState<{ channelId: string; text: string } | null>(null);

  // Focus moves into the panel once, on open, so a keyboard user lands on it
  // instead of continuing from the header button. Deliberately not re-run on
  // data changes: a refetch (channel switch, pin update) must never steal focus
  // from wherever the user has since moved it.
  useEffect(() => {
    closeButtonRef.current?.focus();
  }, []);

  const details = state.details;
  const files = state.files;
  const channelId = details.status === "ready" ? details.data.id : "";

  // Open only while the channel it was opened for is still the one on screen.
  const pickerOpen = pickerFor !== null && pickerFor === channelId && channelId !== "";

  /**
   * Closes the picker and returns focus to the control that opened it.
   *
   * The button is only rendered while the caller may manage members, so the ref
   * can be detached by the time this runs (a refetch that revoked the
   * permission). Focusing a detached node would drop focus to <body>; the
   * optional call leaves it where the browser put it instead.
   */
  const closePicker = useCallback(() => {
    setPickerFor(null);
    addMembersButtonRef.current?.focus();
  }, []);

  const reload = state.reload;
  const handleAdded = useCallback(
    (result: AddMembersResult) => {
      closePicker();
      // The server's own numbers, never a local increment: someone else may have
      // added people between the search and this response.
      setAddedNotice({
        channelId,
        text:
          result.added === 0
            ? "Todas as pessoas selecionadas já participam deste canal."
            : result.added === 1
              ? "1 pessoa adicionada ao canal."
              : `${result.added} pessoas adicionadas ao canal.`,
      });
      // The single reconciliation path. The response is not merged into the
      // rendered list; the panel refetches, so the member list and both counters
      // come from one authority and a concurrent WebSocket event refetching too
      // cannot double-count anything.
      reload();
    },
    [channelId, closePicker, reload],
  );

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
          {/*
            Rendered only once the server has answered and said this caller may
            manage members (issue #398). While loading, on error, and for a
            caller without the permission, the control is absent — the safe
            default, since `canManageMembers` is false unless the server sent
            true. Hiding it is not the security boundary: POST .../members
            re-derives the decision from the session on every call.
          */}
          {details.status === "ready" && details.data.canManageMembers && (
            <button
              ref={addMembersButtonRef}
              type="button"
              className="chat-details__wide-action"
              onClick={() => setPickerFor(channelId)}
              data-testid="chat-details-add-members"
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                person_add
              </span>
              Adicionar membros
            </button>
          )}
          {addedNotice?.channelId === channelId && (
            // Announced rather than shown as a transient toast: the panel below
            // has already been refetched, and this says what changed.
            <p className="chat-details__note" role="status">
              {addedNotice.text}
            </p>
          )}
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

      {pickerOpen && (
        <AddMembersDialog
          target={{ kind: "channel", channelId }}
          /*
            The viewer plus the members the panel can see. It is deliberately
            partial — onlineMembers is a capped, presence-filtered preview, so an
            offline member is not in it and will still be offered. That is
            harmless: the server reports them under already_members and writes no
            duplicate row, which is exactly why this is a UX filter and not a
            check.
          */
          /*
            Only the viewer. Current members are excluded by the search endpoint
            itself, in SQL — this list deliberately does not carry them, because
            the section above is a capped preview and passing it made members it
            could not show appear as selectable.
          */
          excludedUserIds={currentUserId ? [currentUserId] : []}
          onClose={closePicker}
          onAdded={handleAdded}
        />
      )}
    </aside>
  );
}
