/**
 * The management surface of the Admin API (issue #579).
 *
 * Every function here sends the console's only credential — the HttpOnly
 * cookie, via adminFetch — and echoes the CSRF token on mutations. None of them
 * sends a role, a capability or an actor: the server derives all three from the
 * session, and a client that sent them would be ignored.
 */

import { adminFetch } from "./client";
import {
  bool,
  contractError,
  num,
  nullableStr,
  parsePage,
  requireArray,
  requireRecord,
  str,
  strList,
  type Page,
} from "./parse";

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

export interface WorkspaceRole {
  workspaceId: string;
  workspaceName: string;
  role: string;
  status: string;
  joinedAt: string;
}

export interface AdminUser {
  id: string;
  email: string;
  displayName: string;
  fullName: string;
  avatarUrl: string;
  status: string;
  authSource: string;
  externalProvider: string;
  /** Whether an external identity provider owns this identity's fields. */
  identityManagedExternally: boolean;
  lastLoginAt: string | null;
  createdAt: string;
  platformAdmin: boolean;
  adminRoles: string[];
  workspaceRoles: WorkspaceRole[];
  activeSessions: number;
}

export interface AdminRole {
  slug: string;
  description: string;
  capabilities: string[];
}

export interface AdminRoleGrant extends AdminRole {
  grantedAt: string;
  grantedBy: string;
}

export interface AdminUserDetail extends AdminUser {
  memberships: WorkspaceRole[];
  channelCount: number;
  roleGrants: AdminRoleGrant[];
  availableRoles: AdminRole[];
}

export interface UserFilters {
  q?: string;
  status?: string;
  authSource?: string;
  platformAdmin?: string;
  /**
   * A workspace role, not a platform one.
   *
   * It is a separate question from `platformAdmin` and combines with it: being
   * the owner of a workspace confers no platform authority, and the two filters
   * narrow the same list rather than replacing each other.
   */
  workspaceRole?: string;
  inactivity?: string;
}

/**
 * The account statuses the server accepts as a filter.
 *
 * `deleted` is a status `auth.users` can hold and is deliberately absent: the
 * directory excludes soft-deleted accounts unconditionally, so the value could
 * never return a row and the API refuses it with 400.
 */
export const USER_STATUSES = ["active", "invited", "suspended", "locked"] as const;

/** The workspace roles the server accepts, in the order the console offers them. */
export const WORKSPACE_ROLES = ["owner", "admin", "moderator", "member", "guest"] as const;

function parseWorkspaceRole(raw: Record<string, unknown>, field: string): WorkspaceRole {
  return {
    workspaceId: str(raw, "workspace_id", field),
    workspaceName: str(raw, "workspace_name", field),
    role: str(raw, "role", field),
    status: str(raw, "status", field),
    joinedAt: str(raw, "joined_at", field),
  };
}

function parseUser(raw: Record<string, unknown>, field: string): AdminUser {
  return {
    id: str(raw, "id", field),
    email: str(raw, "email", field),
    displayName: str(raw, "display_name", field),
    fullName: str(raw, "full_name", field),
    avatarUrl: str(raw, "avatar_url", field),
    status: str(raw, "status", field),
    authSource: str(raw, "auth_source", field),
    externalProvider: str(raw, "external_provider", field),
    identityManagedExternally: bool(raw, "identity_managed_externally", field),
    lastLoginAt: nullableStr(raw, "last_login_at", field),
    createdAt: str(raw, "created_at", field),
    platformAdmin: bool(raw, "platform_admin", field),
    adminRoles: strList(raw, "admin_roles", field),
    workspaceRoles: requireArray(raw.workspace_roles, `${field}.workspace_roles`).map((entry, i) =>
      parseWorkspaceRole(requireRecord(entry, `${field}.workspace_roles[${i}]`), field),
    ),
    activeSessions: num(raw, "active_sessions", field),
  };
}

function parseRole(raw: Record<string, unknown>, field: string): AdminRole {
  return {
    slug: str(raw, "slug", field),
    description: str(raw, "description", field),
    capabilities: strList(raw, "capabilities", field),
  };
}

/**
 * Builds the query string from the filters the console offers.
 *
 * Only non-empty values are sent. An empty filter is absent rather than sent as
 * "", because the server treats an unrecognised value as an error and "" is not
 * one of its allowlists.
 */
export function buildUserQuery(filters: UserFilters, cursor: string | null, limit: number): string {
  const params = new URLSearchParams({ limit: String(limit) });
  for (const [key, value] of Object.entries({
    q: filters.q,
    status: filters.status,
    auth_source: filters.authSource,
    platform_admin: filters.platformAdmin,
    workspace_role: filters.workspaceRole,
    inactivity: filters.inactivity,
  })) {
    if (value) params.set(key, value);
  }
  if (cursor) params.set("cursor", cursor);
  return params.toString();
}

export async function listUsers(
  filters: UserFilters,
  cursor: string | null,
  limit: number,
  signal?: AbortSignal,
): Promise<Page<AdminUser>> {
  const body = await adminFetch<unknown>(`/users?${buildUserQuery(filters, cursor, limit)}`, {
    signal,
  });
  return parsePage(body, "users", (raw, index) => parseUser(raw, `users[${index}]`));
}

export async function getUser(id: string, signal?: AbortSignal): Promise<AdminUserDetail> {
  const body = await adminFetch<unknown>(`/users/${encodeURIComponent(id)}`, { signal });
  const raw = requireRecord(body, "user");
  return {
    ...parseUser(raw, "user"),
    memberships: requireArray(raw.memberships, "user.memberships").map((entry, i) =>
      parseWorkspaceRole(requireRecord(entry, `user.memberships[${i}]`), "user"),
    ),
    channelCount: num(raw, "channel_count", "user"),
    roleGrants: requireArray(raw.role_grants, "user.role_grants").map((entry, i) => {
      const grant = requireRecord(entry, `user.role_grants[${i}]`);
      return {
        ...parseRole(grant, "user.role_grants"),
        grantedAt: str(grant, "granted_at", "user.role_grants"),
        grantedBy: str(grant, "granted_by", "user.role_grants"),
      };
    }),
    availableRoles: requireArray(raw.available_roles, "user.available_roles").map((entry, i) =>
      parseRole(requireRecord(entry, `user.available_roles[${i}]`), "user.available_roles"),
    ),
  };
}

export interface UserStatusResult {
  userId: string;
  fromStatus: string;
  toStatus: string;
  revokedSessions: number;
}

export async function updateUserStatus(
  id: string,
  status: "active" | "suspended",
): Promise<UserStatusResult> {
  const body = await adminFetch<unknown>(`/users/${encodeURIComponent(id)}/status`, {
    method: "PATCH",
    body: JSON.stringify({ status }),
  });
  const raw = requireRecord(body, "status");
  return {
    userId: str(raw, "user_id", "status"),
    fromStatus: str(raw, "from_status", "status"),
    toStatus: str(raw, "to_status", "status"),
    revokedSessions: num(raw, "revoked_sessions", "status"),
  };
}

export async function revokeUserSessions(id: string): Promise<number> {
  const body = await adminFetch<unknown>(`/users/${encodeURIComponent(id)}/sessions`, {
    method: "DELETE",
  });
  return num(requireRecord(body, "sessions"), "revoked_sessions", "sessions");
}

export async function grantAdminRole(id: string, roleSlug: string): Promise<void> {
  await adminFetch<void>(`/users/${encodeURIComponent(id)}/admin-roles`, {
    method: "POST",
    body: JSON.stringify({ role_slug: roleSlug }),
  });
}

export async function revokeAdminRole(id: string, roleSlug: string): Promise<void> {
  await adminFetch<void>(
    `/users/${encodeURIComponent(id)}/admin-roles/${encodeURIComponent(roleSlug)}`,
    { method: "DELETE" },
  );
}

// ---------------------------------------------------------------------------
// Channels and conversations
// ---------------------------------------------------------------------------

export interface AdminChannel {
  id: string;
  workspaceId: string;
  workspaceName: string;
  slug: string;
  displayName: string;
  type: string;
  status: string;
  isGeneral: boolean;
  memberCount: number;
  moderatorCount: number;
  createdByName: string;
  createdByEmail: string;
  createdAt: string;
  lastActivityAt: string | null;
}

export interface ChannelMember {
  userId: string;
  displayName: string;
  email: string;
  role: string;
}

export interface AdminChannelDetail extends AdminChannel {
  categoryName: string;
  moderators: ChannelMember[];
  workspaceAdmins: ChannelMember[];
  /** A bounded preview of the membership. `memberCount` is the real total. */
  members: ChannelMember[];
  messageCount: number;
}

/**
 * Somebody the console may offer as a new member of one channel.
 *
 * Narrower than `AdminUser` on purpose: a picker shows who a person is, and a
 * search behind a channel capability must not double as a second, wider user
 * directory.
 */
export interface ChannelMemberCandidate {
  userId: string;
  displayName: string;
  fullName: string;
  email: string;
  avatarUrl: string;
  /** The person's role in the channel's workspace. */
  workspaceRole: string;
}

/**
 * Searches the people who may be added to a channel.
 *
 * The workspace is derived from the channel server-side, so this cannot be
 * pointed at another tenant's directory. It is a convenience and never a
 * control: `addChannelMembers` re-decides eligibility for whoever is actually
 * submitted.
 */
export async function listMemberCandidates(
  channelId: string,
  query: string,
  signal?: AbortSignal,
): Promise<ChannelMemberCandidate[]> {
  const params = new URLSearchParams();
  if (query) params.set("q", query);
  const body = await adminFetch<unknown>(
    `/channels/${encodeURIComponent(channelId)}/member-candidates?${params.toString()}`,
    { signal },
  );
  const raw = requireRecord(body, "candidates");
  return requireArray(raw.candidates, "candidates").map((entry, index) => {
    const candidate = requireRecord(entry, `candidates[${index}]`);
    const field = `candidates[${index}]`;
    return {
      userId: str(candidate, "user_id", field),
      displayName: str(candidate, "display_name", field),
      fullName: str(candidate, "full_name", field),
      email: str(candidate, "email", field),
      avatarUrl: str(candidate, "avatar_url", field),
      workspaceRole: str(candidate, "workspace_role", field),
    };
  });
}

/** What an applied membership mutation reports back. */
export interface MembershipResult {
  channelId: string;
  workspaceId: string;
  added: number;
  alreadyMembers: number;
  removed: boolean;
  memberCount: number;
}

export interface ChannelFilters {
  q?: string;
  type?: string;
  status?: string;
  minMembers?: string;
  activeWithin?: string;
  /**
   * A user id. Selects the channels that person administers — which in this
   * domain means created or moderates, because a channel has no owner and no
   * admin of its own. It is a separate parameter from `q` on purpose: a text
   * search that also matched identifiers would make "who administers this" a
   * guess rather than a predicate.
   */
  administeredBy?: string;
}

function parseChannel(raw: Record<string, unknown>, field: string): AdminChannel {
  return {
    id: str(raw, "id", field),
    workspaceId: str(raw, "workspace_id", field),
    workspaceName: str(raw, "workspace_name", field),
    slug: str(raw, "slug", field),
    displayName: str(raw, "display_name", field),
    type: str(raw, "type", field),
    status: str(raw, "status", field),
    isGeneral: bool(raw, "is_general", field),
    memberCount: num(raw, "member_count", field),
    moderatorCount: num(raw, "moderator_count", field),
    createdByName: str(raw, "created_by_name", field),
    createdByEmail: str(raw, "created_by_email", field),
    createdAt: str(raw, "created_at", field),
    lastActivityAt: nullableStr(raw, "last_activity_at", field),
  };
}

function parseMember(raw: Record<string, unknown>, field: string): ChannelMember {
  return {
    userId: str(raw, "user_id", field),
    displayName: str(raw, "display_name", field),
    email: str(raw, "email", field),
    role: str(raw, "role", field),
  };
}

export function buildChannelQuery(
  filters: ChannelFilters,
  cursor: string | null,
  limit: number,
): string {
  const params = new URLSearchParams({ limit: String(limit) });
  for (const [key, value] of Object.entries({
    q: filters.q,
    type: filters.type,
    status: filters.status,
    min_members: filters.minMembers,
    active_within: filters.activeWithin,
    administered_by: filters.administeredBy,
  })) {
    if (value) params.set(key, value);
  }
  if (cursor) params.set("cursor", cursor);
  return params.toString();
}

export async function listChannels(
  filters: ChannelFilters,
  cursor: string | null,
  limit: number,
  signal?: AbortSignal,
): Promise<Page<AdminChannel>> {
  const body = await adminFetch<unknown>(`/channels?${buildChannelQuery(filters, cursor, limit)}`, {
    signal,
  });
  return parsePage(body, "channels", (raw, index) => parseChannel(raw, `channels[${index}]`));
}

export async function getChannel(id: string, signal?: AbortSignal): Promise<AdminChannelDetail> {
  const body = await adminFetch<unknown>(`/channels/${encodeURIComponent(id)}`, { signal });
  const raw = requireRecord(body, "channel");
  return {
    ...parseChannel(raw, "channel"),
    categoryName: str(raw, "category_name", "channel"),
    moderators: requireArray(raw.moderators, "channel.moderators").map((entry, i) =>
      parseMember(requireRecord(entry, `channel.moderators[${i}]`), "channel.moderators"),
    ),
    workspaceAdmins: requireArray(raw.workspace_admins, "channel.workspace_admins").map(
      (entry, i) =>
        parseMember(
          requireRecord(entry, `channel.workspace_admins[${i}]`),
          "channel.workspace_admins",
        ),
    ),
    members: requireArray(raw.members, "channel.members").map((entry, i) =>
      parseMember(requireRecord(entry, `channel.members[${i}]`), "channel.members"),
    ),
    messageCount: num(raw, "message_count", "channel"),
  };
}

function parseMembership(body: unknown): MembershipResult {
  const raw = requireRecord(body, "membership");
  return {
    channelId: str(raw, "channel_id", "membership"),
    workspaceId: str(raw, "workspace_id", "membership"),
    added: num(raw, "added", "membership"),
    alreadyMembers: num(raw, "already_members", "membership"),
    removed: bool(raw, "removed", "membership"),
    memberCount: num(raw, "member_count", "membership"),
  };
}

/**
 * Adds people to a channel.
 *
 * The body carries user ids and nothing else — no role, no workspace. The
 * server derives the workspace from the channel and admits everybody as an
 * ordinary member, and a body carrying either would be refused outright.
 */
export async function addChannelMembers(
  channelId: string,
  userIds: string[],
): Promise<MembershipResult> {
  const body = await adminFetch<unknown>(`/channels/${encodeURIComponent(channelId)}/members`, {
    method: "POST",
    body: JSON.stringify({ user_ids: userIds }),
  });
  return parseMembership(body);
}

export async function removeChannelMember(
  channelId: string,
  userId: string,
): Promise<MembershipResult> {
  const body = await adminFetch<unknown>(
    `/channels/${encodeURIComponent(channelId)}/members/${encodeURIComponent(userId)}`,
    { method: "DELETE" },
  );
  return parseMembership(body);
}

export async function updateChannelStatus(
  id: string,
  status: "active" | "archived",
): Promise<AdminChannel> {
  const body = await adminFetch<unknown>(`/channels/${encodeURIComponent(id)}/status`, {
    method: "PATCH",
    body: JSON.stringify({ status }),
  });
  return parseChannel(requireRecord(body, "channel"), "channel");
}

/**
 * Operational metadata about one private conversation.
 *
 * There is no message, no title and no participant identity here, because the
 * API sends none: being a platform administrator does not make somebody a
 * participant. This type is the client half of that contract.
 */
export interface AdminConversation {
  id: string;
  workspaceId: string;
  workspaceName: string;
  type: string;
  status: string;
  participantCount: number;
  messageCount: number;
  createdAt: string;
  updatedAt: string;
  lastActivityAt: string | null;
}

export interface ConversationFilters {
  type?: string;
  status?: string;
}

export async function listConversations(
  filters: ConversationFilters,
  cursor: string | null,
  limit: number,
  signal?: AbortSignal,
): Promise<Page<AdminConversation>> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (filters.type) params.set("type", filters.type);
  if (filters.status) params.set("status", filters.status);
  if (cursor) params.set("cursor", cursor);
  const body = await adminFetch<unknown>(`/conversations?${params.toString()}`, { signal });
  return parsePage(body, "conversations", (raw, index) => {
    const field = `conversations[${index}]`;
    return {
      id: str(raw, "id", field),
      workspaceId: str(raw, "workspace_id", field),
      workspaceName: str(raw, "workspace_name", field),
      type: str(raw, "type", field),
      status: str(raw, "status", field),
      participantCount: num(raw, "participant_count", field),
      messageCount: num(raw, "message_count", field),
      createdAt: str(raw, "created_at", field),
      updatedAt: str(raw, "updated_at", field),
      lastActivityAt: nullableStr(raw, "last_activity_at", field),
    };
  });
}

// ---------------------------------------------------------------------------
// Operational policies
// ---------------------------------------------------------------------------

export interface WorkspaceRef {
  id: string;
  slug: string;
  name: string;
  status: string;
}

/**
 * The server's own bounds, echoed with every policy response.
 *
 * The console validates and renders against these rather than restating limits
 * it decided, so a bound that changes on the server changes the form without a
 * frontend release. `step`, when present, is the granularity a value must be an
 * exact multiple of.
 */
export interface PolicyBounds {
  min: number;
  max: number;
  default: number;
  unit: string;
  step?: number;
}

export interface AntiSpamPolicy {
  workspace: WorkspaceRef;
  messageRateLimitPerMinute: number;
}

export interface UploadPolicy {
  workspace: WorkspaceRef;
  maxUploadBytes: number;
}

function parseWorkspaceRef(raw: Record<string, unknown>, field: string): WorkspaceRef {
  const workspace = requireRecord(raw.workspace, `${field}.workspace`);
  return {
    id: str(workspace, "id", field),
    slug: str(workspace, "slug", field),
    name: str(workspace, "name", field),
    status: str(workspace, "status", field),
  };
}

function parseBounds(value: unknown): PolicyBounds {
  const raw = requireRecord(value, "bounds");
  const bounds: PolicyBounds = {
    min: num(raw, "min", "bounds"),
    max: num(raw, "max", "bounds"),
    default: num(raw, "default", "bounds"),
    unit: str(raw, "unit", "bounds"),
  };
  if (raw.step !== undefined) bounds.step = num(raw, "step", "bounds");
  if (bounds.min > bounds.max) throw contractError("bounds.min é maior que bounds.max");
  return bounds;
}

export interface AntiSpamPage extends Page<AntiSpamPolicy> {
  bounds: PolicyBounds;
}

export async function listAntiSpamPolicies(
  cursor: string | null,
  limit: number,
  signal?: AbortSignal,
): Promise<AntiSpamPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (cursor) params.set("cursor", cursor);
  const body = await adminFetch<unknown>(`/policies/anti-spam?${params.toString()}`, { signal });
  const page = parsePage(body, "policies", (raw, index) => ({
    workspace: parseWorkspaceRef(raw, `policies[${index}]`),
    messageRateLimitPerMinute: num(raw, "message_rate_limit_per_minute", `policies[${index}]`),
  }));
  return { ...page, bounds: parseBounds(requireRecord(body, "data").bounds) };
}

export async function updateAntiSpamPolicy(
  workspaceId: string,
  value: number,
): Promise<AntiSpamPolicy> {
  const body = await adminFetch<unknown>(`/policies/anti-spam/${encodeURIComponent(workspaceId)}`, {
    method: "PATCH",
    body: JSON.stringify({ message_rate_limit_per_minute: value }),
  });
  const raw = requireRecord(requireRecord(body, "data").policy, "policy");
  return {
    workspace: parseWorkspaceRef(raw, "policy"),
    messageRateLimitPerMinute: num(raw, "message_rate_limit_per_minute", "policy"),
  };
}

export interface UploadPage extends Page<UploadPolicy> {
  bounds: PolicyBounds;
  /** The static gateway ceiling. A workspace limit can never exceed it. */
  gatewayHardCapBytes: number;
  /** Upload controls that are real, enforced, and not editable from here. */
  deploymentManaged: string[];
}

export async function listUploadPolicies(
  cursor: string | null,
  limit: number,
  signal?: AbortSignal,
): Promise<UploadPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (cursor) params.set("cursor", cursor);
  const body = await adminFetch<unknown>(`/policies/upload?${params.toString()}`, { signal });
  const record = requireRecord(body, "data");
  const page = parsePage(body, "policies", (raw, index) => ({
    workspace: parseWorkspaceRef(raw, `policies[${index}]`),
    maxUploadBytes: num(raw, "max_upload_bytes", `policies[${index}]`),
  }));
  return {
    ...page,
    bounds: parseBounds(record.bounds),
    gatewayHardCapBytes: num(record, "gateway_hard_cap_bytes", "data"),
    deploymentManaged: strList(record, "deployment_managed", "data"),
  };
}

export async function updateUploadPolicy(
  workspaceId: string,
  bytes: number,
): Promise<UploadPolicy> {
  const body = await adminFetch<unknown>(`/policies/upload/${encodeURIComponent(workspaceId)}`, {
    method: "PATCH",
    body: JSON.stringify({ max_upload_bytes: bytes }),
  });
  const raw = requireRecord(requireRecord(body, "data").policy, "policy");
  return {
    workspace: parseWorkspaceRef(raw, "policy"),
    maxUploadBytes: num(raw, "max_upload_bytes", "policy"),
  };
}
