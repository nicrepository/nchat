import { act, cleanup, render, screen } from "@testing-library/react";
import { StrictMode, useLayoutEffect, useState, type ReactNode } from "react";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as auth from "../lib/authSession";
import { useSessionOwner } from "./useSessionOwner";

let setIdentity: (identity: string | null) => void;
let renders: number;

function OwnedState() {
  const [generation] = useState(auth.getSessionGeneration);
  return <span data-testid="owned-generation">{generation}</span>;
}

function Probe() {
  const session = useSessionOwner();
  useLayoutEffect(() => {
    renders += 1;
  });
  return (
    <>
      <output data-testid="owner" data-owner={session.owner ?? ""} data-settled={session.settled}>
        {session.sessionKey}
      </output>
      {session.owner !== null && (
        <div hidden={!session.settled}>
          <OwnedState key={`${session.sessionKey}:${session.owner}`} />
        </div>
      )}
    </>
  );
}

function Shell() {
  const [sessionIdentity, set] = useState<string | null>("u1:w1");
  useLayoutEffect(() => {
    setIdentity = set;
  }, []);
  return <Outlet context={{ sessionIdentity }} />;
}

function LayoutEvent({ children, event }: { children: ReactNode; event?: () => void }) {
  // Descendants have rendered; their passive subscriptions are not installed yet.
  useLayoutEffect(() => {
    event?.();
  }, [event]);
  return children;
}

function mount(event?: () => void, strict = false) {
  const content = (
    <MemoryRouter>
      <LayoutEvent event={event}>
        <Routes>
          <Route element={<Shell />}>
            <Route path="/" element={<Probe />} />
          </Route>
        </Routes>
      </LayoutEvent>
    </MemoryRouter>
  );
  return render(strict ? <StrictMode>{content}</StrictMode> : content);
}

const key = () => Number(screen.getByTestId("owner").textContent);
const ownedGeneration = () => Number(screen.getByTestId("owned-generation").textContent);

beforeEach(() => {
  auth.setTokens("u1-original");
  renders = 0;
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  auth.clearTokens();
});

describe("useSessionOwner: render-to-subscription race (R3)", () => {
  it("resynchronizes a session replacement before the subscription, even without a context render", () => {
    mount(() => auth.setTokens("replacement"));
    expect(key()).toBe(auth.getSessionGeneration());
    expect(renders).toBeLessThan(8);
  });

  it("logout and login of U2 in the gap cannot keep U1's session key or mounted state", () => {
    mount(() => {
      auth.clearTokens();
      auth.setTokens("u2");
      setIdentity(null);
    });
    expect(screen.getByTestId("owner")).toHaveAttribute("data-settled", "false");
    expect(screen.getByTestId("owned-generation")).not.toBeVisible();
    act(() => setIdentity("u2:w1"));
    expect(key()).toBe(auth.getSessionGeneration());
    expect(screen.getByTestId("owner")).toHaveAttribute("data-owner", "u2:w1");
    expect(ownedGeneration()).toBe(auth.getSessionGeneration());
  });

  it("a new login of U1 in the gap invalidates state even though the identity string is unchanged", () => {
    mount(() => {
      auth.setTokens("u1-new-login");
      setIdentity(null);
    });
    act(() => setIdentity("u1:w1"));
    expect(key()).toBe(auth.getSessionGeneration());
    expect(ownedGeneration()).toBe(auth.getSessionGeneration());
  });

  it("a refresh in the gap preserves the session key and its mounted state", () => {
    const original = auth.getSessionGeneration();
    mount(() => auth.setTokens("rotated", "refresh"));
    expect(auth.getSessionGeneration()).toBeGreaterThan(original);
    expect(key()).toBe(original);
    expect(ownedGeneration()).toBe(original);
    act(() => auth.setTokens("rotated-again", "refresh"));
    expect(key()).toBe(original);
    expect(ownedGeneration()).toBe(original);
  });

  it("a replacement followed by refresh in the gap must not disguise the new session", () => {
    let replacement = -1;
    mount(() => {
      auth.setTokens("u1-new-login");
      replacement = auth.getSessionGeneration();
      auth.setTokens("rotated", "refresh");
    });
    expect(key()).toBe(replacement);
  });

  it("separates users and workspaces while pending and on direct confirmation", () => {
    mount();
    act(() => setIdentity(null));
    expect(screen.getByTestId("owner")).toHaveAttribute("data-settled", "false");
    act(() => setIdentity("u1:w2"));
    expect(screen.getByTestId("owner")).toHaveAttribute("data-owner", "u1:w2");
    act(() => setIdentity("u2:w2"));
    expect(screen.getByTestId("owner")).toHaveAttribute("data-owner", "u2:w2");
  });

  it("StrictMode cleans subscriptions and remount reads the current session", () => {
    const onAuthChange = auth.onAuthChange;
    let active = 0;
    vi.spyOn(auth, "onAuthChange").mockImplementation((listener) => {
      const unsubscribe = onAuthChange(listener);
      active += 1;
      return () => {
        active -= 1;
        unsubscribe();
      };
    });
    const first = mount(undefined, true);
    expect(active).toBe(1);
    act(() => auth.setTokens("strict-new-session"));
    expect(key()).toBe(auth.getSessionGeneration());
    first.unmount();
    expect(active).toBe(0);
    const before = renders;
    act(() => auth.setTokens("while-unmounted"));
    expect(renders).toBe(before);
    const second = mount(() => auth.setTokens("strict-layout-session"), true);
    expect(active).toBe(1);
    expect(key()).toBe(auth.getSessionGeneration());
    expect(renders - before).toBeLessThan(8);
    second.unmount();
    expect(active).toBe(0);
  });

  it("a delayed notification of the active subscription reads the latest snapshot", () => {
    const subscriptions = vi.spyOn(auth, "onAuthChange");
    mount();
    const notify = subscriptions.mock.calls.at(-1)![0];
    act(() => {
      auth.setTokens("u2-new-session");
      setIdentity("u2:w2");
    });
    const current = key();
    const before = renders;
    act(() => {
      notify("session");
    });
    expect(key()).toBe(current);
    expect(ownedGeneration()).toBe(current);
    expect(screen.getByTestId("owner")).toHaveAttribute("data-owner", "u2:w2");
    expect(renders).toBe(before);
  });

  it("a late old notification cannot restore an older session or mounted state", () => {
    const subscriptions = vi.spyOn(auth, "onAuthChange");
    const first = mount();
    const oldListener = subscriptions.mock.calls.at(-1)![0];
    first.unmount();
    auth.setTokens("u2-session");
    mount();
    act(() => setIdentity("u2:w1"));
    const current = key();
    act(() => {
      oldListener("session");
      oldListener("refresh");
    });
    expect(key()).toBe(current);
    expect(screen.getByTestId("owner")).toHaveAttribute("data-owner", "u2:w1");
    expect(ownedGeneration()).toBe(current);
  });
});
