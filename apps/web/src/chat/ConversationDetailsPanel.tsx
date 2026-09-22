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

import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";

import "./ConversationDetailsPanel.css";
import AddMembersDialog from "./AddMembersDialog";
import AttachmentThumbnail from "./AttachmentThumbnail";
import AttachmentVideo from "./AttachmentVideo";
import ConversationNameField from "./ConversationNameField";
import ExpandableDetailsSection, {
  SectionMessage,
  type ExpandableSectionContent,
} from "./ExpandableDetailsSection";
import type { ConversationRenameAction } from "./conversationRename";
import RichTextRenderer from "./RichTextRenderer";
import type {
  AddMembersResult,
  ChannelAttachment,
  ChannelDetails,
  ChannelRoster,
  DirectDetails,
  GroupDetails,
  PinnedItem,
} from "./chatTypes";
import ParticipantRow, { type ParticipantRemoval } from "./ParticipantRow";
import RemoveMemberDialog from "./RemoveMemberDialog";
import { useMemberRemoval, type MemberRemovalFlow } from "./useMemberRemoval";
import {
  orderRoster,
  rosterPresenceStates,
  type RosterContext,
  type RosterParticipant,
} from "./participantRosterOrder";
import type { DirectMessageAccess } from "./directMessage";
import {
  avatarColorFor,
  formatDayLabel,
  formatLongDate,
  formatTime,
  initialsFrom,
  senderLabel,
} from "./messageDisplay";
import PresenceDot from "./PresenceDot";
import { presenceLabel, presenceTargetKey, usePresence, usePresenceTarget } from "./presence";
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

/** One "Sobre" row: an icon that decorates, and the text that carries it. */
function AboutRow({
  icon,
  children,
  testId,
}: {
  icon: string;
  children: ReactNode;
  /**
   * Names the row for a test that would otherwise have to match its words. The
   * size row's words are a substring of the roster's "N de M ... carregados."
   * note (issue #895), and two ways of saying the same number are not two
   * elements a test should have to tell apart by text.
   */
  testId?: string;
}) {
  return (
    <p className="chat-details__meta" data-testid={testId}>
      <span className="material-symbols-outlined" aria-hidden="true">
        {icon}
      </span>
      {children}
    </p>
  );
}

/**
 * The part of "Sobre" that a channel and a group answer identically
 * (issue #894): what the conversation is for, when it was created and by whom.
 *
 * Shared because the questions are the same, not to save lines. Only the empty
 * description differs by kind — a channel and a group say different words for
 * the same absence — so that one string is the parameter and nothing else is.
 * What follows these rows does differ (a channel has a visibility and counts
 * members, a group counts participants), and stays with each caller rather than
 * becoming a configurable slot here.
 *
 * Every value is rendered as a text node. `description` is server-side content
 * and the only markup-shaped thing in the block: React escapes it, so a stored
 * `<script>` is five words on screen and never a tag. Nothing here builds HTML,
 * and the line breaks a description may contain are preserved by CSS rather
 * than by interpreting anything.
 *
 * Both absences have a rendering of their own. An unusable creation date reads
 * as unavailable instead of "Criado em " with nothing after it, and an
 * unresolved creator reads as unidentified — never as an id, a slug or part of
 * one, none of which the server even sends.
 */
function AboutMetadata({
  description,
  emptyDescription,
  createdAt,
  creatorDisplayName,
}: {
  description: string;
  emptyDescription: string;
  createdAt: string;
  creatorDisplayName?: string;
}) {
  // formatLongDate answers "" for anything that is not a usable date, which
  // folds "the server sent nothing" and "the server sent something unusable"
  // into the one state the panel can honestly render.
  const createdOn = formatLongDate(createdAt);
  return (
    <>
      <h4 className="chat-details__sublabel">Descrição</h4>
      {description ? (
        <p className="chat-details__description" data-testid="chat-details-description">
          {description}
        </p>
      ) : (
        <p className="chat-details__empty" data-testid="chat-details-description">
          {emptyDescription}
        </p>
      )}
      <AboutRow icon="calendar_today">
        {createdOn ? `Criado em ${createdOn}` : "Data de criação indisponível"}
      </AboutRow>
      <AboutRow icon="person">
        {creatorDisplayName ? `Criado por ${creatorDisplayName}` : "Criador não identificado"}
      </AboutRow>
    </>
  );
}

/**
 * The "Sobre" section of a channel: the shared metadata, then the two facts
 * only a channel has — its visibility and its size.
 *
 * The member total is `memberCount`, the server's own figure for every active
 * member of the channel. It is never `onlineMembers.length` and never
 * `onlineCount`: the preview is capped and presence-filtered, and a channel
 * does not shrink when someone disconnects. The vocabulary stays "membro" here
 * rather than "participante" because it is the word the rest of this surface
 * already uses — the online-members heading, the add-members flow and the
 * channel-details contract — and one block disagreeing with the panel around it
 * would be the inconsistency, not the fix.
 */
function ChannelAboutSection({ details }: { details: ChannelDetails }) {
  return (
    <>
      <AboutMetadata
        description={details.description}
        emptyDescription="Este canal ainda não tem descrição."
        createdAt={details.createdAt}
        creatorDisplayName={details.creatorDisplayName}
      />
      <AboutRow icon={details.type === "private" ? "lock" : "public"}>
        {details.type === "private" ? "Canal privado" : "Canal público"}
      </AboutRow>
      <AboutRow icon="group" testId="chat-details-people-count">
        {details.memberCount === 1 ? "1 membro" : `${details.memberCount} membros`}
      </AboutRow>
    </>
  );
}

/**
 * The "Sobre" section of a group: the shared metadata, then its participant
 * total.
 *
 * Deliberately without visibility: a group is not public or private, it is a
 * closed conversation between the people in it, and rendering a channel's
 * vocabulary here would state something the domain never says. The name moved
 * up to AboutSection with issue #893 — a channel has one too, and both are now
 * rendered by the same field so only one of them can grow a rename.
 *
 * The total is `participantCount`, the figure the same query that produced the
 * preview counted, and never `participants.length`.
 */
function GroupAboutSection({ details }: { details: GroupDetails }) {
  return (
    <>
      <AboutMetadata
        description={details.description}
        emptyDescription="Este grupo ainda não tem descrição."
        createdAt={details.createdAt}
        creatorDisplayName={details.creatorDisplayName}
      />
      <AboutRow icon="group" testId="chat-details-people-count">
        {details.participantCount === 1
          ? "1 participante"
          : `${details.participantCount} participantes`}
      </AboutRow>
    </>
  );
}

/**
 * What a section may honestly say about a collection it only partly holds
 * (issue #895).
 *
 * Both rosters are server-capped previews whose authoritative total arrives
 * beside them, and the two disagree exactly when the conversation is larger than
 * the cap. Expanding then reveals every row the client has and still not every
 * row there is, so a control reading "Ver todos" states something no client
 * work can make true — and this client has no paginated listing to make it true
 * with: the group's participants come from `GET /dm/{id}/details`, capped at
 * `MaxDMDetailsParticipants`, and no route lists the rest (issue #895 §5.1).
 *
 * So the words change and the shortfall is named. "Mostrar mais" promises only
 * what expanding does. The note says how much is in hand, in the caller's own
 * noun, and nothing else: it does not guess where the missing people are, does
 * not call them offline, and does not call them unavailable — it does not know,
 * and neither does anything else on this screen.
 *
 * `loaded === 0` carries no note: the section is showing its empty state, and a
 * sentence counting rows nobody can see would describe a list that is not there.
 */
function previewShortfall(
  total: number,
  loaded: number,
  noun: string,
): { expandLabel?: string; note: string | null } {
  if (total <= loaded) return { note: null };
  return {
    expandLabel: "Mostrar mais",
    note: loaded === 0 ? null : `${loaded} de ${total} ${noun} carregados.`,
  };
}

/**
 * The roster's rows, ordered and keyed, ready for the expansion primitive.
 *
 * Presence is read once per person from the one snapshot `PeopleSection`
 * subscribed to — never by a hook inside each row — so thirty rows are one
 * subscription and the values the sort used are the values the rows draw.
 *
 * Deliberately a function and not a component: the primitive takes a list of
 * rendered rows, and a component here would have to hand one back through a
 * prop instead of returning it.
 */
function rosterItems(
  participants: readonly RosterParticipant[],
  context: RosterContext,
  removal?: ParticipantRemoval,
): ReactNode[] {
  const access = context.openDM;
  // Bound once to this host's claim, so the row never handles an origin and
  // never sees the coordinator's write side.
  const openDM = access
    ? (userId: string) => access.coordinator.open(userId, access.origin)
    : undefined;
  const states = rosterPresenceStates(participants, context.presence);
  const presenceOf = (userId: string) => states.get(userId) ?? "unknown";
  return orderRoster(participants, presenceOf).map((participant) => {
    const isCurrentUser =
      context.currentUserId !== "" && participant.userId === context.currentUserId;
    return (
      <ParticipantRow
        key={participant.userId}
        participant={participant}
        presence={presenceOf(participant.userId)}
        isCurrentUser={isCurrentUser}
        // The viewer's own row activates nothing: there is no conversation to
        // open with yourself, and the flow refuses it anyway.
        onOpenDM={isCurrentUser ? undefined : openDM}
        // The row subscribes for its own participant; nothing about who is
        // pending is computed here, so a request starting for one person does
        // not rebuild the list.
        pendingSource={context.openDM?.coordinator}
        // Never on the viewer's own row (issue #469). Leaving yourself is
        // "Sair da conversa", the server refuses this route for the caller,
        // and a control that can only fail is not an affordance.
        removal={isCurrentUser ? undefined : removal}
      />
    );
  });
}

/**
 * The channel's online-members rows (issue #435): presence-filtered server-side.
 *
 * This is *not* the participant roster issue #895 describes, and the heading it
 * is drawn under still says so. A channel has no roster contract: the details
 * endpoint's `online_members` is filtered by presence inside the query itself —
 * the CTE selects the online subset *before* ORDER BY and LIMIT run — so what
 * arrives is "who is here now", and no amount of client work turns that into
 * "who belongs". Issue #877 owns the authoritative channel membership; until it
 * lands, calling this section "Participantes (N)" would name a list after
 * something it is not.
 *
 * `count` is `onlineCount`, not `onlineMembers.length`: the array is a preview
 * the server caps at `MaxChannelDetailsMembers`, and the two disagree exactly
 * when more people are online than the preview carries. Handing the section
 * both is what lets it say how many there are without claiming it can show them
 * all — the total is drawn beside the heading and never becomes an offer to
 * list them.
 *
 * What issue #895 does deliver here is the row itself: the ordering helper and
 * the navigable identity are presentation, they depend on no membership
 * contract, and a member the server already vouched for is someone this user
 * may open a conversation with. So the rows behave exactly like a group's.
 */
function channelMembersContent(
  details: ChannelDetails,
  context: RosterContext,
  removal?: ParticipantRemoval,
): ExpandableSectionContent {
  return {
    status: "ready",
    count: details.onlineCount,
    items: rosterItems(
      details.onlineMembers.map((member) => ({
        userId: member.userId,
        displayName: member.displayName,
        avatarUrl: member.avatarUrl,
        subtitle: member.role === "moderator" ? "Moderador" : "Membro",
      })),
      context,
      // Everyone here is a chat.channel_members row — the presence filter
      // narrows that population, it does not come from another one — so the
      // removal is as valid on a preview row as on a roster row. It matters
      // while the roster request is in flight, and if it failed.
      removal,
    ),
    // "ninguem online agora", never "este canal nao tem membros" — the
    // channel's size is reported separately and is unaffected.
    empty: <SectionMessage>Nenhum membro online no momento.</SectionMessage>,
  };
}

/**
 * The channel's administrable membership (issue #469), when the server has
 * answered one.
 *
 * This is the section a manager sees instead of the online preview, and the
 * difference is the whole reason the roster route exists: `online_members` is
 * intersected with presence inside the query, so an offline member has no row
 * — and no row means no way to remove them. Everyone else keeps the preview,
 * unchanged, because nothing about #469 decides what a reader should see.
 *
 * `count` is the roster's own total, from the same statement that produced the
 * rows, so the heading and the list cannot describe different sets — and when
 * the channel is larger than the page, `RosterShortfallNote` says so in words
 * rather than letting the heading imply the list is complete. Turning that
 * shortfall into navigation (five compact rows, "Ver todos", the paginated
 * collection) is issue #895's, which owns how this roster is presented.
 *
 * The empty state is about membership and says so. A public channel really can
 * have no explicit members while people read it — that divergence is issue
 * #883's — and the honest sentence is that there is nobody to administer, not
 * that the channel is deserted.
 */
function channelRosterContent(
  roster: ChannelRoster,
  context: RosterContext,
  removal: ParticipantRemoval,
): ExpandableSectionContent {
  return {
    status: "ready",
    count: roster.memberCount,
    items: rosterItems(
      roster.members.map((member) => ({
        userId: member.userId,
        displayName: member.displayName,
        avatarUrl: member.avatarUrl,
        subtitle: member.role === "moderator" ? "Moderador" : "Membro",
      })),
      context,
      removal,
    ),
    empty: <SectionMessage>Nenhum membro para administrar neste canal.</SectionMessage>,
  };
}

/**
 * The group's participant rows (issue #441), which issue #895 turns into the
 * roster the panel was specified to have.
 *
 * Every active participant appears, online or not: the server's own query
 * applies no presence predicate, so presence is shown beside a participant and
 * used to order them, never to decide whether they are shown. That is the whole
 * difference from the channel section above, and it is a difference of
 * contract, not of rendering — which is why both sections share the row and the
 * ordering and nothing else.
 *
 * `participantCount` is the total, and it is presentation for the same reason
 * `onlineCount` is above: the array is a capped preview, no loader exists for
 * the remainder, and so no control claims one. It is the same query's own
 * `COUNT(*) OVER ()`, so the heading's number and the rows below it cannot
 * describe different sets of people.
 */
function groupParticipantsContent(
  details: GroupDetails,
  context: RosterContext,
  removal?: ParticipantRemoval,
): ExpandableSectionContent {
  return {
    status: "ready",
    count: details.participantCount,
    items: rosterItems(
      details.participants.map((participant) => ({
        userId: participant.userId,
        displayName: participant.displayName,
        avatarUrl: participant.avatarUrl,
        subtitle: "Participante",
      })),
      context,
      removal,
    ),
    empty: <SectionMessage>Nenhum participante para exibir.</SectionMessage>,
  };
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
 * There is no longer a "still unavailable" sentence for the people or the files
 * section (issue #892). Both controls used to be `UnavailableAction`s described
 * by one, and both now expand for real when there is anything to expand to — a
 * reason for an unavailability that no longer exists would be a false statement,
 * and a control that reveals nothing is exactly what #892 removed.
 */
const conversationCopy = {
  channel: {
    peopleHeading: "Membros online",
    peopleLabel: "Membros online do canal",
    // What the same section is called once it shows the administrable
    // membership instead of the presence preview (issue #469).
    membersHeading: "Membros",
    membersLabel: "Membros do canal",
    addAction: "Adicionar membros",
    addedNone: "Todas as pessoas selecionadas já participam deste canal.",
    addedOne: "1 pessoa adicionada ao canal.",
    addedMany: (count: number) => `${count} pessoas adicionadas ao canal.`,
    pinEmpty: "Nenhuma mensagem fixada neste canal.",
    filesEmpty: "Nenhum arquivo enviado neste canal.",
  },
  group: {
    peopleHeading: "Participantes",
    peopleLabel: "Participantes do grupo",
    // A group's participants already are its membership, so administering it
    // changes nothing about what the section is called.
    membersHeading: "Participantes",
    membersLabel: "Participantes do grupo",
    addAction: "Adicionar participantes",
    addedNone: "Todas as pessoas selecionadas já participam deste grupo.",
    addedOne: "1 pessoa adicionada ao grupo.",
    addedMany: (count: number) => `${count} pessoas adicionadas ao grupo.`,
    pinEmpty: "Nenhuma mensagem fixada neste grupo.",
    filesEmpty: "Nenhum arquivo enviado neste grupo.",
  },
} as const;

/**
 * One row of the recent-files list.
 *
 * Extracted so the files section hands the shared primitive a list of rows and
 * nothing else: the scan badge, the thumbnail and the player are this row's
 * concern and stay entirely inside it (RF-22, RF-31).
 */
function FileRow({ file }: { file: ChannelAttachment }) {
  return (
    <li className="chat-details__file">
      {/* The thumbnail owns its own fetch and object URL; the icon stays exactly
          as it was and is what shows whenever there is no preview to show. */}
      <AttachmentThumbnail
        attachment={file}
        fallback={
          <span className="chat-details__file-icon" aria-hidden="true">
            <span className="material-symbols-outlined">{fileIconFor(file.contentType)}</span>
          </span>
        }
      />
      <span className="chat-details__file-text">
        {/* A filename is text. It is never a URL and never markup. */}
        <span className="chat-details__file-name">{file.filename}</span>
        <span className="chat-details__file-meta">
          {file.createdAt && `${formatDayLabel(file.createdAt)}, ${formatTime(file.createdAt)} · `}
          {formatFileSize(file.size)}
          <span
            className={`chat-details__file-status chat-details__file-status--${file.status}`}
            data-testid={`chat-details-file-status-${file.id}`}
          >
            {attachmentStatusLabel[file.status]}
          </span>
        </span>
      </span>
      {/* The player is a sibling of the row's text rather than part of it, so it
          wraps onto its own line and a file that is not a playable video renders
          nothing at all — the row keeps its icon, its size and its status. */}
      <AttachmentVideo attachment={file} />
    </li>
  );
}

/**
 * Everything the people section needs to decide what to draw.
 *
 * One value instead of five positional arguments, because the five are not
 * independent: `roster` is only ever non-null when `removal` is present, and
 * both only ever apply to the aggregate `details` already identifies. Passing
 * them together is what lets the reader see that.
 */
interface PeopleView {
  kind: "channel" | "group";
  details: ConversationDetailsState["details"];
  /**
   * The channel's administrable membership, or null (issue #469).
   *
   * Null covers every case in which the preview stays: a group, a caller
   * without the capability, and the moment before the roster request answers.
   * A failed roster request is also null — the preview is still correct, just
   * narrower, and blanking the section would be the worse outcome.
   */
  roster: ChannelRoster | null;
  context: RosterContext;
  removal?: ParticipantRemoval;
}

/** The words each aggregate uses while its people are loading or failed. */
const peopleSectionMessages = {
  channel: { loading: "Carregando membros…", error: "Não foi possível carregar os membros." },
  group: {
    loading: "Carregando participantes…",
    error: "Não foi possível carregar os participantes.",
  },
} as const;

/**
 * The people section's content, in the vocabulary of whichever conversation is
 * loaded (issues #435, #441, #469).
 *
 * The payload's three states, in that order, and then the one decision that is
 * left (issue #469): for a channel, the administrable membership replaces the
 * presence preview — but only for a caller who may act on it, and only once
 * the roster is actually in hand.
 *
 * The direct variant cannot arrive here: `ConversationBody` has already turned
 * "ready, but tagged for the other aggregate" into loading. It is still named,
 * because that is what narrows `details.data` to a channel for the line below
 * — a cast would assert the same thing without the type system checking it.
 */
function peopleContent(view: PeopleView): ExpandableSectionContent {
  const { kind, details, roster, context, removal } = view;
  const words = peopleSectionMessages[kind];
  if (details.status === "error") return { status: "error", message: words.error };
  if (details.status !== "ready") return { status: "loading", message: words.loading };
  if (details.data.kind === "group") {
    return groupParticipantsContent(details.data, context, removal);
  }
  if (details.data.kind === "direct") return { status: "loading", message: words.loading };
  return roster && removal
    ? channelRosterContent(roster, context, removal)
    : channelMembersContent(details.data, context, removal);
}

/**
 * The recent-files section's content (issue #892).
 *
 * No `count` and no `hasMore`: the list endpoint reports neither a total nor a
 * cursor, and the panel asks it for exactly `channelFilesPreviewLimit` rows. So
 * this section is structurally expandable and, with today's contract, never has
 * anything to expand to — which is why it shows no control at all rather than
 * one that would reveal nothing.
 */
function filesContent(
  files: ConversationDetailsState["files"],
  emptyText: string,
): ExpandableSectionContent {
  if (files.status === "loading") {
    return { status: "loading", message: "Carregando arquivos…" };
  }
  if (files.status === "error") {
    return { status: "error", message: "Não foi possível carregar os arquivos." };
  }
  return {
    status: "ready",
    items: files.data.map((file) => <FileRow key={file.id} file={file} />),
    empty: (
      <p className="chat-details__empty" data-testid="chat-details-files-empty">
        {emptyText}
      </p>
    ),
  };
}

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

type ConversationCopy = (typeof conversationCopy)[keyof typeof conversationCopy];

/** What one add-members call is reported as, in the conversation's vocabulary. */
function addedText(copy: ConversationCopy, added: number): string {
  if (added === 0) return copy.addedNone;
  if (added === 1) return copy.addedOne;
  return copy.addedMany(added);
}

/**
 * The name of the conversation the panel is describing, and the inline rename
 * it offers (issue #893).
 *
 * Rendered from the loaded payload rather than from a prop, so the name shown
 * here is the same projection the rest of the section describes, and it
 * converges the way every other field does — by refetching, which
 * useReloadOnRename asks for when the canonical name of this target moves.
 *
 * Keyed by the target, because the panel is deliberately *not* remounted when
 * the conversation changes under it. Without the key an editor opened for one
 * conversation would stay open, with its draft and its error, under the next
 * one's name. The remount React already offers is the whole mechanism.
 */
function ConversationName({
  kind,
  details,
  onRename,
}: {
  kind: "channel" | "group";
  details: ConversationDetailsState["details"];
  onRename?: ConversationRenameAction;
}) {
  if (details.status !== "ready" || details.data.kind === "direct") return null;
  return (
    <ConversationNameField
      key={`name-${details.data.id}`}
      kind={kind}
      name={details.data.name}
      onRename={onRename}
    />
  );
}

/**
 * The "Sobre" section: the metadata card, or the state of trying to load it.
 *
 * The three outcomes are exclusive, and the fourth — ready with a payload
 * tagged for the other aggregate — cannot arrive: `ConversationBody` has
 * already folded it into loading.
 */
function AboutSection({
  kind,
  details,
  onRename,
}: {
  kind: "channel" | "group";
  details: ConversationDetailsState["details"];
  onRename?: ConversationRenameAction;
}) {
  return (
    <section className="chat-details__section" aria-labelledby="chat-details-about">
      <h3 id="chat-details-about" className="chat-details__label">
        Sobre
      </h3>
      <ConversationName kind={kind} details={details} onRename={onRename} />
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
  );
}

/** The one pinned message the panel and the bar above the conversation share. */
function PinnedMessageSection({
  latestPin,
  emptyText,
}: {
  latestPin: PinnedItem | null;
  emptyText: string;
}) {
  return (
    <section className="chat-details__section" aria-labelledby="chat-details-pin">
      <h3 id="chat-details-pin" className="chat-details__label">
        Mensagem fixada
      </h3>
      {latestPin === null ? (
        <p className="chat-details__empty" data-testid="chat-details-pin-empty">
          {emptyText}
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
  );
}

/**
 * The conversation currently described and what this caller may do to it.
 *
 * "" while loading, and "" for a direct payload — which cannot reach here, but
 * the type permits it and an id borrowed from the wrong aggregate would be the
 * worst possible default. `canManage` is the server's own answer, never
 * inferred: a rendering hint only, since POST .../members re-derives it from
 * the session on every call (issue #398).
 */
function manageableTarget(details: ConversationDetailsState["details"]): {
  id: string;
  name: string;
  canManage: boolean;
  canRemove: boolean;
} {
  if (details.status !== "ready" || details.data.kind === "direct") {
    return { id: "", name: "", canManage: false, canRemove: false };
  }
  return {
    id: details.data.id,
    // The server's own name for the conversation, which the removal
    // confirmation states so it cannot be about the wrong one.
    name: details.data.name,
    canManage: details.data.canManageMembers,
    // The server's separate answer for removal (issue #469). Read, never
    // derived: in a group the two differ, and in a channel they are two
    // questions that happen to share a predicate today.
    canRemove: details.data.canRemoveMembers,
  };
}

/**
 * The sentence naming how much of a roster the client holds, or nothing.
 *
 * Its own unit because it is its own statement: the heading says how many
 * people the conversation has, the list shows the ones in hand, and this is the
 * only place the difference between those two is put into words.
 */
function RosterShortfallNote({ note }: { note: string | null }) {
  if (!note) return null;
  return (
    <p className="chat-details__note" data-testid="chat-details-roster-shortfall">
      {note}
    </p>
  );
}

/**
 * The conversation presence is resolved within.
 *
 * A group is a `chat.dm_conversations` row on the wire, so its presence is
 * scoped by the dm target even though this panel calls it a group; a channel is
 * its own kind. One place, because the roster and anything else that asks has to
 * ask about the same conversation.
 */
function rosterPresenceKey(kind: "channel" | "group", targetId: string): string {
  return presenceTargetKey(kind === "channel" ? "channel" : "dm", targetId);
}

/**
 * How much of this conversation's roster the client is actually holding, in the
 * vocabulary of whichever aggregate is loaded (issue #895).
 *
 * Both totals are the server's own figure for the whole collection and both
 * arrays are its capped preview, so the same comparison answers for a channel
 * and for a group — but the noun is not the same, and neither is which pair of
 * fields to read. That is the only thing this decides; the words and the rule
 * live in `previewShortfall`.
 *
 * Nothing is claimed before the payload has arrived: while loading or on a
 * failure the section says nothing about what it is missing, because it does
 * not yet know what it has.
 */
function rosterShortfall(
  details: ConversationDetailsState["details"],
  roster: ChannelRoster | null,
): {
  expandLabel?: string;
  note: string | null;
} {
  // The administrable membership, when it is what the section is showing: its
  // own total, its own noun, and the same capped-page rule (issue #469).
  if (roster) return previewShortfall(roster.memberCount, roster.members.length, "membros");
  if (details.status !== "ready") return { note: null };
  if (details.data.kind === "channel") {
    return previewShortfall(
      details.data.onlineCount,
      details.data.onlineMembers.length,
      "membros online",
    );
  }
  if (details.data.kind === "group") {
    return previewShortfall(
      details.data.participantCount,
      details.data.participants.length,
      "participantes",
    );
  }
  return { note: null };
}

/**
 * What this section is called, which depends on what it is showing.
 *
 * A manager of a channel is shown its membership, and calling that "Membros
 * online" would name the list after a filter it no longer has (issue #469).
 * Everything else keeps the wording it already had.
 */
function peopleSectionWords(
  copy: ConversationCopy,
  roster: ChannelRoster | null,
): { heading: string; label: string } {
  if (roster) return { heading: copy.membersHeading, label: copy.membersLabel };
  return { heading: copy.peopleHeading, label: copy.peopleLabel };
}

/**
 * The administrable membership this section should draw, or null (issue #469).
 *
 * Null is every case in which the presence preview stays: a group, whose
 * participants already are its membership; a caller the server did not grant
 * the capability to, who never requested a roster; and the moment before the
 * request answers — or after it failed, where the preview is still correct,
 * only narrower.
 */
function administrableRoster(
  kind: "channel" | "group",
  canRemove: boolean,
  roster: ConversationDetailsState["roster"],
): ChannelRoster | null {
  if (kind !== "channel" || !canRemove || roster.status !== "ready") return null;
  return roster.data;
}

/**
 * The removal action every row in this conversation gets, or nothing.
 *
 * The noun is the conversation's own word, because the control's accessible
 * name says where the person is being removed from. Which rows actually get it
 * is decided further down — the viewer's own never does.
 */
function rowRemovalFor(
  kind: "channel" | "group",
  canRemove: boolean,
  request: MemberRemovalFlow["request"],
): ParticipantRemoval | undefined {
  if (!canRemove) return undefined;
  return { noun: kind === "channel" ? "canal" : "grupo", onRemove: request };
}

/**
 * The confirmation, mounted only while a member is under it (issue #469).
 *
 * Its own component so the section that renders a list does not also decide
 * what a removal costs: the one fact it has to look up — whether this is a
 * private channel, the only conversation where losing the membership really
 * does revoke reading — is read from the loaded payload here, and nowhere
 * else.
 */
function MemberRemovalDialog({
  kind,
  details,
  conversationName,
  removal,
}: {
  kind: "channel" | "group";
  details: ConversationDetailsState["details"];
  conversationName: string;
  removal: MemberRemovalFlow;
}) {
  if (!removal.member) return null;
  const isPrivateChannel =
    details.status === "ready" &&
    details.data.kind === "channel" &&
    details.data.type === "private";
  return (
    <RemoveMemberDialog
      kind={kind}
      isPrivateChannel={isPrivateChannel}
      conversationName={conversationName}
      member={removal.member}
      onClose={removal.cancel}
      onConfirm={removal.confirm}
    />
  );
}

/**
 * The people section: the roster, the add-members flow that acts on it, and
 * nothing else (issues #398, #892).
 *
 * It owns the add flow's state rather than the panel, so `ConversationBody`
 * stays an arrangement of sections instead of the state machine of each one.
 * The roster itself is drawn by the shared primitive, which is handed rows and
 * a status and knows neither what a member is nor who may add one.
 */
function PeopleSection({
  kind,
  details,
  roster,
  currentUserId,
  copy,
  reload,
  openDM,
}: {
  kind: "channel" | "group";
  details: ConversationDetailsState["details"];
  /** The channel's administrable membership section (issue #469). */
  roster: ConversationDetailsState["roster"];
  currentUserId: string;
  copy: ConversationCopy;
  reload: () => void;
  openDM?: DirectMessageAccess;
}) {
  // Both the picker and the notice are keyed on the conversation rather than on
  // a boolean, which is what makes confirming into the wrong conversation
  // unrepresentable: the panel is deliberately not remounted on a target
  // switch, so a boolean would survive one and let a dialog opened for A post
  // its selection to B.
  const { id: targetId, name: conversationName, canManage, canRemove } = manageableTarget(details);

  /*
    One subscription for the whole roster, scoped to this conversation, read
    here rather than by each row.

    Two properties at once. A list that *orders itself* by presence has to read
    everybody's state in one consistent pass — thirty rows each subscribing on
    their own would each re-render alone, and the sort would be over values
    sampled at thirty different moments. And the subscription is per
    conversation, so a presence frame about a conversation this panel is not
    describing returns the identical snapshot and React bails out: neither the
    sort nor a single row runs again. The rows below receive their presence
    already resolved — see rosterItems.
  */
  const presence = usePresenceTarget(rosterPresenceKey(kind, targetId));

  const addMembersButtonRef = useRef<HTMLButtonElement>(null);
  const [pickerFor, setPickerFor] = useState<string | null>(null);
  const [addedNotice, setAddedNotice] = useState<{ targetId: string; text: string } | null>(null);

  // The removal flow, with focus landing on the add-members control once a
  // removed row is gone: it is the one control in this section that outlives
  // the list it acts on (issue #469).
  const removal = useMemberRemoval({
    kind,
    targetId,
    reload,
    fallbackFocusRef: addMembersButtonRef,
  });
  const channelRoster = administrableRoster(kind, canRemove, roster);
  const rowRemoval = rowRemovalFor(kind, canRemove, removal.request);
  const shortfall = rosterShortfall(details, channelRoster);
  const sectionWords = peopleSectionWords(copy, channelRoster);

  // Open only while the conversation it was opened for is still on screen. The
  // comparison closes it during render — the dialog unmounts, its
  // AbortController cancels any in-flight search or submit, and the selection
  // goes with it. One structural mechanism, no effect.
  const pickerOpen = pickerFor !== null && pickerFor === targetId && targetId !== "";

  // Plain functions rather than useCallback: AddMembersDialog only calls them
  // — it keeps neither in an effect's dependencies and is not memoized — so
  // their identity buys nothing, and after issue #469 added a second flow to
  // this section the compiler reports it can no longer preserve the
  // memoization anyway.
  function closePicker() {
    setPickerFor(null);
    // The button is only rendered while the caller may manage members, so the
    // ref can be detached by the time this runs (a refetch that revoked the
    // permission). Focusing a detached node would drop focus to <body>.
    addMembersButtonRef.current?.focus();
  }

  function handleAdded(result: AddMembersResult) {
    closePicker();
    // The server's own numbers, never a local increment: someone else may have
    // added people between the search and this response.
    setAddedNotice({ targetId, text: addedText(copy, result.added) });
    // The single reconciliation path: the response is not merged into the
    // rendered list, the panel refetches. So the roster and both counters come
    // from one authority, and a concurrent members.added refetching too cannot
    // double-count anything.
    reload();
  }

  return (
    <>
      {/*
        Keyed by the conversation. The panel is deliberately not remounted on a
        target switch, so without this the roster expanded for one conversation
        would stay expanded under the next one's name — and that expansion is a
        statement about a list that no longer exists. The remount React already
        offers is the whole mechanism; there is no reset protocol to maintain.
      */}
      <ExpandableDetailsSection
        key={`people-${targetId}`}
        title={sectionWords.heading}
        listLabel={sectionWords.label}
        /*
          Left undefined whenever the preview is the whole collection, which is
          what keeps "Ver todos" as the default wording for every section that
          can genuinely show everything.
        */
        expandLabel={shortfall.expandLabel}
        content={peopleContent({
          kind,
          details,
          roster: channelRoster,
          context: { presence, currentUserId, openDM },
          removal: rowRemoval,
        })}
      >
        {/*
          Named before the actions, directly under the list it is about: how many
          of the conversation's people this client is holding. The heading above
          already says how many there are.
        */}
        <RosterShortfallNote note={shortfall.note} />
        {/*
          Rendered only once the server has answered and said this caller may
          manage members. Loading, error and "not permitted" all leave it absent
          — the safe default, since canManageMembers is false unless the server
          sent exactly true. Hiding it is not the security boundary.
        */}
        {canManage && (
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
        {removal.notice !== "" && (
          /*
            A removal is announced rather than only seen (issue #469): the row
            it happened to is gone from the list, and focus has moved to the
            control above — neither of which says anything to someone who is
            not looking at the panel.
          */
          <p className="chat-details__note" role="status">
            {removal.notice}
          </p>
        )}
      </ExpandableDetailsSection>

      <MemberRemovalDialog
        kind={kind}
        details={details}
        conversationName={conversationName}
        removal={removal}
      />

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
    </>
  );
}

/**
 * The body of a channel or group panel: about, people, pin and files.
 *
 * Extracted so the direct variant can be a sibling rather than a set of
 * conditionals threaded through four sections. The conversation vocabulary and
 * the profile vocabulary now live in separate functions, and neither can grow a
 * branch for the other by accident.
 *
 * Each section decides its own content; this function decides only which
 * sections exist and in what order.
 */
function ConversationBody({
  kind,
  details: rawDetails,
  files,
  roster,
  currentUserId,
  latestPin,
  reload,
  onRename,
  openDM,
}: {
  kind: "channel" | "group";
  details: ConversationDetailsState["details"];
  files: ConversationDetailsState["files"];
  /** The channel's administrable membership (issue #469). */
  roster: ConversationDetailsState["roster"];
  currentUserId: string;
  latestPin: PinnedItem | null;
  reload: () => void;
  onRename?: ConversationRenameAction;
  openDM?: DirectMessageAccess;
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

  return (
    <>
      <AboutSection kind={kind} details={details} onRename={onRename} />
      <PeopleSection
        kind={kind}
        details={details}
        roster={roster}
        currentUserId={currentUserId}
        copy={copy}
        reload={reload}
        openDM={openDM}
      />
      <PinnedMessageSection latestPin={latestPin} emptyText={copy.pinEmpty} />
      <ExpandableDetailsSection
        key={`files-${manageableTarget(details).id}`}
        title="Arquivos recentes"
        listLabel="Arquivos recentes"
        content={filesContent(files, copy.filesEmpty)}
      />
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
  /**
   * Renames the conversation this panel describes (issue #893).
   *
   * Absent means no rename affordance at all, which is how the caller states
   * every case that has none: a 1:1, the workspace's general channel, a
   * channel the server did not authorize, and a host with no mutation wired.
   * It is presentation only — PATCH re-derives the caller's authority from the
   * session — and the panel never decides it here, because the server's
   * capability lives in the canonical sidebar payload the caller holds.
   */
  onRename?: ConversationRenameAction;
  /**
   * Opens a direct conversation with a roster participant (issue #895).
   *
   * The capability, never the request: resolving or creating a DM is one
   * operation with rules this panel does not own — self is refused, a recipient
   * already being resolved is not resolved twice, and a reply that outlives the
   * conversation it was asked from navigates nowhere. The panel's hosts already
   * run that flow for mentions and message authors, so they hand it down rather
   * than letting a second copy of it grow here.
   *
   * Absent means the host has not wired it, and the roster then shows people
   * without offering to open conversations with them — which is honest, rather
   * than rows that look activatable and do nothing.
   *
   * No error is carried on it at all. The flow has one owner and one place that
   * reports a refusal — the shell — because this panel and the conversation
   * behind it are both on screen and both used to draw the same sentence, which
   * is one failure announced twice.
   *
   * Its identity is stable: a request starting, finishing or failing does not
   * replace it, so nothing in this panel is rebuilt by one.
   */
  openDM?: DirectMessageAccess;
  onClose: () => void;
}

export default function ConversationDetailsPanel({
  kind,
  state,
  currentUserId,
  latestPin,
  onRename,
  openDM,
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
  const roster = state.roster;

  // Escape closes the panel (issue #467). It matters most where the panel covers
  // the conversation instead of sitting beside it, but the gesture is the same in
  // both compositions, so it is wired once rather than being switched on by
  // width.
  //
  // The containment check is not defensive noise: React bubbles a portal's
  // events to its React parent rather than its DOM one, so without it the
  // Escape that dismisses a dialog this panel opened — the member picker, the
  // edit history — would close the panel out from under it, and the focus that
  // dialog restores would land on an element that no longer exists.
  function handleKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    if (event.key !== "Escape") return;
    if (!event.currentTarget.contains(event.target as Node)) return;
    event.stopPropagation();
    onClose();
  }

  return (
    <aside
      id={conversationDetailsPanelId}
      className="chat-details"
      aria-labelledby={conversationDetailsTitleId}
      data-testid="chat-conversation-details"
      data-conversation-kind={kind}
      onKeyDown={handleKeyDown}
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
            roster={roster}
            currentUserId={currentUserId}
            latestPin={latestPin}
            reload={state.reload}
            onRename={onRename}
            openDM={openDM}
          />
        )}
      </div>
    </aside>
  );
}
