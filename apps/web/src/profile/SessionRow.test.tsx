import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import SessionRow from "./SessionRow";
import type { Session } from "./sessionsApi";

const FIREFOX_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:142.0) Gecko/20100101 Firefox/142.0";

const base: Session = {
  id: "s1",
  createdAt: "2026-08-01T00:00:00Z",
  lastSeenAt: "2026-08-27T10:00:00Z",
  ipAddress: "187.10.x.x",
  userAgent: FIREFOX_WINDOWS,
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

  it("shows the masked IP, the browser and the platform instead of the raw user agent", () => {
    render(<SessionRow session={base} onRevoke={vi.fn()} />);
    expect(screen.getByText("187.10.x.x")).toBeInTheDocument();
    expect(screen.getByText("Firefox 142")).toBeInTheDocument();
    expect(screen.getByText("Windows 10/11")).toBeInTheDocument();
    expect(screen.queryByText(FIREFOX_WINDOWS)).not.toBeInTheDocument();
  });

  it("identifies Edge as Edge and not as the Chrome token it also carries", () => {
    render(
      <SessionRow
        session={{
          ...base,
          userAgent:
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0",
        }}
        onRevoke={vi.fn()}
      />,
    );
    expect(screen.getByText("Microsoft Edge 153")).toBeInTheDocument();
    expect(screen.queryByText(/Chrome/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Safari/)).not.toBeInTheDocument();
  });

  it("falls back to 'Navegador desconhecido' when userAgent is empty", () => {
    render(<SessionRow session={{ ...base, userAgent: "" }} onRevoke={vi.fn()} />);
    expect(screen.getByText("Navegador desconhecido")).toBeInTheDocument();
  });

  it("omits the IP line entirely when ipAddress is empty", () => {
    render(<SessionRow session={{ ...base, ipAddress: "" }} onRevoke={vi.fn()} />);
    expect(screen.queryByText(/aproximado/i)).not.toBeInTheDocument();
  });
});
