import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";

import ChatPlaceholder from "./ChatPlaceholder";

// ── Helper ────────────────────────────────────────────────────────────────────

function renderAtPath(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/chat" element={<ChatPlaceholder />} />
        <Route path="/chat/channel/:id" element={<ChatPlaceholder type="channel" />} />
        <Route path="/chat/dm/:id" element={<ChatPlaceholder type="dm" />} />
      </Routes>
    </MemoryRouter>,
  );
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("ChatPlaceholder — /chat (index)", () => {
  it("renders the empty/select state", () => {
    renderAtPath("/chat");
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
  });

  it("shows invite-to-select message", () => {
    renderAtPath("/chat");
    expect(screen.getByText(/selecione um canal ou mensagem direta/i)).toBeInTheDocument();
  });

  it("shows guidance subtitle", () => {
    renderAtPath("/chat");
    expect(screen.getByText(/escolha um canal ou uma conversa/i)).toBeInTheDocument();
  });
});

describe("ChatPlaceholder — /chat/channel/:id", () => {
  it("renders channel selected state for /chat/channel/geral", () => {
    renderAtPath("/chat/channel/geral");
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
  });

  it("shows channel name with # prefix", () => {
    renderAtPath("/chat/channel/geral");
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent("#geral");
  });

  it("shows coming-soon subtitle for channel", () => {
    renderAtPath("/chat/channel/geral");
    expect(screen.getByText(/as mensagens aparecerão aqui em breve/i)).toBeInTheDocument();
  });

  it("shows correct channel name for infraestrutura", () => {
    renderAtPath("/chat/channel/infraestrutura");
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent("#infraestrutura");
  });
});

describe("ChatPlaceholder — /chat/dm/:id", () => {
  it("renders DM selected state for /chat/dm/alvaro", () => {
    renderAtPath("/chat/dm/alvaro");
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
  });

  it("shows coming-soon subtitle for DM", () => {
    renderAtPath("/chat/dm/alvaro");
    expect(screen.getByText(/as mensagens aparecerão aqui em breve/i)).toBeInTheDocument();
  });

  it("does not show channel # prefix for DM route", () => {
    renderAtPath("/chat/dm/alvaro");
    const heading = screen.getByRole("heading", { level: 2 });
    expect(heading.textContent).not.toMatch(/^#/);
  });
});
