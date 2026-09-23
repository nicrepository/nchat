import { act } from "@testing-library/react";
import { createRoot, type Root } from "react-dom/client";
import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import RemoveMemberDialog from "./RemoveMemberDialog";

/**
 * RemoveMemberDialog's own lifecycle (issue #469, CQ-3).
 *
 * The dialog is mounted for real here — portal, buttons, confirmation — rather
 * than exercised through the hook that opens it, because the defect is the
 * dialog's: a request it started can settle after the commit that removed it,
 * and what it does then is its own business, not the flow's.
 *
 * Mounted against a real root instead of Testing Library's `render`, so the
 * unmount is a commit this test performed and the request settles after it,
 * in that order, with no timer and nothing to tune.
 *
 * These lock the contract for all six timelines, and the two "still on screen"
 * cases are what stops the unmounted ones from being satisfied by silencing
 * every asynchronous outcome — removing the error handling makes five tests
 * fail, here and in the dialog's own suite.
 *
 * What they do not prove, and the reason the component does not rely on them
 * for it: that the mounted flag is invalidated in the layout phase rather than
 * the passive one. Measured in this environment (React 19.2 + jsdom), both
 * post-unmount effects are already inert whichever phase clears the flag — a
 * state update on an unmounted component is a silent no-op, and React detaches
 * host refs during the deletion's mutation phase, so `cancelRef.current` is
 * null before any cleanup runs and `focus()` never happens. The layout cleanup
 * is what makes the invariant hold without depending on either of those two
 * React internals.
 */

const member = { userId: "u-2", displayName: "Fernanda Nicácio" };

interface Mounted {
  root: Root;
  container: HTMLDivElement;
  onClose: ReturnType<typeof vi.fn>;
  resolve: () => void;
  reject: (error: unknown) => void;
  confirmed: () => Promise<void>;
}

let mounted: Mounted | null = null;

/** Mounts the dialog with a confirmation that stays pending until settled. */
function mountDialog(options: { strict?: boolean } = {}): Mounted {
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const onClose = vi.fn();
  let settle: (() => void) | undefined;
  let fail: ((error: unknown) => void) | undefined;
  let started: Promise<void> | undefined;
  const onConfirm = vi.fn(() => {
    started = new Promise<void>((resolveConfirm, rejectConfirm) => {
      settle = () => resolveConfirm();
      fail = (error: unknown) => rejectConfirm(error);
    });
    return started;
  });

  const tree = (
    <RemoveMemberDialog
      kind="channel"
      conversationName="Infraestrutura"
      member={member}
      onClose={onClose}
      onConfirm={onConfirm}
    />
  );
  act(() => {
    root.render(options.strict ? <StrictMode>{tree}</StrictMode> : tree);
  });

  const state: Mounted = {
    root,
    container,
    onClose,
    resolve: () => settle?.(),
    reject: (error: unknown) => fail?.(error),
    confirmed: () => {
      if (!started) throw new Error("the confirmation never started");
      return started;
    },
  };
  mounted = state;
  return state;
}

function confirmButton(): HTMLButtonElement {
  const button = [...document.querySelectorAll("button")].find(
    (candidate) => candidate.textContent === "Remover membro",
  );
  if (!button) throw new Error("the dialog is not showing its confirm action");
  return button as HTMLButtonElement;
}

function startConfirmation(): void {
  act(() => {
    confirmButton().click();
  });
}

/** What a dialog that is still alive would put on screen. */
function alertText(): string | null {
  return document.querySelector('[role="alert"]')?.textContent ?? null;
}

afterEach(() => {
  if (mounted) {
    act(() => mounted?.root.unmount());
    mounted.container.remove();
    mounted = null;
  }
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("RemoveMemberDialog lifecycle", () => {
  // Case 4 — the scenario the review named: the request is refused after the
  // dialog is gone. Nobody is there to be told, and the reader's focus belongs
  // to whatever they moved on to.
  it("says nothing and takes no focus when a refusal lands after unmount", async () => {
    const dialog = mountDialog();
    startConfirmation();
    const elsewhere = document.createElement("button");
    document.body.append(elsewhere);
    elsewhere.focus();

    act(() => dialog.root.unmount());
    dialog.reject(new ApiRequestError(403, "forbidden", "forbidden"));
    await act(async () => {
      await dialog.confirmed().catch(() => undefined);
    });

    expect(alertText()).toBeNull();
    expect(document.body).not.toHaveTextContent(/não tem permissão/i);
    expect(document.activeElement).toBe(elsewhere);
    expect(dialog.onClose).not.toHaveBeenCalled();
    mounted = null;
    dialog.container.remove();
  });

  // Case 3 — the write succeeded and the dialog is gone. The removal stands;
  // the dialog simply has nothing left to do.
  it("does nothing local when a success lands after unmount", async () => {
    const dialog = mountDialog();
    startConfirmation();
    const elsewhere = document.createElement("button");
    document.body.append(elsewhere);
    elsewhere.focus();

    act(() => dialog.root.unmount());
    dialog.resolve();
    await act(async () => {
      await dialog.confirmed();
    });

    expect(alertText()).toBeNull();
    expect(document.activeElement).toBe(elsewhere);
    // Closing is the caller's decision, and there is no caller left to ask.
    expect(dialog.onClose).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    mounted = null;
    dialog.container.remove();
  });

  // Case 2 — the control: still mounted, so a refusal is reported exactly as
  // before and the dialog stays usable. A fix that silenced every async
  // outcome would fail here.
  it("still reports a refusal while it is on screen", async () => {
    const dialog = mountDialog();
    startConfirmation();

    dialog.reject(new ApiRequestError(403, "forbidden", "forbidden"));
    await act(async () => {
      await dialog.confirmed().catch(() => undefined);
    });

    expect(alertText()).toBe("Você não tem permissão para remover esta pessoa.");
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    // Out of pending: both actions are usable again, and focus is on the safe
    // one so the refusal is announced where the reader already is.
    expect(confirmButton()).not.toBeDisabled();
    expect(document.activeElement?.textContent).toBe("Cancelar");
  });

  // Case 1 — the control for success: no error, still pending-free, and the
  // dialog leaves closing to its caller.
  it("still finishes cleanly when a success lands while it is on screen", async () => {
    const dialog = mountDialog();
    startConfirmation();

    dialog.resolve();
    await act(async () => {
      await dialog.confirmed();
    });

    expect(alertText()).toBeNull();
    expect(confirmButton()).not.toBeDisabled();
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  });

  // Case 6 — StrictMode runs setup → cleanup → setup. The instance that is
  // really mounted must end up alive, or a refusal would go unreported.
  it("survives StrictMode's double invocation", async () => {
    const dialog = mountDialog({ strict: true });
    startConfirmation();

    dialog.reject(new ApiRequestError(0, "network", "offline"));
    await act(async () => {
      await dialog.confirmed().catch(() => undefined);
    });

    expect(alertText()).toBe("Sem conexão. Verifique sua rede e tente novamente.");
  });
});
