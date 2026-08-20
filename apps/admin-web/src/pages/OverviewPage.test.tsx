import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { AdminBootstrap } from "../api/adminApi";
import { AdminSessionContext, type AdminSessionValue } from "../session/AdminSessionContext";
import OverviewPage from "./OverviewPage";

const BOOTSTRAP: AdminBootstrap = {
  identity: { user_id: "u1", email: "admin@example.test", display_name: "Admin", avatar_url: "" },
  capabilities: ["admin.audit.read", "admin.config.read"],
  environment: "STAGING",
  build: { service: "admin-service", version: "0.0.0", commit: "dev" },
  session: { idle_expires_at: "2026-08-20T12:00:00Z", absolute_expires_at: "2026-08-20T20:00:00Z" },
  csrf_token: "csrf-1",
};

function renderWith(bootstrap: AdminBootstrap | null) {
  const held = new Set(bootstrap?.capabilities ?? []);
  const value: AdminSessionValue = {
    status: bootstrap === null ? "loading" : "ready",
    bootstrap,
    message: "",
    reload: () => {},
    adopt: () => {},
    signOut: async () => {},
    can: (capability) => held.has(capability) || held.has("admin.superuser"),
  };
  return render(
    <AdminSessionContext.Provider value={value}>
      <OverviewPage />
    </AdminSessionContext.Provider>,
  );
}

describe("OverviewPage", () => {
  it("states the session, environment and effective capabilities", () => {
    renderWith(BOOTSTRAP);

    expect(screen.getByRole("heading", { name: "Visão geral" })).toBeInTheDocument();
    expect(screen.getByText("STAGING")).toBeInTheDocument();
    expect(screen.getByText("admin.audit.read")).toBeInTheDocument();
    expect(screen.getByText("admin.config.read")).toBeInTheDocument();
  });

  it("says plainly when the session grants nothing", () => {
    renderWith({ ...BOOTSTRAP, capabilities: [] });

    expect(screen.getByText("Nenhuma permissão administrativa atribuída.")).toBeInTheDocument();
  });

  // The page is honest about how much of the console exists: it counts the
  // implemented sections rather than implying the rest work.
  it("counts implemented sections against visible ones", () => {
    renderWith({ ...BOOTSTRAP, capabilities: ["admin.superuser"] });

    expect(screen.getByText(/2 de 13 seções visíveis/)).toBeInTheDocument();
  });

  it("renders nothing without a session", () => {
    const { container } = renderWith(null);
    expect(container).toBeEmptyDOMElement();
  });
});
