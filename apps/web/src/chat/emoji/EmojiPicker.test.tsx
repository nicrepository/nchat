import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import EmojiPicker from "./EmojiPicker";
import { loadEmojiCatalog, resetEmojiCatalogCache } from "./emojiCatalog";
import { emptyEmojiUsage, type EmojiUsage } from "./emojiUsage";

// Only the load is stubbed, and only so a failure can be provoked: everything
// the picker renders still comes from the real generated catalog.
vi.mock("./emojiCatalog", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./emojiCatalog")>();
  return { ...actual, loadEmojiCatalog: vi.fn(actual.loadEmojiCatalog) };
});
const loadCatalog = vi.mocked(loadEmojiCatalog);

const usageWithHistory: EmojiUsage = {
  tone: 0,
  entries: [
    { emoji: "🚀", count: 1, usedAt: 30 },
    { emoji: "🎉", count: 4, usedAt: 20 },
  ],
};

async function renderPicker(usage: EmojiUsage = emptyEmojiUsage) {
  const onSelect = vi.fn();
  const onToneChange = vi.fn();
  render(<EmojiPicker usage={usage} onToneChange={onToneChange} onSelect={onSelect} />);
  await waitFor(() => expect(screen.queryByText("Carregando emojis…")).toBeNull());
  // The picker makes exactly one focus move of its own, into the search field,
  // in an effect that runs once the catalog lands. Waiting for it here is what
  // stops a test that then focuses a grid cell from racing it.
  await waitFor(() => expect(searchBox()).toHaveFocus());
  return { onSelect, onToneChange };
}

function searchBox() {
  return screen.getByRole("searchbox", { name: "Buscar emoji" });
}

/**
 * Puts a whole term in the search field at once.
 *
 * Deliberately a paste rather than `type`: typing fires one input event per
 * character, and every one of them re-filters and re-renders a grid of
 * thousands of cells. The longest term here — "pessoas de mãos dadas", 21
 * characters — made that test twice as slow as its siblings and the first to
 * exceed findBy's default timeout on a loaded machine, which is what made it
 * flaky under a joint run. A paste is one input event, so the cost no longer
 * scales with the length of the term. What is under test is the search result,
 * never the keystrokes; the tests that do exercise keys use the grid directly.
 */
async function search(term: string) {
  const box = searchBox();
  await userEvent.clear(box);
  box.focus();
  await userEvent.paste(term);
}

beforeEach(() => {
  resetEmojiCatalogCache();
  // Back to the real loader for every case; the failure tests opt out per test.
  loadCatalog.mockReset();
});

describe("EmojiPicker", () => {
  it("shows a loading notice until the catalog arrives", () => {
    render(<EmojiPicker usage={emptyEmojiUsage} onToneChange={vi.fn()} onSelect={vi.fn()} />);
    expect(screen.getByText("Carregando emojis…")).toBeInTheDocument();
  });

  // Opening a dialog moves focus into it — this is the only focus move the
  // picker makes on its own.
  it("focuses its search field when it opens", async () => {
    await renderPicker();
    expect(searchBox()).toHaveFocus();
  });

  it("names every emoji it offers", async () => {
    await renderPicker();
    expect(screen.getByRole("button", { name: "rosto risonho" })).toBeInTheDocument();
  });

  it("finds emoji by name and by keyword", async () => {
    const { onSelect } = await renderPicker();

    await search("foguete");
    await userEvent.click(await screen.findByRole("button", { name: "foguete" }));
    expect(onSelect).toHaveBeenCalledWith("🚀");

    await search("felino");
    expect(screen.getByRole("button", { name: "rosto de gato" })).toBeInTheDocument();
  });

  it("matches a term typed without its accent", async () => {
    await renderPicker();
    await search("coracao");
    // A search answers one question, so its results carry no heading of their own.
    expect(screen.getByRole("group", { name: "Resultados da busca" })).toBeInTheDocument();
    expect(screen.queryByText("Nenhum emoji encontrado.")).toBeNull();
  });

  it("says so when a search matches nothing", async () => {
    await renderPicker();
    await search("zzzzzz");
    expect(screen.getByText("Nenhum emoji encontrado.")).toBeInTheDocument();
  });

  // The bar is glyphs, so every tab carries its name for a screen reader and a
  // tooltip for a pointer — nothing is truncated and nothing scrolls sideways.
  it("switches between categories from the compact icon bar", async () => {
    await renderPicker();
    expect(screen.queryByRole("button", { name: "bandeira: Brasil" })).toBeNull();

    const flags = screen.getByRole("tab", { name: "Bandeiras" });
    expect(flags).toHaveAttribute("title", "Bandeiras");
    await userEvent.click(flags);

    expect(screen.getByRole("button", { name: "bandeira: Brasil" })).toBeInTheDocument();
    expect(flags).toHaveAttribute("aria-selected", "true");
  });

  it("offers one tab per populated category, and no more", async () => {
    await renderPicker(usageWithHistory);
    const tabs = screen.getAllByRole("tab").map((tab) => tab.getAttribute("aria-label"));
    expect(tabs[0]).toBe("Recentes");
    expect(tabs).toContain("Rostos e emoções");
    expect(tabs).toContain("Bandeiras");
    // Unicode's "Component" group holds skin tones and hair, which nobody reacts
    // with; it has no emoji in the catalog and so must have no tab.
    expect(tabs).not.toContain("Componentes");
    expect(tabs).toHaveLength(10);
  });

  it("opens on the reader's own history and shows what they use most", async () => {
    await renderPicker(usageWithHistory);

    const recent = screen.getByRole("group", { name: "Recentes" });
    expect(within(recent).getByRole("button", { name: "foguete" })).toBeInTheDocument();
    // "Mais usados" needs an emoji actually reached for more than once, so the
    // section is not a second, worse copy of "Recentes".
    const frequent = screen.getByRole("group", { name: "Mais usados" });
    expect(within(frequent).getByRole("button", { name: "cone de festa" })).toBeInTheDocument();
    expect(within(frequent).queryByRole("button", { name: "foguete" })).toBeNull();
  });

  it("offers no history tab to a reader who has never reacted", async () => {
    await renderPicker();
    expect(screen.queryByRole("tab", { name: "Recentes" })).toBeNull();
    expect(screen.getByRole("tab", { name: "Rostos e emoções" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("moves through the grid with the arrow keys and selects with Enter", async () => {
    const { onSelect } = await renderPicker();
    const grid = screen.getByRole("group", { name: "Rostos e emoções" });
    const buttons = within(grid).getAllByRole("button");

    buttons[0].focus();
    await userEvent.keyboard("{ArrowRight}");
    expect(buttons[1]).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}");
    expect(buttons[9]).toHaveFocus();
    await userEvent.keyboard("{ArrowUp}{ArrowLeft}");
    expect(buttons[0]).toHaveFocus();

    await userEvent.keyboard("{Enter}");
    expect(onSelect).toHaveBeenCalledWith("😀");
  });

  it("selects with Space as well", async () => {
    const { onSelect } = await renderPicker();
    const grid = screen.getByRole("group", { name: "Rostos e emoções" });
    within(grid).getAllByRole("button")[0].focus();

    await userEvent.keyboard(" ");

    expect(onSelect).toHaveBeenCalledWith("😀");
  });

  it("keeps the grid one tab stop", async () => {
    await renderPicker();
    const grid = screen.getByRole("group", { name: "Rostos e emoções" });
    const tabbable = within(grid)
      .getAllByRole("button")
      .filter((button) => button.tabIndex === 0);
    expect(tabbable).toHaveLength(1);
  });

  it("draws the grid in the tone the reader last chose", async () => {
    await renderPicker({ ...usageWithHistory, tone: 5 });
    await search("polegar para cima");
    const cell = await screen.findByRole("button", { name: "polegar para cima" });
    expect(cell).toHaveTextContent("👍🏿");
  });

  it("offers a complete emoji when the sequence is composed", async () => {
    const { onSelect } = await renderPicker();

    await search("familia");
    const family = await screen.findAllByRole("button", { name: /família/ });
    await userEvent.click(family[0]);

    expect(onSelect).toHaveBeenCalledTimes(1);
    expect([...String(onSelect.mock.calls[0][0])].length).toBeGreaterThan(1);
  });
});

/**
 * The chunk carrying the catalog crosses the network like anything else. A
 * failure used to leave the picker on its loading notice for ever, which is
 * indistinguishable from a very slow connection and offers the reader nothing.
 */
describe("EmojiPicker when the catalog cannot be loaded", () => {
  function expectFailureNotice() {
    const notice = screen.getByRole("alert");
    expect(notice).toHaveTextContent("Não foi possível carregar os emojis.");
    // The reason is internal to the bundler and means nothing to a reader.
    expect(notice).not.toHaveTextContent(/chunk|import|Error/i);
    return screen.getByRole("button", { name: "Tentar novamente" });
  }

  it("shows a failure notice with a retry instead of loading for ever", async () => {
    loadCatalog.mockRejectedValueOnce(new Error("chunk unreachable"));
    render(<EmojiPicker usage={emptyEmojiUsage} onToneChange={vi.fn()} onSelect={vi.fn()} />);

    expect(screen.getByRole("status")).toHaveTextContent("Carregando emojis…");

    await waitFor(() => expectFailureNotice());
    expect(screen.queryByText("Carregando emojis…")).toBeNull();
  });

  it("loads the catalog again when the reader retries, and recovers", async () => {
    loadCatalog.mockRejectedValueOnce(new Error("chunk unreachable"));
    render(<EmojiPicker usage={emptyEmojiUsage} onToneChange={vi.fn()} onSelect={vi.fn()} />);
    const retry = await waitFor(() => expectFailureNotice());
    expect(loadCatalog).toHaveBeenCalledTimes(1);

    await userEvent.click(retry);

    expect(loadCatalog).toHaveBeenCalledTimes(2);
    expect(await screen.findByRole("button", { name: "rosto risonho" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows the failure again when the retry also fails", async () => {
    loadCatalog.mockRejectedValue(new Error("chunk unreachable"));
    render(<EmojiPicker usage={emptyEmojiUsage} onToneChange={vi.fn()} onSelect={vi.fn()} />);

    await userEvent.click(await waitFor(() => expectFailureNotice()));

    await waitFor(() => expectFailureNotice());
    // A retry is the reader's decision every time: nothing here loops on its own.
    expect(loadCatalog).toHaveBeenCalledTimes(2);
  });

  it("does not write state into a picker the reader already closed", async () => {
    let settle: (() => void) | undefined;
    loadCatalog.mockReturnValueOnce(
      new Promise((_, reject) => {
        settle = () => reject(new Error("chunk unreachable"));
      }),
    );
    const { container, unmount } = render(
      <EmojiPicker usage={emptyEmojiUsage} onToneChange={vi.fn()} onSelect={vi.fn()} />,
    );
    expect(screen.getByRole("status")).toBeInTheDocument();

    unmount();
    settle?.();
    await Promise.resolve();
    await Promise.resolve();

    // Nothing came back after the picker was gone — no failure notice rendered
    // into a detached tree, and no state written to an unmounted component.
    expect(container).toBeEmptyDOMElement();
  });
});

/**
 * The tone choice is contextual: it belongs to the emoji it applies to, not to a
 * control in the header that governs emoji the reader cannot see.
 */
describe("EmojiPicker skin tone", () => {
  async function openToneFor(name: string, usage: EmojiUsage = emptyEmojiUsage) {
    const picked = await renderPicker(usage);
    await search(name);
    const cell = await screen.findByRole("button", { name });
    await userEvent.click(cell);
    return { ...picked, cell };
  }

  it("selects an emoji without tones immediately, with no palette", async () => {
    const { onSelect } = await renderPicker();

    await search("foguete");
    await userEvent.click(await screen.findByRole("button", { name: "foguete" }));

    expect(onSelect).toHaveBeenCalledWith("🚀");
    expect(screen.queryByRole("dialog", { name: /Tom de pele/ })).toBeNull();
  });

  it("opens a palette of six tones for an emoji that has them", async () => {
    const { onSelect } = await openToneFor("polegar para cima");

    const palette = screen.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
    expect(within(palette).getAllByRole("button")).toHaveLength(6);
    expect(
      within(palette).getByRole("button", { name: "polegar para cima — Morena escura" }),
    ).toBeInTheDocument();
    // Nothing is applied until the reader chooses.
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("applies the chosen variant and remembers the tone", async () => {
    const { onSelect, onToneChange } = await openToneFor("polegar para cima");

    await userEvent.click(
      screen.getByRole("button", { name: "polegar para cima — Morena escura" }),
    );

    expect(onSelect).toHaveBeenCalledWith("👍🏾");
    expect(onToneChange).toHaveBeenCalledWith(4);
    expect(screen.queryByRole("dialog", { name: /Tom de pele/ })).toBeNull();
  });

  // The bug this covers is the cartesian product: tone 3 of a two-person
  // sequence must tone *both* people, never pair a light hand with a dark one.
  it("offers homogeneous variants for a multi-person sequence", async () => {
    const { onSelect } = await openToneFor("pessoas de mãos dadas");

    await userEvent.click(screen.getByRole("button", { name: "pessoas de mãos dadas — Morena" }));

    expect(onSelect).toHaveBeenCalledWith("🧑🏽‍🤝‍🧑🏽");
  });

  it("marks the remembered tone as the current one", async () => {
    await openToneFor("polegar para cima", { ...emptyEmojiUsage, tone: 2 });

    const current = screen.getByRole("button", { name: "polegar para cima — Morena clara" });
    expect(current).toHaveAttribute("aria-current", "true");
    await waitFor(() => expect(current).toHaveFocus());
  });

  it("moves through the tones with the arrows and chooses with Enter", async () => {
    const { onSelect } = await openToneFor("polegar para cima");
    const palette = screen.getByRole("dialog", { name: /Tom de pele/ });
    await waitFor(() => expect(within(palette).getAllByRole("button")[0]).toHaveFocus());

    await userEvent.keyboard("{ArrowRight}{ArrowRight}");
    expect(within(palette).getAllByRole("button")[2]).toHaveFocus();
    await userEvent.keyboard("{Enter}");

    expect(onSelect).toHaveBeenCalledWith("👍🏼");
  });

  it("closes on Escape and gives focus back to the emoji", async () => {
    const { onSelect, cell } = await openToneFor("polegar para cima");

    await userEvent.keyboard("{Escape}");

    expect(screen.queryByRole("dialog", { name: /Tom de pele/ })).toBeNull();
    expect(cell).toHaveFocus();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("jumps to the ends with Home and End, and refuses to leave them", async () => {
    const { onSelect } = await openToneFor("polegar para cima");
    const palette = screen.getByRole("dialog", { name: /Tom de pele/ });
    const options = within(palette).getAllByRole("button");
    await waitFor(() => expect(options[0]).toHaveFocus());

    // Already at the first tone: the arrow is not ours to consume.
    await userEvent.keyboard("{ArrowLeft}");
    expect(options[0]).toHaveFocus();

    await userEvent.keyboard("{End}");
    expect(options[5]).toHaveFocus();
    await userEvent.keyboard("{ArrowRight}");
    expect(options[5]).toHaveFocus();

    await userEvent.keyboard("{Home}");
    expect(options[0]).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}{ArrowUp}");
    expect(options[0]).toHaveFocus();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("dismisses when the reader clicks away from it", async () => {
    const { onSelect } = await openToneFor("polegar para cima");

    await userEvent.click(document.body);

    expect(screen.queryByRole("dialog", { name: /Tom de pele/ })).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("sits above the emoji when there is room for it", async () => {
    const rect = (over: Partial<DOMRect>): DOMRect =>
      ({
        x: 0,
        y: 0,
        left: 0,
        right: 0,
        top: 0,
        bottom: 0,
        width: 0,
        height: 0,
        ...over,
        toJSON: () => ({}),
      }) as DOMRect;
    const spy = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      if (this.classList.contains("chat-emoji-tone")) {
        return rect({ right: 220, bottom: 44, width: 220, height: 44 });
      }
      return rect({ left: 400, right: 438, top: 300, bottom: 338, width: 38, height: 38 });
    });

    await openToneFor("polegar para cima");

    // 300 (emoji top) − 44 (palette) − 6 (gap), centred on a 38px cell.
    const palette = screen.getByRole("dialog", { name: /Tom de pele/ });
    expect(palette).toHaveStyle({ top: "250px", left: "309px" });
    expect(palette.style.transformOrigin).toBe("center bottom");
    spy.mockRestore();
  });

  it("keeps the palette inside the viewport", async () => {
    const rect = (over: Partial<DOMRect>): DOMRect =>
      ({
        x: 0,
        y: 0,
        left: 0,
        right: 0,
        top: 0,
        bottom: 0,
        width: 0,
        height: 0,
        ...over,
        toJSON: () => ({}),
      }) as DOMRect;
    const spy = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      if (this.classList.contains("chat-emoji-tone")) {
        return rect({ right: 220, bottom: 44, width: 220, height: 44 });
      }
      // An emoji hard against the left edge, near the top of the viewport.
      return rect({ left: 2, right: 40, top: 4, bottom: 42, width: 38, height: 38 });
    });

    await openToneFor("polegar para cima");

    const palette = screen.getByRole("dialog", { name: /Tom de pele/ });
    // Clamped to the 8px gutter, and flipped below the emoji because it does not
    // fit above it.
    expect(palette).toHaveStyle({ left: "8px", top: "48px", visibility: "visible" });
    spy.mockRestore();
  });
});
