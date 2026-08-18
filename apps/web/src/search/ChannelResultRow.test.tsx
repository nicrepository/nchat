import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams } from "react-router";
import { describe, expect, it } from "vitest";

import ChannelResultRow from "./ChannelResultRow";
import type { ChannelSearchResult } from "./searchTypes";

function makeResult(overrides: Partial<ChannelSearchResult> = {}): ChannelSearchResult {
  return {
    id: "chan-1",
    slug: "engenharia",
    displayName: "Engenharia",
    isGeneral: false,
    ...overrides,
  };
}

function ChannelMarker() {
  const params = useParams<{ id: string }>();
  return <div data-testid="channel-route">channel id={params.id}</div>;
}

describe("ChannelResultRow", () => {
  it("renders the highlighted display name and slug", () => {
    render(
      <MemoryRouter>
        <ChannelResultRow result={makeResult()} query="enge" />
      </MemoryRouter>,
    );
    expect(screen.getByText("Enge", { selector: "mark" })).toBeInTheDocument();
    expect(screen.getByText("engenharia")).toBeInTheDocument();
  });

  it("shows a 'Geral' badge only when is_general is true", () => {
    const { rerender } = render(
      <MemoryRouter>
        <ChannelResultRow result={makeResult({ isGeneral: true })} query="" />
      </MemoryRouter>,
    );
    expect(screen.getByText("Geral")).toBeInTheDocument();

    rerender(
      <MemoryRouter>
        <ChannelResultRow result={makeResult({ isGeneral: false })} query="" />
      </MemoryRouter>,
    );
    expect(screen.queryByText("Geral")).not.toBeInTheDocument();
  });

  it("navigates using the channel id, not the slug", async () => {
    render(
      <MemoryRouter initialEntries={["/chat/search"]}>
        <Routes>
          <Route
            path="/chat/search"
            element={<ChannelResultRow result={makeResult({ id: "chan-uuid-1" })} query="" />}
          />
          <Route path="/chat/channel/:id" element={<ChannelMarker />} />
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button"));

    expect(await screen.findByTestId("channel-route")).toHaveTextContent("channel id=chan-uuid-1");
  });
});
