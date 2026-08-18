import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import HighlightedText from "./HighlightedText";

describe("HighlightedText", () => {
  it("wraps matched text in a <mark>", () => {
    render(<HighlightedText text="Orion Rising" query="orion" />);
    const mark = screen.getByText("Orion");
    expect(mark.tagName).toBe("MARK");
  });

  it("renders plain text with no <mark> when there is no match", () => {
    const { container } = render(<HighlightedText text="hello world" query="xyz" />);
    expect(container).toHaveTextContent("hello world");
    expect(container.querySelector("mark")).toBeNull();
  });

  it("never renders raw HTML from the query — a script-like query stays literal text", () => {
    const { container } = render(
      <HighlightedText text="see <script>alert(1)</script> here" query="<script>" />,
    );
    expect(container.querySelector("script")).toBeNull();
    expect(container.textContent).toBe("see <script>alert(1)</script> here");
    const mark = container.querySelector("mark");
    expect(mark?.textContent).toBe("<script>");
  });

  it("does not alter the original text content", () => {
    const text = "The (quick) a+b=c fox.jumps* ação Orion 42";
    const { container } = render(<HighlightedText text={text} query="fox" />);
    expect(container.textContent).toBe(text);
  });
});
