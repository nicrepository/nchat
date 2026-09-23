/**
 * ExpandableDetailsSection (issue #892).
 *
 * These are the primitive's own contracts, exercised through nothing but the
 * props a consumer can actually pass: rows as nodes, a discriminated content
 * value and the optional expansion callback. No domain type appears anywhere in
 * this file, which is the point — if a member, a file or a pin were needed to
 * describe the behaviour, the primitive would know about them.
 */

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ExpandableDetailsSection, {
  defaultCollapsedLimit,
  type ExpandableSectionContent,
} from "./ExpandableDetailsSection";

/** `n` anonymous rows, named so a case can assert exactly which ones are drawn. */
function rows(n: number, prefix = "Item") {
  return Array.from({ length: n }, (_, index) => <li key={index}>{`${prefix} ${index + 1}`}</li>);
}

function ready(overrides: Partial<Extract<ExpandableSectionContent, { status: "ready" }>> = {}) {
  return {
    status: "ready" as const,
    items: rows(0),
    empty: <p>Nada por aqui.</p>,
    ...overrides,
  };
}

function renderSection(
  content: ExpandableSectionContent,
  props: Partial<React.ComponentProps<typeof ExpandableDetailsSection>> = {},
) {
  return render(
    <ExpandableDetailsSection
      title="Coleção"
      listLabel="Itens da coleção"
      content={content}
      {...props}
    />,
  );
}

const expandToggle = () => screen.getByRole("button", { name: /Ver todos/ });
const collapseToggle = () => screen.getByRole("button", { name: /Mostrar menos/ });
const visibleRows = () => within(screen.getByRole("list")).getAllByRole("listitem");

describe("ExpandableDetailsSection — coleção vazia", () => {
  it("renders the consumer's empty state and offers no expansion at all", () => {
    renderSection(ready());

    expect(screen.getByText("Nada por aqui.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    // No phantom list: an empty collection is not a list with nothing in it,
    // and `aria-controls` would otherwise point at an element that is absent.
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("still offers no control when the consumer reports a total it cannot show", () => {
    // A count without a single loaded row cannot be revealed by expanding, and
    // there would be no list for `aria-controls` to point at. Even with a real
    // loader attached, an empty collection stays a bare empty state.
    renderSection(ready({ count: 42, hasMore: true }), { onExpand: vi.fn() });

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText("Nada por aqui.")).toBeInTheDocument();
  });
});

describe("ExpandableDetailsSection — abaixo do limite compacto", () => {
  it("shows a single item with no toggle", () => {
    renderSection(ready({ items: rows(1) }));

    expect(visibleRows()).toHaveLength(1);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("shows exactly five items with no toggle when there is nothing more", () => {
    renderSection(ready({ items: rows(5), count: 5 }));

    expect(visibleRows()).toHaveLength(5);
    expect(screen.getByRole("heading", { name: "Coleção (5)" })).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("names the default compact limit once, for consumers that slice nothing", () => {
    expect(defaultCollapsedLimit).toBe(5);
  });
});

describe("ExpandableDetailsSection — acima do limite compacto", () => {
  it("caps the compact state at five and reveals the rest on expand", async () => {
    renderSection(ready({ items: rows(7), count: 7 }));

    expect(visibleRows()).toHaveLength(5);
    expect(screen.getByText("Item 5")).toBeInTheDocument();
    expect(screen.queryByText("Item 6")).not.toBeInTheDocument();
    expect(expandToggle()).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(expandToggle());

    expect(visibleRows()).toHaveLength(7);
    expect(screen.getByText("Item 7")).toBeInTheDocument();
    expect(collapseToggle()).toHaveAttribute("aria-expanded", "true");

    await userEvent.click(collapseToggle());

    expect(visibleRows()).toHaveLength(5);
    expect(screen.queryByText("Item 6")).not.toBeInTheDocument();
    expect(expandToggle()).toHaveAttribute("aria-expanded", "false");
  });

  it("survives repeated toggling without duplicating rows or losing the control", async () => {
    renderSection(ready({ items: rows(8) }));

    for (let round = 0; round < 3; round += 1) {
      await userEvent.click(expandToggle());
      expect(visibleRows()).toHaveLength(8);
      await userEvent.click(collapseToggle());
      expect(visibleRows()).toHaveLength(5);
    }

    expect(expandToggle()).toHaveFocus();
  });

  it("honours a custom compact limit at its boundary", async () => {
    const { unmount } = renderSection(ready({ items: rows(3) }), { collapsedLimit: 3 });
    // Exactly at the limit is not "more": three of three is the whole thing.
    expect(visibleRows()).toHaveLength(3);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    unmount();

    renderSection(ready({ items: rows(4) }), { collapsedLimit: 3 });
    expect(visibleRows()).toHaveLength(3);
    await userEvent.click(expandToggle());
    expect(visibleRows()).toHaveLength(4);
  });
});

describe("ExpandableDetailsSection — instâncias independentes", () => {
  function renderTwo() {
    render(
      <>
        <ExpandableDetailsSection
          title="Alfa"
          listLabel="Itens de Alfa"
          content={ready({ items: rows(7, "A") })}
        />
        <ExpandableDetailsSection
          title="Beta"
          listLabel="Itens de Beta"
          content={ready({ items: rows(7, "B") })}
        />
      </>,
    );
    return {
      alfa: screen.getByRole("list", { name: "Itens de Alfa" }),
      beta: screen.getByRole("list", { name: "Itens de Beta" }),
      toggleOf: (section: string) =>
        screen.getByRole("button", { name: new RegExp(`(Ver todos|Mostrar menos) ${section}`) }),
    };
  }

  it("keeps each section's expansion to itself", async () => {
    const { toggleOf } = renderTwo();

    await userEvent.click(toggleOf("Alfa"));
    expect(
      within(screen.getByRole("list", { name: "Itens de Alfa" })).getAllByRole("listitem"),
    ).toHaveLength(7);
    expect(
      within(screen.getByRole("list", { name: "Itens de Beta" })).getAllByRole("listitem"),
    ).toHaveLength(5);

    await userEvent.click(toggleOf("Beta"));
    expect(
      within(screen.getByRole("list", { name: "Itens de Alfa" })).getAllByRole("listitem"),
    ).toHaveLength(7);
    expect(
      within(screen.getByRole("list", { name: "Itens de Beta" })).getAllByRole("listitem"),
    ).toHaveLength(7);

    await userEvent.click(toggleOf("Alfa"));
    expect(
      within(screen.getByRole("list", { name: "Itens de Alfa" })).getAllByRole("listitem"),
    ).toHaveLength(5);
    expect(
      within(screen.getByRole("list", { name: "Itens de Beta" })).getAllByRole("listitem"),
    ).toHaveLength(7);
  });

  it("gives the two instances distinct ARIA identifiers", () => {
    const { alfa, beta } = renderTwo();

    expect(alfa.id).not.toBe(beta.id);
    const headings = screen.getAllByRole("heading", { level: 3 });
    expect(headings[0].id).not.toBe(headings[1].id);
    for (const section of screen.getAllByRole("region")) {
      expect(section).toHaveAttribute("aria-labelledby");
    }
  });
});

describe("ExpandableDetailsSection — carregando, erro e vazio", () => {
  it("announces the consumer's loading line and shows nothing else", () => {
    renderSection({ status: "loading", message: "Carregando itens…" });

    expect(screen.getByRole("status")).toHaveTextContent("Carregando itens…");
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
    expect(screen.queryByText("Nada por aqui.")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    // The total belongs to data that has not arrived; the heading stays bare.
    expect(screen.getByRole("heading", { name: "Coleção" })).toBeInTheDocument();
  });

  it("raises the consumer's failure and leaves its recovery action interactive", async () => {
    const retry = vi.fn();
    renderSection(
      { status: "error", message: "Não foi possível carregar." },
      {
        children: (
          <button type="button" onClick={retry}>
            Tentar novamente
          </button>
        ),
      },
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível carregar.");
    expect(screen.queryByRole("list")).not.toBeInTheDocument();

    // The retry is entirely the consumer's: the section renders it and never
    // decides what it does.
    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it("uses no placeholder wording of its own for an empty collection", () => {
    renderSection(ready({ empty: <p>Nenhum item por aqui.</p> }));

    expect(screen.getByText("Nenhum item por aqui.")).toBeInTheDocument();
    expect(screen.queryByText(/ainda não está disponível/)).not.toBeInTheDocument();
  });
});

describe("ExpandableDetailsSection — contrato de lazy loading", () => {
  it("expands and notifies once when the consumer says more exists, without fetching", async () => {
    const onExpand = vi.fn();
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const { rerender } = render(
      <ExpandableDetailsSection
        title="Coleção"
        listLabel="Itens da coleção"
        content={ready({ items: rows(5), hasMore: true })}
        onExpand={onExpand}
      />,
    );

    // Five loaded, a total nobody knows and more to come: the control exists
    // even though the compact cap hides nothing yet.
    expect(visibleRows()).toHaveLength(5);
    await userEvent.click(expandToggle());

    expect(onExpand).toHaveBeenCalledTimes(1);
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(collapseToggle()).toHaveAttribute("aria-expanded", "true");

    // The consumer's own loading line, below the rows it already has.
    rerender(
      <ExpandableDetailsSection
        title="Coleção"
        listLabel="Itens da coleção"
        content={ready({ items: rows(5), hasMore: true })}
        onExpand={onExpand}
      >
        <p>Carregando mais…</p>
      </ExpandableDetailsSection>,
    );
    expect(screen.getByText("Carregando mais…")).toBeInTheDocument();
    expect(collapseToggle()).toHaveAttribute("aria-expanded", "true");

    // The page arrives: new rows appear and the expansion is undisturbed.
    rerender(
      <ExpandableDetailsSection
        title="Coleção"
        listLabel="Itens da coleção"
        content={ready({ items: rows(9), count: 9 })}
        onExpand={onExpand}
      />,
    );
    expect(visibleRows()).toHaveLength(9);
    expect(collapseToggle()).toHaveAttribute("aria-expanded", "true");
    expect(onExpand).toHaveBeenCalledTimes(1);

    // Collapsing does not ask for another page.
    await userEvent.click(collapseToggle());
    expect(onExpand).toHaveBeenCalledTimes(1);
    fetchSpy.mockRestore();
  });

  it("does not offer expansion for rows nobody can load, however many exist", () => {
    // Five loaded of thirty, and no way to ask for the other twenty-five. The
    // total is worth showing and is not worth a control: expanding would end on
    // the same five rows it started from.
    renderSection(ready({ items: rows(5), count: 30 }));

    expect(screen.getByRole("heading", { name: "Coleção (30)" })).toBeInTheDocument();
    expect(visibleRows()).toHaveLength(5);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("does not offer expansion for a capped preview of a larger total", () => {
    // The shape a server-capped roster actually has: thirty carried, forty
    // reported. Thirty is what the section can show, and the compact cap is
    // what hides twenty-five of them — so the control that appears is about
    // those twenty-five and never about the ten that were never sent.
    renderSection(ready({ items: rows(30), count: 40 }), { collapsedLimit: 30 });

    expect(screen.getByRole("heading", { name: "Coleção (40)" })).toBeInTheDocument();
    expect(visibleRows()).toHaveLength(30);
    // collapsedLimit is 30 here, so nothing local is hidden either: the only
    // thing that could justify a control is the count, and it does not.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("claiming more without a loader is inert", async () => {
    const onExpand = vi.fn();
    const { rerender } = render(
      <ExpandableDetailsSection
        title="Coleção"
        listLabel="Itens da coleção"
        content={ready({ items: rows(5), hasMore: true })}
      />,
    );

    // `hasMore` says rows exist; nothing here can go and get them, so the
    // section does not offer to.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();

    // The same content becomes expandable the moment a loader is supplied.
    rerender(
      <ExpandableDetailsSection
        title="Coleção"
        listLabel="Itens da coleção"
        content={ready({ items: rows(5), hasMore: true })}
        onExpand={onExpand}
      />,
    );
    expect(expandToggle()).toBeInTheDocument();

    await userEvent.click(expandToggle());
    expect(onExpand).toHaveBeenCalledTimes(1);
  });

  it("expands on local rows alone and asks for nothing while doing it", async () => {
    const onExpand = vi.fn();
    renderSection(ready({ items: rows(7), count: 40 }), { onExpand });

    // Seven held, forty reported, no `hasMore`: the two hidden rows are the
    // whole reason the control exists, and expanding shows seven — never a
    // promise of forty.
    expect(visibleRows()).toHaveLength(5);
    await userEvent.click(expandToggle());

    expect(visibleRows()).toHaveLength(7);
    expect(screen.getByRole("heading", { name: "Coleção (40)" })).toBeInTheDocument();
    // Nothing remote to fetch, so the loader is left alone.
    expect(onExpand).not.toHaveBeenCalled();
  });
});

describe("ExpandableDetailsSection — teclado e foco", () => {
  it("reaches the toggle by Tab and drives it with Enter and Space", async () => {
    renderSection(ready({ items: rows(7) }));

    await userEvent.tab();
    expect(expandToggle()).toHaveFocus();

    await userEvent.keyboard("{Enter}");
    expect(visibleRows()).toHaveLength(7);
    expect(collapseToggle()).toHaveAttribute("aria-expanded", "true");

    await userEvent.keyboard(" ");
    expect(visibleRows()).toHaveLength(5);
    expect(expandToggle()).toHaveAttribute("aria-expanded", "false");
  });

  it("keeps focus on the same control across expand and collapse", async () => {
    renderSection(ready({ items: rows(7) }));
    const before = expandToggle();

    await userEvent.click(before);
    expect(collapseToggle()).toHaveFocus();
    // The same element throughout: only its label and its state change, so
    // there is no removed node for focus to fall off.
    expect(collapseToggle()).toBe(before);

    await userEvent.click(collapseToggle());
    expect(expandToggle()).toHaveFocus();
    expect(expandToggle()).toBe(before);
  });

  it("makes the expanded region scrollable by keyboard and adds no tab stop while compact", async () => {
    renderSection(ready({ items: rows(7) }));

    expect(screen.getByRole("list")).not.toHaveAttribute("tabindex");

    await userEvent.click(expandToggle());
    expect(screen.getByRole("list")).toHaveAttribute("tabindex", "0");

    // The region follows the control in the tab order, and focus stays inside
    // the section rather than skipping past the rows.
    await userEvent.tab();
    expect(screen.getByRole("list")).toHaveFocus();
  });
});

describe("ExpandableDetailsSection — semântica ARIA", () => {
  it("associates the heading, names the control in context and controls the list", async () => {
    renderSection(ready({ items: rows(7), count: 12 }));

    const section = screen.getByRole("region");
    const heading = screen.getByRole("heading", { level: 3, name: "Coleção (12)" });
    expect(section).toHaveAttribute("aria-labelledby", heading.id);

    // "Ver todos" alone would be the same name in every section of the panel.
    const toggle = expandToggle();
    expect(toggle).toHaveAccessibleName("Ver todos Coleção (12)");
    expect(toggle).toHaveAttribute("type", "button");
    expect(toggle).not.toBeDisabled();
    expect(toggle).not.toHaveAttribute("aria-disabled");
    expect(document.getElementById(toggle.getAttribute("aria-controls") ?? "")).toBe(
      screen.getByRole("list"),
    );

    await userEvent.click(toggle);
    expect(collapseToggle()).toHaveAccessibleName("Mostrar menos Coleção (12)");
  });
});
