/**
 * The surfaces that consume presence (RF-58).
 *
 * presence.test.tsx proves the store; this proves the wiring: that a single
 * server event reaches the sidebar, the details panel and the conversation
 * header, that each states the status in words as well as in colour, and that
 * none of them shows a grey "offline" dot for a person the server has said
 * nothing about yet.
 *
 * The frames are delivered through a fake socket rather than by poking the
 * store, so what is asserted is the path an event actually travels.
 */

import { act, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import type { SelfProfile } from "../profile/profileApi";
import { _resetSelfProfile } from "../profile/selfProfile";
import { HeaderDM } from "./ChatMessageArea";
import ChatSidebar from "./ChatSidebar";
import { _resetChatSocket } from "./chatSocket";
import type { Channel, ChannelDetails, DMConversation, Message } from "./chatTypes";
import ConversationDetailsPanel from "./ConversationDetailsPanel";
import MessageBubble from "./MessageBubble";
import { _resetPresenceStore } from "./presence";
import type { ConversationDetailsState } from "./useConversationDetails";

const { mockFetchMyProfile } = vi.hoisted(() => ({
  mockFetchMyProfile: vi.fn<(signal?: AbortSignal) => Promise<SelfProfile>>(),
}));

vi.mock("../profile/profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../profile/profileApi")>();
  return { ...actual, fetchMyProfile: (signal?: AbortSignal) => mockFetchMyProfile(signal) };
});

// ── fake socket ──────────────────────────────────────────────────────────────

class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;

  constructor() {
    FakeWebSocket.instances.push(this);
  }

  send() {}

  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }

  open() {
    this.onopen?.();
  }

  emit(data: unknown) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(data) }));
  }
}

const OriginalWebSocket = global.WebSocket;

function openSocket(): void {
  const instance = FakeWebSocket.instances.at(-1);
  if (!instance) throw new Error("no socket was created");
  act(() => {
    instance.open();
  });
}

function deliver(frame: unknown): void {
  const instance = FakeWebSocket.instances.at(-1);
  if (!instance) throw new Error("no socket was created");
  act(() => {
    instance.emit(frame);
  });
}

function presenceUpdate(userId: string, state: string, updatedAt: string) {
  return {
    type: "presence.updated",
    target_type: "dm",
    target_id: "dm-1",
    presence: { user_id: userId, state, updated_at: updatedAt },
  };
}

const T1 = "2026-08-11T10:00:00.000Z";
const T2 = "2026-08-11T10:00:05.000Z";

beforeEach(() => {
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;
  setTokens("test-token");
  _resetChatSocket(() => 0);
  _resetPresenceStore();
  _resetSelfProfile();
  mockFetchMyProfile.mockResolvedValue({ id: "user-self", displayName: "Ana Souza" });
});

afterEach(() => {
  _resetPresenceStore();
  _resetChatSocket();
  _resetSelfProfile();
  clearTokens();
  global.WebSocket = OriginalWebSocket;
  vi.restoreAllMocks();
});

// ── sidebar ──────────────────────────────────────────────────────────────────

const CHANNELS: Channel[] = [{ id: "geral", name: "geral", type: "public", canWrite: true }];

const DMS: DMConversation[] = [
  {
    id: "dm-1",
    type: "1:1",
    name: "Juliane Lino",
    participants: [],
    counterpart: { userId: "user-juliane", displayName: "Juliane Lino" },
  },
  {
    id: "group-1",
    type: "group",
    name: "Equipe Infra",
    participants: [],
  },
];

function renderSidebar() {
  return render(
    <MemoryRouter initialEntries={["/chat"]}>
      <ChatSidebar
        state={{
          status: "ready",
          currentUserId: "user-self",
          channels: CHANNELS,
          dms: DMS,
          // These tests are about presence, not grouping: no category means the
          // sidebar renders its channels ungrouped, which is what they assert on.
          categories: [],
        }}
        retry={() => {}}
      />
    </MemoryRouter>,
  );
}

function dmRow(): HTMLElement {
  return screen.getByRole("option", { name: /Mensagem direta com Juliane Lino/ });
}

describe("sidebar DM rows", () => {
  it("shows no indicator and makes no claim before the server answers", () => {
    renderSidebar();
    openSocket();

    expect(dmRow()).toHaveAccessibleName("Mensagem direta com Juliane Lino");
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
  });

  it("shows the counterpart online, then away, then offline without a reload", () => {
    renderSidebar();
    openSocket();

    deliver({
      type: "presence.snapshot",
      target_type: "dm",
      target_id: "dm-1",
      users: [{ user_id: "user-juliane", state: "online", updated_at: T1 }],
      complete: true,
    });
    expect(dmRow()).toHaveAccessibleName("Mensagem direta com Juliane Lino, Online");
    expect(within(dmRow()).getByTestId("presence-dot")).toHaveAttribute("data-presence", "online");

    deliver(presenceUpdate("user-juliane", "away", T2));
    expect(dmRow()).toHaveAccessibleName("Mensagem direta com Juliane Lino, Ausente");
    expect(within(dmRow()).getByTestId("presence-dot")).toHaveAttribute("data-presence", "away");

    deliver(presenceUpdate("user-juliane", "offline", "2026-08-11T10:00:09.000Z"));
    expect(dmRow()).toHaveAccessibleName("Mensagem direta com Juliane Lino, Offline");
    expect(within(dmRow()).getByTestId("presence-dot")).toHaveAttribute("data-presence", "offline");
  });

  it("never gives a group row a presence of its own", () => {
    renderSidebar();
    openSocket();
    deliver({
      type: "presence.snapshot",
      target_type: "dm",
      target_id: "dm-1",
      users: [{ user_id: "user-juliane", state: "online", updated_at: T1 }],
      complete: true,
    });

    const group = screen.getByRole("option", { name: /Grupo Equipe Infra/ });
    expect(group).toHaveAccessibleName("Grupo Equipe Infra");
    expect(within(group).queryByTestId("presence-dot")).not.toBeInTheDocument();
  });

  it("shows the authenticated user's own presence in the footer", async () => {
    renderSidebar();
    openSocket();
    // The footer only exists once the profile has resolved; until then it is an
    // identity-neutral placeholder with nothing to decorate.
    const profileLink = await screen.findByRole("link", { name: /Meu perfil de Ana Souza/ });

    deliver({
      type: "presence.snapshot",
      target_type: "channel",
      target_id: "geral",
      users: [{ user_id: "user-self", state: "online", updated_at: T1 }],
      complete: true,
    });

    expect(profileLink).toHaveAccessibleName("Meu perfil de Ana Souza, Online");
    expect(within(profileLink).getByTestId("presence-dot")).toHaveAttribute(
      "data-presence",
      "online",
    );
  });
});

// ── details panel ────────────────────────────────────────────────────────────

function channelDetails(): ChannelDetails {
  return {
    id: "geral",
    slug: "geral",
    name: "geral",
    type: "public",
    createdAt: "2026-01-01T00:00:00Z",
    memberCount: 3,
    onlineCount: 1,
    onlineMembers: [
      {
        userId: "user-juliane",
        displayName: "Juliane Lino",
        role: "member",
        presence: "online",
      },
    ],
    canManageMembers: false,
  };
}

function detailsState(): ConversationDetailsState {
  return detailsStateFor("user-juliane");
}

function detailsStateFor(userId: string): ConversationDetailsState {
  const data = channelDetails();
  data.onlineMembers = data.onlineMembers.map((member) => ({ ...member, userId }));
  return {
    details: { status: "ready", data: { kind: "channel", ...data } },
    files: { status: "ready", data: [] },
    reload: () => {},
  };
}

function renderDetails() {
  return render(
    <MemoryRouter>
      <ConversationDetailsPanel
        kind="channel"
        state={detailsState()}
        currentUserId="user-self"
        latestPin={null}
        onClose={() => {}}
      />
    </MemoryRouter>,
  );
}

describe("channel details member list", () => {
  // CQ-3. The endpoint reports `presence: "online"` for this member and the
  // panel deliberately ignores it: presence has one authority, and until it
  // answers the panel says what every other surface says, which is nothing.
  it("does not report the HTTP presence field", () => {
    renderDetails();
    openSocket();

    expect(screen.queryByText(/Membro · Online/)).not.toBeInTheDocument();
    expect(screen.getByText("Membro")).toBeInTheDocument();
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
  });

  it("follows the live state once the socket has answered", () => {
    renderDetails();
    openSocket();

    deliver({
      type: "presence.updated",
      target_type: "channel",
      target_id: "geral",
      presence: { user_id: "user-juliane", state: "away", updated_at: T2 },
    });

    expect(screen.getByText(/Membro · Ausente/)).toBeInTheDocument();
    expect(screen.getByTestId("presence-dot")).toHaveAttribute("data-presence", "away");
    expect(screen.queryByText(/Membro · Online/)).not.toBeInTheDocument();
  });

  it("keeps the state readable without colour", () => {
    renderDetails();
    openSocket();
    deliver({
      type: "presence.updated",
      target_type: "channel",
      target_id: "geral",
      presence: { user_id: "user-juliane", state: "offline", updated_at: T2 },
    });

    // The word, not only the dot: the row still says what the colour says.
    expect(screen.getByText(/Membro · Offline/)).toBeInTheDocument();
    // And the dot carries a hover tooltip with the same word.
    expect(screen.getByTestId("presence-dot")).toHaveAttribute("title", "Offline");
  });
});

// ── one authority for every surface (CQ-3) ───────────────────────────────────

// The sidebar row, the details panel and the conversation header, all showing
// the same person at the same time. Whatever they say, they say together: the
// bug this replaces let the panel read "Online" from an HTTP field while the
// other two, which only ever read the store, showed nothing.
describe("one presence for one person", () => {
  function renderAllThree() {
    return render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar
          state={{
            status: "ready",
            currentUserId: "user-self",
            channels: CHANNELS,
            dms: DMS,
            // These tests are about presence, not grouping: no category means the
            // sidebar renders its channels ungrouped, which is what they assert on.
            categories: [],
          }}
          retry={() => {}}
        />
        <ConversationDetailsPanel
          kind="channel"
          state={detailsStateFor("user-juliane")}
          currentUserId="user-self"
          latestPin={null}
          onClose={() => {}}
        />
        <HeaderDM
          name="Juliane Lino"
          counterpart={{ userId: "user-juliane", displayName: "Juliane Lino" }}
          presenceTarget="dm:dm-1"
        />
      </MemoryRouter>,
    );
  }

  function surfaces() {
    return {
      sidebar: within(dmRow()).queryByTestId("presence-dot")?.getAttribute("data-presence") ?? null,
      details:
        within(screen.getByTestId("chat-conversation-details"))
          .queryByTestId("presence-dot")
          ?.getAttribute("data-presence") ?? null,
      header:
        within(screen.getByTestId("chat-msg-header"))
          .queryByTestId("presence-dot")
          ?.getAttribute("data-presence") ?? null,
    };
  }

  it("agrees across the sidebar, the panel and the header at every step", () => {
    renderAllThree();
    openSocket();

    // Nothing has been said yet, so nothing is claimed anywhere.
    expect(surfaces()).toEqual({ sidebar: null, details: null, header: null });

    for (const [state, at] of [
      ["online", T1],
      ["away", T2],
      ["offline", "2026-08-11T10:00:09.000Z"],
    ] as const) {
      // The server publishes a presence change into every conversation the
      // person is visible in, so both of the ones on screen are told — the DM
      // the row and header are showing, and the channel the panel is showing.
      for (const [kind, targetId] of [
        ["dm", "dm-1"],
        ["channel", "geral"],
      ] as const) {
        deliver({
          type: "presence.updated",
          target_type: kind,
          target_id: targetId,
          presence: { user_id: "user-juliane", state, updated_at: at },
        });
      }
      expect(surfaces()).toEqual({ sidebar: state, details: state, header: state });
    }
  });
});

// ── message bubbles ──────────────────────────────────────────────────────────

// The dot beside a sender's avatar is decorative and lives inside an
// aria-hidden wrapper, so on this surface — unlike the sidebar row or the
// header, which fold the state into an accessible name — nothing stated it in
// words. A colour is not a fact a screen reader can read, and it is not one a
// colour-blind reader can distinguish either.
describe("message bubbles", () => {
  const message: Message = {
    id: "msg-1",
    senderId: "user-juliane",
    senderDisplayName: "Juliane Lino",
    senderEmail: "",
    kind: "user",
    bodyText: "olá",
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt: T1,
    updatedAt: T1,
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
  };

  function renderBubble() {
    return render(
      <MemoryRouter initialEntries={["/chat"]}>
        <MessageBubble
          message={message}
          presenceTarget="dm:dm-1"
          onToggleReaction={() => {}}
          onReplyMessage={() => {}}
          onReferenceMessage={() => {}}
          onToggleFavorite={() => {}}
          onEditMessage={() => Promise.resolve(message)}
          onEditForbidden={() => {}}
          onDeleteMessage={() => Promise.resolve()}
          allowedReactionEmojis={[]}
          recentReactionEmojis={[]}
          reactionMenuVisible={false}
          onReactionMenuVisibleChange={() => {}}
          pickerOpen={false}
          onPickerOpenChange={() => {}}
        />
      </MemoryRouter>,
    );
  }

  /** The message's text, as an assistive technology would read it. */
  function announced(): string {
    return screen.getByTestId("chat-msg-bubble").textContent ?? "";
  }

  it.each([
    ["online", "Online"],
    ["away", "Ausente"],
    ["offline", "Offline"],
  ])("states %s in words next to the sender", (state, label) => {
    renderBubble();
    openSocket();
    deliver(presenceUpdate("user-juliane", state, T1));

    expect(screen.getByText("Juliane Lino")).toBeInTheDocument();
    expect(screen.getByTestId("chat-msg-sender-presence")).toHaveTextContent(`Status: ${label}`);
    // And it is not the dot's colour doing the work: the dot stays decorative.
    expect(screen.getByTestId("presence-dot")).toHaveAttribute("aria-hidden", "true");
    expect(announced()).toContain(label);
  });

  it("claims nothing about a sender the server has not answered for", () => {
    renderBubble();
    openSocket();

    expect(screen.getByText("Juliane Lino")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-msg-sender-presence")).not.toBeInTheDocument();
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
    expect(announced()).not.toContain("Offline");
    expect(announced()).not.toContain("Status");
  });
});
