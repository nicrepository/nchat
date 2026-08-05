import { expect, type Locator, type Page, type Route, type TestInfo } from "@playwright/test";

export const CURRENT_USER_ID = "e2e-author";
export const CURRENT_USER_NAME = "E2E Autor";
export const OTHER_USER_ID = "e2e-participant";
export const OTHER_USER_NAME = "E2E Participante";
export const GROUP_DM_ID = "e2e-dm-group";
export const GROUP_DM_NAME = "E2E Grupo";
export const OTHER_CHANNEL_ID = "e2e-channel-other";
export const OTHER_CHANNEL_NAME = "Canal E2E";
// Extra fixture candidates so a new ad-hoc group (which needs the caller plus
// at least two others) can be created without inventing IDs per spec file.
export const SECOND_CANDIDATE_ID = "e2e-candidate-two";
export const SECOND_CANDIDATE_NAME = "E2E Candidata Dois";
export const THIRD_CANDIDATE_ID = "e2e-candidate-three";
export const THIRD_CANDIDATE_NAME = "E2E Candidata Três";

type TargetKind = "channel" | "dm";
type ConversationAccessGuard = (targetId: string) => boolean;

/**
 * The two ordering keys the real sidebar sorts each section by (issue #414).
 * Optional here because most specs do not care where a row lands, and the app
 * must render a payload that carries neither.
 */
interface SidebarActivityFixture {
  created_at?: string;
  last_message_at?: string | null;
}

interface SidebarChannelFixture extends SidebarActivityFixture {
  id: string;
  slug: string;
  display_name: string;
  type: "public" | "private";
  can_write: boolean;
  unread_count: number;
}

interface SidebarDMFixture extends SidebarActivityFixture {
  id: string;
  type: "direct" | "group";
  name: string;
  unread_count: number;
  /**
   * The other participant of a 1:1, as the real sidebar resolves it. Absent on
   * groups and on legacy conversations whose counterpart could not be resolved
   * — both are shapes the UI must cope with, so neither is defaulted here.
   */
  counterpart?: { user_id: string; display_name: string; avatar_url?: string };
}

export interface DMCandidateFixture {
  userId: string;
  displayName: string;
}

interface RawMessage {
  id: string;
  sender_id: string;
  sender_display_name: string;
  sender_email: string;
  kind: "user" | "system";
  body_text?: string;
  body_format: "v1" | "v2" | "v3";
  status: "active" | "deleted";
  is_removed: boolean;
  created_at: string;
  updated_at: string;
  edited_at?: string | null;
  edit_count: number;
  is_edited: boolean;
  deleted_at?: string | null;
  reactions: Array<{ emoji: string; count: number; reacted_by_me: boolean }>;
  is_favorited: boolean;
  is_forwarded: boolean;
  quoted?: RawQuote;
  reference?: RawReference;
}

interface RawReference {
  available: boolean;
  message_id?: string;
  target_type?: TargetKind;
  target_id?: string;
  target_label?: string;
  author_display_name?: string;
  body?: string;
  body_format?: "v1" | "v2" | "v3";
  created_at?: string;
}

interface RawQuote {
  id: string;
  author_id: string;
  body: string;
  body_format: "v1" | "v2" | "v3";
  is_removed: boolean;
  deleted_at?: string | null;
  created_at: string;
}

interface MessagingScenarioOptions {
  kind: TargetKind;
  conversationType?: SidebarDMFixture["type"];
  targetId: string;
  targetName: string;
  messages: RawMessage[];
  editWindowExpiredIds?: string[];
  /** People returned by GET /dm-candidates, used by "nova conversa" specs. */
  dmCandidates?: DMCandidateFixture[];
}

interface PatchRequest {
  messageId: string;
  method: string;
  endpoint: string;
  body: unknown;
  body_format: unknown;
  raw: Record<string, unknown>;
}

export interface MessagingScenario {
  kind: TargetKind;
  targetId: string;
  targetName: string;
  messagesByTarget: Map<string, RawMessage[]>;
  requests: {
    channelPosts: Array<{
      body_text?: string;
      parent_message_id?: string;
      referenced_message_id?: string;
    }>;
    dmPosts: Array<{
      body_text?: string;
      parent_message_id?: string;
      referenced_message_id?: string;
    }>;
    forwards: Array<{
      destinationChannelId: string;
      sourceMessageId?: string;
      idempotencyKey?: string;
      raw: Record<string, unknown>;
    }>;
    patches: PatchRequest[];
    deletes: string[];
    favorites: Array<{ messageId: string; action: "add" | "remove" }>;
    pins: Array<{ messageId: string; targetId: string; action: "add" | "remove" }>;
    reactions: Array<{ messageId: string; emoji: string; added: boolean }>;
    dmCreates: Array<{ otherUserId: string }>;
    groupCreates: Array<{ participantUserIds: string[]; title: string }>;
  };
  forwardedByIdempotencyKey: Map<
    string,
    { destinationChannelId: string; sourceMessageId: string; message: RawMessage }
  >;
  // Mutable sidebar fixture. Starts with the same defaults the sidebar route
  // always served; DM/group creation mocks append to it so a post-creation
  // refetch (ChatSidebar's retry()) reflects the new conversation.
  sidebarChannels: SidebarChannelFixture[];
  sidebarDMs: SidebarDMFixture[];
  // Candidates returned by GET /dm-candidates.
  dmCandidates: DMCandidateFixture[];
  // Pinned message IDs per "kind:targetId" key.
  pinnedIds: Map<string, Set<string>>;
  // Channel-details payload per channel id (issue #435).
  channelDetails: Map<string, ChannelDetailsFixture>;
  // Group-details payload per conversation id (issues #441, #398).
  groupDetails: Map<string, GroupDetailsFixture>;
  // The *complete* membership per target (issue #398), which the details
  // fixtures deliberately do not carry: a channel preview shows only online
  // members and a group preview is capped, and the point of the contextual
  // search is that the server knows the rest.
  channelMemberships: Map<string, Set<string>>;
  groupMemberships: Map<string, Set<string>>;
  // Add-members calls the app made, and the status the mock should answer with
  // (issue #398). Recording the body is what lets a spec assert that only user
  // IDs were sent.
  addMembersRequests: Array<{ channelId: string; userIds: string[] }>;
  addMembersStatus: number;
  // Channel attachments per channel id, newest first, as the server returns them.
  channelAttachments: Map<string, AttachmentFixture[]>;
  // Group-details payload per conversation id (issue #441).
  groupDetails: Map<string, GroupDetailsFixture>;
  // 1:1 profile payload per conversation id (issue #443).
  directProfiles: Map<string, DirectProfileFixture>;
  // Conversation attachments per conversation id, newest first.
  conversationAttachments: Map<string, AttachmentFixture[]>;
}

export interface GroupParticipantFixture {
  user_id: string;
  display_name: string;
  presence?: "online" | "away" | "offline";
}

/**
 * A group's details. Deliberately without visibility, slug or description: a
 * chat.dm_conversations row has none of them, and the panel must never show a
 * channel's vocabulary for a group.
 */
export interface GroupDetailsFixture {
  id: string;
  type: "group";
  name: string;
  created_at: string;
  /** Every active participant; may exceed participants.length. */
  participant_count: number;
  participants: GroupParticipantFixture[];
  /**
   * Whether the server would let this caller add participants (issue #398).
   * Always sent, so a spec that omits it exercises the safe default: absent
   * reads as false and the action stays hidden.
   */
  can_manage_members: boolean;
}

/**
 * A 1:1 conversation's profile payload.
 *
 * Deliberately a profile and not a roster: the server has already decided which
 * of the two participants the caller is looking at, so there is nothing here
 * for a client to choose from. job_title, department and timezone are optional
 * because no column stores them today — a fixture that always sent them would
 * test a contract the server cannot honour.
 */
export interface DirectProfileFixture {
  kind: "direct";
  conversation_id: string;
  profile: {
    user_id: string;
    display_name: string;
    avatar_url?: string;
    email?: string;
    presence?: "online" | "away" | "offline";
    job_title?: string;
    department?: string;
    timezone?: string;
  };
}

export interface ChannelMemberFixture {
  user_id: string;
  display_name: string;
  role: "member" | "moderator";
  presence: "online";
}

export interface ChannelDetailsFixture {
  id: string;
  slug: string;
  display_name: string;
  type: "public" | "private";
  created_at: string;
  /** Every active member of the channel, online or not. */
  member_count: number;
  /** How many of them are online; may exceed online_members.length. */
  online_member_count: number;
  /** Presence-filtered, capped preview — never a general roster. */
  online_members: ChannelMemberFixture[];
  /**
   * Whether the server would let this caller add participants (issue #398).
   * Always sent, so a spec that omits it exercises the safe default: absent
   * reads as false and the action stays hidden.
   */
  can_manage_members: boolean;
}

export interface AttachmentFixture {
  id: string;
  filename: string;
  contentType: string;
  size: number;
  status: "pending_scan" | "clean" | "rejected";
  createdAt: string;
}

export function uniqueId(testInfo: TestInfo, suffix: string): string {
  const stable = `${testInfo.project.name}-${testInfo.titlePath.join("-")}-${suffix}`
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 80);
  return `e2e-${stable}`;
}

export function makeMessage(overrides: Partial<RawMessage> = {}): RawMessage {
  const now = overrides.created_at ?? "2026-07-15T12:00:00.000Z";
  const isRemoved = overrides.is_removed ?? overrides.status === "deleted";
  return {
    id: overrides.id ?? "msg-e2e",
    sender_id: overrides.sender_id ?? CURRENT_USER_ID,
    sender_display_name: overrides.sender_display_name ?? CURRENT_USER_NAME,
    sender_email: overrides.sender_email ?? "author@example.test",
    kind: overrides.kind ?? "user",
    body_text: isRemoved ? undefined : (overrides.body_text ?? "Mensagem E2E"),
    body_format: overrides.body_format ?? "v3",
    status: isRemoved ? "deleted" : (overrides.status ?? "active"),
    is_removed: isRemoved,
    created_at: now,
    updated_at: overrides.updated_at ?? now,
    edited_at: overrides.edited_at ?? null,
    edit_count: overrides.edit_count ?? 0,
    is_edited: overrides.is_edited ?? (overrides.edit_count ?? 0) > 0,
    deleted_at: overrides.deleted_at ?? null,
    reactions: overrides.reactions ?? [],
    is_favorited: overrides.is_favorited ?? false,
    is_forwarded: overrides.is_forwarded ?? false,
    quoted: overrides.quoted,
    reference: overrides.reference,
  };
}

export function quoteFrom(message: RawMessage): RawQuote {
  return {
    id: message.id,
    author_id: message.sender_id,
    body: message.is_removed ? "" : (message.body_text ?? ""),
    body_format: message.body_format,
    is_removed: message.is_removed,
    deleted_at: message.deleted_at ?? null,
    created_at: message.created_at,
  };
}

export function createScenario(options: MessagingScenarioOptions): MessagingScenario {
  const messagesByTarget = new Map<string, RawMessage[]>();
  messagesByTarget.set(targetKey(options.kind, options.targetId), [...options.messages]);
  // These defaults exist so every scenario has a full three-category sidebar
  // fixture (ISSUE #396) even when the primary target above is the DM/channel
  // under test — guarded so a scenario built *around* one of these ids (e.g. a
  // spec exercising the ad-hoc group directly) never has its own messages
  // clobbered back to empty.
  for (const [kind, id] of [
    ["channel", OTHER_CHANNEL_ID],
    ["dm", "e2e-dm-other"],
    ["dm", GROUP_DM_ID],
  ] as const) {
    const key = targetKey(kind, id);
    if (!messagesByTarget.has(key)) messagesByTarget.set(key, []);
  }

  const sidebarChannels: SidebarChannelFixture[] =
    options.kind === "channel"
      ? [
          {
            id: options.targetId,
            slug: "e2e-canal",
            display_name: options.targetName,
            type: "public",
            can_write: true,
            unread_count: 0,
          },
          {
            id: OTHER_CHANNEL_ID,
            slug: "e2e-canal-secundario",
            display_name: OTHER_CHANNEL_NAME,
            type: "public",
            can_write: true,
            unread_count: 0,
          },
        ]
      : [
          {
            id: OTHER_CHANNEL_ID,
            slug: "e2e-canal",
            display_name: OTHER_CHANNEL_NAME,
            type: "public",
            can_write: true,
            unread_count: 0,
          },
        ];
  const conversationType =
    options.kind === "dm" ? (options.conversationType ?? "direct") : undefined;
  const directDM: SidebarDMFixture =
    options.kind === "dm" && conversationType === "direct"
      ? {
          id: options.targetId,
          type: "direct",
          name: options.targetName,
          unread_count: 0,
          counterpart: { user_id: OTHER_USER_ID, display_name: options.targetName },
        }
      : {
          id: "e2e-dm-other",
          type: "direct",
          name: OTHER_USER_NAME,
          unread_count: 0,
          counterpart: { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME },
        };
  const groupDM: SidebarDMFixture =
    options.kind === "dm" && conversationType === "group"
      ? { id: options.targetId, type: "group", name: options.targetName, unread_count: 0 }
      : { id: GROUP_DM_ID, type: "group", name: GROUP_DM_NAME, unread_count: 0 };
  const sidebarDMs =
    directDM.id === groupDM.id
      ? [conversationType === "group" ? groupDM : directDM]
      : [directDM, groupDM];

  return {
    kind: options.kind,
    targetId: options.targetId,
    targetName: options.targetName,
    messagesByTarget,
    requests: {
      channelPosts: [],
      dmPosts: [],
      forwards: [],
      patches: [],
      deletes: [],
      favorites: [],
      pins: [],
      reactions: [],
      dmCreates: [],
      groupCreates: [],
    },
    forwardedByIdempotencyKey: new Map(),
    sidebarChannels,
    sidebarDMs,
    dmCandidates: options.dmCandidates ?? [{ userId: OTHER_USER_ID, displayName: OTHER_USER_NAME }],
    pinnedIds: new Map(),
    channelDetails: new Map(),
    groupDetails: new Map(),
    channelMemberships: new Map(),
    groupMemberships: new Map(),
    channelAttachments: new Map(),
    addMembersRequests: [],
    addMembersStatus: 200,
    directProfiles: new Map(),
    conversationAttachments: new Map(),
  };
}

/**
 * Default group-details payload for a conversation in the fixture sidebar.
 *
 * participantCount defaults to the number of participants but is overridable,
 * because the two are independent: the preview is capped and the total is not.
 *
 * canManageMembers defaults to false (issue #398), matching the server's own
 * strict normalization: a spec that says nothing about the permission exercises
 * the safe default, where the add action stays hidden.
 */
export function groupDetailsFixture(
  conversation: { id: string; name: string },
  participants: GroupParticipantFixture[],
  participantCount = participants.length,
  canManageMembers = false,
): GroupDetailsFixture {
  return {
    id: conversation.id,
    type: "group",
    name: conversation.name,
    created_at: "2024-03-04T15:00:00Z",
    participant_count: participantCount,
    participants,
    can_manage_members: canManageMembers,
  };
}

/**
 * Default 1:1 profile payload for a conversation in the fixture sidebar.
 *
 * Every field is overridable so a spec can exercise both a fully-populated card
 * and today's real shape, which is an identity and little else.
 */
export function directProfileFixture(
  conversationId: string,
  profile: Partial<DirectProfileFixture["profile"]> & { user_id: string; display_name: string },
): DirectProfileFixture {
  return {
    kind: "direct",
    conversation_id: conversationId,
    profile,
  };
}

/**
 * Default channel-details payload for a channel in the fixture sidebar. Every
 * value is derived from the channel itself, so a spec that switches channels
 * sees genuinely different content without having to script both.
 *
 * memberCount defaults to the number of online members but is overridable,
 * because the two are independent: a channel keeps its size when nobody is
 * connected, and specs need to assert exactly that.
 */
export function channelDetailsFixture(
  channel: { id: string; slug: string; display_name: string; type: "public" | "private" },
  onlineMembers: ChannelMemberFixture[],
  memberCount = onlineMembers.length,
  canManageMembers = false,
): ChannelDetailsFixture {
  return {
    id: channel.id,
    slug: channel.slug,
    display_name: channel.display_name,
    type: channel.type,
    created_at: "2024-01-12T09:30:00Z",
    member_count: memberCount,
    online_member_count: onlineMembers.length,
    online_members: onlineMembers,
    can_manage_members: canManageMembers,
  };
}

export function messagesFor(
  scenario: MessagingScenario,
  kind: TargetKind = scenario.kind,
  targetId: string = scenario.targetId,
): RawMessage[] {
  const key = targetKey(kind, targetId);
  const messages = scenario.messagesByTarget.get(key);
  if (messages) {
    return messages;
  }
  const emptyMessages: RawMessage[] = [];
  scenario.messagesByTarget.set(key, emptyMessages);
  return emptyMessages;
}

export async function emitMessageCreated(
  page: Page,
  scenario: MessagingScenario,
  options: {
    kind: TargetKind;
    targetId: string;
    message: RawMessage;
    eventId?: string;
  },
) {
  const messages = messagesFor(scenario, options.kind, options.targetId);
  if (!messages.some((message) => message.id === options.message.id)) {
    messages.push(options.message);
  }
  const event = {
    schema_version: 1,
    type: "message.created",
    workspace_id: "e2e-workspace",
    target_type: options.kind,
    target_id: options.targetId,
    message_id: options.message.id,
    event_id: options.eventId ?? `${options.message.id}-event`,
    created_at: options.message.created_at,
    payload: {
      id: options.message.id,
      workspace_id: "e2e-workspace",
      ...(options.kind === "channel"
        ? { channel_id: options.targetId }
        : { dm_conversation_id: options.targetId }),
      sender_id: options.message.sender_id,
      sender_display_name: options.message.sender_display_name,
      kind: options.message.kind,
      body_text: options.message.body_text ?? "",
      body_format: options.message.body_format,
      status: options.message.status,
      is_removed: options.message.is_removed,
      created_at: options.message.created_at,
      updated_at: options.message.updated_at,
      edited_at: options.message.edited_at,
      deleted_at: options.message.deleted_at,
      quoted: options.message.quoted,
      is_forwarded: options.message.is_forwarded,
    },
  };
  await page.waitForFunction(
    ({ kind, targetId }) =>
      (
        window as unknown as {
          __e2eHasSubscription?: (kind: string, targetId: string) => boolean;
        }
      ).__e2eHasSubscription?.(kind, targetId) === true,
    { kind: options.kind, targetId: options.targetId },
  );
  await page.evaluate((messageCreatedEvent) => {
    (
      window as unknown as {
        __e2eEmitMessageCreated: (event: typeof messageCreatedEvent) => void;
      }
    ).__e2eEmitMessageCreated(messageCreatedEvent);
  }, event);
}

export async function installMessagingMocks(
  page: Page,
  scenario: MessagingScenario,
  options: { editWindowExpiredIds?: string[]; forbiddenTargetIds?: string[] } = {},
) {
  const expired = new Set(options.editWindowExpiredIds ?? []);
  const forbidden = new Set(options.forbiddenTargetIds ?? []);
  function assertConversationAccess(targetId: string): boolean {
    return !forbidden.has(targetId);
  }
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: "http://localhost:5173",
  });
  // RF-23: call.start/call.accept now run a real getUserMedia() preflight.
  // Grant camera/microphone so that preflight resolves deterministically
  // against the fake devices configured in playwright.config.ts; tests that
  // exercise the denied path override navigator.mediaDevices.getUserMedia
  // directly instead of revoking this context-level grant.
  await page.context().grantPermissions(["camera", "microphone"], {
    origin: "http://localhost:5173",
  });
  await installWebSocketMock(page, scenario, assertConversationAccess);
  await installSidebarMocks(page, scenario);
  await installInteractionMocks(page, scenario, assertConversationAccess);
  await installConversationMocks(page, scenario);
  await installMessageMocks(page, scenario, expired, assertConversationAccess);
  await installChannelDetailsMocks(page, scenario, assertConversationAccess);
}

/**
 * GET /api/chat/channels/{id}/details and GET /api/files/channels/{id}/attachments.
 *
 * Both mirror the server's refusal shape: a channel the caller cannot reach is a
 * 404, never a 200 with empty data, so a spec cannot mistake "denied" for
 * "nothing here".
 */
async function installChannelDetailsMocks(
  page: Page,
  scenario: MessagingScenario,
  assertConversationAccess: ConversationAccessGuard,
) {
  await page.route("**/api/chat/channels/*/details", async (route) => {
    const channelId = pathSegmentAfter(route.request().url(), "channels");
    if (!channelId || !assertConversationAccess(channelId)) {
      await route.fulfill({ status: 404 });
      return;
    }
    const details = scenario.channelDetails.get(channelId);
    if (!details) {
      await route.fulfill({ status: 404 });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: details }),
    });
  });

  // GET /api/chat/channels/{id}/member-candidates and the DM equivalent
  // (issue #398).
  //
  // The mock excludes current members/participants itself, exactly as the real
  // SQL does, so a spec can seed an offline member or a participant beyond the
  // preview cap and assert the picker never offers them.
  await page.route("**/api/chat/channels/*/member-candidates**", async (route) => {
    const channelId = pathSegmentAfter(route.request().url(), "channels");
    if (!channelId || !assertConversationAccess(channelId)) {
      await route.fulfill({ status: 404 });
      return;
    }
    const current = scenario.channelMemberships.get(channelId) ?? new Set<string>();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          candidates: scenario.dmCandidates
            .filter((candidate) => !current.has(candidate.userId))
            .map((candidate) => ({
              user_id: candidate.userId,
              display_name: candidate.displayName,
            })),
        },
      }),
    });
  });

  await page.route("**/api/chat/dm/*/member-candidates**", async (route) => {
    const conversationId = pathSegmentAfter(route.request().url(), "dm");
    if (!conversationId) {
      await route.fulfill({ status: 404 });
      return;
    }
    const current = scenario.groupMemberships.get(conversationId) ?? new Set<string>();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          candidates: scenario.dmCandidates
            .filter((candidate) => !current.has(candidate.userId))
            .map((candidate) => ({
              user_id: candidate.userId,
              display_name: candidate.displayName,
            })),
        },
      }),
    });
  });

  await page.route("**/api/chat/dm/*/details", async (route) => {
    const conversationID = pathSegmentAfter(route.request().url(), "dm");
    if (!conversationID || !assertConversationAccess(conversationID)) {
      await route.fulfill({ status: 404 });
      return;
    }
    const details = scenario.groupDetails.get(conversationID);
    if (!details) {
      // Mirrors the server: a 1:1 conversation and one the caller cannot reach
      // are the same 404, so a spec cannot mistake "no panel" for "denied".
      await route.fulfill({ status: 404 });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: details }),
    });
  });

  // POST /api/chat/dm/{id}/members (issue #398). Mutates the fixture so the
  // panel's refetch observes the new participant, exactly as it would against
  // the real service.
  await page.route("**/api/chat/dm/*/members", async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    const conversationId = pathSegmentAfter(route.request().url(), "dm");
    if (!conversationId) {
      await route.fulfill({ status: 404 });
      return;
    }
    const body = route.request().postDataJSON() as { user_ids?: string[] };
    const userIds = body?.user_ids ?? [];
    scenario.addMembersRequests.push({ channelId: conversationId, userIds });

    if (scenario.addMembersStatus !== 200) {
      await route.fulfill({
        status: scenario.addMembersStatus,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "denied", message: "denied" } }),
      });
      return;
    }
    const details = scenario.groupDetails.get(conversationId);
    if (details) {
      details.participant_count += userIds.length;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          added: userIds.length,
          already_members: 0,
          member_count: details?.participant_count ?? userIds.length,
        },
      }),
    });
  });

  // POST /api/chat/channels/{id}/members (issue #398).
  //
  // On success it mutates the scenario's own details fixture, so the panel's
  // refetch observes the new member and the new count exactly as it would
  // against the real service — a mock that returned counts without changing the
  // fixture would let a broken refetch pass.
  await page.route("**/api/chat/channels/*/members", async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    const channelId = pathSegmentAfter(route.request().url(), "channels");
    if (!channelId || !assertConversationAccess(channelId)) {
      await route.fulfill({ status: 404 });
      return;
    }
    const body = route.request().postDataJSON() as { user_ids?: string[] };
    const userIds = body?.user_ids ?? [];
    scenario.addMembersRequests.push({ channelId, userIds });

    if (scenario.addMembersStatus !== 200) {
      await route.fulfill({
        status: scenario.addMembersStatus,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "denied", message: "denied" } }),
      });
      return;
    }

    const details = scenario.channelDetails.get(channelId);
    if (details) {
      details.member_count += userIds.length;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          added: userIds.length,
          already_members: 0,
          member_count: details?.member_count ?? userIds.length,
        },
      }),
    });
  });

  // GET /api/chat/dm/{id}/profile. Mirrors the server: a conversation the
  // caller cannot reach, and a group — which has no profile — are the same 404,
  // so a spec cannot mistake "denied" for "this person has no attributes".
  await page.route("**/api/chat/dm/*/profile", async (route) => {
    const conversationID = pathSegmentAfter(route.request().url(), "dm");
    if (!conversationID || !assertConversationAccess(conversationID)) {
      await route.fulfill({ status: 404 });
      return;
    }
    const profile = scenario.directProfiles.get(conversationID);
    if (!profile) {
      await route.fulfill({ status: 404 });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: profile }),
    });
  });

  await page.route("**/api/files/dm/*/attachments**", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const conversationID = pathSegmentAfter(route.request().url(), "dm");
    if (!conversationID || !assertConversationAccess(conversationID)) {
      await route.fulfill({ status: 404 });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: { attachments: scenario.conversationAttachments.get(conversationID) ?? [] },
      }),
    });
  });

  await page.route("**/api/files/channels/*/attachments**", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const channelId = pathSegmentAfter(route.request().url(), "channels");
    if (!channelId || !assertConversationAccess(channelId)) {
      await route.fulfill({ status: 404 });
      return;
    }
    const attachments = scenario.channelAttachments.get(channelId) ?? [];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { attachments } }),
    });
  });
}

function pathSegmentAfter(url: string, collection: string): string | undefined {
  const path = new URL(url).pathname.split("/").filter(Boolean);
  const index = path.indexOf(collection);
  if (index === -1) return undefined;
  const segment = path[index + 1];
  return segment ? decodeURIComponent(segment) : undefined;
}

async function installWebSocketMock(
  page: Page,
  scenario: MessagingScenario,
  assertConversationAccess: ConversationAccessGuard,
) {
  // Reaction toggles travel over the WebSocket, not REST (RF-08). The fake
  // socket below calls back into this exposed function so a toggle mutates
  // the same scenario state the REST message routes read, and a reload sees
  // the persisted reaction exactly like the real server would produce it.
  await page.exposeFunction(
    "__e2eToggleReaction",
    (
      messageId: string,
      emoji: string,
    ): { added: boolean; reactions: RawMessage["reactions"] } | undefined => {
      const location = findMessageLocation(scenario, messageId);
      if (!location || !assertConversationAccess(location.targetId)) return undefined;
      const reactions = location.message.reactions;
      const index = reactions.findIndex((reaction) => reaction.emoji === emoji);
      let updated: RawMessage["reactions"];
      let added: boolean;
      if (index === -1) {
        updated = [...reactions, { emoji, count: 1, reacted_by_me: true }];
        added = true;
      } else if (reactions[index].reacted_by_me) {
        added = false;
        updated =
          reactions[index].count <= 1
            ? reactions.filter((_, i) => i !== index)
            : reactions.map((reaction, i) =>
                i === index
                  ? { ...reaction, count: reaction.count - 1, reacted_by_me: false }
                  : reaction,
              );
      } else {
        added = true;
        updated = reactions.map((reaction, i) =>
          i === index ? { ...reaction, count: reaction.count + 1, reacted_by_me: true } : reaction,
        );
      }
      location.messages[location.index] = { ...location.message, reactions: updated };
      scenario.requests.reactions.push({ messageId, emoji, added });
      return { added, reactions: updated };
    },
  );

  await page.addInitScript(
    (args) => {
      const { accessToken, targetKind, targetId, currentUserId, allowedTargets } = args;
      sessionStorage.setItem("nchat_at", accessToken);
      const allowed = new Set(allowedTargets);
      const sockets = new Set<StableWebSocket>();
      const sentMessages: Array<Record<string, unknown>> = [];

      class StableWebSocket {
        static readonly CONNECTING = 0;
        static readonly OPEN = 1;
        static readonly CLOSING = 2;
        static readonly CLOSED = 3;
        readonly readyState = StableWebSocket.OPEN;
        onopen: ((event: Event) => void) | null = null;
        onmessage: ((event: MessageEvent) => void) | null = null;
        onerror: ((event: Event) => void) | null = null;
        onclose: ((event: CloseEvent) => void) | null = null;
        readonly subscriptions = new Set<string>();

        constructor() {
          sockets.add(this);
          setTimeout(() => this.onopen?.(new Event("open")), 0);
        }

        send(data: string) {
          let parsed: Record<string, unknown>;
          try {
            parsed = JSON.parse(data);
          } catch {
            return;
          }
          sentMessages.push(parsed);
          if (parsed["type"] === "call.sync") {
            queueMicrotask(() =>
              this.onmessage?.(
                new MessageEvent("message", {
                  data: JSON.stringify({
                    type: "call.error",
                    operation: "call.sync",
                    code: "call_not_found",
                  }),
                }),
              ),
            );
            return;
          }
          if (parsed["type"] === "subscribe" || parsed["type"] === "unsubscribe") {
            const kind = parsed["target_type"];
            const id = parsed["target_id"];
            if ((kind !== "channel" && kind !== "dm") || typeof id !== "string") return;
            const key = `${kind}:${id}`;
            if (parsed["type"] === "unsubscribe") {
              this.subscriptions.delete(key);
              return;
            }
            if (!allowed.has(key)) {
              queueMicrotask(() =>
                this.onmessage?.(
                  new MessageEvent("message", {
                    data: JSON.stringify({
                      type: "error",
                      operation: "subscribe",
                      code: "room_access_denied",
                      target_type: kind,
                      target_id: id,
                    }),
                  }),
                ),
              );
              return;
            }
            this.subscriptions.add(key);
            queueMicrotask(() =>
              this.onmessage?.(
                new MessageEvent("message", {
                  data: JSON.stringify({
                    type: "subscribed",
                    operation: "subscribe",
                    target_type: kind,
                    target_id: id,
                  }),
                }),
              ),
            );
            return;
          }
          if (parsed["type"] !== "reaction.toggle") return;
          const messageId = parsed["message_id"] as string;
          const emoji = parsed["emoji"] as string;
          void (
            window as unknown as {
              __e2eToggleReaction: (
                messageId: string,
                emoji: string,
              ) => Promise<
                { added: boolean; reactions: Array<{ emoji: string; count: number }> } | undefined
              >;
            }
          )
            .__e2eToggleReaction(messageId, emoji)
            .then((result) => {
              if (!result) return;
              this.onmessage?.(
                new MessageEvent("message", {
                  data: JSON.stringify({
                    type: "reaction.updated",
                    target_type: targetKind,
                    target_id: targetId,
                    message_id: messageId,
                    reaction: {
                      message_id: messageId,
                      actor_user_id: currentUserId,
                      emoji,
                      added: result.added,
                      reactions: result.reactions,
                    },
                  }),
                }),
              );
            });
        }

        close() {
          sockets.delete(this);
          this.onclose?.(new CloseEvent("close"));
        }
      }

      (
        window as unknown as {
          __e2eEmitMessageCreated: (event: { target_type: string; target_id: string }) => void;
        }
      ).__e2eEmitMessageCreated = (event) => {
        const key = `${event.target_type}:${event.target_id}`;
        for (const socket of sockets) {
          if (!socket.subscriptions.has(key)) continue;
          socket.onmessage?.(new MessageEvent("message", { data: JSON.stringify(event) }));
        }
      };
      (
        window as unknown as {
          __e2eHasSubscription: (kind: string, targetId: string) => boolean;
        }
      ).__e2eHasSubscription = (kind, targetId) => {
        const key = `${kind}:${targetId}`;
        return [...sockets].some((socket) => socket.subscriptions.has(key));
      };
      (
        window as unknown as {
          __e2eEmitWebSocketEvent: (event: Record<string, unknown>) => void;
        }
      ).__e2eEmitWebSocketEvent = (event) => {
        for (const socket of sockets) {
          socket.onmessage?.(new MessageEvent("message", { data: JSON.stringify(event) }));
        }
      };
      (
        window as unknown as {
          __e2eWebSocketMessages: () => Array<Record<string, unknown>>;
        }
      ).__e2eWebSocketMessages = () => [...sentMessages];

      window.WebSocket = StableWebSocket as unknown as typeof WebSocket;
    },
    {
      accessToken: `e2e-at-${scenario.targetId}`,
      targetKind: scenario.kind,
      targetId: scenario.targetId,
      currentUserId: CURRENT_USER_ID,
      allowedTargets: [
        ...scenario.sidebarChannels
          .filter((channel) => assertConversationAccess(channel.id))
          .map((channel) => `channel:${channel.id}`),
        ...scenario.sidebarDMs
          .filter((dm) => assertConversationAccess(dm.id))
          .map((dm) => `dm:${dm.id}`),
      ],
    },
  );
}

async function installSidebarMocks(page: Page, scenario: MessagingScenario) {
  await page.route("**/api/chat/sidebar", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          current_user_id: CURRENT_USER_ID,
          channels: scenario.sidebarChannels,
          dm_conversations: scenario.sidebarDMs,
        },
      }),
    }),
  );
}

async function installInteractionMocks(
  page: Page,
  scenario: MessagingScenario,
  assertConversationAccess: ConversationAccessGuard,
) {
  await page.route("**/api/chat/reactions/allowed-emojis", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { emojis: ["👍", "🎉"] } }),
    }),
  );

  await page.route("**/api/chat/messages/*/favorite", async (route) => {
    const request = route.request();
    const messageId = decodeMessageId(request.url(), 2);
    if (!messageId) {
      await route.fulfill({ status: 404 });
      return;
    }
    const location = findMessageLocation(scenario, messageId);
    if (location && !assertConversationAccess(location.targetId)) {
      await route.fulfill({ status: 403 });
      return;
    }
    if (request.method() === "POST") {
      scenario.requests.favorites.push({ messageId, action: "add" });
      if (!location) {
        await route.fulfill({ status: 404 });
        return;
      }
      location.messages[location.index] = { ...location.message, is_favorited: true };
      await route.fulfill({ status: 204 });
      return;
    }
    if (request.method() === "DELETE") {
      scenario.requests.favorites.push({ messageId, action: "remove" });
      if (location) {
        location.messages[location.index] = { ...location.message, is_favorited: false };
      }
      await route.fulfill({ status: 204 });
      return;
    }
    await route.fallback();
  });

  await page.route("**/api/chat/channels/*/messages/*/pin", (route) =>
    handlePinToggleRoute(route, scenario, "channel", assertConversationAccess),
  );
  await page.route("**/api/chat/dm/*/messages/*/pin", (route) =>
    handlePinToggleRoute(route, scenario, "dm", assertConversationAccess),
  );
  await page.route("**/api/chat/channels/*/pins", (route) =>
    handleListPinsRoute(route, scenario, "channel", assertConversationAccess),
  );
  await page.route("**/api/chat/dm/*/pins", (route) =>
    handleListPinsRoute(route, scenario, "dm", assertConversationAccess),
  );
}

async function installConversationMocks(page: Page, scenario: MessagingScenario) {
  await page.route("**/api/chat/dm-candidates**", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          candidates: scenario.dmCandidates.map((candidate) => ({
            user_id: candidate.userId,
            display_name: candidate.displayName,
          })),
        },
      }),
    });
  });

  await page.route("**/api/chat/dms", async (route) => {
    const request = route.request();
    if (request.method() !== "POST") {
      await route.fallback();
      return;
    }
    const raw = (await request.postDataJSON()) as { other_user_id?: string };
    const otherUserId = raw.other_user_id ?? "";
    scenario.requests.dmCreates.push({ otherUserId });
    const candidate = scenario.dmCandidates.find((c) => c.userId === otherUserId);
    if (!candidate) {
      await route.fulfill({ status: 404 });
      return;
    }
    const conversationId = `e2e-dm-with-${otherUserId}`;
    if (!scenario.sidebarDMs.some((dm) => dm.id === conversationId)) {
      scenario.sidebarDMs.push({
        id: conversationId,
        type: "direct",
        name: candidate.displayName,
        unread_count: 0,
      });
    }
    if (!scenario.messagesByTarget.has(targetKey("dm", conversationId))) {
      scenario.messagesByTarget.set(targetKey("dm", conversationId), []);
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { conversation_id: conversationId, created: true } }),
    });
  });

  await page.route("**/api/chat/dms/group", async (route) => {
    const request = route.request();
    if (request.method() !== "POST") {
      await route.fallback();
      return;
    }
    const raw = (await request.postDataJSON()) as {
      participant_user_ids?: string[];
      title?: string;
    };
    const participantUserIds = raw.participant_user_ids ?? [];
    scenario.requests.groupCreates.push({ participantUserIds, title: raw.title ?? "" });
    const unknown = participantUserIds.some(
      (userId) => !scenario.dmCandidates.some((c) => c.userId === userId),
    );
    if (unknown) {
      await route.fulfill({ status: 404 });
      return;
    }
    const conversationId = `e2e-group-${scenario.requests.groupCreates.length}`;
    const name = raw.title?.trim() || "Grupo sem nome";
    scenario.sidebarDMs.push({ id: conversationId, type: "group", name, unread_count: 0 });
    scenario.messagesByTarget.set(targetKey("dm", conversationId), []);
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({ data: { conversation_id: conversationId } }),
    });
  });
}

async function installMessageMocks(
  page: Page,
  scenario: MessagingScenario,
  expired: Set<string>,
  assertConversationAccess: ConversationAccessGuard,
) {
  await page.route("**/api/chat/messages/*/history?*", (route) => {
    const messageId = decodeMessageId(route.request().url(), 2);
    const message = messageId ? findMessageLocation(scenario, messageId)?.message : undefined;
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          history:
            message && message.is_edited
              ? [
                  {
                    body: "Texto original antes da edição",
                    body_format: message.body_format,
                    versioned_at: message.created_at,
                  },
                ]
              : [],
          offset: 0,
        },
      }),
    });
  });

  await page.route("**/api/chat/messages/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const messageId = decodeMessageId(request.url(), 1);
    if (!messageId) {
      await route.fulfill({ status: 404 });
      return;
    }
    const location = findMessageLocation(scenario, messageId);

    if (request.method() === "PATCH") {
      if (!location) {
        await route.fulfill({ status: 404 });
        return;
      }
      if (!assertConversationAccess(location.targetId)) {
        await route.fulfill({ status: 403 });
        return;
      }
      if (location.message.sender_id !== CURRENT_USER_ID) {
        await route.fulfill({ status: 403 });
        return;
      }
      const raw = (await request.postDataJSON()) as Record<string, unknown>;
      scenario.requests.patches.push({
        messageId,
        method: request.method(),
        endpoint: url.pathname,
        body: raw.body,
        body_format: raw.body_format,
        raw,
      });

      if (
        typeof raw.body !== "string" ||
        raw.body.trim() === "" ||
        !isBodyFormat(raw.body_format)
      ) {
        await route.fulfill({
          status: 400,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "invalid_message_payload", message: "invalid message payload" },
          }),
        });
        return;
      }

      if (expired.has(messageId)) {
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "edit_window_expired", message: "edit window expired" },
          }),
        });
        return;
      }

      const previous = location.message;
      const updated = makeMessage({
        ...previous,
        body_text: raw.body,
        body_format: raw.body_format,
        is_edited: true,
        edit_count: previous.edit_count + 1,
        edited_at: "2026-07-15T12:05:00.000Z",
        updated_at: "2026-07-15T12:05:00.000Z",
      });
      location.messages[location.index] = updated;
      await fulfillMessage(route, updated);
      return;
    }

    if (request.method() === "DELETE") {
      if (!location) {
        await route.fulfill({ status: 404 });
        return;
      }
      if (!assertConversationAccess(location.targetId)) {
        await route.fulfill({ status: 403 });
        return;
      }
      if (location.message.sender_id !== CURRENT_USER_ID) {
        await route.fulfill({ status: 403 });
        return;
      }
      scenario.requests.deletes.push(messageId);
      const previous = location.message;
      const deleted = makeMessage({
        ...previous,
        body_text: undefined,
        status: "deleted",
        is_removed: true,
        deleted_at: "2026-07-15T12:10:00.000Z",
        updated_at: "2026-07-15T12:10:00.000Z",
        reactions: [],
        quoted: undefined,
      });
      location.messages[location.index] = deleted;
      await fulfillMessage(route, deleted);
      return;
    }

    await route.fallback();
  });

  await page.route("**/api/chat/channels/*/messages/*", async (route) => {
    await handleSingleTargetMessageRoute(route, scenario, "channel", assertConversationAccess);
  });

  await page.route("**/api/chat/dm/*/messages/*", async (route) => {
    await handleSingleTargetMessageRoute(route, scenario, "dm", assertConversationAccess);
  });

  await page.route("**/api/chat/channels/*/messages", async (route) => {
    await handleTargetMessagesRoute(route, scenario, "channel", assertConversationAccess);
  });

  await page.route("**/api/chat/dm/*/messages", async (route) => {
    await handleTargetMessagesRoute(route, scenario, "dm", assertConversationAccess);
  });

  await page.route("**/api/chat/channels/*/messages/forward", async (route) => {
    const request = route.request();
    const target = parseMessagesTarget(request.url(), "channel");
    const raw = (await request.postDataJSON()) as Record<string, unknown>;
    const sourceMessageId =
      typeof raw.source_message_id === "string" ? raw.source_message_id : undefined;
    const idempotencyKey = request.headers()["idempotency-key"];
    if (request.method() !== "POST" || !target) {
      await route.fulfill({ status: 404 });
      return;
    }
    if (!assertConversationAccess(target.targetId)) {
      await route.fulfill({ status: 403 });
      return;
    }
    const source = sourceMessageId ? findMessageLocation(scenario, sourceMessageId) : undefined;
    if (!source || source.message.is_removed) {
      await route.fulfill({ status: 404 });
      return;
    }
    if (!assertConversationAccess(source.targetId)) {
      await route.fulfill({ status: 403 });
      return;
    }
    scenario.requests.forwards.push({
      destinationChannelId: target.targetId,
      sourceMessageId,
      idempotencyKey,
      raw,
    });
    if (idempotencyKey) {
      const replay = scenario.forwardedByIdempotencyKey.get(idempotencyKey);
      if (replay) {
        if (
          replay.destinationChannelId !== target.targetId ||
          replay.sourceMessageId !== sourceMessageId
        ) {
          await route.fulfill({ status: 409 });
          return;
        }
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: replay.message }),
        });
        return;
      }
    }
    const destination = messagesFor(scenario, "channel", target.targetId);
    const created = makeMessage({
      id: `${target.targetId}-forward-${destination.length + 1}`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: source.message.body_text,
      body_format: source.message.body_format,
      created_at: "2026-07-15T12:04:00.000Z",
      updated_at: "2026-07-15T12:04:00.000Z",
      reactions: [],
      is_favorited: false,
      is_forwarded: true,
      quoted: undefined,
      reference: undefined,
    });
    destination.push(created);
    if (idempotencyKey && sourceMessageId) {
      scenario.forwardedByIdempotencyKey.set(idempotencyKey, {
        destinationChannelId: target.targetId,
        sourceMessageId,
        message: created,
      });
    }
    await fulfillMessage(route, created, 201);
  });

  await page.route("**/api/chat/**/message-references", async (route) => {
    const body = (await route.request().postDataJSON()) as { message_ids?: string[] };
    const references = (body.message_ids ?? []).map((messageId) => ({
      message_id: messageId,
      reference: findMessageLocation(scenario, messageId)?.message.reference ?? {
        available: false,
      },
    }));
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { references } }),
    });
  });
}

async function handleSingleTargetMessageRoute(
  route: Route,
  scenario: MessagingScenario,
  routeKind: TargetKind,
  assertConversationAccess: ConversationAccessGuard,
) {
  const target = parseMessagesTarget(route.request().url(), routeKind);
  const messageId = decodeMessageId(route.request().url(), 1);
  const location = messageId ? findMessageLocation(scenario, messageId) : undefined;
  if (target && !assertConversationAccess(target.targetId)) {
    await route.fulfill({ status: 404 });
    return;
  }
  if (
    route.request().method() !== "GET" ||
    !target ||
    !location ||
    location.kind !== routeKind ||
    location.targetId !== target.targetId
  ) {
    await route.fulfill({ status: 404 });
    return;
  }
  await fulfillMessage(route, location.message);
}

async function handleTargetMessagesRoute(
  route: Route,
  scenario: MessagingScenario,
  routeKind: TargetKind,
  assertConversationAccess: ConversationAccessGuard,
) {
  const request = route.request();
  const target = parseMessagesTarget(request.url(), routeKind);
  if (!target) {
    await route.fulfill({ status: 404 });
    return;
  }
  // Mirrors the server: a non-participant gets the same 404 as a missing
  // conversation, never a 403 that would confirm the target exists.
  if (!assertConversationAccess(target.targetId)) {
    await route.fulfill({ status: 404 });
    return;
  }
  const messages = messagesFor(scenario, routeKind, target.targetId);

  if (request.method() === "GET") {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { messages, next_cursor: "" } }),
    });
    return;
  }

  if (request.method() === "POST") {
    const body = (await request.postDataJSON()) as {
      body_text?: string;
      body_format?: RawMessage["body_format"];
      parent_message_id?: string;
      referenced_message_id?: string;
    };
    const requests =
      routeKind === "channel" ? scenario.requests.channelPosts : scenario.requests.dmPosts;
    requests.push({
      body_text: body.body_text,
      parent_message_id: body.parent_message_id,
      referenced_message_id: body.referenced_message_id,
    });

    const parent = messages.find((message) => message.id === body.parent_message_id);
    const source = body.referenced_message_id
      ? findMessageLocation(scenario, body.referenced_message_id)
      : undefined;
    const created = makeMessage({
      id: `${target.targetId}-reply-${messages.length + 1}`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: body.body_text ?? "",
      body_format: body.body_format ?? (routeKind === "channel" ? "v3" : "v2"),
      created_at: "2026-07-15T12:03:00.000Z",
      updated_at: "2026-07-15T12:03:00.000Z",
      quoted: parent ? quoteFrom(parent) : undefined,
      reference: source
        ? {
            available: true,
            message_id: source.message.id,
            target_type: source.kind,
            target_id: source.targetId,
            target_label:
              source.targetId === scenario.targetId
                ? scenario.targetName
                : source.kind === "channel"
                  ? "Canal E2E"
                  : OTHER_USER_NAME,
            author_display_name: source.message.sender_display_name,
            body: source.message.body_text ?? "",
            body_format: source.message.body_format,
            created_at: source.message.created_at,
          }
        : undefined,
    });
    messages.push(created);
    await fulfillMessage(route, created);
    return;
  }

  await route.fallback();
}

async function fulfillMessage(route: Route, message: RawMessage, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify({ data: message }),
  });
}

function targetKey(kind: TargetKind, targetId: string): string {
  return `${kind}:${targetId}`;
}

function parseTargetKey(key: string): { kind: TargetKind; targetId: string } | undefined {
  const [kind, ...targetParts] = key.split(":");
  if ((kind !== "channel" && kind !== "dm") || targetParts.length === 0) {
    return undefined;
  }
  return { kind, targetId: targetParts.join(":") };
}

function parseMessagesTarget(
  url: string,
  expectedKind: TargetKind,
): { kind: TargetKind; targetId: string } | undefined {
  const path = new URL(url).pathname.split("/").filter(Boolean);
  const collection = expectedKind === "channel" ? "channels" : "dm";
  const collectionIndex = path.indexOf(collection);
  if (collectionIndex === -1 || path[collectionIndex + 2] !== "messages") {
    return undefined;
  }
  const targetId = path[collectionIndex + 1];
  return targetId ? { kind: expectedKind, targetId: decodeURIComponent(targetId) } : undefined;
}

function parsePinsListTarget(url: string, expectedKind: TargetKind): string | undefined {
  const path = new URL(url).pathname.split("/").filter(Boolean);
  const collection = expectedKind === "channel" ? "channels" : "dm";
  const collectionIndex = path.indexOf(collection);
  if (collectionIndex === -1 || path[collectionIndex + 2] !== "pins") {
    return undefined;
  }
  const targetId = path[collectionIndex + 1];
  return targetId ? decodeURIComponent(targetId) : undefined;
}

async function handlePinToggleRoute(
  route: Route,
  scenario: MessagingScenario,
  kind: TargetKind,
  assertConversationAccess: ConversationAccessGuard,
) {
  const request = route.request();
  const target = parseMessagesTarget(request.url(), kind);
  const messageId = decodeMessageId(request.url(), 2);
  if (!target || !messageId) {
    await route.fulfill({ status: 404 });
    return;
  }
  if (!assertConversationAccess(target.targetId)) {
    await route.fulfill({ status: 403 });
    return;
  }
  const location = findMessageLocation(scenario, messageId);
  if (!location || location.kind !== kind || location.targetId !== target.targetId) {
    await route.fulfill({ status: 404 });
    return;
  }
  const key = targetKey(kind, target.targetId);
  const pinned = scenario.pinnedIds.get(key) ?? new Set<string>();
  scenario.pinnedIds.set(key, pinned);

  if (request.method() === "POST") {
    scenario.requests.pins.push({ messageId, targetId: target.targetId, action: "add" });
    pinned.add(messageId);
    await route.fulfill({ status: 204 });
    return;
  }
  if (request.method() === "DELETE") {
    scenario.requests.pins.push({ messageId, targetId: target.targetId, action: "remove" });
    pinned.delete(messageId);
    await route.fulfill({ status: 204 });
    return;
  }
  await route.fallback();
}

async function handleListPinsRoute(
  route: Route,
  scenario: MessagingScenario,
  kind: TargetKind,
  assertConversationAccess: ConversationAccessGuard,
) {
  if (route.request().method() !== "GET") {
    await route.fallback();
    return;
  }
  const targetId = parsePinsListTarget(route.request().url(), kind);
  if (!targetId) {
    await route.fallback();
    return;
  }
  if (!assertConversationAccess(targetId)) {
    await route.fulfill({ status: 403 });
    return;
  }
  const pinnedIds = scenario.pinnedIds.get(targetKey(kind, targetId)) ?? new Set<string>();
  const messages = messagesFor(scenario, kind, targetId);
  const pins = messages
    .filter((message) => pinnedIds.has(message.id))
    .map((message) => ({
      message,
      pinned_by_user_id: CURRENT_USER_ID,
      pinned_at: "2026-07-15T12:06:00.000Z",
    }));
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ data: { pins, total_count: pins.length } }),
  });
}

function decodeMessageId(url: string, trailingSegments: number): string | undefined {
  const path = new URL(url).pathname.split("/").filter(Boolean);
  const messageIndex = path.indexOf("messages");
  const idIndex = messageIndex + 1;
  if (messageIndex === -1 || path.length < idIndex + trailingSegments) {
    return undefined;
  }
  return decodeURIComponent(path[idIndex]);
}

function findMessageLocation(
  scenario: MessagingScenario,
  messageId: string,
):
  | {
      kind: TargetKind;
      targetId: string;
      messages: RawMessage[];
      index: number;
      message: RawMessage;
    }
  | undefined {
  for (const [key, messages] of scenario.messagesByTarget.entries()) {
    const parsed = parseTargetKey(key);
    if (!parsed) {
      continue;
    }
    const index = messages.findIndex((message) => message.id === messageId);
    if (index >= 0) {
      return { ...parsed, messages, index, message: messages[index] };
    }
  }
  return undefined;
}

function isBodyFormat(value: unknown): value is RawMessage["body_format"] {
  return value === "v1" || value === "v2" || value === "v3";
}

export function messageBubble(page: Page, messageId: string): Locator {
  return page.locator(`[data-testid="chat-msg-bubble"][data-message-id="${messageId}"]`);
}

export async function revealActions(page: Page, messageId: string): Promise<Locator> {
  const bubble = messageBubble(page, messageId);
  await expect(bubble).toBeVisible();
  // Playwright tracks a virtual mouse position across page.reload(): se o
  // cursor já estiver nas coordenadas do bubble (mesmo layout), hover() não
  // dispara um mousemove real e o onMouseEnter do React nunca é chamado.
  // Mover para longe primeiro garante um movimento real em qualquer cenário.
  await page.mouse.move(0, 0);
  await bubble.hover();
  return bubble;
}

export async function fillComposer(page: Page, text: string) {
  const input = page.getByTestId("chat-composer-input");
  await expect(input).toBeVisible();
  await input.click();
  await page.keyboard.insertText(text);
  await expect(input).toContainText(text);
}

export async function replaceEditorText(page: Page, editor: Locator, text: string) {
  await expect(editor).toBeVisible();
  await editor.click();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+A" : "Control+A");
  await page.keyboard.press("Backspace");
  await page.keyboard.type(text);
  await expect(editor).toHaveText(text);
}
