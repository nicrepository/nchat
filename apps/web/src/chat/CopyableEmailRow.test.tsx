import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import CopyableEmailRow from "./CopyableEmailRow";

const email = "juliane.lino@nic-labs.test";
const success = "E-mail copiado";
const failure = "Não foi possível copiar o e-mail";

/** A clipboard write the test settles by hand, to order two attempts. */
function deferred() {
  let resolve: () => void = () => {};
  let reject: () => void = () => {};
  const promise = new Promise<void>((onResolve, onReject) => {
    resolve = () => onResolve();
    reject = () => onReject(new Error("denied"));
  });
  return { promise, resolve, reject };
}

function stubClipboard(clipboard: unknown) {
  Object.defineProperty(navigator, "clipboard", { value: clipboard, configurable: true });
}

function copyButton() {
  return screen.getByRole("button", { name: email });
}

afterEach(() => {
  Reflect.deleteProperty(navigator, "clipboard");
  vi.restoreAllMocks();
});

describe("CopyableEmailRow", () => {
  it("is a real button named by the visible address, described as a copy", () => {
    render(<CopyableEmailRow email={email} />);

    const button = copyButton();
    expect(button.tagName).toBe("BUTTON");
    expect(button).toHaveAttribute("type", "button");
    expect(button).toHaveAccessibleDescription("Copiar e-mail");
    // The live region exists before anything happens, so the first outcome is
    // announced, and it says nothing until then.
    expect(screen.getByRole("status").textContent).toBe("");
  });

  it("copies exactly the visible address once and says so", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    stubClipboard({ writeText });
    render(<CopyableEmailRow email={email} />);

    fireEvent.click(copyButton());

    await screen.findByText(success);
    expect(screen.getByRole("status")).toHaveTextContent(success);
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText).toHaveBeenCalledWith(email);
    // The address stays on screen beside the feedback.
    expect(copyButton()).toBeInTheDocument();
  });

  it("reports a refused write without leaking the address or the error", async () => {
    const log = vi.spyOn(console, "error");
    stubClipboard({ writeText: vi.fn().mockRejectedValue(new Error(`denied for ${email}`)) });
    render(<CopyableEmailRow email={email} />);

    fireEvent.click(copyButton());

    await screen.findByText(failure);
    expect(screen.getByRole("status")).toHaveTextContent(failure);
    expect(screen.queryByText("E-mail copiado")).not.toBeInTheDocument();
    expect(screen.queryByText(/denied/)).not.toBeInTheDocument();
    expect(copyButton()).toBeInTheDocument();
    expect(log).not.toHaveBeenCalled();
  });

  it("treats a missing Clipboard API as a failure, never as a success", async () => {
    stubClipboard(undefined);
    render(<CopyableEmailRow email={email} />);

    fireEvent.click(copyButton());

    expect(await screen.findByText("Não foi possível copiar o e-mail")).toBeInTheDocument();
    expect(screen.queryByText("E-mail copiado")).not.toBeInTheDocument();
  });

  it("announces a repeated failure again, even when the API fails synchronously", async () => {
    // No Clipboard API: every attempt fails before any await, so a reset and
    // the failure land in the same event and React may batch them into
    // nothing. The second attempt must still put a new announcement in the
    // live region, not leave the first one's text untouched.
    stubClipboard(undefined);
    render(<CopyableEmailRow email={email} />);
    fireEvent.click(copyButton());
    await screen.findByText(failure);

    const added: string[] = [];
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) added.push(node.textContent ?? "");
      }
    });
    observer.observe(screen.getByRole("status"), {
      childList: true,
      subtree: true,
      characterData: true,
    });

    fireEvent.click(copyButton());

    await waitFor(() => expect(added).toContain(failure));
    observer.disconnect();
    expect(screen.getByRole("status")).toHaveTextContent(failure);
    expect(copyButton()).toBeInTheDocument();
  });

  it.each([
    { latest: "rejects", stale: "resolves", outcome: failure },
    { latest: "resolves", stale: "rejects", outcome: success },
  ])(
    "keeps the latest attempt's outcome when it $latest before an older one $stale",
    async ({ latest, stale, outcome }) => {
      const first = deferred();
      const second = deferred();
      stubClipboard({
        writeText: vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise),
      });
      render(<CopyableEmailRow email={email} />);

      fireEvent.click(copyButton());
      fireEvent.click(copyButton());
      // While the latest attempt is pending, no earlier outcome is claimed.
      expect(screen.getByRole("status").textContent).toBe("");

      await act(async () => (latest === "rejects" ? second.reject() : second.resolve()));
      expect(screen.getByRole("status")).toHaveTextContent(outcome);

      // The first click settles last; it describes an interaction the user
      // has already superseded.
      await act(async () => (stale === "rejects" ? first.reject() : first.resolve()));
      expect(screen.getByRole("status")).toHaveTextContent(outcome);
    },
  );
});
