import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import SessionRow from "./SessionRow";
import type { Session } from "./sessionsApi";

const base: Session = {
  id: "s1",
  createdAt: "2026-08-01T00:00:00Z",
  lastSeenAt: "2026-08-27T10:00:00Z",
  ipAddress: "187.10.x.x",
  userAgent: "Mozilla/5.0 Firefox",
  current: false,
};

describe("SessionRow", () => {
  it("shows a 'Sessão atual' badge and no revoke button for the current session", () => {
    render(<SessionRow session={{ ...base, current: true }} onRevoke={vi.fn()} />);
    expect(screen.getByText("Sessão atual")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /revogar/i })).not.toBeInTheDocument();
  });

  it("shows Revogar sessão for a remote session and calls onRevoke with its id", async () => {
    const onRevoke = vi.fn();
    const user = userEvent.setup();
    render(<SessionRow session={base} onRevoke={onRevoke} />);
    await user.click(screen.getByRole("button", { name: "Revogar sessão" }));
    expect(onRevoke).toHaveBeenCalledWith("s1");
  });

  it("shows the masked IP and raw sanitized user agent as-is", () => {
    render(<SessionRow session={base} onRevoke={vi.fn()} />);
    expect(screen.getByText("187.10.x.x")).toBeInTheDocument();
    expect(screen.getByText("Mozilla/5.0 Firefox")).toBeInTheDocument();
  });

  it("falls back to 'Dispositivo desconhecido' when userAgent is empty", () => {
    render(<SessionRow session={{ ...base, userAgent: "" }} onRevoke={vi.fn()} />);
    expect(screen.getByText("Dispositivo desconhecido")).toBeInTheDocument();
  });

  it("omits the IP line entirely when ipAddress is empty", () => {
    render(<SessionRow session={{ ...base, ipAddress: "" }} onRevoke={vi.fn()} />);
    expect(screen.queryByText(/aproximado/i)).not.toBeInTheDocument();
  });
});
