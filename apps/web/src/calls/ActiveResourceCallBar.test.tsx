import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ActiveResourceCallBar from "./ActiveResourceCallBar";

import type {
  BarAvailable,
  BarParticipatingInfo,
  BarParticipatingLocal,
} from "./ActiveResourceCallBar";

const baseShared = {
  title: "Chamada de voz — #infraestrutura",
  startedAt: "2024-01-01T12:00:00.000Z",
};

const baseLocalProps: Omit<BarParticipatingLocal, "mode" | "title" | "startedAt"> = {
  participants: [
    { identity: "user-jl", displayName: "Juliane Lino" },
    { identity: "user-ca", displayName: "Caio Almeida" },
  ],
  localId: "user-an",
  localName: "Álvaro Neto (você)",
  localInitials: "AN",
  activeSpeakerId: undefined,
  microphoneEnabled: true,
  microphonePending: false,
  onToggleMicrophone: vi.fn(),
  onLeave: vi.fn(),
  onOpenFullCall: vi.fn(),
};

function renderBarAvailable(overrides: Partial<BarAvailable> = {}) {
  const props: BarAvailable = {
    mode: "available",
    joinDisabled: false,
    onJoin: vi.fn(),
    ...baseShared,
    ...overrides,
  };
  return render(<ActiveResourceCallBar {...props} />);
}

function renderBarParticipatingLocal(overrides: Partial<BarParticipatingLocal> = {}) {
  const props: BarParticipatingLocal = {
    mode: "participating-local",
    ...baseShared,
    ...baseLocalProps,
    ...overrides,
  };
  return render(<ActiveResourceCallBar {...props} />);
}

function renderBarParticipatingInfo(overrides: Partial<BarParticipatingInfo> = {}) {
  const props: BarParticipatingInfo = {
    mode: "participating-info",
    ...baseShared,
    ...overrides,
  };
  return render(<ActiveResourceCallBar {...props} />);
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2024-01-01T12:00:05.000Z"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ActiveResourceCallBar", () => {
  it("renders in available mode: shows title, timer, and join button, no fake avatars", () => {
    const onJoin = vi.fn();
    renderBarAvailable({ joinDisabled: false, onJoin });
    expect(screen.getByText("Chamada de voz — #infraestrutura")).toBeInTheDocument();
    expect(screen.getByText("00:05")).toBeInTheDocument();

    // Controls should not exist
    expect(screen.queryByRole("button", { name: /Mutar microfone/ })).not.toBeInTheDocument();
    expect(screen.queryByText("3 participantes")).not.toBeInTheDocument();

    const joinBtn = screen.getByRole("button", { name: "Entrar na chamada" });
    expect(joinBtn).toBeEnabled();
    fireEvent.click(joinBtn);
    expect(onJoin).toHaveBeenCalledOnce();
  });

  it("renders in participating-info mode: shows title and timer only, no controls", () => {
    renderBarParticipatingInfo();
    expect(screen.getByText("Chamada de voz — #infraestrutura")).toBeInTheDocument();
    expect(screen.getByText("00:05")).toBeInTheDocument();

    // Controls should not exist
    expect(screen.queryByRole("button", { name: "Entrar na chamada" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Mutar microfone/ })).not.toBeInTheDocument();
    expect(screen.queryByText("3 participantes")).not.toBeInTheDocument();
  });

  it.each([
    ["available", renderBarAvailable],
    ["participating-info", renderBarParticipatingInfo],
  ])("keeps the timer non-live in %s mode", (_mode, renderBar) => {
    const { container } = renderBar();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Chamada ativa" })).toBeInTheDocument();
    expect(container.querySelector("[aria-live]")).toBeNull();
  });

  it("disables joining when available mode receives joinDisabled", () => {
    const onJoin = vi.fn();
    renderBarAvailable({ joinDisabled: true, onJoin });
    const joinButton = screen.getByRole("button", { name: "Entrar na chamada" });
    expect(joinButton).toBeDisabled();
    fireEvent.click(joinButton);
    expect(onJoin).not.toHaveBeenCalled();
  });

  it("renders the title, participant count (remotes + local), and elapsed time derived from startedAt in participating-local mode", () => {
    renderBarParticipatingLocal();
    expect(screen.getByText("Chamada de voz — #infraestrutura")).toBeInTheDocument();
    expect(screen.getByText("3 participantes")).toBeInTheDocument();
    expect(screen.getByText("00:05")).toBeInTheDocument();
  });

  it("ticks the elapsed time every second from the authoritative startedAt, never resetting to 00:00", () => {
    renderBarParticipatingLocal();
    expect(screen.getByText("00:05")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(5_000));
    expect(screen.getByText("00:10")).toBeInTheDocument();
  });

  it("formats elapsed time as H:MM:SS once past 59 minutes", () => {
    vi.setSystemTime(new Date("2024-01-01T12:00:00.000Z"));
    renderBarParticipatingLocal({ startedAt: "2024-01-01T11:00:00.000Z" });
    // Elapsed at mount is already exactly 3600s; verify the format after one
    // more tick to avoid depending on the pre-effect initial state value.
    act(() => vi.advanceTimersByTime(61_000));
    expect(screen.getByText("1:01:01")).toBeInTheDocument();
  });

  it("cleans up its interval on unmount — no further updates after unmount", () => {
    const { unmount } = renderBarParticipatingLocal();
    expect(screen.getByText("00:05")).toBeInTheDocument();
    unmount();
    // No assertion target left to observe, but advancing timers after unmount
    // must not throw (a React act warning would surface as a test failure).
    act(() => vi.advanceTimersByTime(5_000));
  });

  it('shows "Mutar microfone" when the microphone is enabled', () => {
    renderBarParticipatingLocal({ microphoneEnabled: true });
    expect(screen.getByRole("button", { name: "Mutar microfone" })).toBeInTheDocument();
  });

  it('shows "Ativar microfone" when the microphone is disabled', () => {
    renderBarParticipatingLocal({ microphoneEnabled: false });
    expect(screen.getByRole("button", { name: "Ativar microfone" })).toBeInTheDocument();
  });

  it("disables the microphone button while a toggle is pending, without flipping its label", () => {
    renderBarParticipatingLocal({ microphoneEnabled: true, microphonePending: true });
    const button = screen.getByRole("button", { name: "Mutar microfone" });
    expect(button).toBeDisabled();
  });

  it("clicking the microphone button calls onToggleMicrophone exactly once, never onOpenFullCall", () => {
    const onToggleMicrophone = vi.fn();
    const onOpenFullCall = vi.fn();
    renderBarParticipatingLocal({ onToggleMicrophone, onOpenFullCall });
    fireEvent.click(screen.getByRole("button", { name: "Mutar microfone" }));
    expect(onToggleMicrophone).toHaveBeenCalledOnce();
    expect(onOpenFullCall).not.toHaveBeenCalled();
  });

  it("clicking the leave button calls onLeave exactly once, never onOpenFullCall", () => {
    const onLeave = vi.fn();
    const onOpenFullCall = vi.fn();
    renderBarParticipatingLocal({ onLeave, onOpenFullCall });
    fireEvent.click(screen.getByRole("button", { name: /Sair/ }));
    expect(onLeave).toHaveBeenCalledOnce();
    expect(onOpenFullCall).not.toHaveBeenCalled();
  });

  it("clicking the main area calls onOpenFullCall, never mute or leave", () => {
    const onToggleMicrophone = vi.fn();
    const onLeave = vi.fn();
    const onOpenFullCall = vi.fn();
    renderBarParticipatingLocal({ onToggleMicrophone, onLeave, onOpenFullCall });
    fireEvent.click(screen.getByRole("button", { name: /Abrir chamada/ }));
    expect(onOpenFullCall).toHaveBeenCalledOnce();
    expect(onToggleMicrophone).not.toHaveBeenCalled();
    expect(onLeave).not.toHaveBeenCalled();
  });

  it("is keyboard accessible: the main area is a native button reachable and activatable without a mouse", () => {
    renderBarParticipatingLocal();
    const openButton = screen.getByRole("button", { name: /Abrir chamada/ });
    expect(openButton.tagName).toBe("BUTTON");
    const muteButton = screen.getByRole("button", { name: "Mutar microfone" });
    const leaveButton = screen.getByRole("button", { name: /Sair/ });
    expect(muteButton.tagName).toBe("BUTTON");
    expect(leaveButton.tagName).toBe("BUTTON");
  });

  it("marks the active speaker with a non-color-only visual indicator and an accessible (non-live) label", () => {
    const { container } = renderBarParticipatingLocal({ activeSpeakerId: "user-jl" });
    const speakingAvatar = container.querySelector(".voicebanner__avatar--speaking");
    expect(speakingAvatar).not.toBeNull();
    expect(screen.getByText("Juliane Lino está falando")).toBeInTheDocument();
    expect(container.querySelector("[aria-live]")).toBeNull();
  });

  it("marks the local avatar as speaking when activeSpeakerId matches localId", () => {
    renderBarParticipatingLocal({ activeSpeakerId: "user-an" });
    expect(screen.getByText("Álvaro Neto (você) está falando")).toBeInTheDocument();
  });

  it("updates the avatar stack and count when participants join or leave", () => {
    const { rerender } = renderBarParticipatingLocal();
    expect(screen.getByText("3 participantes")).toBeInTheDocument();
    rerender(
      <ActiveResourceCallBar
        {...baseShared}
        {...baseLocalProps}
        mode="participating-local"
        participants={[...baseLocalProps.participants, { identity: "user-x", displayName: "Novo" }]}
      />,
    );
    expect(screen.getByText("4 participantes")).toBeInTheDocument();
    rerender(
      <ActiveResourceCallBar
        {...baseShared}
        {...baseLocalProps}
        mode="participating-local"
        participants={[]}
      />,
    );
    expect(screen.getByText("1 participante")).toBeInTheDocument();
  });

  it("caps visible avatars and shows a +N overflow badge beyond the cap", () => {
    const { container } = renderBarParticipatingLocal({
      participants: [
        { identity: "p1", displayName: "P1" },
        { identity: "p2", displayName: "P2" },
        { identity: "p3", displayName: "P3" },
        { identity: "p4", displayName: "P4" },
      ],
    });
    // 5 total (local + 4 remotes), capped at 3 visible + overflow badge.
    expect(container.querySelectorAll(".voicebanner__avatar").length).toBe(4);
    expect(screen.getByText("+2")).toBeInTheDocument();
  });

  it("keeps the title on a single line via overflow/ellipsis styling, not layout that forces horizontal scroll", () => {
    renderBarParticipatingLocal({
      title:
        "Chamada de voz — #um-nome-de-canal-extremamente-longo-que-nao-deveria-nunca-quebrar-o-layout",
    });
    const title = screen.getByText(/um-nome-de-canal-extremamente-longo/);
    expect(title).toHaveClass("voicebanner__title");
  });

  // ── #642 review, HIGH: active speaker must never be silently hidden behind +N ──

  it("swaps an active speaker outside the visible cap into a visible slot, never duplicated, overflow count unchanged", () => {
    const { container } = renderBarParticipatingLocal({
      participants: [
        { identity: "p1", displayName: "P1" },
        { identity: "p2", displayName: "P2" },
        { identity: "p3", displayName: "P3" },
        { identity: "p4", displayName: "P4 falante" },
      ],
      // 5 total (local + 4 remotes). Natural head slice (local, p1, p2)
      // would hide p4 — the actual speaker — behind "+2".
      activeSpeakerId: "p4",
    });
    const visibleAvatars = container.querySelectorAll(
      ".voicebanner__avatar:not(.voicebanner__avatar--overflow)",
    );
    expect(visibleAvatars).toHaveLength(3);
    const speaking = container.querySelector(".voicebanner__avatar--speaking");
    expect(speaking).not.toBeNull();
    expect(speaking).toHaveTextContent("PF"); // initialsFrom("P4 falante")
    // Never duplicated: exactly one speaking avatar, and the local avatar
    // (always kept) plus the speaker together with no repeats.
    expect(container.querySelectorAll(".voicebanner__avatar--speaking")).toHaveLength(1);
    // Overflow count is unaffected by WHICH entries are hidden, only how many.
    expect(screen.getByText("+2")).toBeInTheDocument();
    expect(screen.getByText("P4 falante está falando")).toBeInTheDocument();
  });

  it("keeps the local avatar visible even when a remote speaker outside the cap is swapped in", () => {
    const { container } = renderBarParticipatingLocal({
      participants: [
        { identity: "p1", displayName: "P1" },
        { identity: "p2", displayName: "P2" },
        { identity: "p3", displayName: "P3" },
        { identity: "p4", displayName: "P4" },
      ],
      activeSpeakerId: "p4",
    });
    // localInitials is the caller-provided "AN" — confirms the local avatar
    // (index 0) was never bumped out to make room for the speaker swap.
    const visibleAvatars = container.querySelectorAll(
      ".voicebanner__avatar:not(.voicebanner__avatar--overflow)",
    );
    expect(visibleAvatars[0]).toHaveTextContent("AN");
  });

  // ── #642 review — accessible name includes elapsed time + participant count ──

  it("includes elapsed duration and participant count in the main area's accessible name, without aria-live", () => {
    const { container } = renderBarParticipatingLocal();
    const openButton = screen.getByRole("button", {
      name: /Abrir chamada.*Chamada de voz.*00:05.*3 participantes/s,
    });
    expect(openButton).toBeInTheDocument();
    expect(container.querySelector("[aria-live]")).toBeNull();
  });

  it("updates the accessible name's duration as the timer ticks", () => {
    renderBarParticipatingLocal();
    act(() => vi.advanceTimersByTime(5_000));
    expect(screen.getByRole("button", { name: /00:10.*3 participantes/s })).toBeInTheDocument();
  });
});
