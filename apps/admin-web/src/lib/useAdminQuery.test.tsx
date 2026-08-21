import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useCallback, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AdminApiError } from "../api/client";
import { classify, useAdminQuery } from "./useAdminQuery";

afterEach(() => {
  vi.restoreAllMocks();
});

function Probe({ load }: { load: (signal: AbortSignal) => Promise<string> }) {
  // The loader arrives already stable from the spec, which is the contract
  // useAdminQuery documents; wrapping it again here would only re-state that.
  const query = useAdminQuery(load);
  return (
    <div>
      <span data-testid="status">{query.status}</span>
      <span data-testid="data">{query.data ?? "—"}</span>
      <span data-testid="message">{query.message}</span>
      <button type="button" onClick={query.reload}>
        recarregar
      </button>
    </div>
  );
}

describe("classify", () => {
  it("keeps a permission failure, a network failure and a broken response apart", () => {
    expect(classify(new AdminApiError(403, "forbidden", "")).status).toBe("forbidden");
    expect(classify(new AdminApiError(0, "network_error", "")).status).toBe("network");
    expect(classify(new AdminApiError(503, "service_unavailable", "")).status).toBe("error");
    expect(classify(new AdminApiError(500, "internal_error", "boom")).message).toBe("boom");
    expect(classify(new Error("anything else")).status).toBe("error");
  });
});

describe("useAdminQuery", () => {
  it("reports loading before the first answer", () => {
    render(<Probe load={() => new Promise(() => {})} />);
    expect(screen.getByTestId("status")).toHaveTextContent("loading");
  });

  it("reports the data once it lands", async () => {
    render(<Probe load={() => Promise.resolve("página 1")} />);
    await waitFor(() => expect(screen.getByTestId("data")).toHaveTextContent("página 1"));
    expect(screen.getByTestId("status")).toHaveTextContent("ready");
  });

  it("classifies a refusal without treating it as an empty result", async () => {
    render(<Probe load={() => Promise.reject(new AdminApiError(403, "forbidden", ""))} />);
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("forbidden"));
    expect(screen.getByTestId("data")).toHaveTextContent("—");
  });

  it("reloads on demand", async () => {
    let calls = 0;
    const load = () => Promise.resolve(`carga ${++calls}`);
    render(<Probe load={load} />);
    await waitFor(() => expect(screen.getByTestId("data")).toHaveTextContent("carga 1"));

    await userEvent.click(screen.getByRole("button", { name: "recarregar" }));
    await waitFor(() => expect(screen.getByTestId("data")).toHaveTextContent("carga 2"));
  });

  // The case the hook exists for: a slow first request must not overwrite a
  // fast second one just by arriving last.
  it("drops a stale answer that lands after a newer one", async () => {
    function Swapper() {
      const [term, setTerm] = useState("a");
      const load = useCallback(
        () =>
          new Promise<string>((resolve) => {
            // "a" is deliberately the slower of the two.
            setTimeout(() => resolve(`resultado de ${term}`), term === "a" ? 50 : 0);
          }),
        [term],
      );
      const query = useAdminQuery(load);
      return (
        <div>
          <span data-testid="data">{query.data ?? "—"}</span>
          <button type="button" onClick={() => setTerm("b")}>
            trocar
          </button>
        </div>
      );
    }
    vi.useFakeTimers();
    render(<Swapper />);
    screen.getByRole("button", { name: "trocar" }).click();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(screen.getByTestId("data")).toHaveTextContent("resultado de b");
    vi.useRealTimers();
  });

  // An aborted request is a replacement, not a failure: showing an error for it
  // would flash a message the operator caused by typing.
  it("does not surface an abort as an error", async () => {
    function Aborting() {
      const [term, setTerm] = useState("a");
      const load = useCallback(
        (signal: AbortSignal) =>
          new Promise<string>((resolve, reject) => {
            signal.addEventListener("abort", () => reject(new Error("aborted")));
            if (term === "b") resolve("segundo");
          }),
        [term],
      );
      const query = useAdminQuery(load);
      return (
        <div>
          <span data-testid="status">{query.status}</span>
          <button type="button" onClick={() => setTerm("b")}>
            trocar
          </button>
        </div>
      );
    }
    render(<Aborting />);
    await userEvent.click(screen.getByRole("button", { name: "trocar" }));
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
  });

  // A result that arrives after unmount must not set state on a screen that is
  // gone.
  it("survives a result arriving after unmount", async () => {
    let settle: (value: string) => void = () => {};
    const { unmount } = render(
      <Probe load={() => new Promise<string>((resolve) => (settle = resolve))} />,
    );
    unmount();
    await act(async () => {
      settle("tarde demais");
    });
  });
});
