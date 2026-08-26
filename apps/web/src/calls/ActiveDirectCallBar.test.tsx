import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ActiveDirectCallBar, { type ActiveDirectCallBarProps } from "./ActiveDirectCallBar";

const baseProps: ActiveDirectCallBarProps = {
  title: "Chamada de voz — Juliane Lino",
  startedAt: "2024-01-01T12:00:00.000Z",
  peerUserId: "user-jl",
  peerName: "Juliane Lino",
  microphoneEnabled: true,
  microphonePending: false,
  onToggleMicrophone: vi.fn(),
  onLeave: vi.fn(),
  onOpenFullCall: vi.fn(),
};

function renderBar(overrides: Partial<ActiveDirectCallBarProps> = {}) {
  return render(<ActiveDirectCallBar {...baseProps} {...overrides} />);
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2024-01-01T12:00:05.000Z"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ActiveDirectCallBar", () => {
  it("renders title, peer identity, and elapsed time derived from the authoritative startedAt", () => {
    renderBar();
    expect(screen.getByText("Chamada de voz — Juliane Lino")).toBeInTheDocument();
    expect(screen.getByText("00:05")).toBeInTheDocument();
    // Never a fake participant count — a direct call has exactly one peer.
    expect(screen.queryByText(/participante/)).not.toBeInTheDocument();
  });

  it("ticks the elapsed time every second from startedAt, never resetting to 00:00", () => {
    renderBar();
    expect(screen.getByText("00:05")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(5_000));
    expect(screen.getByText("00:10")).toBeInTheDocument();
  });

  it("cleans up its interval on unmount", () => {
    const { unmount } = renderBar();
    unmount();
    act(() => vi.advanceTimersByTime(5_000));
  });

  it('shows "Mutar microfone" when the microphone is enabled, "Ativar microfone" when disabled', () => {
    renderBar({ microphoneEnabled: true });
    expect(screen.getByRole("button", { name: "Mutar microfone" })).toBeInTheDocument();
    renderBar({ microphoneEnabled: false });
    expect(screen.getByRole("button", { name: "Ativar microfone" })).toBeInTheDocument();
  });

  it("reflects mute state via aria-pressed and text, never color/icon alone", () => {
    renderBar({ microphoneEnabled: false });
    const button = screen.getByRole("button", { name: "Ativar microfone" });
    expect(button).toHaveAttribute("aria-pressed", "false");
    expect(button).toHaveTextContent("Ativar");
  });

  it("disables the microphone button while a toggle is pending, without flipping its label", () => {
    renderBar({ microphoneEnabled: true, microphonePending: true });
    expect(screen.getByRole("button", { name: "Mutar microfone" })).toBeDisabled();
  });

  it("wires each control to exactly its own callback — mute, leave, and open never cross-trigger", () => {
    const onToggleMicrophone = vi.fn();
    const onLeave = vi.fn();
    const onOpenFullCall = vi.fn();
    renderBar({ onToggleMicrophone, onLeave, onOpenFullCall });

    fireEvent.click(screen.getByRole("button", { name: "Mutar microfone" }));
    expect(onToggleMicrophone).toHaveBeenCalledOnce();
    expect(onLeave).not.toHaveBeenCalled();
    expect(onOpenFullCall).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /Sair/ }));
    expect(onLeave).toHaveBeenCalledOnce();
    expect(onOpenFullCall).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /Abrir chamada/ }));
    expect(onOpenFullCall).toHaveBeenCalledOnce();
    expect(onToggleMicrophone).toHaveBeenCalledOnce();
  });

  it("is keyboard accessible: every control is a native, reachable button", () => {
    renderBar();
    expect(screen.getByRole("button", { name: /Abrir chamada/ }).tagName).toBe("BUTTON");
    expect(screen.getByRole("button", { name: "Mutar microfone" }).tagName).toBe("BUTTON");
    expect(screen.getByRole("button", { name: /Sair/ }).tagName).toBe("BUTTON");
  });

  it("includes the elapsed duration in the main area's accessible name, and updates it as the timer ticks", () => {
    renderBar();
    expect(screen.getByRole("button", { name: /Abrir chamada.*00:05/s })).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(5_000));
    expect(screen.getByRole("button", { name: /Abrir chamada.*00:10/s })).toBeInTheDocument();
  });

  it("keeps the title on a single line via overflow/ellipsis styling", () => {
    renderBar({ title: "Chamada de vídeo — Um nome extremamente longo que não deveria quebrar" });
    const title = screen.getByText(/Um nome extremamente longo/);
    expect(title).toHaveClass("voicebanner__title");
  });
});
