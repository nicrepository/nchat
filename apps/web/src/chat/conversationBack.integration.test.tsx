/**
 * The conversation header's Back (issue #1124), through the real <App />: the
 * document's router over jsdom's real session history, the real shell, the
 * real message area. Only the chat APIs and the socket are fakes.
 *
 * jsdom applies no stylesheet, so the button is in the DOM at every width here;
 * where it is *shown* is the stylesheet's job and is covered by Playwright.
 */

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, use } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import App from "../App";
import { clearTokens, setTokens } from "../lib/authSession";
import { _resetDocumentEntry, documentHistory } from "../lib/documentEntry";
import { withRestoredSearch } from "../search/searchNavigation";
import { groupResult, resultPage } from "../search/searchFixtures";
import { _resetChatSocket } from "./chatSocket";
import { NAV_DRAWER_QUERY } from "./useNavDrawer";
import type { Message, MessagePage } from "./chatTypes";

const { api, favoritesGate } = vi.hoisted(() => {
  let open = () => {};
  const promise = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { favoritesGate: { promise, open: () => open(), real: false }, api: makeApi() };
  function makeApi() {
    return {
      fetchSidebarData: vi.fn(),
      fetchChannelMessages: vi.fn(),
      fetchDMMessages: vi.fn(),
      fetchPins: vi.fn(),
      fetchAllowedReactionEmojis: vi.fn(),
      fetchMentionCandidates: vi.fn(),
      fetchChannelDetails: vi.fn(),
      fetchConversationAttachments: vi.fn(),
      fetchFavorites: vi.fn(),
      searchCategory: vi.fn(),
    };
  }
});

vi.mock("./chatApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./chatApi")>()),
  fetchSidebarData: api.fetchSidebarData,
  fetchChannelMessages: api.fetchChannelMessages,
  fetchDMMessages: api.fetchDMMessages,
  fetchPins: api.fetchPins,
  fetchAllowedReactionEmojis: api.fetchAllowedReactionEmojis,
  fetchMentionCandidates: api.fetchMentionCandidates,
  fetchChannelDetails: api.fetchChannelDetails,
  fetchFavorites: api.fetchFavorites,
}));

vi.mock("../search/searchApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../search/searchApi")>()),
  searchCategory: api.searchCategory,
}));

// A route whose render suspends until the test lets it through, so a move to it
// stays a pending transition while the previous screen is still on display.
vi.mock("./FavoritesPage", async (importOriginal) => {
  const { default: FavoritesPage } = await importOriginal<typeof import("./FavoritesPage")>();
  return {
    default: function SuspendedFavorites() {
      if (favoritesGate.real) return <FavoritesPage />;
      use(favoritesGate.promise);
      return <p>Favoritos prontos</p>;
    },
  };
});

vi.mock("./filesApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./filesApi")>()),
  fetchConversationAttachments: api.fetchConversationAttachments,
}));

class FakeWebSocket {
  static OPEN = 1;
  static instances: FakeWebSocket[] = [];
  readyState = FakeWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() {
    FakeWebSocket.instances.push(this);
    queueMicrotask(() => this.onopen?.());
  }
  send() {}
  close() {}
}

const OriginalWebSocket = global.WebSocket;

beforeAll(async () => {
  // Resolved before any assertion starts, as chatTargetNavigation does: this
  // only loads the lazy routes' modules — a one-off transform cost of the test
  // runner, not of the app — so no test is charged for compiling them. Nothing
  // is rendered here: every test's mount is a real first mount of its own, in
  // whatever order the tests run.
  await Promise.all([
    import("./ChatMessageArea"),
    import("./ChatComposer"),
    import("../search/GlobalSearchPage"),
  ]);
});

afterAll(() => {
  global.WebSocket = OriginalWebSocket;
});

const channelId = "11111111-1111-4111-8111-111111111111";
const dmId = "22222222-2222-4222-8222-222222222222";
const groupId = "33333333-3333-4333-8333-333333333333";
const goneId = "44444444-4444-4444-8444-444444444444";

const CHANNEL = `/chat/channel/${channelId}`;
const DM = `/chat/dm/${dmId}`;
const GROUP = `/chat/dm/${groupId}`;

function message(id: string, bodyText: string): Message {
  return {
    id,
    senderId: "sender-1",
    senderDisplayName: "Alice",
    senderEmail: "alice@example.com",
    kind: "user",
    bodyText,
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    deletedAt: null,
    createdAt: "2026-01-01T10:00:00Z",
    updatedAt: "2026-01-01T10:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
  };
}

const page = (messages: Message[]): MessagePage => ({ messages, nextCursor: "" });

/** The sidebar payload for `userId`; every user here can open the same conversations. */
function sidebarFor(userId: string, extra: { id: string; name: string }[] = []) {
  return {
    currentUserId: userId,
    workspaceId: "ws-1",
    channels: [{ id: channelId, name: "geral", type: "public" }],
    dms: [
      { id: dmId, type: "1:1", name: "Juliane", participants: [] },
      { id: groupId, type: "group", name: "Projeto", participants: [] },
      ...extra.map(({ id, name }) => ({ id, type: "group", name, participants: [] })),
    ],
  };
}

/** Lets every promise already settled run its callbacks, and React commit what they did. */
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 0)));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

let user: ReturnType<typeof userEvent.setup>;

/** The session, the socket and the chat API answers every test starts from. */
function installFakes() {
  clearTokens();
  setTokens("test-token");
  vi.clearAllMocks();
  favoritesGate.real = false;
  api.fetchFavorites.mockResolvedValue({ favorites: [], nextCursor: "" });
  window.matchMedia = undefined as unknown as typeof window.matchMedia;
  // A message jump scrolls its target into view; jsdom has no layout to do it with.
  window.Element.prototype.scrollIntoView = vi.fn();
  // No timer between pointer events: user-event dispatches the very same events,
  // in the same order, each awaited through act, so every render and effect a
  // press causes has run before the next step. It only drops the setTimeout(0)
  // it otherwise waits between events, which a user's real gestures do not
  // guarantee either. One test below runs with the default delay to show the
  // outcome does not depend on it.
  user = userEvent.setup({ delay: null });
  FakeWebSocket.instances = [];
  global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  _resetChatSocket();

  api.fetchSidebarData.mockResolvedValue(sidebarFor("me-1"));
  api.searchCategory.mockReturnValue(new Promise(() => {}));
  api.fetchChannelMessages.mockResolvedValue(page([message("m-chan", "no canal")]));
  api.fetchDMMessages.mockResolvedValue(page([message("m-dm", "na conversa")]));
  api.fetchPins.mockResolvedValue([]);
  api.fetchAllowedReactionEmojis.mockResolvedValue(["👍"]);
  api.fetchMentionCandidates.mockResolvedValue([]);
  api.fetchChannelDetails.mockResolvedValue({
    id: channelId,
    slug: "geral",
    name: "geral",
    type: "public",
    createdAt: "2026-01-01T10:00:00Z",
    memberCount: 1,
    onlineCount: 0,
    onlineMembers: [],
    canManageMembers: false,
  });
  api.fetchConversationAttachments.mockResolvedValue([]);
}

beforeEach(installFakes);

afterEach(async () => {
  // A traversal still queued — a test that failed right after a press — lands
  // here, not as a popstate in the next test.
  await settle();
  _resetChatSocket();
  global.WebSocket = OriginalWebSocket;
  FakeWebSocket.instances = [];
  clearTokens();
});

/** A fresh document entered at `path` — a deep link, or a reload of it. */
function loadAt(path: string, { strict = false } = {}) {
  window.history.replaceState(null, "", path);
  _resetDocumentEntry();
  return render(strict ? <StrictMode>{<App />}</StrictMode> : <App />);
}

const back = () => screen.getByRole("button", { name: "Voltar" });
const navToggle = () => screen.getByTestId("chat-nav-toggle");
const path = () => window.location.pathname + window.location.search;

async function expectHeader(name: string) {
  await waitFor(() =>
    expect(within(screen.getByTestId("chat-msg-header")).getByText(name)).toBeInTheDocument(),
  );
}

async function openFromSidebar(name: RegExp) {
  await user.click(await screen.findByRole("option", { name }));
}

/** In-app navigation as any link drives the router: a PUSH on the document's history. */
async function push(to: string, state?: unknown) {
  await act(async () => {
    documentHistory.push(to, state);
  });
}

/**
 * The next popstate, awaited the way every other expectation here is: a press
 * that does not travel fails the test within waitFor's timeout instead of
 * leaving it hanging — a hung test keeps running after it times out and would
 * take the next test's popstate as its own.
 */
function nextPopstate() {
  let fired = false;
  const onPop = () => {
    fired = true;
  };
  window.addEventListener("popstate", onPop, { once: true });
  return () =>
    waitFor(() => expect(fired).toBe(true)).finally(() =>
      window.removeEventListener("popstate", onPop),
    );
}

/** How long a press may take to travel: waitFor's default, which nextPopstate uses. */
const POPSTATE_LIMIT_MS = 1000;

/**
 * Back travels with history.go, which the browser answers with a popstate.
 *
 * The popstate is awaited inside act: it arrives as a task of its own, after the
 * press, and the router's update it causes then joins act's queue — so the
 * travel is rendered, committed and its effects run before the next step. Out
 * of act, that update goes to React's scheduler, and under load the next press
 * could come before the effect that ends the travel had run (the shell ignores
 * a press while a travel is pending). A plain promise, not waitFor: waitFor
 * turns the act environment off while it waits, which React reports inside act.
 * The watchdog only rejects, so a press that does not travel still fails
 * instead of leaving the test hanging.
 */
async function pressBack() {
  let onPop = () => {};
  let watchdog: ReturnType<typeof setTimeout> | undefined;
  let startWatchdog = () => {};
  const popped = new Promise<void>((resolve, reject) => {
    onPop = () => resolve();
    startWatchdog = () => {
      watchdog = setTimeout(
        () => reject(new Error(`Back did not travel within ${POPSTATE_LIMIT_MS}ms`)),
        POPSTATE_LIMIT_MS,
      );
    };
  });
  window.addEventListener("popstate", onPop, { once: true });
  try {
    await user.click(back());
    startWatchdog();
    await act(() => popped);
  } finally {
    clearTimeout(watchdog);
    window.removeEventListener("popstate", onPop);
  }
}

async function walkThroughThreeConversations() {
  await expectHeader("geral");
  await openFromSidebar(/Mensagem direta com Juliane/);
  await expectHeader("Juliane");
  await openFromSidebar(/Grupo Projeto/);
  await expectHeader("Projeto");
}

describe("retorno seguro das superfícies (#1124)", () => {
  function compact() {
    window.matchMedia = vi.fn((query: string) => ({
      matches: query === NAV_DRAWER_QUERY,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: () => true,
    }));
  }

  it.each(["/outside-before-document", "/login"])(
    "Escape no deep link da busca não retorna ao predecessor desconhecido %s",
    async (predecessor) => {
      compact();
      // jsdom requires same-origin URLs; the predecessor is unknown to this document's trail.
      window.history.replaceState(null, "", predecessor);
      window.history.pushState(null, "", "/chat/search");
      _resetDocumentEntry();
      render(<App />);
      await screen.findByRole("searchbox");
      const entries = window.history.length;
      const go = vi.spyOn(window.history, "go");
      try {
        await user.keyboard("{Escape}");
        expect(go).not.toHaveBeenCalled();
        expect(path()).toBe("/chat/search");
        expect(window.history.length).toBe(entries);
        expect(navToggle()).toHaveAttribute("aria-expanded", "true");
        expect(navToggle()).toHaveFocus();
      } finally {
        go.mockRestore();
      }
    },
  );

  it.each(["Escape", "Enter", "Space", "pointer", "touch"])(
    "busca interna retorna à conversa por %s com URL e foco coerentes",
    async (press) => {
      compact();
      loadAt(CHANNEL);
      await expectHeader("geral");
      await user.click(navToggle());
      await user.click(screen.getByRole("button", { name: "Buscar no NChat" }));
      await screen.findByRole("searchbox");
      const popped = nextPopstate();
      if (press === "Escape") await user.keyboard("{Escape}");
      else {
        const control = back();
        expect(control).toHaveAttribute("type", "button");
        expect(control).toHaveAttribute("title", "Voltar");
        if (press === "pointer") await user.click(control);
        else if (press === "touch") await user.pointer({ keys: "[TouchA]", target: control });
        else {
          control.focus();
          await user.keyboard(press === "Enter" ? "{Enter}" : " ");
        }
      }
      await popped();
      await expectHeader("geral");
      expect(path()).toBe(CHANNEL);
      expect(navToggle()).toHaveFocus();
    },
  );

  it("Escape já consumido por outro controle não fecha a busca", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await push("/chat/search");
    const field = await screen.findByRole("searchbox");
    const consumed = (event: Event) => event.preventDefault();
    field.addEventListener("keydown", consumed);
    fireEvent.keyDown(field, { key: "Escape" });
    field.removeEventListener("keydown", consumed);
    expect(path()).toBe("/chat/search");
    expect(field).toHaveFocus();
  });

  it("busca → resultado → busca restaura consulta/aba e mantém o Forward nativo", async () => {
    api.searchCategory.mockImplementation((category: string) =>
      Promise.resolve(
        resultPage(category === "groups" ? [groupResult({ id: groupId, title: "Projeto" })] : []),
      ),
    );
    loadAt(CHANNEL);
    await expectHeader("geral");
    await push("/chat/search");
    const field = await screen.findByRole("searchbox");
    await user.type(field, "projeto");
    await user.click(screen.getByRole("tab", { name: "Grupos" }));
    await user.click(await screen.findByRole("button", { name: /Grupo Projeto/ }));
    await expectHeader("Projeto");
    await pressBack();
    expect(path()).toBe("/chat/search");
    expect(await screen.findByRole("searchbox")).toHaveValue("projeto");
    expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
    const popped = nextPopstate();
    act(() => window.history.forward());
    await popped();
    await expectHeader("Projeto");
    expect(path()).toBe(GROUP);
  });

  it("favoritos por deep link abre a lista sem inventar predecessor", async () => {
    compact();
    favoritesGate.real = true;
    loadAt("/chat/favorites");
    await screen.findByText(/Nenhuma mensagem favoritada/);
    const entries = window.history.length;
    await user.click(back());
    expect(path()).toBe("/chat/favorites");
    expect(window.history.length).toBe(entries);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
    expect(navToggle()).toHaveFocus();
  });

  it("favoritos internos retornam à conversa anterior validada", async () => {
    compact();
    favoritesGate.real = true;
    loadAt(CHANNEL);
    await expectHeader("geral");
    await push("/chat/favorites");
    await screen.findByText(/Nenhuma mensagem favoritada/);
    await pressBack();
    expect(path()).toBe(CHANNEL);
    await expectHeader("geral");
    expect(document.body).not.toHaveFocus();
  });

  it("favoritos → conversa → favoritos usa o mesmo histórico contextual", async () => {
    favoritesGate.real = true;
    api.fetchFavorites.mockResolvedValue({
      favorites: [{
        message: message("favorite-1", "Favorito seguro"),
        channelId,
        dmConversationId: "",
        favoritedAt: "2026-01-01",
      }],
      nextCursor: "",
    });
    loadAt("/chat/favorites");
    await user.click(await screen.findByRole("link", { name: "Ver no canal" }));
    await expectHeader("geral");
    await pressBack();
    expect(path()).toBe("/chat/favorites");
    await screen.findByText("Favorito seguro");
  });

  it.each(["/chat/search", "/chat/favorites"])(
    "a Home registrada é o retorno de %s, sem confundi-la com o drawer",
    async (surface) => {
      compact();
      favoritesGate.real = true;
      loadAt("/chat");
      await screen.findByRole("option", { name: /geral/ });
      await push(surface);
      if (surface.endsWith("search")) await screen.findByRole("searchbox");
      else await screen.findByText(/Nenhuma mensagem favoritada/);
      await pressBack();
      expect(path()).toBe("/chat");
      expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
      expect(navToggle()).toHaveAttribute("aria-expanded", "false");
      expect(document.body).not.toHaveFocus();
    },
  );

  it("conversa aberta da Home mantém o fallback existente para a lista e a seleção", async () => {
    compact();
    loadAt("/chat");
    await openFromSidebar(/Canal geral/);
    await expectHeader("geral");
    const entries = window.history.length;
    await user.click(back());
    expect(path()).toBe(CHANNEL);
    expect(window.history.length).toBe(entries);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("option", { name: /Canal geral/ })).toHaveAttribute("aria-selected", "true");
  });

  it("nova sessão impede que Escape reutilize o predecessor da busca", async () => {
    compact();
    loadAt(CHANNEL);
    await expectHeader("geral");
    await push("/chat/search");
    await screen.findByRole("searchbox");
    act(() => setTokens("new-session"));
    await screen.findByRole("searchbox");
    await user.keyboard("{Escape}");
    expect(path()).toBe("/chat/search");
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
  });
});

describe("Voltar contextual do cabeçalho (#1124)", () => {
  it("sidebar A → B → C: volta a B, depois a A, depois abre a lista sem sair de A", async () => {
    loadAt(CHANNEL);
    await walkThroughThreeConversations();

    await pressBack();
    expect(path()).toBe(DM);
    await expectHeader("Juliane");

    await pressBack();
    expect(path()).toBe(CHANNEL);
    await expectHeader("geral");

    expect(navToggle()).toHaveAttribute("aria-expanded", "false");
    await user.click(back());
    expect(path()).toBe(CHANNEL);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
    expect(navToggle()).toHaveFocus();
  });

  it("deep link ou reload sem histórico: Voltar abre a lista e nunca navega", async () => {
    const before = window.history.length;
    loadAt(DM);
    await expectHeader("Juliane");

    await user.click(back());

    expect(path()).toBe(DM);
    expect(window.history.length).toBe(before);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
  });

  it("um salto para mensagem na mesma conversa não é uma conversa anterior", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");
    await push(`${DM}?message=m-dm`, { messageJump: true });
    await push(`${DM}?message=m-dm-2`, { messageJump: true });

    await pressBack();

    await waitFor(() => expect(path()).toBe(CHANNEL));
    await expectHeader("geral");
  });

  it("resultado da busca volta à busca, com o estado que ela guardou no histórico", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await push("/chat/search");
    await screen.findByRole("searchbox");
    await push(GROUP);
    await expectHeader("Projeto");

    await pressBack();

    expect(path()).toBe("/chat/search");
  });

  it("link interno ou menção para outra conversa: Voltar retorna à de origem", async () => {
    loadAt(GROUP);
    await expectHeader("Projeto");
    // A mention's DM and an internal link both arrive as a PUSH to the target.
    await push(DM);
    await expectHeader("Juliane");

    await pressBack();

    expect(path()).toBe(GROUP);
    await expectHeader("Projeto");
  });

  it("Voltar/Avançar nativos mantêm o destino coerente, sem ciclos", async () => {
    loadAt(CHANNEL);
    await walkThroughThreeConversations();

    // Native Back to B, native Forward to C, native Back to B again.
    for (const move of [-1, 1, -1]) {
      const popped = nextPopstate();
      act(() => window.history.go(move));
      await popped();
    }
    await expectHeader("Juliane");

    await pressBack();
    expect(path()).toBe(CHANNEL);

    // From A, native Forward to B: its Back is still A.
    const popped = nextPopstate();
    act(() => window.history.go(1));
    await popped();
    await expectHeader("Juliane");
    await pressBack();
    expect(path()).toBe(CHANNEL);
  });

  it("ignora uma conversa que não está mais disponível e cai para a anterior válida", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await push(`/chat/dm/${goneId}`);
    await push(DM);
    await expectHeader("Juliane");

    await pressBack();

    expect(path()).toBe(CHANNEL);
  });

  it("detalhes abertos: o fechar do painel é o primeiro passo do Voltar, depois o Voltar sai (CQ-04)", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Grupo Projeto/);
    await expectHeader("Projeto");
    const toggle = screen.getByTestId("chat-details-toggle");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const close = await screen.findByTestId("chat-details-close");
    // One control, named for what it does, wherever the stylesheet puts it.
    expect(close).toHaveAccessibleName("Fechar detalhes do grupo");
    expect(close).toHaveFocus();

    await user.click(close);

    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(path()).toBe(GROUP);
    await waitFor(() => expect(toggle).toHaveFocus());

    await pressBack();
    expect(path()).toBe(CHANNEL);
  });

  it("detalhes abertos: o fechar do painel responde a Enter e a Espaço (CQ-04)", async () => {
    loadAt(GROUP);
    await expectHeader("Projeto");
    const toggle = screen.getByTestId("chat-details-toggle");
    for (const key of ["{Enter}", " "]) {
      await user.click(toggle);
      expect(await screen.findByTestId("chat-details-close")).toHaveFocus();
      await user.keyboard(key);
      expect(toggle).toHaveAttribute("aria-expanded", "false");
      await waitFor(() => expect(toggle).toHaveFocus());
    }
    expect(path()).toBe(GROUP);
  });

  it("um segundo toque antes da chegada não volta duas vezes", async () => {
    loadAt(CHANNEL);
    await walkThroughThreeConversations();
    const popped = nextPopstate();

    fireEvent.click(back());
    fireEvent.click(back());
    await popped();

    await expectHeader("Juliane");
    expect(path()).toBe(DM);
  });

  it.each([
    { presses: 1, strict: false },
    { presses: 2, strict: false },
    { presses: 1, strict: true },
    { presses: 2, strict: true },
  ])(
    "foco DM → canal após $presses acionamento(s), StrictMode=$strict",
    async ({ presses, strict }) => {
      loadAt(CHANNEL, { strict });
      await expectHeader("geral");
      await openFromSidebar(/Mensagem direta com Juliane/);
      await expectHeader("Juliane");
      const original = back();
      original.focus();
      const go = vi.spyOn(window.history, "go");
      const popped = nextPopstate();
      try {
        // Both activations happen before the browser can deliver the POP.
        act(() => {
          for (let press = 0; press < presses; press += 1) fireEvent.click(original);
        });
        expect(go).toHaveBeenCalledExactlyOnceWith(-1);
        await popped();

        await expectHeader("geral");
        expect(path()).toBe(CHANNEL);
        expect(original).not.toBeInTheDocument();
        await waitFor(() => expect(back()).toHaveFocus());
        expect(go).toHaveBeenCalledTimes(1);
      } finally {
        go.mockRestore();
      }
    },
  );

  it("uma nova sessão não reaproveita o histórico da anterior", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");

    act(() => setTokens("another-session"));
    await expectHeader("Juliane");
    await user.click(back());

    expect(path()).toBe(DM);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
  });

  it("opera por teclado (Enter e Espaço) e nunca deixa o foco cair no body", async () => {
    // The default user-event, timers between events and all: same outcome.
    user = userEvent.setup();
    loadAt(CHANNEL);
    await walkThroughThreeConversations();
    const composer = (name: string) =>
      screen.getByRole("textbox", { name: `Mensagem para ${name}…` });

    back().focus();
    let popped = nextPopstate();
    await user.keyboard("{Enter}");
    await popped();
    await expectHeader("Juliane");
    // The same first focus a conversation picked in the sidebar gets.
    await waitFor(() => expect(composer("Juliane")).toHaveFocus());

    back().focus();
    popped = nextPopstate();
    await user.keyboard(" ");
    await popped();
    await expectHeader("geral");
    // Channel ← DM swaps the header and the pressed button with it: focus goes
    // to the new header's Back rather than dropping to <body>.
    await waitFor(() => expect(back()).toHaveFocus());
    expect(composer("#geral")).not.toHaveFocus();
  });

  it("preserva o rascunho da conversa de destino e não recria a conexão", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    const input = await screen.findByTestId("chat-composer-input");
    fireEvent.paste(input, {
      clipboardData: {
        files: [],
        types: ["text/plain"],
        getData: (type: string) => (type === "text/plain" ? "rascunho do canal" : ""),
      },
    });
    await waitFor(() => expect(input).toHaveTextContent("rascunho do canal"));
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");
    const sockets = FakeWebSocket.instances.length;

    await pressBack();

    await expectHeader("geral");
    await waitFor(() =>
      expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("rascunho do canal"),
    );
    expect(FakeWebSocket.instances).toHaveLength(sockets);
  });

  it("em StrictMode o percurso A → B → C → B → A é o mesmo", async () => {
    loadAt(CHANNEL, { strict: true });
    await walkThroughThreeConversations();

    await pressBack();
    expect(path()).toBe(DM);
    // On screen before the next press: one pressed while the move is still
    // rendering is ignored by design, and a loaded runner can get there first.
    await expectHeader("Juliane");
    await pressBack();
    expect(path()).toBe(CHANNEL);
    await expectHeader("geral");
  });

  it("na faixa do drawer, resize não navega nem cria entradas; o fallback abre o drawer modal", async () => {
    let drawer = true;
    const listeners = new Set<() => void>();
    window.matchMedia = ((query: string) => ({
      get matches() {
        return query === NAV_DRAWER_QUERY && drawer;
      },
      media: query,
      addEventListener: (_: string, callback: () => void) => void listeners.add(callback),
      removeEventListener: (_: string, callback: () => void) => void listeners.delete(callback),
    })) as unknown as typeof window.matchMedia;
    const resizeTo = (isDrawer: boolean) => {
      drawer = isDrawer;
      act(() => listeners.forEach((callback) => callback()));
    };

    loadAt(CHANNEL);
    await expectHeader("geral");
    await user.click(navToggle());
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");
    const entries = window.history.length;

    resizeTo(false);
    resizeTo(true);

    expect(window.history.length).toBe(entries);
    expect(path()).toBe(DM);
    expect(navToggle()).toHaveAttribute("aria-expanded", "false");

    await pressBack();
    expect(path()).toBe(CHANNEL);

    await user.click(back());
    expect(path()).toBe(CHANNEL);
    expect(screen.getByRole("main", { hidden: true })).toHaveAttribute("inert");
    expect(navToggle()).toHaveFocus();
  });

  it("REPLACE intermediário seguido de Avançar: C → B′ → A (CQ-01)", async () => {
    loadAt(CHANNEL);
    await walkThroughThreeConversations();
    const go = async (move: number) => {
      const popped = nextPopstate();
      act(() => window.history.go(move));
      await popped();
    };

    await go(-1);
    await expectHeader("Juliane");
    // B replaced in place — the same conversation, only the query changes.
    await act(async () => {
      documentHistory.replace(`${DM}?message=m-dm`, { messageJump: true });
    });
    await go(1);
    await expectHeader("Projeto");

    await pressBack();
    expect(path()).toBe(`${DM}?message=m-dm`);
    await expectHeader("Juliane");
    await pressBack();
    expect(path()).toBe(CHANNEL);
  });

  it("REPLACE intermediário para outro destino: Avançar e Voltar chegam ao substituto (CQ-01)", async () => {
    loadAt(CHANNEL);
    await walkThroughThreeConversations();
    let popped = nextPopstate();
    act(() => window.history.go(-1));
    await popped();
    await expectHeader("Juliane");
    await act(async () => {
      documentHistory.replace(CHANNEL);
    });
    await expectHeader("geral");
    popped = nextPopstate();
    act(() => window.history.go(1));
    await popped();
    await expectHeader("Projeto");

    // B′ is the channel now: one step back, in B's slot of the list.
    await pressBack();
    expect(path()).toBe(CHANNEL);
    await expectHeader("geral");
    // From B′ (the channel), A is the same conversation: nothing further back.
    await user.click(back());
    expect(path()).toBe(CHANNEL);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
  });

  it("refresh do mesmo usuário preserva o histórico depois de confirmado (CQ-02)", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");
    const check = deferred<ReturnType<typeof sidebarFor>>();
    api.fetchSidebarData.mockReturnValueOnce(check.promise);

    act(() => setTokens("rotated", "refresh"));
    // Not confirmed yet: the sidebar on screen may be someone else's.
    await user.click(back());
    expect(path()).toBe(DM);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
    await user.click(navToggle());

    check.resolve(sidebarFor("me-1"));
    await settle();
    await pressBack();
    expect(path()).toBe(CHANNEL);
  });

  it("refresh que confirma outro usuário descarta o histórico, mesmo de conversa que ele acessa (CQ-02)", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");
    // The other user can open #geral too: access is not a licence to reuse the trail.
    api.fetchSidebarData.mockResolvedValue(sidebarFor("other-2", [{ id: goneId, name: "Equipe" }]));

    act(() => setTokens("someone-else", "refresh"));
    await screen.findByRole("option", { name: /Grupo Equipe/ });
    await user.click(back());

    expect(path()).toBe(DM);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
  });

  it("logout e novo login do mesmo usuário não reaproveitam o histórico (CQ-02)", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");
    const view = screen.getByTestId("chat-msg-header");
    expect(view).toBeInTheDocument();

    act(() => {
      clearTokens();
      setTokens("me-again");
    });
    await expectHeader("Juliane");
    await user.click(back());

    expect(path()).toBe(DM);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
  });

  it("remount com o mesmo usuário mantém o histórico; com outro, em StrictMode, não (CQ-02)", async () => {
    let view = loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");

    view.unmount();
    view = render(<App />);
    await expectHeader("Juliane");
    await screen.findByRole("option", { name: /Grupo Projeto/ });
    await pressBack();
    expect(path()).toBe(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");

    // Unmounted, nobody is listening: the rotation is never confirmed, and the
    // next mount's sidebar is the first to say whose token it is.
    view.unmount();
    api.fetchSidebarData.mockResolvedValue(sidebarFor("other-2", [{ id: goneId, name: "Equipe" }]));
    act(() => setTokens("someone-else", "refresh"));
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    );
    await screen.findByRole("option", { name: /Grupo Equipe/ });
    await user.click(back());

    expect(path()).toBe(DM);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
  });

  it("sidebar carregando: não atravessa a conversa nem pula para a busca; volta quando carrega (CQ-03)", async () => {
    const sidebar = deferred<ReturnType<typeof sidebarFor>>();
    api.fetchSidebarData.mockReturnValueOnce(sidebar.promise);
    loadAt("/chat/search");
    // No identity yet: the search shows that it is confirming, not a field (CQ-10).
    await screen.findByText("Confirmando sua sessão…");
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    await push(CHANNEL);
    await push(DM);
    await screen.findByTestId("chat-msg-header");
    const entries = window.history.length;

    await user.click(back());

    // Neither to the channel it cannot vouch for, nor past it to the search.
    expect(path()).toBe(DM);
    expect(window.history.length).toBe(entries);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");
    await user.click(navToggle());

    sidebar.resolve(sidebarFor("me-1"));
    await expectHeader("Juliane");
    await pressBack();
    expect(path()).toBe(CHANNEL);
  });

  it("sidebar com erro: não trata como revogado; volta depois de recuperar (CQ-03)", async () => {
    api.fetchSidebarData.mockRejectedValueOnce(new Error("temporária"));
    loadAt("/chat/search");
    await screen.findByText("Não foi possível confirmar sua sessão.");
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    await push(CHANNEL);
    await push(DM);
    const retry = await screen.findByRole("button", { name: "Tentar novamente" });

    await user.click(back());
    expect(path()).toBe(DM);
    expect(navToggle()).toHaveAttribute("aria-expanded", "true");

    await user.click(retry);
    await expectHeader("Juliane");
    await pressBack();
    expect(path()).toBe(CHANNEL);
  });

  it("transição suspensa: Voltar conta do que está na tela, e a descartada nunca aparece", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await openFromSidebar(/Mensagem direta com Juliane/);
    await expectHeader("Juliane");

    // The entry exists already; its screen is still suspended.
    await push("/chat/favorites");
    expect(window.location.pathname).toBe("/chat/favorites");
    await expectHeader("Juliane");

    // Back from Juliane, on screen, is the channel — two entries behind the history.
    // fireEvent rather than user-event here: pressed through user-event's async
    // act wrapper, the suspended lane was never retried in jsdom (React warns
    // of a component "suspended inside an act scope"), while the very same
    // history.go(-2) dispatched directly or by fireEvent commits. (Not in
    // Playwright: at drawer widths the only way to a lazy route is the drawer,
    // which keeps the conversation inert until that route arrives.)
    const popped = nextPopstate();
    fireEvent.click(back());
    await popped();
    expect(path()).toBe(CHANNEL);
    await expectHeader("geral");

    favoritesGate.open();
    await settle();
    expect(screen.queryByText("Favoritos prontos")).not.toBeInTheDocument();
    await expectHeader("geral");
  });

  it("volta à busca com a consulta e a aba que ela registrou", async () => {
    loadAt(CHANNEL);
    await expectHeader("geral");
    await push("/chat/search");
    await screen.findByRole("searchbox");
    // What GlobalSearchPage does when a result is opened: REPLACE its own entry
    // with the search in its state, then PUSH the result.
    await act(async () => {
      documentHistory.replace(
        "/chat/search",
        withRestoredSearch(null, { query: "projeto", tab: "groups" }, "me-1:ws-1"),
      );
    });
    await push(GROUP);
    await expectHeader("Projeto");

    await pressBack();

    expect(path()).toBe("/chat/search");
    expect(await screen.findByRole("searchbox")).toHaveValue("projeto");
    expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
  });

  describe("identidade pendente (CQ-05)", () => {
    const OTHER = () => sidebarFor("other-2", [{ id: goneId, name: "Equipe" }]);

    /** U1: channel → search (with its query recorded on the entry) → group. */
    async function searchThenGroup() {
      loadAt(CHANNEL);
      await expectHeader("geral");
      await push("/chat/search");
      await screen.findByRole("searchbox");
      await act(async () => {
        documentHistory.replace(
          "/chat/search",
          withRestoredSearch(null, { query: "segredo-u1", tab: "groups" }, "me-1:ws-1"),
        );
      });
      await push(GROUP);
      await expectHeader("Projeto");
    }

    /** A rotation whose confirming sidebar answer the test holds. */
    function rotateAndHold() {
      const check = deferred<ReturnType<typeof sidebarFor>>();
      api.fetchSidebarData.mockReturnValueOnce(check.promise);
      act(() => setTokens("rotated", "refresh"));
      return check;
    }

    /** Back is pressed and opens the list over `at`, without travelling or adding entries. */
    async function expectBackStaysAt(at: string) {
      const entries = window.history.length;
      await user.click(back());
      expect(path()).toBe(at);
      expect(window.history.length).toBe(entries);
      expect(navToggle()).toHaveAttribute("aria-expanded", "true");
      await user.click(navToggle());
    }

    it("U1 → busca → conversa → refresh pendente → Voltar → confirma U2: a busca de U1 nunca volta", async () => {
      await searchThenGroup();
      api.fetchSidebarData.mockResolvedValue(OTHER());
      const check = rotateAndHold();

      // Pending: the search is U1's, and nobody knows yet whether U1 is still here.
      await expectBackStaysAt(GROUP);
      expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();

      check.resolve(OTHER());
      await screen.findByRole("option", { name: /Grupo Equipe/ });

      // Confirmed U2: U1's trail is gone — Back still opens the list.
      await expectBackStaysAt(GROUP);
      await settle();
      expect(path()).toBe(GROUP);
      expect(screen.queryByDisplayValue("segredo-u1")).not.toBeInTheDocument();
    });

    it("refresh pendente que confirma o mesmo usuário: espera, e depois volta à busca com consulta e aba", async () => {
      await searchThenGroup();
      const check = rotateAndHold();

      await expectBackStaysAt(GROUP);

      check.resolve(sidebarFor("me-1"));
      await settle();
      await pressBack();
      expect(path()).toBe("/chat/search");
      expect(await screen.findByRole("searchbox")).toHaveValue("segredo-u1");
      expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
    });

    it.each([
      ["outro usuário", "other-2"],
      ["mesmo usuário", "me-1"],
    ])("favoritos não escapam da confirmação: %s", async (_, confirmedUser) => {
      loadAt(CHANNEL);
      await expectHeader("geral");
      await push("/chat/favorites");
      await push(GROUP);
      await expectHeader("Projeto");
      const confirmed = confirmedUser === "me-1" ? sidebarFor("me-1") : OTHER();
      api.fetchSidebarData.mockResolvedValue(confirmed);
      const check = rotateAndHold();

      await expectBackStaysAt(GROUP);

      check.resolve(confirmed);
      await settle();
      if (confirmedUser === "me-1") {
        await pressBack();
        expect(path()).toBe("/chat/favorites");
      } else {
        await screen.findByRole("option", { name: /Grupo Equipe/ });
        await expectBackStaysAt(GROUP);
      }
    });

    it("conversas que os dois acessam: o histórico de U1 não é reaproveitado por U2", async () => {
      // Every conversation on U1's trail is in U2's sidebar too.
      loadAt(CHANNEL);
      await expectHeader("geral");
      await openFromSidebar(/Mensagem direta com Juliane/);
      await expectHeader("Juliane");
      api.fetchSidebarData.mockResolvedValue(sidebarFor("other-2"));
      const check = rotateAndHold();

      await expectBackStaysAt(DM);
      check.resolve(sidebarFor("other-2"));
      await settle();
      await waitFor(() => expect(api.fetchSidebarData).toHaveBeenCalledTimes(3));
      await expectHeader("Juliane");
      await expectBackStaysAt(DM);
    });

    describe.each([
      ["Voltar contextual", () => fireEvent.click(back())],
      ["Voltar nativo", () => window.history.back()],
    ])("CQ-07 — %s pedido e refresh no mesmo turno", (_, press) => {
      /** The press, and the rotation in the same turn: the POP lands while the check is held. */
      async function pressThenRotate() {
        const check = deferred<ReturnType<typeof sidebarFor>>();
        api.fetchSidebarData.mockReturnValueOnce(check.promise);
        const popped = nextPopstate();
        act(() => {
          press();
          setTokens("rotated", "refresh");
        });
        await popped();
        await settle();
        return check;
      }

      it("confirmando outro usuário: a busca de U1 nunca é mostrada", async () => {
        await searchThenGroup();
        api.fetchSidebarData.mockResolvedValue(OTHER());
        const check = await pressThenRotate();

        // Landed with nobody confirmed: no field, nothing restored (CQ-10).
        expect(path()).toBe("/chat/search");
        expect(screen.getByRole("status")).toHaveTextContent("Confirmando sua sessão…");
        expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();

        check.resolve(OTHER());
        await screen.findByRole("option", { name: /Grupo Equipe/ });
        expect(await screen.findByRole("searchbox")).toHaveValue("");
        expect(screen.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "true");
        expect(screen.queryByDisplayValue("segredo-u1")).not.toBeInTheDocument();
        expect(api.searchCategory).not.toHaveBeenCalledWith(
          expect.anything(),
          "segredo-u1",
          expect.anything(),
        );
      });

      it("confirmando o mesmo usuário: a busca volta com consulta e aba", async () => {
        await searchThenGroup();
        const check = await pressThenRotate();
        // Nothing to type into that the restore would then overwrite (CQ-11).
        expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();

        check.resolve(sidebarFor("me-1"));
        expect(await screen.findByRole("searchbox")).toHaveValue("segredo-u1");
        expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute(
          "aria-selected",
          "true",
        );
      });
    });

    it("CQ-07 — Avançar nativo para a busca de U1 depois de confirmado U2: nada volta", async () => {
      await searchThenGroup();
      await pressBack();
      expect(await screen.findByRole("searchbox")).toHaveValue("segredo-u1");
      const popped = nextPopstate();
      act(() => window.history.forward());
      await popped();
      await expectHeader("Projeto");

      api.fetchSidebarData.mockResolvedValue(OTHER());
      act(() => setTokens("someone-else", "refresh"));
      await screen.findByRole("option", { name: /Grupo Equipe/ });
      const back = nextPopstate();
      act(() => window.history.back());
      await back();
      expect(path()).toBe("/chat/search");
      expect(await screen.findByRole("searchbox")).toHaveValue("");
    });

    it("CQ-07 — busca aberta em StrictMode: troca de usuário apaga a consulta", async () => {
      window.history.replaceState(null, "", CHANNEL);
      _resetDocumentEntry();
      render(
        <StrictMode>
          <App />
        </StrictMode>,
      );
      await expectHeader("geral");
      await push("/chat/search");
      const field = await screen.findByRole("searchbox");
      await user.type(field, "segredo-u1");
      const check = deferred<ReturnType<typeof sidebarFor>>();
      api.fetchSidebarData.mockReturnValueOnce(check.promise);
      api.fetchSidebarData.mockResolvedValue(OTHER());
      act(() => setTokens("rotated", "refresh"));
      expect(screen.queryByDisplayValue("segredo-u1")).not.toBeInTheDocument();

      check.resolve(OTHER());
      await screen.findByRole("option", { name: /Grupo Equipe/ });
      expect(await screen.findByRole("searchbox")).toHaveValue("");
    });

    it("rotação antes de qualquer confirmação: a primeira sidebar não adota o histórico anterior", async () => {
      // The first load fails, so no identity ever confirmed the trail; the
      // rotation then has nobody to be checked against.
      api.fetchSidebarData.mockRejectedValueOnce(new Error("temporária"));
      loadAt(CHANNEL);
      const retry = await screen.findByRole("button", { name: "Tentar novamente" });
      await push("/chat/search");
      await act(async () => {
        documentHistory.replace(
          "/chat/search",
          withRestoredSearch(null, { query: "segredo-u1", tab: "groups" }, "me-1:ws-1"),
        );
      });
      await push(GROUP);
      act(() => setTokens("rotated", "refresh"));
      api.fetchSidebarData.mockResolvedValue(OTHER());

      await user.click(retry);
      await screen.findByRole("option", { name: /Grupo Equipe/ });
      await expectBackStaysAt(GROUP);
      expect(screen.queryByDisplayValue("segredo-u1")).not.toBeInTheDocument();
    });

    it.each([
      ["outro usuário", OTHER],
      ["mesmo usuário", () => sidebarFor("me-1")],
    ])(
      "CQ-13 — primeira sidebar pedida antes da rotação não confirma ninguém: %s",
      async (_, confirmed) => {
        // U1's first answer is held; a rotation installs the next token; then
        // the old answer lands. It speaks for the previous token only.
        const first = deferred<ReturnType<typeof sidebarFor>>();
        const current = deferred<ReturnType<typeof sidebarFor>>();
        api.fetchSidebarData
          .mockReturnValueOnce(first.promise)
          .mockReturnValueOnce(current.promise);
        loadAt("/chat/search", { strict: true });
        act(() => setTokens("rotated", "refresh"));
        first.resolve(sidebarFor("me-1"));
        await settle();

        // Nobody confirmed: no field to type into, nothing asked of the search.
        expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
        expect(api.searchCategory).not.toHaveBeenCalled();
        expect(api.fetchSidebarData).toHaveBeenCalledTimes(2);

        current.resolve(confirmed());
        await screen.findByRole("searchbox");
        // Whose the confirmed identity is: a search U1 recorded is restored for U1 only.
        await push(GROUP);
        await expectHeader("Projeto");
        await push(
          "/chat/search",
          withRestoredSearch(null, { query: "segredo-u1", tab: "groups" }, "me-1:ws-1"),
        );
        const field = await screen.findByRole("searchbox");
        if (confirmed().currentUserId === "me-1") {
          expect(field).toHaveValue("segredo-u1");
        } else {
          expect(screen.getByRole("option", { name: /Grupo Equipe/ })).toBeInTheDocument();
          expect(field).toHaveValue("");
          expect(api.searchCategory).not.toHaveBeenCalledWith(
            expect.anything(),
            "segredo-u1",
            expect.anything(),
          );
        }
      },
    );

    it.each([
      ["outro usuário", OTHER],
      ["mesmo usuário", () => sidebarFor("me-1")],
    ])(
      "CQ-13 (R6) — rotação entre a validação da resposta e sua aceitação: %s",
      async (_, confirmed) => {
        // U1's first answer lands and, one microtask later, before anything
        // that answer scheduled has run, a refresh installs the next token.
        const first = deferred<ReturnType<typeof sidebarFor>>();
        const current = deferred<ReturnType<typeof sidebarFor>>();
        api.fetchSidebarData
          .mockReturnValueOnce(first.promise)
          .mockReturnValueOnce(current.promise)
          .mockResolvedValue(confirmed());
        loadAt("/chat/search", { strict: true });
        await act(async () => {
          first.resolve(sidebarFor("me-1"));
          queueMicrotask(() => setTokens("token-u2", "refresh"));
        });
        await settle();

        // The token installed now is asked whose it is; until it answers, nobody
        // is confirmed and the search has nothing to show.
        expect(api.fetchSidebarData).toHaveBeenCalledTimes(2);
        expect(screen.getByRole("status")).toHaveTextContent("Confirmando sua sessão…");
        expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
        expect(api.searchCategory).not.toHaveBeenCalled();

        current.resolve(confirmed());
        await screen.findByRole("searchbox");
        await push(GROUP);
        await expectHeader("Projeto");
        await push(
          "/chat/search",
          withRestoredSearch(null, { query: "segredo-u1", tab: "groups" }, "me-1:ws-1"),
        );
        const field = await screen.findByRole("searchbox");
        expect(sessionStorage.getItem("nchat_at")).toBe("token-u2");
        if (confirmed().currentUserId === "me-1") {
          expect(field).toHaveValue("segredo-u1");
        } else {
          expect(screen.getByRole("option", { name: /Grupo Equipe/ })).toBeInTheDocument();
          expect(field).toHaveValue("");
          expect(screen.queryByDisplayValue("segredo-u1")).not.toBeInTheDocument();
          expect(api.searchCategory).not.toHaveBeenCalledWith(
            expect.anything(),
            "segredo-u1",
            expect.anything(),
          );
        }
      },
    );
  });

  it("título longo: o Voltar segue primeiro no cabeçalho, nomeado, e o título inteiro é lido", async () => {
    const long = "Projeto de integração contínua com um nome muito, muito comprido mesmo";
    api.fetchSidebarData.mockResolvedValue({
      ...sidebarFor("me-1"),
      dms: [{ id: groupId, type: "group", name: long, participants: [] }],
    });
    loadAt(GROUP);
    await expectHeader(long);
    const header = screen.getByTestId("chat-msg-header");
    const controls = within(header).getAllByRole("button");

    expect(controls[0]).toBe(back());
    expect(back()).toHaveAccessibleName("Voltar");
    expect(within(header).getByText(long)).toBeInTheDocument();
  });
});
