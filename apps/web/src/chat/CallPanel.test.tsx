import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import CallPanel from "./CallPanel";
import type { CallController } from "./useCallSignaling";

const currentUserId = "00000000-0000-4000-8000-000000000301";
const otherUserId = "00000000-0000-4000-8000-000000000302";

function controller(
  status: "ringing" | "active" | "declined" | "cancelled" | "timed_out" | "ended",
  callerId = otherUserId,
): CallController {
  return {
    call: {
      call_id: "00000000-0000-4000-8000-000000000303",
      request_id: "00000000-0000-4000-8000-000000000304",
      caller_id: callerId,
      callee_id: callerId === currentUserId ? otherUserId : currentUserId,
      call_type: "video",
      status,
      version: 1,
      created_at: "2026-07-30T12:00:00Z",
      occurred_at: "2026-07-30T12:00:00Z",
      expires_at: "2026-07-30T12:00:30Z",
    },
    pending: false,
    error: null,
    mediaReady: false,
    start: vi.fn(),
    accept: vi.fn(),
    decline: vi.fn(),
    cancel: vi.fn(),
    end: vi.fn(),
    retryMedia: vi.fn(),
    clearTerminal: vi.fn(),
  };
}

describe("CallPanel", () => {
  it("renders an incoming call and disables repeated actions while loading", () => {
    const calls = controller("ringing");
    calls.pending = true;
    render(<CallPanel calls={calls} currentUserId={currentUserId} participantName="Ana" />);
    expect(screen.getByText("Ana está chamando")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Atender" })).toBeDisabled();
  });

  it("answers, declines, cancels and ends through server commands", () => {
    const incoming = controller("ringing");
    const view = render(
      <CallPanel calls={incoming} currentUserId={currentUserId} participantName="Ana" />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Atender" }));
    fireEvent.click(screen.getByRole("button", { name: "Recusar" }));
    expect(incoming.accept).toHaveBeenCalledOnce();
    expect(incoming.decline).toHaveBeenCalledOnce();

    const outgoing = controller("ringing", currentUserId);
    view.rerender(
      <CallPanel calls={outgoing} currentUserId={currentUserId} participantName="Ana" />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Cancelar chamada" }));
    expect(outgoing.cancel).toHaveBeenCalledOnce();

    const active = controller("active", currentUserId);
    view.rerender(<CallPanel calls={active} currentUserId={currentUserId} participantName="Ana" />);
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    expect(active.end).toHaveBeenCalledOnce();
  });

  it("shows timeout and backend errors predictably", () => {
    const calls = controller("timed_out");
    calls.error = "Falha previsível";
    render(<CallPanel calls={calls} currentUserId={currentUserId} participantName="Ana" />);
    expect(screen.getByText("Chamada não atendida")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Falha previsível");
  });
});
