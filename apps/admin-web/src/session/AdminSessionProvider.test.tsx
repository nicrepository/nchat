import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AdminBootstrap } from "../api/adminApi";
import AdminSessionProvider from "./AdminSessionProvider";
import { useAdminSession } from "./useAdminSession";

const BOOTSTRAP: AdminBootstrap = {
  identity: { user_id: "u1", email: "a@example.test", display_name: "Admin", avatar_url: "" },
  capabilities: ["admin.audit.read"],
  environment: "PRODUCTION",
  build: { service: "admin-service", version: "0.0.0", commit: "dev" },
  session: { idle_expires_at: "2026-08-20T12:00:00Z", absolute_expires_at: "2026-08-20T20:00:00Z" },
  csrf_token: "csrf-1",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function errorResponse(status: number) {
  return jsonResponse({ error: { code: "x", message: "x" } }, status);
}

function Probe() {
  const { status, can, signOut, reload } = useAdminSession();
  return (
    <div>
      <span data-testid="status">{status}</span>
      <span data-testid="audit">{String(can("admin.audit.read"))}</span>
      <span data-testid="users">{String(can("admin.users.manage"))}</span>
      <button type="button" onClick={() => void signOut()}>
        sair
      </button>
      <button type="button" onClick={reload}>
        recarregar
      </button>
    </div>
  );
}

function renderProbe() {
  return render(
    <AdminSessionProvider>
      <Probe />
    </AdminSessionProvider>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("AdminSessionProvider", () => {
  it("starts loading and becomes ready with the bootstrap payload", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ data: BOOTSTRAP })));
    renderProbe();

    expect(screen.getByTestId("status")).toHaveTextContent("loading");
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
    expect(screen.getByTestId("audit")).toHaveTextContent("true");
    expect(screen.getByTestId("users")).toHaveTextContent("false");
  });

  // The superuser grant is expanded on the client for display only; the server
  // does the same expansion for the decision that matters.
  it("treats the superuser grant as covering every section", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ data: { ...BOOTSTRAP, capabilities: ["admin.superuser"] } }),
        ),
    );
    renderProbe();

    await waitFor(() => expect(screen.getByTestId("users")).toHaveTextContent("true"));
  });

  it.each([
    [401, "unauthenticated"],
    [403, "forbidden"],
    [503, "unavailable"],
    [500, "error"],
    [418, "error"],
  ])("maps %i onto the %s state", async (status, expected) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(status)));
    renderProbe();

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent(expected));
  });

  it("treats a network failure as an error, never as a session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("offline")));
    renderProbe();

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("error"));
    expect(screen.getByTestId("audit")).toHaveTextContent("false");
  });

  it("retries on reload", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(errorResponse(500)))
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: BOOTSTRAP })));
    vi.stubGlobal("fetch", fetchMock);
    renderProbe();

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("error"));
    await userEvent.click(screen.getByRole("button", { name: "recarregar" }));
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
  });

  // Whatever the server answers, the browser must stop showing an
  // administrative session once the operator asked to leave it.
  it("drops the session locally even when the logout request fails", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: BOOTSTRAP })))
      .mockImplementationOnce(() => Promise.resolve(errorResponse(500)));
    vi.stubGlobal("fetch", fetchMock);
    renderProbe();

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
    await userEvent.click(screen.getByRole("button", { name: "sair" }));

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated"));
    expect(screen.getByTestId("audit")).toHaveTextContent("false");
  });

  it("signs out cleanly on success", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: BOOTSTRAP })))
      .mockImplementationOnce(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal("fetch", fetchMock);
    renderProbe();

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
    await userEvent.click(screen.getByRole("button", { name: "sair" }));
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated"));
  });
});

describe("useAdminSession", () => {
  it("refuses to be used outside the provider", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() => render(<Probe />)).toThrow(/AdminSessionProvider/);
    consoleError.mockRestore();
  });
});

describe("unexpected failures", () => {
  // A rejection that is not an AdminApiError at all — a bug in the client, say —
  // must still land in `error`, never in `ready`.
  it("never becomes ready on an unclassifiable failure", async () => {
    vi.stubGlobal("fetch", () => {
      throw new RangeError("something else entirely");
    });
    renderProbe();

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("error"));
  });
});

// ── Stale asynchronous results ─────────────────────────────────────────────
//
// The provider starts a bootstrap on mount while the single sign-on return can
// finish its own exchange and call adopt(). The two overlap, so the older
// result can land last. These are deterministic: the bootstrap promise is held
// open by hand and settled at the exact moment each scenario needs.

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/**
 * Renders the provider with a bootstrap request the test settles by hand.
 *
 * The session callbacks are driven through buttons rather than captured during
 * render: reassigning an outer variable while rendering is a side effect, and
 * the point of these tests is ordering, which a click expresses exactly.
 */
function renderWithPendingBootstrap(adopted: AdminBootstrap = SSO_BOOTSTRAP) {
  const pending = deferred<Response>();
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((url: string) => {
      if (url.startsWith("/api/admin/bootstrap")) return pending.promise;
      return Promise.resolve(new Response(null, { status: 204 }));
    }),
  );

  function Controls() {
    const session = useAdminSession();
    return (
      <div>
        <span data-testid="status">{session.status}</span>
        <span data-testid="email">{session.bootstrap?.identity.email ?? "none"}</span>
        <button type="button" onClick={() => session.adopt(adopted)}>
          adotar
        </button>
        <button type="button" onClick={() => void session.signOut()}>
          sair
        </button>
      </div>
    );
  }

  render(
    <AdminSessionProvider>
      <Controls />
    </AdminSessionProvider>,
  );
  return {
    pending,
    adopt: () => fireEvent.click(screen.getByRole("button", { name: "adotar" })),
    signOut: () => fireEvent.click(screen.getByRole("button", { name: "sair" })),
  };
}

const SSO_BOOTSTRAP: AdminBootstrap = {
  ...BOOTSTRAP,
  identity: { ...BOOTSTRAP.identity, email: "sso@example.test" },
};

describe("stale bootstrap results", () => {
  // The race that motivated the generation counter: the mount-time request was
  // correct to get a 401 — there was no session when it was sent — but by the
  // time it lands the SSO callback has created one.
  it("a 401 from before adopt() must not unauthenticate a live session", async () => {
    const { pending, adopt } = renderWithPendingBootstrap();
    expect(screen.getByTestId("status")).toHaveTextContent("loading");

    adopt();
    expect(screen.getByTestId("status")).toHaveTextContent("ready");

    await act(async () => {
      pending.resolve(errorResponse(401));
      await Promise.resolve();
    });

    expect(screen.getByTestId("status")).toHaveTextContent("ready");
    expect(screen.getByTestId("status")).not.toHaveTextContent("unauthenticated");
    expect(screen.getByTestId("email")).toHaveTextContent("sso@example.test");
  });

  // Same ordering, but the stale result succeeds. An older 200 is still older:
  // it must not replace the session adopt() established.
  it("a 200 from before adopt() must not replace the newer session", async () => {
    const { pending, adopt } = renderWithPendingBootstrap();

    adopt();

    await act(async () => {
      pending.resolve(jsonResponse({ data: BOOTSTRAP }));
      await Promise.resolve();
    });

    expect(screen.getByTestId("status")).toHaveTextContent("ready");
    expect(screen.getByTestId("email")).toHaveTextContent("sso@example.test");
  });

  // The inverse direction: a request in flight when the operator signs out must
  // not bring the session back.
  it("a 200 arriving after signOut() must not resurrect the session", async () => {
    const { pending, signOut } = renderWithPendingBootstrap();

    signOut();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated"));

    await act(async () => {
      pending.resolve(jsonResponse({ data: BOOTSTRAP }));
      await Promise.resolve();
    });

    expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated");
    expect(screen.getByTestId("email")).toHaveTextContent("none");
  });

  // With nothing racing it, the ordinary paths are untouched.
  it("keeps the uncontended bootstrap paths working", async () => {
    const ready = renderWithPendingBootstrap();
    await act(async () => {
      ready.pending.resolve(jsonResponse({ data: BOOTSTRAP }));
      await Promise.resolve();
    });
    expect(screen.getByTestId("status")).toHaveTextContent("ready");
    expect(screen.getByTestId("email")).toHaveTextContent("a@example.test");

    cleanup();

    const refused = renderWithPendingBootstrap();
    await act(async () => {
      refused.pending.resolve(errorResponse(401));
      await Promise.resolve();
    });
    expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated");
  });
});
