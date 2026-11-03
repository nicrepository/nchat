import { createRef } from "react";
import { restoreOpenerFocus } from "../chat/restoreOpenerFocus";
import userEvent from "@testing-library/user-event";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryRouter, RouterProvider, MemoryRouter } from "react-router";
import type { Channel, DMConversation } from "../chat/chatTypes";
import { setTokens, clearTokens } from "../lib/authSession";
import { useMeetingScheduling } from "./meetingContext";
import MeetingSchedulingProvider from "./MeetingSchedulingProvider";
import MeetingSchedulingDialog from "./MeetingSchedulingDialog";
import { createMeetingSession } from "./meetingSession";
import type { MeetingDraft, MeetingResult } from "./meetingTypes";

const network = vi.fn();
const peer = { user_id: "peer", display_name: "Ana" };
const second = { user_id: "second", display_name: "Bia" };
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify({ data }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const failure = (status: number) =>
  new Response(JSON.stringify({ error: { code: "refused", message: "private" } }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const created: MeetingResult = {
  id: "meeting-1",
  status: "scheduled",
  version: 1,
  starts_at: "2026-10-30T10:00:00Z",
  ends_at: "2026-10-30T10:30:00Z",
  timezone: "UTC",
};
beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
});
beforeEach(() => {
  setTokens("test-token");
  vi.stubGlobal("fetch", network);
  network.mockReset().mockImplementation((url: string) => {
    if (url.includes("meeting-candidates")) return Promise.resolve(json({ items: [peer, second] }));
    if (url.endsWith("invite-all")) return Promise.resolve(json({ added: 2 }));
    return Promise.resolve(json(created, 201));
  });
});
afterEach(() => vi.unstubAllGlobals());
const schedule = () => screen.getByRole("button", { name: "Agendar reunião" });
const title = () => screen.getByRole("textbox", { name: "Título (obrigatório)" });
function Opener() {
  const open = useMeetingScheduling();
  return (
    <>
      <button onClick={(e) => open?.("dm", "dm", e.currentTarget)}>Abrir DM</button>
      <button onClick={(e) => open?.("dm", "group", e.currentTarget)}>Abrir grupo</button>
      <button onClick={(e) => open?.("channel", "ch", e.currentTarget)}>Abrir canal</button>
      <button onClick={() => open?.("channel", "missing")}>Desconhecido</button>
    </>
  );
}
function flow(
  userId = "me",
  workspaceId = "workspace",
  restoreFocus?: (opener: HTMLElement | null) => void,
) {
  const channels = [{ id: "ch", name: "Infra" }] as Channel[];
  const dms = [
    { id: "dm", type: "1:1", name: "Ana" },
    { id: "group", type: "group", name: "Equipe" },
  ] as DMConversation[];
  const router = createMemoryRouter(
    [
      {
        path: "*",
        element: (
          <MeetingSchedulingProvider
            userId={userId}
            restoreFocus={restoreFocus}
            workspaceId={workspaceId}
            channels={channels}
            dms={dms}
          >
            <Opener />
          </MeetingSchedulingProvider>
        ),
      },
    ],
    { initialEntries: ["/chat/dm/dm"] },
  );
  render(<RouterProvider router={router} />);
  return router;
}
async function open(name = "Abrir canal") {
  fireEvent.click(screen.getByRole("button", { name }));
  if (
    !(screen.getByLabelText("Buscar participantes pelo início do nome") as HTMLInputElement)
      .disabled
  )
    await screen.findByRole("checkbox", { name: "Ana" });
}
const posts = () => network.mock.calls.filter(([, init]) => init.method === "POST");

describe("meeting scheduling flow with authenticated HTTP", () => {
  it("prefills an eligible DM peer, excludes organizer, sends JSON/header and completes", async () => {
    flow();
    await open("Abrir DM");
    expect(title()).toHaveValue("Reunião com Ana");
    expect(screen.getByRole("checkbox", { name: "Ana" })).toBeChecked();
    expect(screen.queryByText("Convidar todos os membros elegíveis")).not.toBeInTheDocument();
    fireEvent.click(schedule());
    await screen.findByText(/Reunião criada com sucesso/);
    const [[url, init]] = posts();
    expect(url).toBe("/api/chat/meetings");
    expect(JSON.parse(init.body)).toMatchObject({
      conversation: { kind: "dm", id: "dm" },
      participants: [{ user_id: "peer", role: "attendee" }],
    });
    expect(new Headers(init.headers).get("Idempotency-Key")).toMatch(/^[\w.:-]{1,128}$/);
    fireEvent.click(screen.getByRole("button", { name: "Concluir" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
  it("supports group partial selection and never preselects channel members", async () => {
    flow();
    await open("Abrir grupo");
    expect(title()).toHaveValue("Equipe");
    expect(screen.getByRole("checkbox", { name: "Ana" })).not.toBeChecked();
    fireEvent.click(screen.getByRole("checkbox", { name: "Bia" }));
    fireEvent.click(schedule());
    await screen.findByText(/Reunião criada com sucesso/);
    expect(JSON.parse(posts()[0][1].body)).toMatchObject({
      conversation: { kind: "dm", id: "group" },
      participants: [{ user_id: "second", role: "attendee" }],
    });
  });
  it("preserves dirty draft, timezone and selections on close/reopen; requires explicit discard", async () => {
    flow();
    await open();
    fireEvent.change(title(), { target: { value: "Planejar" } });
    fireEvent.change(screen.getByLabelText("Timezone IANA (obrigatória)"), {
      target: { value: "Europe/Lisbon" },
    });
    fireEvent.click(screen.getByRole("checkbox", { name: "Ana" }));
    fireEvent.click(screen.getByRole("button", { name: "Continuar depois" }));
    await open();
    expect(title()).toHaveValue("Planejar");
    expect(screen.getByLabelText("Timezone IANA (obrigatória)")).toHaveValue("Europe/Lisbon");
    expect(screen.getByRole("checkbox", { name: "Ana" })).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: "Descartar rascunho" }));
    fireEvent.click(screen.getByRole("button", { name: "Continuar editando" }));
    expect(title()).toHaveValue("Planejar");
    fireEvent.click(screen.getByRole("button", { name: "Descartar rascunho" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirmar descarte" }));
    await open();
    expect(title()).toHaveValue("Infra");
    expect(screen.getByRole("checkbox", { name: "Ana" })).not.toBeChecked();
  });
  it("focuses first invalid field, binds its error and preserves the draft", async () => {
    flow();
    await open();
    fireEvent.change(title(), { target: { value: "  " } });
    fireEvent.click(schedule());
    expect(title()).toHaveFocus();
    expect(title()).toHaveAttribute("aria-invalid", "true");
    expect(title()).toHaveAccessibleDescription(/1 a 120/);
    expect(posts()).toHaveLength(0);
    fireEvent.change(title(), { target: { value: "Válido" } });
    expect(title()).toHaveAttribute("aria-invalid", "false");
    fireEvent.change(screen.getByLabelText("Descrição (opcional)"), {
      target: { value: "x".repeat(5001) },
    });
    fireEvent.blur(screen.getByLabelText("Descrição (opcional)"));
    expect(screen.getByText(/Use até 5000/)).toBeInTheDocument();
  });
  it("focuses invalid custom minutes and updates only their visual and accessible error", async () => {
    const user = userEvent.setup();
    flow();
    await open();
    await user.selectOptions(screen.getByLabelText("Duração", { exact: true }), "custom");
    const minutes = screen.getByLabelText("Duração personalizada em minutos");
    await user.type(minutes, "0");
    await user.click(schedule());
    expect(minutes).toHaveFocus();
    expect(minutes).toHaveAttribute("aria-invalid", "true");
    expect(minutes).toHaveAccessibleDescription(/duração positiva/);
    expect(screen.getByLabelText("Duração", { exact: true })).toHaveAttribute(
      "aria-invalid",
      "false",
    );
    await user.clear(minutes);
    await user.type(minutes, "45");
    expect(minutes).toHaveAttribute("aria-invalid", "false");
    expect(screen.queryByText(/Informe uma duração positiva/)).not.toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });
  it.each([
    ["Timezone IANA (obrigatória)", "America/Sao_Paulo"],
    ["Data (obrigatória)", "2026-03-09"],
    ["Hora (obrigatória)", "03:30"],
  ])(
    "revalidates a DST gap after changing %s without clearing independent errors",
    async (label, value) => {
      flow();
      await open();
      fireEvent.change(title(), { target: { value: "" } });
      fireEvent.blur(title());
      fireEvent.change(screen.getByLabelText("Timezone IANA (obrigatória)"), {
        target: { value: "America/New_York" },
      });
      fireEvent.change(screen.getByLabelText("Data (obrigatória)"), {
        target: { value: "2026-03-08" },
      });
      const time = screen.getByLabelText("Hora (obrigatória)");
      fireEvent.change(time, { target: { value: "02:30" } });
      fireEvent.blur(time);
      expect(time).toHaveAccessibleDescription(/não existe/);
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
      expect(time).toHaveAttribute("aria-invalid", "false");
      expect(screen.queryByText(/Este horário não existe/)).not.toBeInTheDocument();
      expect(title()).toHaveAttribute("aria-invalid", "true");
      expect(title()).toHaveAccessibleDescription(/1 a 120/);
    },
  );
  it("preserves an independent invalid date when correcting custom duration", async () => {
    flow();
    await open();
    const date = screen.getByLabelText("Data (obrigatória)");
    fireEvent.change(date, { target: { value: "" } });
    fireEvent.blur(date);
    fireEvent.change(screen.getByLabelText("Duração", { exact: true }), {
      target: { value: "custom" },
    });
    const minutes = screen.getByLabelText("Duração personalizada em minutos");
    fireEvent.change(minutes, { target: { value: "0" } });
    fireEvent.blur(minutes);
    fireEvent.change(minutes, { target: { value: "30" } });
    expect(minutes).toHaveAttribute("aria-invalid", "false");
    expect(date).toHaveAttribute("aria-invalid", "true");
  });
  it("treats create and bulk separately and retries only the persisted meeting", async () => {
    let bulk = 0;
    network.mockImplementation((url: string) =>
      Promise.resolve(
        url.includes("meeting-candidates")
          ? json({ items: [peer] })
          : url.endsWith("invite-all")
            ? ++bulk === 1
              ? failure(503)
              : json({ added: 1 })
            : json(created, 201),
      ),
    );
    flow();
    await open();
    expect(screen.getByRole("checkbox", { name: "Ana" })).not.toBeChecked();
    fireEvent.click(screen.getByRole("checkbox", { name: "Convidar todos os membros elegíveis" }));
    fireEvent.click(schedule());
    await screen.findByText("A reunião foi criada. O convite coletivo ainda não foi confirmado.");
    expect(title()).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Continuar depois" }));
    await open();
    expect(screen.queryByText("Buscando participantes…")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Tentar convite coletivo novamente" }));
    await screen.findByText(/Convite coletivo concluído/);
    expect(posts().map(([url]) => url)).toEqual([
      "/api/chat/meetings",
      "/api/chat/meetings/meeting-1/participants/invite-all",
      "/api/chat/meetings/meeting-1/participants/invite-all",
    ]);
  });
  it.each([400, 429])(
    "retains the mounted candidate page without refetch after create HTTP %s",
    async (status) => {
      let settle!: (response: Response) => void;
      network.mockImplementation((url: string) =>
        url.includes("meeting-candidates")
          ? Promise.resolve(json({ items: [peer], next_cursor: "peer" }))
          : new Promise((resolve) => {
              settle = resolve;
            }),
      );
      const user = userEvent.setup();
      flow();
      await open();
      const candidates = screen.getByRole("list", { name: "Participantes elegíveis" });
      const checkbox = within(candidates).getByRole("checkbox", { name: "Ana" });
      await user.click(checkbox);
      await user.click(schedule());
      expect(screen.getByRole("list", { name: "Participantes elegíveis" })).toBe(candidates);
      expect(checkbox).toBeDisabled();
      await act(async () => settle(failure(status)));
      await screen.findByRole("alert");
      expect(screen.getByRole("list", { name: "Participantes elegíveis" })).toBe(candidates);
      expect(checkbox).toBeEnabled();
      expect(checkbox).toBeChecked();
      expect(network.mock.calls.filter(([url]) => url.includes("meeting-candidates"))).toHaveLength(
        1,
      );
    },
  );
  it("retries failed candidate loading through the visible component action", async () => {
    let attempts = 0;
    network.mockImplementation((url: string) =>
      Promise.resolve(
        url.includes("meeting-candidates")
          ? ++attempts === 1
            ? failure(503)
            : json({ items: [peer] })
          : json(created, 201),
      ),
    );
    const user = userEvent.setup();
    flow();
    await user.click(screen.getByRole("button", { name: "Abrir canal" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /Não foi possível buscar participantes/,
    );
    await user.click(screen.getByRole("button", { name: "Tentar busca novamente" }));
    expect(await screen.findByRole("checkbox", { name: "Ana" })).toBeEnabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    const queries = network.mock.calls.filter(([url]) => url.includes("meeting-candidates"));
    expect(queries).toHaveLength(2);
    expect(queries[0][0]).toBe(queries[1][0]);
  });
  it("pages candidates through the visible action and preserves a unique partial selection", async () => {
    network.mockImplementation((url: string) => {
      if (!url.includes("meeting-candidates")) return Promise.resolve(json(created, 201));
      const cursor = new URL(url, "http://nchat.test").searchParams.get("cursor");
      return Promise.resolve(
        json(cursor ? { items: [second] } : { items: [peer], next_cursor: "peer" }),
      );
    });
    const user = userEvent.setup();
    flow();
    await open();
    await user.click(screen.getByRole("checkbox", { name: "Ana" }));
    await user.click(screen.getByRole("button", { name: "Próxima página de participantes" }));
    await user.click(await screen.findByRole("checkbox", { name: "Bia" }));
    const selected = within(screen.getByRole("list", { name: "Participantes selecionados" }));
    expect(selected.getAllByRole("listitem")).toHaveLength(2);
    expect(selected.getByText("Ana")).toBeInTheDocument();
    expect(selected.getByText("Bia")).toBeInTheDocument();
    const queries = network.mock.calls.filter(([url]) => url.includes("meeting-candidates"));
    expect(queries).toHaveLength(2);
    expect(new URL(queries[1][0], "http://nchat.test").searchParams.get("cursor")).toBe("peer");
    expect(
      screen.queryByRole("button", { name: "Próxima página de participantes" }),
    ).not.toBeInTheDocument();
    await user.click(schedule());
    await screen.findByText(/Reunião criada com sucesso/);
    expect(JSON.parse(posts()[0][1].body).participants).toEqual([
      { user_id: "peer", role: "attendee" },
      { user_id: "second", role: "attendee" },
    ]);
  });
  it("preserves a 429 draft and retries with the same idempotency key", async () => {
    let attempts = 0;
    network.mockImplementation((url: string) =>
      Promise.resolve(
        url.includes("meeting-candidates")
          ? json({ items: [peer] })
          : ++attempts === 1
            ? failure(429)
            : json(created, 200),
      ),
    );
    flow();
    await open();
    fireEvent.change(title(), { target: { value: "Limitado" } });
    fireEvent.click(schedule());
    await screen.findByText(/Aguarde um minuto/);
    expect(title()).toHaveValue("Limitado");
    fireEvent.click(schedule());
    await screen.findByText(/já registrada/);
    const calls = posts();
    expect(new Headers(calls[0][1].headers).get("Idempotency-Key")).toBe(
      new Headers(calls[1][1].headers).get("Idempotency-Key"),
    );
  });
  it("prevents concurrent submits and closing during creation", async () => {
    let resolve!: (response: Response) => void;
    network.mockImplementation((url: string) =>
      url.includes("meeting-candidates")
        ? Promise.resolve(json({ items: [peer] }))
        : new Promise((done) => {
            resolve = done;
          }),
    );
    flow();
    await open();
    const form = schedule().closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    expect(posts()).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Fechar agendamento" })).toBeDisabled();
    fireEvent(screen.getByRole("dialog"), new Event("cancel", { cancelable: true }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await act(async () => resolve(json(created, 201)));
    await screen.findByText(/Reunião criada com sucesso/);
  });
  it("freezes an uncertain intent and recovers it without resetting payload", async () => {
    let attempts = 0;
    network.mockImplementation((url: string) =>
      url.includes("meeting-candidates")
        ? Promise.resolve(json({ items: [peer] }))
        : ++attempts === 1
          ? Promise.reject(new TypeError("network"))
          : Promise.resolve(json(created, 200)),
    );
    flow();
    await open();
    fireEvent.click(schedule());
    await screen.findByText(/Resposta não confirmada/);
    expect(title()).toBeDisabled();
    fireEvent.click(schedule());
    await screen.findByText(/já registrada/);
    expect(posts()[0][1].body).toBe(posts()[1][1].body);
  });
  it("closes on navigation, isolates conversations, and restores only the requested draft", async () => {
    const router = flow();
    await open("Abrir DM");
    fireEvent.change(title(), { target: { value: "DM draft" } });
    await act(async () => {
      await router.navigate("/chat/channel/ch");
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await open();
    expect(title()).toHaveValue("Infra");
    expect(screen.getByRole("checkbox", { name: "Ana" })).not.toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: "Fechar agendamento" }));
    await act(async () => {
      await router.navigate("/chat/dm/dm");
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await open("Abrir DM");
    expect(title()).toHaveValue("DM draft");
  });
  it("renders untrusted meeting text and candidate names as data", async () => {
    const text = "<img src=x onerror=alert(1)>";
    network.mockResolvedValue(json({ items: [{ user_id: "peer", display_name: text }] }));
    flow();
    fireEvent.click(screen.getByRole("button", { name: "Abrir canal" }));
    await screen.findByRole("checkbox", { name: text });
    fireEvent.change(title(), { target: { value: text } });
    expect(document.querySelector("dialog img")).toBeNull();
    expect(title()).toHaveValue(text);
  });
  it("uses accessible empty state and removes selected peers", async () => {
    flow();
    await open("Abrir DM");
    fireEvent.click(screen.getByRole("button", { name: "Remover Ana" }));
    expect(screen.getByRole("checkbox", { name: "Ana" })).not.toBeChecked();
    network.mockResolvedValue(json({ items: [] }));
    fireEvent.change(screen.getByLabelText("Buscar participantes pelo início do nome"), {
      target: { value: "Ninguém" },
    });
    await screen.findByText(/Nenhum participante elegível/);
    fireEvent.click(screen.getByRole("button", { name: "Limpar busca" }));
    expect(screen.getByLabelText("Buscar participantes pelo início do nome")).toHaveValue("");
  });
  it("preserves draft and dialog on access-token rotation, then clears on session replacement", async () => {
    flow();
    await open();
    fireEvent.change(title(), { target: { value: "Rotating token" } });
    act(() => setTokens("rotated", "refresh"));
    expect(title()).toHaveValue("Rotating token");
    act(() => setTokens("new-session"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await open();
    expect(title()).toHaveValue("Infra");
  });
  it("uses the shell identity check to hide drafts during refresh and isolates workspace/user changes", async () => {
    const pending = vi.fn(() => false);
    const tree = (confirmingIdentity = false, userId = "me", workspaceId = "workspace") => (
      <MemoryRouter>
        <MeetingSchedulingProvider
          userId={userId}
          workspaceId={workspaceId}
          identityPending={pending}
          confirmingIdentity={confirmingIdentity}
          channels={[{ id: "ch", name: "Infra", type: "public", canWrite: true }]}
          dms={[]}
        >
          <Opener />
        </MeetingSchedulingProvider>
      </MemoryRouter>
    );
    const view = render(tree());
    await open();
    fireEvent.change(title(), { target: { value: "Scoped draft" } });
    fireEvent.click(screen.getByRole("checkbox", { name: "Ana" }));
    pending.mockReturnValue(true);
    view.rerender(tree(true));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Abrir canal" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    act(() => setTokens("rotated", "refresh"));
    pending.mockReturnValue(false);
    view.rerender(tree());
    expect(title()).toHaveValue("Scoped draft");
    view.rerender(tree(false, "other-user", "other-workspace"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await open();
    expect(title()).toHaveValue("Infra");
    expect(screen.getByRole("checkbox", { name: "Ana" })).not.toBeChecked();
  });
  it.each(["valid", "removed", "hidden", "closed drawer"])(
    "restores focus with a %s opener without scrolling",
    async (mode) => {
      const fallback = createRef<HTMLButtonElement>();
      const restore = vi.fn((opener: HTMLElement | null) => {
        restoreOpenerFocus(opener, fallback.current);
      });
      const tree = (updated = false) => (
        <MemoryRouter>
          <MeetingSchedulingProvider
            userId="me"
            workspaceId="workspace"
            channels={[{ id: "ch", name: "Infra", type: "public", canWrite: true }]}
            dms={[]}
            restoreFocus={restore}
          >
            <div
              hidden={updated && mode === "hidden"}
              style={updated && mode === "closed drawer" ? { display: "none" } : undefined}
            >
              {!(updated && mode === "removed") && <Opener />}
            </div>
            <button ref={fallback}>Navegação global</button>
          </MeetingSchedulingProvider>
        </MemoryRouter>
      );
      const view = render(tree());
      await open();
      const opener = screen.getByRole("button", { name: "Abrir canal" });
      const focused = mode === "valid" ? opener : fallback.current!;
      const spy = vi.spyOn(focused, "focus");
      view.rerender(tree(true));
      fireEvent.click(screen.getByRole("button", { name: "Fechar agendamento" }));
      expect(focused).toHaveFocus();
      expect(spy).toHaveBeenCalledWith({ preventScroll: true });
      expect(restore).toHaveBeenCalledWith(opener);
    },
  );
  it("does not restore a previous conversation opener after navigation", async () => {
    const restore = vi.fn();
    const router = flow("me", "workspace", restore);
    await open();
    await act(async () => {
      await router.navigate("/chat/channel/another");
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(restore).not.toHaveBeenCalled();
  });
  it("closes a removed canonical conversation and does not reopen it when the row returns", async () => {
    const restoreFocus = vi.fn();
    const channels = [{ id: "ch", name: "Infra", type: "public", canWrite: true }] as Channel[];
    const tree = (items: Channel[]) => (
      <MemoryRouter>
        <MeetingSchedulingProvider
          userId="me"
          workspaceId="workspace"
          restoreFocus={restoreFocus}
          channels={items}
          dms={[]}
        >
          <Opener />
        </MeetingSchedulingProvider>
      </MemoryRouter>
    );
    const view = render(tree(channels));
    await open();
    fireEvent.change(title(), { target: { value: "Saved draft" } });
    view.rerender(tree([]));
    expect(restoreFocus).toHaveBeenCalledWith(null);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    view.rerender(tree(channels));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await open();
    expect(title()).toHaveValue("Saved draft");
  });
  it("does not expose drafts after auth ends or open unknown contexts", async () => {
    flow();
    fireEvent.click(screen.getByRole("button", { name: "Desconhecido" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await open();
    act(() => clearTokens());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
  it.each([
    ["", "w"],
    ["me", ""],
  ])("ignores scheduling without resolved session %s/%s", (user, workspace) => {
    flow(user, workspace);
    fireEvent.click(screen.getByRole("button", { name: "Abrir canal" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
  it("explains DST overlap and supports custom duration and join policies", async () => {
    flow();
    await open();
    fireEvent.change(screen.getByLabelText("Data (obrigatória)"), {
      target: { value: "2026-11-01" },
    });
    fireEvent.change(screen.getByLabelText("Hora (obrigatória)"), { target: { value: "01:30" } });
    fireEvent.change(screen.getByLabelText("Timezone IANA (obrigatória)"), {
      target: { value: "America/New_York" },
    });
    fireEvent.click(schedule());
    expect(screen.getByLabelText("Ocorrência do horário (obrigatória)")).toHaveFocus();
    fireEvent.change(screen.getByLabelText("Ocorrência do horário (obrigatória)"), {
      target: { value: "later" },
    });
    fireEvent.change(screen.getByLabelText("Duração", { exact: true }), {
      target: { value: "custom" },
    });
    fireEvent.change(screen.getByLabelText("Duração personalizada em minutos"), {
      target: { value: "90" },
    });
    fireEvent.change(screen.getByLabelText("Quem pode entrar"), {
      target: { value: "conversation_members" },
    });
    fireEvent.click(schedule());
    await screen.findByText(/Reunião criada com sucesso/);
    expect(JSON.parse(posts()[0][1].body)).toMatchObject({
      starts_at: "2026-11-01T06:30:00Z",
      ends_at: "2026-11-01T08:00:00Z",
      join_policy: "conversation_members",
    });
  });
  it("Escape cancels discard confirmation before closing the preserved draft", async () => {
    flow();
    await open();
    fireEvent.change(title(), { target: { value: "dirty" } });
    fireEvent.click(screen.getByRole("button", { name: "Descartar rascunho" }));
    fireEvent(screen.getByRole("dialog"), new Event("cancel", { cancelable: true }));
    expect(screen.queryByText("Descartar este rascunho de agendamento?")).not.toBeInTheDocument();
    fireEvent(screen.getByRole("dialog"), new Event("cancel", { cancelable: true }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await open();
    expect(title()).toHaveValue("dirty");
  });
  it("can finish partial bulk without any second create", async () => {
    const session = createMeetingSession(
      { kind: "channel", id: "ch", name: "Infra", direct: false },
      "me",
    );
    session.update({
      operation: {
        stage: "invite-error",
        meeting: created,
        replayed: false,
        message: "Limite 500",
      },
    });
    const close = vi.fn();
    render(<MeetingSchedulingDialog session={session} organizerId="me" onClose={close} />);
    fireEvent.click(screen.getByRole("button", { name: "Finalizar com a seleção original" }));
    await screen.findByText(/Reunião criada com sucesso/);
    fireEvent.click(screen.getByRole("button", { name: "Fechar agendamento" }));
    expect(close).toHaveBeenCalledWith(true);
    expect(posts()).toHaveLength(0);
  });
  it("rejects payloads over the API body limit with a focused selection error", async () => {
    const session = createMeetingSession(
      { kind: "channel", id: "ch", name: "Infra", direct: false },
      "me",
    );
    const participants = Array.from({ length: 1000 }, (_, i) => ({
      user_id: `00000000-0000-4000-8000-${String(i).padStart(12, "0")}`,
      display_name: `Pessoa ${i}`,
    }));
    const draft: MeetingDraft = { ...session.getSnapshot().draft, participants };
    session.update({ draft });
    render(<MeetingSchedulingDialog session={session} organizerId="me" onClose={vi.fn()} />);
    await waitFor(() =>
      expect(screen.getByLabelText("Buscar participantes pelo início do nome")).toBeEnabled(),
    );
    fireEvent.click(schedule());
    expect(screen.getByRole("alert")).toHaveTextContent(/excede o tamanho/);
    expect(screen.getByRole("region", { name: "Participantes" })).toHaveFocus();
    expect(posts()).toHaveLength(0);
    expect(
      within(screen.getByRole("list", { name: "Participantes selecionados" })).getAllByRole(
        "listitem",
      ),
    ).toHaveLength(50);
  });
});
