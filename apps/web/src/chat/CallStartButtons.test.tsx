import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";

import { HeaderChannel, HeaderDM, type ResourceCallHeaderState } from "./ChatMessageArea";

it("starts audio or video only for the server-resolved DM counterpart", () => {
  const start = vi.fn(() => true);
  render(
    <HeaderDM
      name="Ana"
      counterpart={{
        userId: "00000000-0000-4000-8000-000000000401",
        displayName: "Ana",
      }}
      onStartCall={start}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de áudio" }));
  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de vídeo" }));
  expect(start).toHaveBeenNthCalledWith(1, "00000000-0000-4000-8000-000000000401", "audio");
  expect(start).toHaveBeenNthCalledWith(2, "00000000-0000-4000-8000-000000000401", "video");
});

it("does not offer call buttons for a 1:1 DM when onStartCall is unavailable (RF-24 direct call already in progress)", () => {
  render(
    <HeaderDM
      name="Ana"
      counterpart={{ userId: "00000000-0000-4000-8000-000000000401", displayName: "Ana" }}
    />,
  );
  expect(screen.queryByRole("button", { name: /Iniciar chamada/ })).not.toBeInTheDocument();
});

// Issue #622 round 2: no active call — a plain "Chamada" starts a fresh one.
it('RF-24 "none" state: joins the group\'s resource room via a single Chamada action, never the RF-23 1:1 call actions', () => {
  const onCall = vi.fn();
  render(<HeaderDM name="Equipe" resourceCall={{ kind: "none", onCall }} />);

  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada" }));

  expect(onCall).toHaveBeenCalledOnce();
  expect(
    screen.queryByRole("button", { name: "Iniciar chamada de áudio" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Iniciar chamada de vídeo" }),
  ).not.toBeInTheDocument();
});

it("a group with a resolved counterpart never happens, but resourceCall is ignored when counterpart is set", () => {
  const start = vi.fn(() => true);
  const onCall = vi.fn();
  render(
    <HeaderDM
      name="Ana"
      counterpart={{ userId: "00000000-0000-4000-8000-000000000401", displayName: "Ana" }}
      onStartCall={start}
      resourceCall={{ kind: "none", onCall }}
    />,
  );

  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de áudio" }));

  expect(start).toHaveBeenCalledOnce();
  expect(onCall).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "Chamada" })).not.toBeInTheDocument();
});

it('RF-24 "none" state: joins the channel\'s resource room via a single Chamada action', () => {
  const onCall = vi.fn();
  render(<HeaderChannel name="geral" resourceCall={{ kind: "none", onCall }} />);

  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada" }));

  expect(onCall).toHaveBeenCalledOnce();
  expect(
    screen.queryByRole("button", { name: "Iniciar chamada de áudio" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Iniciar chamada de vídeo" }),
  ).not.toBeInTheDocument();
});

it("hides the channel call buttons entirely when resourceCall is absent (e.g. not yet resolvable)", () => {
  render(<HeaderChannel name="geral" />);
  expect(
    screen.queryByRole("button", { name: /Iniciar chamada|Entrar na chamada/ }),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("Chamada ativa")).not.toBeInTheDocument();
});

// Issue #622 round 2 section 14: the header states, no participant count.
// "active" covers both spec states 2 and 3 (joinable vs busy elsewhere),
// discriminated only by `disabled` — same indicator either way.
const joinableState: ResourceCallHeaderState = { kind: "active", disabled: false, onJoin: vi.fn() };
const busyState: ResourceCallHeaderState = { kind: "active", disabled: true, onJoin: vi.fn() };
const participatingState: ResourceCallHeaderState = { kind: "participating" };

it('"active"+enabled state shows "Chamada ativa" and an enabled "Entrar na chamada" that calls onJoin', () => {
  const onJoin = vi.fn();
  render(<HeaderChannel name="geral" resourceCall={{ kind: "active", disabled: false, onJoin }} />);

  expect(screen.getByText("Chamada ativa")).toBeInTheDocument();
  const joinButton = screen.getByRole("button", { name: "Entrar na chamada" });
  expect(joinButton).toBeEnabled();
  fireEvent.click(joinButton);
  expect(onJoin).toHaveBeenCalledOnce();
  expect(screen.queryByRole("button", { name: "Chamada" })).not.toBeInTheDocument();
});

it('"active"+disabled state shows "Chamada ativa" with a disabled "Entrar na chamada" — discovery is never hidden while busy', () => {
  render(<HeaderChannel name="geral" resourceCall={busyState} />);

  expect(screen.getByText("Chamada ativa")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Entrar na chamada" })).toBeDisabled();
});

it("none+disabled state shows a disabled Chamada — no call exists yet, but a direct call is busy (RF-23×RF-24 arbitration)", () => {
  render(
    <HeaderChannel name="geral" resourceCall={{ kind: "none", disabled: true, onCall: vi.fn() }} />,
  );
  expect(screen.getByRole("button", { name: "Iniciar chamada" })).toBeDisabled();
  expect(screen.queryByText("Chamada ativa")).not.toBeInTheDocument();
});

it('"participating" state renders no extra call UI at all — no count, no button, no indicator', () => {
  render(<HeaderChannel name="geral" resourceCall={participatingState} />);

  expect(screen.queryByText("Chamada ativa")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: /Chamada|Entrar na chamada/ }),
  ).not.toBeInTheDocument();
});

it("group DM active+enabled state shows an enabled Entrar na chamada, never RF-23's Áudio/Vídeo", () => {
  render(<HeaderDM name="Equipe" resourceCall={joinableState} />);
  expect(screen.getByText("Chamada ativa")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Entrar na chamada" })).toBeEnabled();
  expect(
    screen.queryByRole("button", { name: "Iniciar chamada de áudio" }),
  ).not.toBeInTheDocument();
});

it("group DM active+disabled state shows a disabled Entrar na chamada", () => {
  render(<HeaderDM name="Equipe" resourceCall={busyState} />);
  expect(screen.getByText("Chamada ativa")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Entrar na chamada" })).toBeDisabled();
});
