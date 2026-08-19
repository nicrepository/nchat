import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import SearchResultList from "./SearchResultList";

interface Item {
  id: string;
  label: string;
}

function baseProps(overrides: Partial<React.ComponentProps<typeof SearchResultList<Item>>> = {}) {
  return {
    status: "ready" as const,
    items: [] as Item[],
    errorKind: null,
    hasMore: false,
    loadingMore: false,
    loadMoreError: null,
    emptyMessage: "Nenhum resultado.",
    listLabel: "Resultados",
    renderItem: (item: Item) => <span>{item.label}</span>,
    itemKey: (item: Item) => item.id,
    onRetry: vi.fn(),
    onLoadMore: vi.fn(),
    ...overrides,
  };
}

describe("SearchResultList", () => {
  it("renders nothing for idle status", () => {
    const { container } = render(<SearchResultList {...baseProps({ status: "idle" })} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders a loading status", () => {
    render(<SearchResultList {...baseProps({ status: "loading" })} />);
    expect(screen.getByRole("status")).toHaveTextContent("Buscando…");
  });

  it("renders an error with a retry button", async () => {
    const onRetry = vi.fn();
    render(
      <SearchResultList {...baseProps({ status: "error", errorKind: "server_error", onRetry })} />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("indisponível");
    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("renders the empty state distinctly from an error", () => {
    render(<SearchResultList {...baseProps({ status: "ready", items: [] })} />);
    expect(screen.getByTestId("global-search-empty")).toHaveTextContent("Nenhum resultado.");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("renders items", () => {
    render(
      <SearchResultList
        {...baseProps({
          items: [
            { id: "1", label: "Item 1" },
            { id: "2", label: "Item 2" },
          ],
        })}
      />,
    );
    expect(screen.getByRole("list", { name: "Resultados" })).toBeInTheDocument();
    expect(screen.getByText("Item 1")).toBeInTheDocument();
    expect(screen.getByText("Item 2")).toBeInTheDocument();
  });

  it("shows a load more button when hasMore is true, and calls onLoadMore", async () => {
    const onLoadMore = vi.fn();
    render(
      <SearchResultList
        {...baseProps({ items: [{ id: "1", label: "Item 1" }], hasMore: true, onLoadMore })}
      />,
    );
    const button = screen.getByRole("button", { name: "Carregar mais" });
    await userEvent.click(button);
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it("disables load more while loadingMore is true", () => {
    render(
      <SearchResultList
        {...baseProps({
          items: [{ id: "1", label: "Item 1" }],
          hasMore: true,
          loadingMore: true,
        })}
      />,
    );
    expect(screen.getByRole("button", { name: "Carregando…" })).toBeDisabled();
  });

  it("hides load more when hasMore is false", () => {
    render(
      <SearchResultList
        {...baseProps({ items: [{ id: "1", label: "Item 1" }], hasMore: false })}
      />,
    );
    expect(screen.queryByRole("button", { name: /Carregar mais/ })).not.toBeInTheDocument();
  });

  it("shows a load-more error without removing already-loaded items", () => {
    render(
      <SearchResultList
        {...baseProps({
          items: [{ id: "1", label: "Item 1" }],
          hasMore: true,
          loadMoreError: "server_error",
        })}
      />,
    );
    expect(screen.getByText("Item 1")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("indisponível");
  });
});
