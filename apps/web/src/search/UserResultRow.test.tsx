import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { mockGetOrCreateDirectDM } = vi.hoisted(() => ({
  mockGetOrCreateDirectDM: vi.fn(),
}));

vi.mock("../chat/chatApi", () => ({
  getOrCreateDirectDM: (...args: unknown[]) => mockGetOrCreateDirectDM(...args),
}));

import UserResultRow from "./UserResultRow";
import type { UserSearchResult } from "./searchTypes";

function makeResult(overrides: Partial<UserSearchResult> = {}): UserSearchResult {
  return { id: "user-1", displayName: "Alice Silva", avatarUrl: null, ...overrides };
}

function DMMarker() {
  const params = useParams<{ id: string }>();
  return <div data-testid="dm-route">dm id={params.id}</div>;
}

beforeEach(() => {
  mockGetOrCreateDirectDM.mockReset();
});

describe("UserResultRow", () => {
  it("renders the highlighted display name and initials fallback", () => {
    render(
      <MemoryRouter>
        <UserResultRow result={makeResult()} query="alice" />
      </MemoryRouter>,
    );
    expect(screen.getByText("Alice", { selector: "mark" })).toBeInTheDocument();
    expect(screen.getByText("AS")).toBeInTheDocument();
  });

  it("opens (or creates) the DM and navigates on success", async () => {
    mockGetOrCreateDirectDM.mockResolvedValue({ conversationId: "dm-123", created: false });
    render(
      <MemoryRouter initialEntries={["/chat/search"]}>
        <Routes>
          <Route
            path="/chat/search"
            element={<UserResultRow result={makeResult({ id: "user-42" })} query="" />}
          />
          <Route path="/chat/dm/:id" element={<DMMarker />} />
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button"));

    expect(mockGetOrCreateDirectDM).toHaveBeenCalledWith("user-42");
    expect(await screen.findByTestId("dm-route")).toHaveTextContent("dm id=dm-123");
  });

  it("shows an inline error and does not navigate when getOrCreateDirectDM fails", async () => {
    mockGetOrCreateDirectDM.mockRejectedValue(new Error("network"));
    render(
      <MemoryRouter initialEntries={["/chat/search"]}>
        <Routes>
          <Route path="/chat/search" element={<UserResultRow result={makeResult()} query="" />} />
          <Route path="/chat/dm/:id" element={<DMMarker />} />
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button"));

    expect(await screen.findByRole("alert")).toHaveTextContent("Não foi possível abrir a conversa");
    expect(screen.queryByTestId("dm-route")).not.toBeInTheDocument();
  });
});
