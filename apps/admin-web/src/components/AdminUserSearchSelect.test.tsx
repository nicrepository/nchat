import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import AdminUserSearchSelect, { type UserOption } from "./AdminUserSearchSelect";
import { SEARCH_DEBOUNCE_MS } from "../lib/useDebouncedValue";

const ANA: UserOption = { id: "user-ana", displayName: "Ana Lima", secondary: "ana@nchat.test" };
const BRUNO: UserOption = {
  id: "user-bruno",
  displayName: "Bruno Reis",
  secondary: "bruno@nchat.test",
};

/**
 * The picker with the state a consumer really owns.
 *
 * `search` answers per term, so a spec can say "'An' finds Ana, 'Bru' finds
 * Bruno" and then assert which of the two a keystroke could have selected.
 */
function Harness({
  search,
  onSelect,
}: {
  search: (term: string, signal: AbortSignal) => Promise<UserOption[]>;
  onSelect: (user: UserOption | null) => void;
}) {
  const [selected, setSelected] = useState<UserOption | null>(null);
  return (
    <AdminUserSearchSelect
      label="Pessoa"
      placeholder="Busque por nome"
      search={search}
      selected={selected}
      onSelect={(user) => {
        setSelected(user);
        onSelect(user);
      }}
    />
  );
}

function byTerm(table: Record<string, UserOption[]>) {
  return vi.fn((term: string) => Promise.resolve(table[term] ?? []));
}

/**
 * The search box. Queried by role because the open listbox carries the same
 * accessible name — that is the combobox pattern, not a mislabelled control.
 */
function field() {
  return screen.getByRole("combobox", { name: "Pessoa" });
}

async function type(text: string) {
  await userEvent.type(field(), text, { delay: null });
}

/** Lets the debounce elapse and the resulting request settle. */
async function settle() {
  await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS + 50);
}

describe("AdminUserSearchSelect", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("offers what the settled term found", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<Harness search={byTerm({ An: [ANA] })} onSelect={vi.fn()} />);

    await type("An");
    await settle();

    expect(await screen.findByRole("option", { name: /Ana Lima/ })).toBeInTheDocument();
  });

  // The finding: between the keystroke and the end of the debounce the list
  // still described the previous term, and every one of those rows was live.
  it("withdraws the previous results the moment the term changes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<Harness search={byTerm({ An: [ANA], Bru: [BRUNO] })} onSelect={vi.fn()} />);

    await type("An");
    await settle();
    expect(await screen.findByRole("option", { name: /Ana Lima/ })).toBeInTheDocument();

    // One keystroke, no timer advance: this is the whole debounce window.
    await userEvent.clear(field());
    await type("Bru");

    expect(screen.queryByRole("option", { name: /Ana Lima/ })).not.toBeInTheDocument();
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    // And it says it is still working, rather than claiming nobody matches a
    // search that has not run.
    expect(screen.getByRole("status")).toHaveTextContent("Carregando");
    expect(screen.queryByText("Nenhuma pessoa encontrada.")).not.toBeInTheDocument();
  });

  it("Enter during the debounce window cannot select the previous result", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onSelect = vi.fn();
    render(<Harness search={byTerm({ An: [ANA], Bru: [BRUNO] })} onSelect={onSelect} />);

    await type("An");
    await settle();
    await screen.findByRole("option", { name: /Ana Lima/ });
    // Highlight Ana explicitly, so this is the worst case and not an accident
    // of the default index.
    await userEvent.keyboard("{ArrowDown}");

    await userEvent.clear(field());
    await type("Bru");
    await userEvent.keyboard("{Enter}");

    expect(onSelect).not.toHaveBeenCalled();
    // The term the operator was typing survived: Enter selected nobody, so it
    // must not have behaved like a selection either.
    expect(field()).toHaveValue("Bru");

    // Once the new search settles, Enter picks from it.
    await settle();
    await userEvent.keyboard("{Enter}");
    expect(onSelect).toHaveBeenCalledWith(BRUNO);
  });

  it("a click cannot land on a row from the previous term", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onSelect = vi.fn();
    render(<Harness search={byTerm({ An: [ANA], Bru: [BRUNO] })} onSelect={onSelect} />);

    await type("An");
    await settle();
    const stale = await screen.findByRole("option", { name: /Ana Lima/ });

    await userEvent.clear(field());
    await type("Bru");

    // The node the operator's pointer was over is gone, not merely disabled.
    expect(stale).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("option", { name: /Bruno Reis/ }));
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(BRUNO);
  });

  // aria-activedescendant is what a screen reader announces. Pointing at a
  // withdrawn row would announce a person who is no longer on offer.
  it("stops pointing at a withdrawn row", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<Harness search={byTerm({ An: [ANA], Bru: [BRUNO] })} onSelect={vi.fn()} />);

    await type("An");
    await settle();
    const input = field();
    await waitFor(() => expect(input).toHaveAttribute("aria-activedescendant"));

    await userEvent.clear(input);
    await type("Bru");
    expect(input).not.toHaveAttribute("aria-activedescendant");

    await settle();
    await waitFor(() => expect(input.getAttribute("aria-activedescendant")).toContain(BRUNO.id));
  });

  // Shortening below the minimum has to take effect on the keystroke: waiting
  // for the debounce would leave a list up that answers a term the operator has
  // already abandoned.
  it("closes immediately when the term drops below the minimum", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const search = byTerm({ An: [ANA] });
    render(<Harness search={search} onSelect={vi.fn()} />);

    await type("An");
    await settle();
    expect(await screen.findByRole("option", { name: /Ana Lima/ })).toBeInTheDocument();

    await userEvent.type(field(), "{Backspace}");

    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(field()).toHaveAttribute("aria-expanded", "false");
    // And no request was made for the single character.
    await settle();
    expect(search.mock.calls.map(([term]) => term)).toEqual(["An"]);
  });

  // The other two keys the list responds to. ArrowUp wraps, so it is the fast
  // way to the last person; Escape puts the list away without abandoning what
  // was typed, which is what an operator who reached for the list by accident
  // expects.
  it("wraps upwards and closes on Escape without losing the term", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onSelect = vi.fn();
    render(<Harness search={byTerm({ re: [ANA, BRUNO] })} onSelect={onSelect} />);

    await type("re");
    await settle();
    await screen.findByRole("option", { name: /Ana Lima/ });

    // From the first row, up is the last row.
    await userEvent.keyboard("{ArrowUp}");
    expect(field().getAttribute("aria-activedescendant")).toContain(BRUNO.id);
    await userEvent.keyboard("{ArrowUp}");
    expect(field().getAttribute("aria-activedescendant")).toContain(ANA.id);

    await userEvent.keyboard("{Escape}");
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(field()).toHaveValue("re");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("says nobody matched only when a search really returned nobody", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<Harness search={byTerm({})} onSelect={vi.fn()} />);

    await type("Zzz");
    expect(screen.queryByText("Nenhuma pessoa encontrada.")).not.toBeInTheDocument();

    await settle();
    expect(await screen.findByText("Nenhuma pessoa encontrada.")).toBeInTheDocument();
  });
});
