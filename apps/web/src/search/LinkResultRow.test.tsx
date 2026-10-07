import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { describe, expect, it } from "vitest";

import LinkResultRow from "./LinkResultRow";
import { linkResult } from "./searchFixtures";

function Landing() {
  const location = useLocation();
  return (
    <output data-testid="landing" data-state={JSON.stringify(location.state)}>
      {location.pathname}
      {location.search}
    </output>
  );
}

function renderRow(row: ReactElement) {
  return render(
    <MemoryRouter initialEntries={["/chat/search"]}>
      <Routes>
        <Route path="/chat/search" element={row} />
        <Route path="/chat/:kind/:id" element={<Landing />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("LinkResultRow", () => {
  it("shows domain, URL, conversation, author and time, with the match highlighted", () => {
    renderRow(<LinkResultRow result={linkResult()} query="runbook" />);
    const card = screen.getByRole("button", { name: /^Link docs\.example\.com/ });
    expect(card).toHaveTextContent("https://docs.example.com/runbook");
    expect(card).toHaveTextContent("#infraestrutura · Juliane Lino ·");
    expect(card.querySelector("time")).toHaveAttribute("datetime", "2026-09-01T09:41:00Z");
    expect(screen.getByText("runbook", { selector: "mark" })).toBeInTheDocument();
  });

  it("opens the message it was shared in as an explicit jump (MESSAGE_TARGET)", async () => {
    const result = linkResult({
      messageId: "m 7",
      conversation: { kind: "dm", id: "g1", type: "group", name: "Projeto" },
    });
    renderRow(<LinkResultRow result={result} query="docs" />);
    await userEvent.click(screen.getByRole("button"));

    const landing = screen.getByTestId("landing");
    expect(landing).toHaveTextContent("/chat/dm/g1?message=m%207");
    expect(JSON.parse(landing.dataset.state!)).toEqual({ messageJump: true });
  });

  it("is reached by keyboard and opened with Enter", async () => {
    renderRow(<LinkResultRow result={linkResult()} query="docs" />);
    const user = userEvent.setup();
    await user.tab();
    expect(screen.getByRole("button")).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("landing")).toHaveTextContent("/chat/channel/c1?message=m7");
  });

  it("never links out and never renders the URL as markup", () => {
    const url = 'https://evil.example.com/"><img src=x onerror="alert(1)">';
    const { container } = renderRow(
      <LinkResultRow result={linkResult({ url, hostname: "evil.example.com" })} query="<img" />,
    );
    expect(container.querySelector("a, [href], img")).toBeNull();
    expect(screen.getByRole("button")).toHaveTextContent(url);
    expect(screen.getByText("<img", { selector: "mark" })).toBeInTheDocument();
  });

  it("keeps a long URL whole in the DOM and clips it only visually", () => {
    const url = `https://docs.example.com/${"segmento-".repeat(60)}fim?ref=abc`;
    renderRow(<LinkResultRow result={linkResult({ url })} query="docs" />);
    const line = screen.getByRole("button").querySelector(".global-search__result-url");
    expect(line).toHaveTextContent(url);
    expect(screen.getByRole("button")).toHaveAccessibleName(expect.stringContaining("fim?ref=abc"));
  });
});
