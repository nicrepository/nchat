/**
 * ConversationDetailsPanel — the side panel for a channel ("Detalhes do canal",
 * issue #435), an ad-hoc group ("Detalhes do grupo", issue #441) and a 1:1 DM
 * ("Perfil", issue #443).
 *
 * One shell, three vocabularies. The frame — the aside, the heading, the close
 * button, the focus handling and the responsive behaviour — is identical for
 * all three, because those concepts are identical for all three. What differs
 * is the aggregate being described, so the body is selected by the `kind` tag
 * rather than assembled from optional props:
 *  - a channel has visibility, a creation date and members;
 *  - a group has a name, a creation date and participants, and no visibility;
 *  - a 1:1 DM has none of those. It has one other person, so the panel shows a
 *    profile and not conversation metadata: no description, no visibility, no
 *    member count, no roster, no pins and no files. The prototype's DM panel is
 *    a profile card, and rendering a two-person "participants" list there would
 *    describe the conversation instead of the person.
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
 *   The RF-31 thumbnail and video player look like exceptions and are not:
 *   AttachmentThumbnail and AttachmentVideo fetch their bytes through the
 *   authenticated client and show them from an object URL scoped to this
 *   document, so there is still no address anyone could share or reuse, and
 *   neither is drawn for a file the scan has not cleared.
 *
 * The panel is a layout sibling of the conversation, never a modal and never a
 * route: it renders beside the messages so opening it cannot unmount the
 * message list, the composer or the WebSocket subscription.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import "./ConversationDetailsPanel.css";
import AddMembersDialog from "./AddMembersDialog";
import AttachmentThumbnail from "./AttachmentThumbnail";
import AttachmentVideo from "./AttachmentVideo";
import RichTextRenderer from "./RichTextRenderer";
import type {
  AddMembersResult,
  ChannelAttachment,
  ChannelDetails,
  ChannelMemberProfile,
  DirectDetails,
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
import PresenceDot from "./PresenceDot";
import { presenceLabel, presenceTargetKey, usePresence } from "./presence";
import type { ConversationDetailsState } from "./useConversationDetails";
import {
  conversationDetailsPanelId,
  conversationDetailsTitleId,
  formatFileSize,
  formatLocalTime,
  isValidTimeZone,
  localTimeRefreshMs,
  notInformedLabel,
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

/**
 * The one place the three scan states get their words (RF-22).
 *
 * All three are drawn, including `clean`. The badge used to be suppressed for
 * an approved file on the theory that "no news is good news", which left the
 * only visible states meaning "wait" and "blocked" — so a user reading the list
 * could not tell an approved file from one whose badge they had simply missed,
 * and the approval this whole feature exists to establish was the one outcome
 * never shown.
 */
const attachmentStatusLabel: Record<ChannelAttachment["status"], string> = {
  pending_scan: "Em análise",
  clean: "Verificado",
  rejected: "Reprovado",
};

/**
 * Presence has exactly one authority in this client, and it is the realtime
 * store (RF-58).
 *
 * The details endpoint also reports a `presence` field, and this panel used to
 * fall back to it while the store was still unknown. That made the panel a
 * second source of truth: the same person could read "Online" here — from a
 * value fetched once, at whatever moment the request happened to be answered —
 * while the sidebar and the conversation header, which only ever read the
 * store, showed nothing. Two answers for one fact is the bug, and the
 * inconsistency was visible on screen.
 *
 * So the field is deliberately not read. Until the store has an answer this
 * panel says the same thing every other surface says, which is nothing.
 */

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
 * It is a real, focusable <button> rather than something hidden or faked: the
 * affordance stays visible and its unavailability is announced instead of the
 * click silently doing nothing or, far worse, showing a success that never
 * happened.
 *
 * The unavailable state is `aria-disabled`, never the HTML `disabled`
 * attribute. A `disabled` button is removed from the tab order, so the very
 * reason this component exists to convey — the sentence `reasonId` points at —
 * is unreachable for anyone navigating by keyboard: they cannot land on the
 * control, so the description is never announced and the action reads as
 * missing rather than as not-yet-available. `aria-disabled` states the same
 * thing to assistive technology while leaving the control reachable.
 *
 * Nothing can be activated: there is no callback in the props, so no caller can
 * attach one, and `type="button"` keeps a click out of any enclosing form. The
 * handler below is the explicit statement of that — it exists to do nothing on
 * click, Enter, Space and touch alike, since all three routes end at the same
 * click event.
 */
function UnavailableAction({
  label,
  icon,
  reasonId,
  className,
}: {
  label: string;
  icon?: string;
  /**
   * Describes why the action is unavailable. Must be the id of an element that
   * is in the DOM whenever this button is, or the announcement is a dangling
   * reference.
   */
  reasonId: string;
  className: string;
}) {
  return (
    <button
      type="button"
      className={className}
      aria-disabled="true"
      aria-describedby={reasonId}
      onClick={(event) => event.preventDefault()}
    >
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
  /** The conversation this roster belongs to; presence is resolved within it. */
  conversationKey: string;
}

function MemberRow({ member, subtitle, isCurrentUser, conversationKey }: MemberRowProps) {
  const color = avatarColorFor(member.userId);
  const presence = usePresence(member.userId, conversationKey);
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
        <PresenceDot state={presence} size="md" />
      </span>
      <span className="chat-details__member-text">
        <span className="chat-details__member-name">
          {member.displayName}
          {isCurrentUser && <span className="chat-details__badge">Você</span>}
        </span>
        {/* The state as a word, beside the dot rather than instead of it: the
            row survives greyscale, a screen reader and a colour-blind reader. */}
        <span className="chat-details__member-role">
          {subtitle}
          {presence !== "unknown" && ` · ${presenceLabel(presence)}`}
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
          conversationKey={presenceTargetKey("channel", details.id)}
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
          // A group is a dm conversation on the wire, so that is the target its
          // presence is scoped by.
          conversationKey={presenceTargetKey("dm", details.id)}
        />
      ))}
    </ul>
  );
}

// ── 1:1 profile (issue #443) ────────────────────────────────────────────────

/**
 * One row of the profile's metadata card.
 *
 * `value` being empty is the normal case for a field the domain does not
 * record, and the row still renders, reading "Não informado". Dropping it would
 * make the card's shape depend on the data and leave the reader unable to tell
 * "not recorded" from "there is no such field here".
 *
 * Both halves are text nodes. A label is a constant and a value is whatever the
 * server sent — a name, a job title, a department, an address — and none of it
 * is ever interpreted as markup or used to build a URL.
 */
function ProfileMetaRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="chat-details__profile-row">
      <span className="chat-details__profile-row-label">{label}</span>
      <span className="chat-details__profile-row-value">{value || notInformedLabel}</span>
    </div>
  );
}

/**
 * The "Horário local" row.
 *
 * The clock is read from the browser but interpreted in the *profile's* zone,
 * never the viewer's: the instant is universal, the wall-clock reading is not.
 * A missing or unusable zone leaves the row absent rather than quietly
 * substituting the reader's own time, which would be a statement about the
 * wrong person.
 *
 * The timer is what makes this a clock instead of a snapshot of when the panel
 * happened to open, and it is cleared on unmount and whenever the zone changes,
 * so switching conversations cannot leave one running.
 */
function ProfileLocalTimeRow({ timezone }: { timezone?: string }) {
  const [now, setNow] = useState(() => new Date());
  const valid = isValidTimeZone(timezone);

  useEffect(() => {
    if (!valid) return;
    const timer = setInterval(() => setNow(new Date()), localTimeRefreshMs);
    return () => clearInterval(timer);
  }, [valid, timezone]);

  return (
    <ProfileMetaRow label="Horário local" value={valid ? formatLocalTime(now, timezone) : ""} />
  );
}

/**
 * The profile of the other participant of a 1:1 DM.
 *
 * `displayName` is the one field the panel refuses to do without — chatApi
 * rejects a payload lacking it, so reaching here means there is a real person
 * to name. Everything else degrades: the avatar falls back to initials, the
 * presence badge disappears when the server tracks nothing rather than claiming
 * "offline", and each metadata row says "Não informado".
 */
function DirectProfileSection({ details }: { details: DirectDetails }) {
  const profile = details.profile;
  const color = avatarColorFor(profile.userId);
  const presence = usePresence(profile.userId, presenceTargetKey("dm", details.conversationId));
  return (
    <div className="chat-details__profile">
      <span
        className={`chat-details__profile-avatar chat-details__avatar--${color}`}
        aria-hidden="true"
        data-testid="chat-details-profile-avatar"
      >
        {profile.avatarUrl ? (
          <img
            className="chat-details__avatar-img"
            src={profile.avatarUrl}
            alt=""
            referrerPolicy="no-referrer"
          />
        ) : (
          initialsFrom(profile.displayName)
        )}
        <PresenceDot state={presence} size="lg" />
      </span>

      <p className="chat-details__profile-name" data-testid="chat-details-profile-name">
        {profile.displayName}
      </p>
      {profile.jobTitle && <p className="chat-details__profile-role">{profile.jobTitle}</p>}

      {/* Presence is a word, not only a colour: the dot repeats what the text
          already says, so the state survives greyscale and a screen reader.
          Absent while unknown — a badge reading "Offline" before the server has
          answered would be a claim about this person, not a placeholder. */}
      {presence !== "unknown" && (
        <p
          className={`chat-details__profile-status chat-details__profile-status--${presence}`}
          data-testid="chat-details-profile-status"
        >
          <span
            className={`chat-details__status-dot chat-details__status-dot--${presence}`}
            aria-hidden="true"
          />
          {presenceLabel(presence)}
        </p>
      )}

      {/* The prototype's order, which reads from role to contact. */}
      <div className="chat-details__profile-meta" data-testid="chat-details-profile-meta">
        <ProfileMetaRow label="Cargo" value={profile.jobTitle ?? ""} />
        <ProfileMetaRow label="Departamento" value={profile.department ?? ""} />
        <ProfileMetaRow
          label="Fuso horário"
          value={isValidTimeZone(profile.timezone) ? profile.timezone : ""}
        />
        <ProfileLocalTimeRow timezone={profile.timezone} />
        {/* Text, never a mailto: link. Nothing in this issue asks for a compose
            action, and turning an address into a target is a decision of its
            own. */}
        <ProfileMetaRow label="E-mail" value={profile.email ?? ""} />
      </div>

      <UnavailableAction
        label="Ver perfil completo"
        icon="person"
        reasonId="chat-details-profile-unavailable"
        className="chat-details__wide-action"
      />
      <p id="chat-details-profile-unavailable" className="chat-details__note">
        {/*
          /profile is this application's *own* account page, not a directory
          entry for someone else, and no route renders another user's full
          profile. The action therefore stays visible and unavailable with this
          sentence announced as its description, rather than linking somewhere
          that would show the reader their own account.

          The sentence is visible rather than sr-only: it fits the panel's
          existing note style, and a reason worth announcing to a screen reader
          is worth showing to everyone else.
        */}
        O perfil completo de outros usuários ainda não está disponível nesta versão.
      </p>
    </div>
  );
}

/**
 * The frame's wording, for every kind including the profile.
 *
 * Separate from `conversationCopy` below because the frame is the part all
 * three share: a heading and a close button. Keeping the direct variant out of
 * the conversation table is what stops it from acquiring a "participants"
 * heading or a "files" empty state it has no section for.
 */
const panelHeader = {
  channel: { title: "Detalhes do canal", closeLabel: "Fechar detalhes do canal" },
  group: { title: "Detalhes do grupo", closeLabel: "Fechar detalhes do grupo" },
  direct: { title: "Perfil", closeLabel: "Fechar perfil" },
} as const;

/**
 * Per-conversation wording, in one table so a missing case is a type error.
 *
 * `peopleUnavailable` is now about the *full list* only. Adding people is a real
 * flow (issue #398), so the sentence that used to say member management was
 * unavailable would be false; what remains unavailable is "Ver todos", which is
 * the one control still pointing at this note as its reason.
 */
const conversationCopy = {
  channel: {
    peopleHeading: "Membros online",
    peopleUnavailable:
      "A lista completa de membros do canal ainda não está disponível nesta versão.",
    addAction: "Adicionar membros",
    addedNone: "Todas as pessoas selecionadas já participam deste canal.",
    addedOne: "1 pessoa adicionada ao canal.",
    addedMany: (count: number) => `${count} pessoas adicionadas ao canal.`,
    pinEmpty: "Nenhuma mensagem fixada neste canal.",
    filesEmpty: "Nenhum arquivo enviado neste canal.",
    filesUnavailable: "A central de arquivos do canal ainda não está disponível nesta versão.",
  },
  group: {
    peopleHeading: "Participantes",
    peopleUnavailable:
      "A lista completa de participantes do grupo ainda não está disponível nesta versão.",
    addAction: "Adicionar participantes",
    addedNone: "Todas as pessoas selecionadas já participam deste grupo.",
    addedOne: "1 pessoa adicionada ao grupo.",
    addedMany: (count: number) => `${count} pessoas adicionadas ao grupo.`,
    pinEmpty: "Nenhuma mensagem fixada neste grupo.",
    filesEmpty: "Nenhum arquivo enviado neste grupo.",
    filesUnavailable: "A central de arquivos do grupo ainda não está disponível nesta versão.",
  },
} as const;

/**
 * The body of a 1:1 panel: one profile, or the state of trying to load it.
 *
 * The three states are exclusive on purpose. A failed load renders the error
 * and nothing else — never a card of "Não informado" rows, which would present
 * a failure as a person with no attributes. And the heading above stays
 * "Perfil" throughout, so the frame does not flicker between vocabularies while
 * the request is in flight.
 *
 * `details.data.kind` is re-checked rather than assumed: the tag is what the
 * hook recorded about the request it actually made, so a response that survived
 * a conversation switch cannot be rendered here as a profile.
 */
function DirectBody({ details }: { details: ConversationDetailsState["details"] }) {
  if (details.status === "loading") {
    return <SectionMessage role="status">Carregando perfil…</SectionMessage>;
  }
  if (details.status === "error" || details.data.kind !== "direct") {
    return <SectionMessage role="alert">Não foi possível carregar o perfil.</SectionMessage>;
  }
  return <DirectProfileSection details={details.data} />;
}

/**
 * The body of a channel or group panel: about, people, pin and files.
 *
 * Extracted so the direct variant can be a sibling rather than a set of
 * conditionals threaded through four sections. The conversation vocabulary and
 * the profile vocabulary now live in separate functions, and neither can grow a
 * branch for the other by accident.
 */
function ConversationBody({
  kind,
  details: rawDetails,
  files,
  currentUserId,
  latestPin,
  reload,
}: {
  kind: "channel" | "group";
  details: ConversationDetailsState["details"];
  files: ConversationDetailsState["files"];
  currentUserId: string;
  latestPin: PinnedItem | null;
  reload: () => void;
}) {
  const copy = conversationCopy[kind];
  // `kind` is the conversation the user is looking at *now*; the loaded data
  // still describes whichever conversation was open when the request was made.
  // For one render after a switch those disagree — the hook resets on its own
  // effect — so data whose tag does not match is treated as not yet loaded.
  //
  // This is not defensive padding: without it, "ready" plus the wrong variant
  // is read as "the other one of the two", which is how a direct payload would
  // reach the participants section and be asked for a list it does not have.
  const details: ConversationDetailsState["details"] =
    rawDetails.status === "ready" && rawDetails.data.kind !== kind
      ? { status: "loading" }
      : rawDetails;

  // ── Add members (issue #398) ─────────────────────────────────────────────
  //
  // The conversation currently described, or "" while loading. Both the picker
  // and the notice are keyed on it rather than on a boolean, which is what makes
  // confirming into the wrong conversation unrepresentable: the panel is
  // deliberately not remounted on a target switch, so a boolean would survive
  // one and let a dialog opened for A post its selection to B.
  const targetId =
    details.status === "ready" && details.data.kind !== "direct" ? details.data.id : "";
  const canManageMembers =
    details.status === "ready" && details.data.kind !== "direct" && details.data.canManageMembers;

  const addMembersButtonRef = useRef<HTMLButtonElement>(null);
  const [pickerFor, setPickerFor] = useState<string | null>(null);
  const [addedNotice, setAddedNotice] = useState<{ targetId: string; text: string } | null>(null);

  // Open only while the conversation it was opened for is still on screen. The
  // comparison closes it during render — the dialog unmounts, its
  // AbortController cancels any in-flight search or submit, and the selection
  // goes with it. One structural mechanism, no effect.
  const pickerOpen = pickerFor !== null && pickerFor === targetId && targetId !== "";

  const closePicker = useCallback(() => {
    setPickerFor(null);
    // The button is only rendered while the caller may manage members, so the
    // ref can be detached by the time this runs (a refetch that revoked the
    // permission). Focusing a detached node would drop focus to <body>.
    addMembersButtonRef.current?.focus();
  }, []);

  const handleAdded = useCallback(
    (result: AddMembersResult) => {
      closePicker();
      // The server's own numbers, never a local increment: someone else may have
      // added people between the search and this response.
      setAddedNotice({
        targetId,
        text:
          result.added === 0
            ? copy.addedNone
            : result.added === 1
              ? copy.addedOne
              : copy.addedMany(result.added),
      });
      // The single reconciliation path: the response is not merged into the
      // rendered list, the panel refetches. So the roster and both counters come
      // from one authority, and a concurrent members.added refetching too cannot
      // double-count anything.
      reload();
    },
    [closePicker, copy, reload, targetId],
  );

  return (
    <>
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
          ) : details.data.kind === "group" ? (
            <GroupAboutSection details={details.data} />
          ) : null)}
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
              details.data.kind !== "direct" &&
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
          ) : details.data.kind === "group" ? (
            <GroupParticipantsSection details={details.data} currentUserId={currentUserId} />
          ) : null)}
        {/*
          Rendered only once the server has answered and said this caller may
          manage members (issue #398). Loading, error and "not permitted" all
          leave it absent — the safe default, since canManageMembers is false
          unless the server sent exactly true. Hiding it is not the security
          boundary: POST .../members re-derives the decision from the session on
          every call.
        */}
        {canManageMembers && (
          <button
            ref={addMembersButtonRef}
            type="button"
            className="chat-details__wide-action"
            onClick={() => setPickerFor(targetId)}
            data-testid="chat-details-add-members"
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              person_add
            </span>
            {copy.addAction}
          </button>
        )}
        {addedNotice?.targetId === targetId && (
          // Announced rather than shown as a transient toast: the panel above
          // has already been refetched, and this says what changed.
          <p className="chat-details__note" role="status">
            {addedNotice.text}
          </p>
        )}
        <p id="chat-details-people-unavailable" className="chat-details__note">
          {copy.peopleUnavailable}
        </p>
      </section>

      {pickerOpen && (
        <AddMembersDialog
          target={
            kind === "channel"
              ? { kind: "channel", channelId: targetId }
              : { kind: "group", conversationId: targetId }
          }
          /*
            Only the viewer. Current members are excluded by the search endpoint
            itself, in SQL — this list deliberately does not carry the rendered
            roster, because both sections are capped previews and passing them
            made members they could not show appear as selectable.
          */
          excludedUserIds={currentUserId ? [currentUserId] : []}
          onClose={closePicker}
          onAdded={handleAdded}
        />
      )}

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
                  {/* The thumbnail owns its own fetch and object URL; the icon
                      stays exactly as it was and is what shows whenever there
                      is no preview to show. */}
                  <AttachmentThumbnail
                    attachment={file}
                    fallback={
                      <span className="chat-details__file-icon" aria-hidden="true">
                        <span className="material-symbols-outlined">
                          {fileIconFor(file.contentType)}
                        </span>
                      </span>
                    }
                  />
                  <span className="chat-details__file-text">
                    {/* A filename is text. It is never a URL and never markup. */}
                    <span className="chat-details__file-name">{file.filename}</span>
                    <span className="chat-details__file-meta">
                      {file.createdAt &&
                        `${formatDayLabel(file.createdAt)}, ${formatTime(file.createdAt)} · `}
                      {formatFileSize(file.size)}
                      <span
                        className={`chat-details__file-status chat-details__file-status--${file.status}`}
                        data-testid={`chat-details-file-status-${file.id}`}
                      >
                        {attachmentStatusLabel[file.status]}
                      </span>
                    </span>
                  </span>
                  {/* The player is a sibling of the row rather than part of it,
                      so it wraps onto its own line and a file that is not a
                      playable video renders nothing at all — the row keeps its
                      icon, its size and its status untouched. */}
                  <AttachmentVideo attachment={file} />
                </li>
              ))}
            </ul>
          ))}
        <p id="chat-details-files-unavailable" className="chat-details__note">
          {copy.filesUnavailable}
        </p>
      </section>
    </>
  );
}

interface ConversationDetailsPanelProps {
  /**
   * Which vocabulary the frame uses. It is the caller's domain discriminant,
   * resolved from the conversation record — never from the route or the name —
   * and it is available before the data loads, so the heading is correct while
   * the sections are still fetching.
   */
  kind: "channel" | "group" | "direct";
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
  const header = panelHeader[kind];
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
          {header.title}
        </h2>
        <button
          ref={closeButtonRef}
          type="button"
          className="chat-details__close"
          aria-label={header.closeLabel}
          onClick={onClose}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            close
          </span>
        </button>
      </div>

      <div className="chat-details__body">
        {kind === "direct" ? (
          <DirectBody details={details} />
        ) : (
          <ConversationBody
            kind={kind}
            details={details}
            files={files}
            currentUserId={currentUserId}
            latestPin={latestPin}
            reload={state.reload}
          />
        )}
      </div>
    </aside>
  );
}
