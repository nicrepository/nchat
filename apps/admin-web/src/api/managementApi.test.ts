import { afterEach, describe, expect, it, vi } from "vitest";

import { AdminApiError, setCSRFToken, _resetCSRFToken } from "./client";
import {
  buildChannelQuery,
  buildUserQuery,
  getChannel,
  getUser,
  grantAdminRole,
  listAntiSpamPolicies,
  listChannels,
  listConversations,
  listMemberCandidates,
  listUploadPolicies,
  listUsers,
  revokeAdminRole,
  revokeUserSessions,
  updateAntiSpamPolicy,
  updateChannelStatus,
  updateUploadPolicy,
  updateUserStatus,
} from "./managementApi";
import { ERR_INVALID_RESPONSE } from "./parse";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function stub(body: unknown, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse(body, status));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const USER = {
  id: "u1",
  email: "ana@example.test",
  display_name: "Ana",
  full_name: "Ana Lima",
  avatar_url: "",
  status: "active",
  auth_source: "oidc",
  external_provider: "keycloak",
  identity_managed_externally: true,
  last_login_at: null,
  created_at: "2026-01-01T00:00:00Z",
  platform_admin: false,
  admin_roles: [],
  workspace_roles: [],
  active_sessions: 0,
};

const CHANNEL = {
  id: "c1",
  workspace_id: "w1",
  workspace_name: "NChat",
  slug: "eng",
  display_name: "Engenharia",
  type: "private",
  status: "active",
  is_general: false,
  member_count: 12,
  moderator_count: 1,
  created_by_name: "Root",
  created_by_email: "root@example.test",
  created_at: "2026-01-01T00:00:00Z",
  last_activity_at: null,
};

const WORKSPACE = { id: "w1", slug: "default", name: "NChat", status: "active" };
const RATE_BOUNDS = { min: 1, max: 600, default: 60, unit: "messages_per_minute" };
const UPLOAD_BOUNDS = {
  min: 1048576,
  max: 536870912,
  default: 262144000,
  unit: "bytes",
  step: 1048576,
};

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("buildUserQuery", () => {
  it("sends only the filters that are set", () => {
    const query = buildUserQuery({ q: "ana", status: "active" }, null, 25);
    expect(query).toContain("limit=25");
    expect(query).toContain("q=ana");
    expect(query).toContain("status=active");
    // An empty filter is absent, not sent as "": the server treats an
    // unrecognised value as an error and "" is in none of its allowlists.
    expect(query).not.toContain("auth_source");
    expect(query).not.toContain("cursor");
  });

  it("escapes values rather than concatenating them", () => {
    const query = buildUserQuery({ q: "a&b=c" }, "cur sor", 10);
    expect(query).toContain("q=a%26b%3Dc");
    expect(query).toContain("cursor=cur+sor");
  });
});

describe("buildChannelQuery", () => {
  it("carries the channel filters", () => {
    const query = buildChannelQuery(
      { q: "eng", type: "private", status: "archived", minMembers: "10", activeWithin: "30d" },
      "c",
      50,
    );
    for (const fragment of [
      "q=eng",
      "type=private",
      "status=archived",
      "min_members=10",
      "active_within=30d",
      "cursor=c",
      "limit=50",
    ]) {
      expect(query).toContain(fragment);
    }
  });
});

describe("listUsers", () => {
  it("maps a page and its pagination", async () => {
    stub({ data: { users: [USER], pagination: { next_cursor: "next", has_more: true } } });
    const page = await listUsers({}, null, 25);

    expect(page.items[0].displayName).toBe("Ana");
    expect(page.items[0].identityManagedExternally).toBe(true);
    expect(page.nextCursor).toBe("next");
    expect(page.hasMore).toBe(true);
  });

  it("treats a missing field as a broken contract, not an empty value", async () => {
    const withoutEmail: Record<string, unknown> = { ...USER };
    delete withoutEmail.email;
    stub({ data: { users: [withoutEmail], pagination: { next_cursor: null, has_more: false } } });

    await expect(listUsers({}, null, 25)).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
  });

  // has_more and next_cursor must agree. When they do not, one is wrong and
  // there is no way to tell which; paging on a cursor we do not trust risks an
  // endless loop.
  it("refuses a page whose pagination contradicts itself", async () => {
    stub({ data: { users: [], pagination: { next_cursor: null, has_more: true } } });
    await expect(listUsers({}, null, 25)).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
  });

  // "" is not a second spelling of "no next page". Tolerating it here is what
  // would let the two ends drift apart silently: the console would keep working
  // against a server that stopped honouring the documented null, right up until
  // something started treating the empty string as a real cursor.
  it("refuses an empty string where the contract says null", async () => {
    stub({ data: { users: [USER], pagination: { next_cursor: "", has_more: false } } });
    await expect(listUsers({}, null, 25)).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
  });

  it("accepts the documented null on the last page", async () => {
    stub({ data: { users: [USER], pagination: { next_cursor: null, has_more: false } } });
    await expect(listUsers({}, null, 25)).resolves.toMatchObject({
      nextCursor: null,
      hasMore: false,
    });
  });

  it("refuses a body that is not the agreed envelope", async () => {
    stub({ data: { users: "not a list", pagination: { next_cursor: null, has_more: false } } });
    await expect(listUsers({}, null, 25)).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
  });

  it("propagates a refusal instead of returning an empty page", async () => {
    stub({ error: { code: "forbidden", message: "forbidden" } }, 403);
    await expect(listUsers({}, null, 25)).rejects.toMatchObject({ status: 403 });
  });
});

describe("getUser", () => {
  it("maps the detail, its memberships and the role catalogue", async () => {
    stub({
      data: {
        ...USER,
        memberships: [
          {
            workspace_id: "w1",
            workspace_name: "NChat",
            role: "member",
            status: "active",
            joined_at: "2026-01-01T00:00:00Z",
          },
        ],
        channel_count: 7,
        role_grants: [
          {
            slug: "platform-auditor",
            description: "Read-only.",
            capabilities: ["admin.audit.read"],
            granted_at: "2026-01-02T00:00:00Z",
            granted_by: "root@example.test",
          },
        ],
        available_roles: [
          {
            slug: "platform-auditor",
            description: "Read-only.",
            capabilities: ["admin.audit.read"],
          },
        ],
      },
    });

    const detail = await getUser("u1");
    expect(detail.channelCount).toBe(7);
    expect(detail.memberships[0].workspaceName).toBe("NChat");
    expect(detail.roleGrants[0].grantedBy).toBe("root@example.test");
    expect(detail.availableRoles).toHaveLength(1);
  });

  it("escapes the identifier in the path", async () => {
    const fetchMock = stub({
      data: { ...USER, memberships: [], channel_count: 0, role_grants: [], available_roles: [] },
    });
    await getUser("a/b");
    expect(String(fetchMock.mock.calls[0][0])).toContain("/users/a%2Fb");
  });
});

describe("mutations", () => {
  it("sends only the field the endpoint accepts", async () => {
    setCSRFToken("csrf-1");
    const fetchMock = stub({
      data: { user_id: "u1", from_status: "active", to_status: "suspended", revoked_sessions: 2 },
    });
    const result = await updateUserStatus("u1", "suspended");

    expect(result.revokedSessions).toBe(2);
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("PATCH");
    // No role, no capability, no actor: the server derives all three, and a
    // body carrying them would be refused outright.
    expect(JSON.parse(String(init.body))).toEqual({ status: "suspended" });
    expect((init.headers as Headers).get("X-NChat-Admin-CSRF")).toBe("csrf-1");
  });

  it("reports how many sessions a revocation ended", async () => {
    stub({ data: { user_id: "u1", revoked_sessions: 3 } });
    expect(await revokeUserSessions("u1")).toBe(3);
  });

  it("grants and revokes a role without a response body", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await grantAdminRole("u1", "platform-auditor");
    await revokeAdminRole("u1", "platform-auditor");

    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      role_slug: "platform-auditor",
    });
    expect(String(fetchMock.mock.calls[1][0])).toContain("/admin-roles/platform-auditor");
  });

  it("maps the channel a status change returns", async () => {
    stub({ data: { ...CHANNEL, status: "archived" } });
    expect((await updateChannelStatus("c1", "archived")).status).toBe("archived");
  });
});

describe("channels and conversations", () => {
  it("maps a channel page", async () => {
    stub({ data: { channels: [CHANNEL], pagination: { next_cursor: null, has_more: false } } });
    const page = await listChannels({}, null, 25);
    expect(page.items[0].slug).toBe("eng");
    expect(page.hasMore).toBe(false);
  });

  it("maps a channel detail with its two separate authority lists", async () => {
    stub({
      data: {
        ...CHANNEL,
        category_name: "Times",
        moderators: [
          { user_id: "u1", display_name: "Ana", email: "ana@example.test", role: "moderator" },
        ],
        workspace_admins: [
          { user_id: "u2", display_name: "Root", email: "root@example.test", role: "owner" },
        ],
        members: [
          { user_id: "u3", display_name: "Zoe", email: "zoe@example.test", role: "member" },
        ],
        message_count: 4200,
      },
    });

    const detail = await getChannel("c1");
    expect(detail.moderators[0].role).toBe("moderator");
    expect(detail.workspaceAdmins[0].role).toBe("owner");
    // The membership preview is a third, separate list: being in a channel is
    // not the same as administering it.
    expect(detail.members[0].role).toBe("member");
    expect(detail.messageCount).toBe(4200);
  });

  it("maps conversation metadata and asks for nothing more", async () => {
    const fetchMock = stub({
      data: {
        conversations: [
          {
            id: "d1",
            workspace_id: "w1",
            workspace_name: "NChat",
            type: "group",
            status: "active",
            participant_count: 4,
            message_count: 120,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-02T00:00:00Z",
            last_activity_at: null,
          },
        ],
        pagination: { next_cursor: null, has_more: false },
      },
    });

    const page = await listConversations({ type: "group", status: "active" }, "cur", 25);
    const conversation = page.items[0];
    expect(conversation.participantCount).toBe(4);
    expect(conversation.messageCount).toBe(120);
    // The client type has no field for content, so there is nothing here to
    // render even if a future server sent it.
    expect(Object.keys(conversation)).not.toContain("title");
    expect(String(fetchMock.mock.calls[0][0])).toContain("type=group");
  });
});

describe("member candidates", () => {
  it("maps people and asks the channel-scoped endpoint", async () => {
    const fetchMock = stub({
      data: {
        candidates: [
          {
            user_id: "u1",
            display_name: "Ana",
            full_name: "Ana Lima",
            email: "ana@example.test",
            avatar_url: "/a.png",
            workspace_role: "guest",
          },
        ],
      },
    });

    const candidates = await listMemberCandidates("c1", "ana");
    expect(candidates[0].displayName).toBe("Ana");
    expect(candidates[0].workspaceRole).toBe("guest");
    const url = String(fetchMock.mock.calls[0][0]);
    // Channel-scoped, so the workspace is the server's to decide.
    expect(url).toContain("/channels/c1/member-candidates");
    expect(url).toContain("q=ana");
  });

  it("omits the term when there is nothing to search for", async () => {
    const fetchMock = stub({ data: { candidates: [] } });
    await listMemberCandidates("c1", "");
    expect(String(fetchMock.mock.calls[0][0])).not.toContain("q=");
  });

  it("treats a missing field as a broken contract", async () => {
    stub({ data: { candidates: [{ user_id: "u1", display_name: "Ana" }] } });
    await expect(listMemberCandidates("c1", "ana")).rejects.toMatchObject({
      code: ERR_INVALID_RESPONSE,
    });
  });
});

describe("policies", () => {
  it("maps anti-spam policies with the server's bounds", async () => {
    stub({
      data: {
        policies: [{ workspace: WORKSPACE, message_rate_limit_per_minute: 60 }],
        bounds: RATE_BOUNDS,
        pagination: { next_cursor: null, has_more: false },
      },
    });

    const page = await listAntiSpamPolicies(null, 25);
    expect(page.items[0].messageRateLimitPerMinute).toBe(60);
    expect(page.bounds.unit).toBe("messages_per_minute");
  });

  it("refuses bounds that contradict themselves", async () => {
    stub({
      data: {
        policies: [],
        bounds: { ...RATE_BOUNDS, min: 900 },
        pagination: { next_cursor: null, has_more: false },
      },
    });
    await expect(listAntiSpamPolicies(null, 25)).rejects.toMatchObject({
      code: ERR_INVALID_RESPONSE,
    });
  });

  it("maps upload policies with the gateway ceiling and the fixed controls", async () => {
    stub({
      data: {
        policies: [{ workspace: WORKSPACE, max_upload_bytes: 262144000 }],
        bounds: UPLOAD_BOUNDS,
        gateway_hard_cap_bytes: 536879104,
        deployment_managed: ["malware_scanning"],
        pagination: { next_cursor: null, has_more: false },
      },
    });

    const page = await listUploadPolicies(null, 25);
    expect(page.items[0].maxUploadBytes).toBe(262144000);
    expect(page.gatewayHardCapBytes).toBe(536879104);
    expect(page.deploymentManaged).toEqual(["malware_scanning"]);
    expect(page.bounds.step).toBe(1048576);
  });

  it("sends the policy value as the single named field", async () => {
    const fetchMock = stub({
      data: {
        policy: { workspace: WORKSPACE, message_rate_limit_per_minute: 30 },
        bounds: RATE_BOUNDS,
      },
    });
    expect((await updateAntiSpamPolicy("w1", 30)).messageRateLimitPerMinute).toBe(30);
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      message_rate_limit_per_minute: 30,
    });
  });

  it("sends the upload limit in bytes", async () => {
    const fetchMock = stub({
      data: {
        policy: { workspace: WORKSPACE, max_upload_bytes: 104857600 },
        bounds: UPLOAD_BOUNDS,
      },
    });
    expect((await updateUploadPolicy("w1", 104857600)).maxUploadBytes).toBe(104857600);
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      max_upload_bytes: 104857600,
    });
  });

  it("surfaces a rejected value as the API's error", async () => {
    stub({ error: { code: "bad_request", message: "invalid request" } }, 400);
    await expect(updateUploadPolicy("w1", 1572864)).rejects.toBeInstanceOf(AdminApiError);
  });
});
