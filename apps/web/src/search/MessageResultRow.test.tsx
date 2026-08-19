import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { describe, expect, it } from "vitest";

import MessageResultRow from "./MessageResultRow";
import type { MessageSearchResult } from "./searchTypes";

function makeResult(overrides: Partial<MessageSearchResult> = {}): MessageSearchResult {
  return {
    id: "msg-1",
    channelId: "chan-1",
    channelName: "geral",
    senderId: "user-1",
    senderDisplayName: "Alice",
    bodyText: "hello orion, how are you",
    createdAt: "2026-01-01T12:00:00Z",
    score: 1,
    ...overrides,
  };
}

function ChannelMarker() {
  const location = useLocation();
  const params = new URLSearchParams(location.search);
  return <div data-testid="channel-route">channel route, message={params.get("message")}</div>;
}

describe("MessageResultRow", () => {
  it("renders sender, channel, and a highlighted snippet", () => {
    render(
      <MemoryRouter>
        <MessageResultRow result={makeResult()} query="orion" />
      </MemoryRouter>,
    );
    expect(screen.getByText("Alice")).toBeInTheDocument();
    expect(screen.getByText(/geral/)).toBeInTheDocument();
    const mark = screen.getByText("orion", { selector: "mark" });
    expect(mark).toBeInTheDocument();
  });

  it("navigates to the channel with a ?message= anchor on click", async () => {
    render(
      <MemoryRouter initialEntries={["/chat/search"]}>
        <Routes>
          <Route
            path="/chat/search"
            element={<MessageResultRow result={makeResult()} query="orion" />}
          />
          <Route path="/chat/channel/:id" element={<ChannelMarker />} />
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button"));

    expect(await screen.findByTestId("channel-route")).toHaveTextContent(
      "channel route, message=msg-1",
    );
  });
});
