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
    expect(screen.queryByText(/^IP/)).not.toBeInTheDocument();
  });

  it("puts browser and platform on one identity line, with the badge beside it", () => {
    render(<SessionRow session={{ ...base, current: true }} onRevoke={vi.fn()} />);
    const identity = screen.getByText("Firefox 142").parentElement;
    expect(identity).toHaveTextContent(/^Firefox 142 · Windows 10\/11Sessão atual$/);
    expect(identity).toContainElement(screen.getByText("Windows 10/11"));
  });

  it("shows the masked IP as approximate metadata next to the activity", () => {
    render(<SessionRow session={{ ...base, ipAddress: "203.0.*.*" }} onRevoke={vi.fn()} />);
    const ip = screen.getByText("203.0.*.*");
    expect(ip.parentElement).toHaveTextContent("IP 203.0.*.* (aproximado)");
    expect(ip.closest(".session-row__meta")).toHaveTextContent(/Último acesso em/);
  });

  it("says 'Ativa agora' for the current session and the last access for a remote one", () => {
    const { unmount } = render(
      <SessionRow session={{ ...base, current: true }} onRevoke={vi.fn()} />,
    );
    expect(screen.getByText("Ativa agora")).toBeInTheDocument();
    expect(screen.queryByText(/Último acesso/)).not.toBeInTheDocument();
    unmount();

    render(<SessionRow session={base} onRevoke={vi.fn()} />);
    expect(screen.queryByText("Ativa agora")).not.toBeInTheDocument();
    expect(screen.getByText(/Último acesso em/).querySelector("time")).toHaveAttribute(
      "datetime",
      base.lastSeenAt,
    );
  });

  it("never invents a location line", () => {
    render(<SessionRow session={base} onRevoke={vi.fn()} />);
    expect(screen.getByTestId("session-row")).not.toHaveTextContent(/, [A-Z]{2}\b/);
  });

  it("picks a decorative device icon from the platform", () => {
    const android =
      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Mobile Safari/537.36";
    const { container, unmount } = render(
      <SessionRow session={{ ...base, userAgent: android }} onRevoke={vi.fn()} />,
    );
    const icon = container.querySelector(".session-row__icon");
    expect(icon).toHaveAttribute("aria-hidden", "true");
    expect(icon).toHaveTextContent("smartphone");
    unmount();

    const desktop = render(<SessionRow session={base} onRevoke={vi.fn()} />);
    expect(desktop.container.querySelector(".session-row__icon")).toHaveTextContent("computer");
  });

  it("uses a neutral icon, not a desktop, when the platform is unknown", () => {
    for (const userAgent of ["", "curl/8.5.0"]) {
      const { container, unmount } = render(
        <SessionRow session={{ ...base, userAgent }} onRevoke={vi.fn()} />,
      );
      const icon = container.querySelector(".session-row__icon");
      expect(icon).toHaveAttribute("aria-hidden", "true");
      expect(icon).toHaveTextContent(/^devices$/);
      unmount();
    }
  });
});
