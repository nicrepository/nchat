import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { MemoryRouter, Outlet, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { mockPresence } = vi.hoisted(() => ({ mockPresence: vi.fn() }));

vi.mock("../chat/presence", async () => {
  const actual = await vi.importActual<typeof import("../chat/presence")>("../chat/presence");
  return { ...actual, usePresence: (...args: unknown[]) => mockPresence(...args) };
});

import type { DMConversation } from "../chat/chatTypes";
import ChannelResultRow from "./ChannelResultRow";
import GroupResultRow from "./GroupResultRow";
import MessageResultRow from "./MessageResultRow";
import UserResultRow from "./UserResultRow";
import { channelResult, groupResult, messageResult, userResult } from "./searchFixtures";
import { readRestoredSearch, withRestoredSearch } from "./searchNavigation";
import { conversationLabel, formatDateTime } from "./searchLabels";

function Landing() {
  const location = useLocation();
  return (
    <output data-testid="landing" data-state={JSON.stringify(location.state)}>
      {location.pathname}
      {location.search}
    </output>
  );
}

function renderRow(row: ReactElement) {
  return render(
    <MemoryRouter initialEntries={["/chat/search"]}>
      <Routes>
        <Route path="/chat/search" element={row} />
        <Route path="/chat/:kind/:id" element={<Landing />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockPresence.mockReset().mockReturnValue("unknown");
});

describe("MessageResultRow", () => {
  it("shows author, conversation, time and a highlighted snippet", () => {
    renderRow(<MessageResultRow result={messageResult()} query="backup" />);
    const card = screen.getByRole("button");
    expect(card).toHaveTextContent("Juliane Lino");
    expect(card).toHaveTextContent("em #infraestrutura");
    expect(card.querySelector("time")).toHaveAttribute("datetime", "2026-09-01T09:41:00Z");
    expect(screen.getByText("backup", { selector: "mark" })).toBeInTheDocument();
    expect(card.querySelector(".global-search__avatar")).toHaveTextContent("JL");
  });

  it("opens a group message on its dm route as an explicit jump (MESSAGE_TARGET)", async () => {
    const result = messageResult({
      id: "m 7",
      conversation: { kind: "dm", id: "g1", type: "group", name: "Projeto" },
    });
    renderRow(<MessageResultRow result={result} query="backup" />);
    await userEvent.click(screen.getByRole("button"));

    const landing = screen.getByTestId("landing");
    expect(landing).toHaveTextContent("/chat/dm/g1?message=m%207");
    expect(JSON.parse(landing.dataset.state!)).toEqual({ messageJump: true });
  });

  it("never renders a snippet as markup", () => {
    const { container } = renderRow(
      <MessageResultRow
        result={messageResult({ bodyText: '<img src=x onerror="alert(1)">' })}
        query="img"
      />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByRole("button")).toHaveTextContent('<img src=x onerror="alert(1)">');
  });
});

describe("UserResultRow", () => {
  it("shows a known presence and hides an unknown one", () => {
    mockPresence.mockReturnValue("online");
    const { unmount } = renderRow(<UserResultRow result={userResult()} query="ju" />);
    expect(screen.getByRole("button")).toHaveTextContent("Disponível");
    expect(mockPresence).toHaveBeenCalledWith("u1");
    unmount();

    mockPresence.mockReturnValue("unknown");
    renderRow(<UserResultRow result={userResult({ avatarUrl: "/media/p.png" })} query="ju" />);
    expect(screen.getByRole("button")).not.toHaveTextContent("Status indisponível");
    expect(screen.getByRole("button").querySelector("img")).toHaveAttribute("src", "/media/p.png");
  });
});

describe("ChannelResultRow", () => {
  it("marks a private channel with a lock and says so to assistive tech", async () => {
    renderRow(
      <ChannelResultRow
        result={channelResult({
          isPrivate: true,
          description: "Operações",
          memberCount: 1,
          isGeneral: true,
        })}
        query="infra"
      />,
    );
    const card = screen.getByRole("button", {
      name: /^Canal privado infraestrutura Operações 1 participante/,
    });
    expect(card).toHaveTextContent("lock");
    expect(card).toHaveTextContent("Geral");
    await userEvent.click(card);
    expect(screen.getByTestId("landing")).toHaveTextContent("/chat/channel/c1");
  });

  it("uses # for a public channel and pluralizes the count", () => {
    renderRow(<ChannelResultRow result={channelResult()} query="" />);
    const card = screen.getByRole("button", { name: /^Canal infraestrutura 42 participantes$/ });
    expect(card).toHaveTextContent("tag");
  });
});

describe("GroupResultRow", () => {
  it("shows participants and activity, and opens the group on its dm route", async () => {
    renderRow(
      <GroupResultRow
        result={groupResult({ participantCount: 1, lastMessageAt: "2026-09-01T09:41:00Z" })}
        query="nchat"
      />,
    );
    const card = screen.getByRole("button");
    expect(card).toHaveTextContent("1 participante · última atividade");
    // Issue #1026: the group's own neutral identity, never a colour by id.
    const avatar = card.querySelector(".group-avatar") as HTMLElement;
    expect(avatar.dataset.mode).toBe("auto");
    expect(card.querySelector("[class*='global-search__avatar--']")).toBeNull();
    await userEvent.click(card);
    expect(screen.getByTestId("landing")).toHaveTextContent("/chat/dm/g1");
  });
});

// Issue #1026: a group result is drawn from the sidebar's canonical list, the
// one the chat outlet already holds — no request per row. The list is what the
// sidebar refetches on realtime updates, so a new list is a new identity.
function renderInChat(row: ReactElement, dms: DMConversation[]) {
  const tree = (list: DMConversation[]) => (
    <MemoryRouter initialEntries={["/chat/search"]}>
      <Routes>
        <Route element={<Outlet context={{ workspaceId: "ws-1", dms: list }} />}>
          <Route path="/chat/search" element={row} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
  const view = render(tree(dms));
  return { refetched: (list: DMConversation[]) => view.rerender(tree(list)) };
}

const sidebarGroup = (overrides: Partial<DMConversation> = {}): DMConversation => ({
  id: "g1",
  type: "group",
  name: "Projeto NChat",
  participants: [],
  ...overrides,
});

const groupAvatar = () => screen.getByRole("button").querySelector(".group-avatar") as HTMLElement;

describe("GroupResultRow identity (issue #1026)", () => {
  it("shows the canonical emoji, and the initials once a refetch returns it to Automático", () => {
    const { refetched } = renderInChat(<GroupResultRow result={groupResult()} query="" />, [
      sidebarGroup({ avatarEmoji: "🚀" }),
    ]);
    expect(groupAvatar()).toHaveTextContent("🚀");
    expect(groupAvatar().dataset.mode).toBe("emoji");

    refetched([sidebarGroup()]);
    expect(groupAvatar()).toHaveTextContent("PN");
    expect(groupAvatar().dataset.mode).toBe("auto");
  });

  it.each([
    ["an Automático group", [sidebarGroup()]],
    ["a group the list does not hold yet", [sidebarGroup({ id: "other", avatarEmoji: "🚀" })]],
    ["a 1:1 that happens to share the id", [sidebarGroup({ type: "1:1", avatarEmoji: "🚀" })]],
  ])("falls back to Automático for %s", (_case, dms) => {
    renderInChat(<GroupResultRow result={groupResult()} query="" />, dms);
    expect(groupAvatar()).toHaveTextContent("PN");
    expect(groupAvatar().dataset.mode).toBe("auto");
  });

  it("keeps a person on UserAvatar and a channel on its own icon", () => {
    renderInChat(<UserResultRow result={userResult()} query="" />, [
      sidebarGroup({ avatarEmoji: "🚀" }),
    ]);
    const person = screen.getByRole("button");
    expect(person.querySelector(".global-search__avatar")).not.toBeNull();
    expect(person.querySelector(".group-avatar")).toBeNull();
  });

  it("draws a channel without any avatar", () => {
    renderInChat(<ChannelResultRow result={channelResult()} query="" />, [
      sidebarGroup({ avatarEmoji: "🚀" }),
    ]);
    const channel = screen.getByRole("button");
    expect(channel).toHaveTextContent("tag");
    expect(channel.querySelector(".group-avatar, .global-search__avatar")).toBeNull();
  });
});

describe("search labels and history state", () => {
  it("names each conversation kind the way the sidebar does", () => {
    expect(conversationLabel({ kind: "channel", id: "c", type: "private", name: "x" })).toBe("#x");
    expect(conversationLabel({ kind: "dm", id: "d", type: "group", name: "" })).toBe(
      "Grupo sem nome",
    );
    expect(conversationLabel({ kind: "dm", id: "d", type: "direct", name: "Ana" })).toBe(
      "Conversa com Ana",
    );
    expect(conversationLabel({ kind: "dm", id: "d", type: "direct", name: "" })).toBe(
      "Conversa direta",
    );
    expect(formatDateTime("not a date")).toBe("");
  });

  it("round-trips a search through history state and rejects anything malformed", () => {
    const saved = withRestoredSearch({ keep: 1 }, { query: "backup", tab: "files" });
    expect(saved.keep).toBe(1);
    expect(readRestoredSearch(saved)).toEqual({ query: "backup", tab: "files" });
    expect(readRestoredSearch(withRestoredSearch(null, { query: "a", tab: "all" }))).toEqual({
      query: "a",
      tab: "all",
    });
    for (const bad of [
      null,
      "x",
      {},
      { globalSearch: null },
      { globalSearch: { query: 1, tab: "all" } },
      { globalSearch: { query: "a".repeat(513), tab: "all" } },
      { globalSearch: { query: "a", tab: "profile" } },
    ]) {
      expect(readRestoredSearch(bad)).toBeUndefined();
    }
  });
});
