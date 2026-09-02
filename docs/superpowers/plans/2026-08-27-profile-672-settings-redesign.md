# Profile & Account Settings Redesign (Issue #672) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the standalone `/profile` form with a four-section account/settings area (`/profile`, `/profile/notifications`, `/profile/security`, `/profile/sessions`) that shares the NChat sidebar and shell chrome with `/chat`, without duplicating `ChatSidebar` or restarting the WebSocket/call session when navigating between the two.

**Architecture:** Extract the chrome `ChatShell` currently owns (nav toggle, `ChatSidebar`, drawer backdrop, details panel, root scroll lock) into a new `AppShell` component mounted once as a parent layout route above both `/chat/*` and `/profile/*`. `ChatShell` shrinks to just its call-session outlet-context wiring; a new `ProfileSettingsShell` renders the four-tab nav and an `<Outlet/>` for the section pages. Sessions data comes from the auth-service endpoints that already exist (`/auth/me/sessions`); Security is a static Keycloak-redirect page (no local MFA); job_title stays user-editable (documented exception, contract already allows it); working hours and per-channel/email-digest notification prefs are omitted (no backend). This is a **frontend-only** change — no Go/backend files are modified.

**Tech Stack:** React 19 + TypeScript, react-router ^8.3.0 (`Outlet`/`useOutletContext`), Vitest + Testing Library, existing hand-rolled dialog/portal pattern (no dialog/tabs library). Frontend scripts (from `apps/web/package.json`): `pnpm --filter web test`, `test:coverage`, `test:e2e`, `lint`, `typecheck`, `build`.

**Spec:** GitHub issue #672 (full text fetched into `/c/Users/login/AppData/Local/Temp/claude/C--Users-login-OneDrive-Documentos-nchat/71212daa-321f-407e-bac1-032c5c5fdde0/scratchpad/issue672.md` during planning — re-fetch with `gh issue view 672 --json body -q .body` if that scratchpad file is gone). Executors should read the issue in full before their task, not just this plan.

## Global Constraints

- No backend/Go changes. Sessions already has real endpoints (`GET/DELETE /auth/me/sessions`, `DELETE /auth/me/sessions`); `PATCH /auth/me` already accepts `job_title` — do not add a backend "make it read-only" change without a product decision, which this plan does not have.
- Never build UI for a preference with no backend persistence (per-channel notifications, email digest, working hours, MFA status, security activity log). Show honest absence, not placeholders.
- Never implement password/MFA/WebAuthn/recovery-codes locally. Keycloak is the sole authority; only a redirect link to the account console.
- Never build a `beforeunload`/`unload`-based security mechanism.
- Identity comes from the session (JWT) on every request — never trust a client-supplied `user_id`.
- Frontend coverage must stay ≥90% lines/functions/branches/statements (`pnpm test:coverage` / `make web-coverage`).
- Conventional Commits; branch `feature/profile-672-settings-redesign` already checked out; PR must say `Closes #672`.
- Follow the codebase's established dialog convention: each dialog re-implements the small portal/focus-trap/`submittingRef` shell (see `RenameChannelDialog.tsx`, `LeaveConversationDialog.tsx` — their own comments say this is deliberate, "rather than introducing a third modal system"). Do not invent a shared `Dialog` abstraction.
- CSS follows the existing BEM-ish convention (`block__element--modifier`) already used in `ChatSidebar.css` / `ProfilePage.css`.

---

## Architecture note (read before Task 1)

Today: `/chat` is a layout route rendering `ChatShell`, which itself mounts `useChatSidebar()` (owns the WebSocket subscription + channel/DM/unread state) and renders `ChatNavBar` + `ChatSidebar` + backdrop + `<main><Outlet/></main>` + `SidebarDetailsPanel`. `/profile` is a **sibling** top-level route rendering the standalone `ProfilePage` — no sidebar at all.

If `ProfileSettingsShell` mounted its **own** `useChatSidebar()` call, navigating `/chat` → `/profile` would unmount `ChatShell`'s hook instance and mount a new one in `ProfileSettingsShell`, and vice versa. `useChatSidebar` calls `useChatWebSocket`, which subscribes "interest" to a **shared, module-level** socket connection (issue #449 fixed multiple consumers each opening their own connection) — so the underlying socket itself would survive, but the sidebar's own `useReducer` state (channels/DMs/unread/pins) would be thrown away and re-fetched from scratch on every chat↔profile navigation. That is exactly the "teardown/restart" and lost-scroll-position regression the issue prohibits.

The fix: mount `useChatSidebar()` **once**, in a component that is a **common route ancestor** of both `/chat/*` and `/profile/*`, so it never unmounts when switching between them. Concretely: extract `AppShell` from `ChatShell`, make it a layout `<Route>` that wraps both the `/chat` and `/profile` route subtrees, and have it pass the sidebar state down through `<Outlet context={...}>` for `ChatShell` (now a thin child) to read and merge with call-session data. `ProfileSettingsShell` doesn't need that context at all — it reads identity via `useSelfProfile()` like the sidebar itself does.

Route tree, before → after:

```text
BEFORE
<Route element={<RequireAuth><CallSessionProvider/></RequireAuth>}>
  <Route path="/call/:callId" element={<DedicatedCallPage/>} />
  <Route path="/chat" element={<ChatShell/>}>            <- owns useChatSidebar + all chrome
    <Route index .../> <Route path="channel/:id" .../> ...
  </Route>
  <Route path="/profile" element={<ProfilePage/>} />      <- no sidebar, standalone
  <Route path="/admin/users" .../> ...
</Route>

AFTER
<Route element={<RequireAuth><CallSessionProvider/></RequireAuth>}>
  <Route path="/call/:callId" element={<DedicatedCallPage/>} />
  <Route element={<AppShell/>}>                           <- owns useChatSidebar + all chrome, NEW
    <Route path="/chat" element={<ChatShell/>}>            <- thin: call-session wiring only
      <Route index .../> <Route path="channel/:id" .../> ...
    </Route>
    <Route path="/profile" element={<ProfileSettingsShell/>}>
      <Route index element={<ProfileOverviewPage/>} />
      <Route path="notifications" element={<Suspense><NotificationsSettingsPage/></Suspense>} />
      <Route path="security" element={<Suspense><SecuritySettingsPage/></Suspense>} />
      <Route path="sessions" element={<Suspense><SessionsSettingsPage/></Suspense>} />
    </Route>
  </Route>
  <Route path="/admin/users" .../> ...                     <- unchanged, still outside AppShell
</Route>
```

---

### Task 1: Extract `AppShell` from `ChatShell`

**Files:**

- Create: `apps/web/src/chat/AppShell.tsx`
- Rename: `apps/web/src/chat/ChatShell.css` → `apps/web/src/chat/AppShell.css` (content unchanged in this step)
- Modify: `apps/web/src/chat/ChatShell.tsx` (shrink drastically)
- Test: `apps/web/src/chat/AppShell.test.tsx` (new)
- Modify: `apps/web/src/chat/ChatShell.test.tsx`, `apps/web/src/chat/ChatShellDetailsFocus.test.tsx` (update router wrapper)

**Interfaces:**

- Produces: `export default function AppShell(): JSX.Element` — a layout route element, renders `<Outlet context={sidebarContext} />` inside `<main>`.
- Produces: `export type AppShellOutletContext = ReturnType<typeof useChatSidebar>` (i.e. `{ state, retry, setPinned, markRead, renameChannel, renameGroup, setMuted, leaveConversation }`) — exported so `ChatShell` can type its `useOutletContext<AppShellOutletContext>()` call.
- Produces: keeps exporting `ROOT_LOCK_CLASS` (moved here from `ChatShell.tsx`; `ChatShell.test.tsx` imports it today — update that import path).
- Consumes: `useChatSidebar`, `useNavDrawer`, `ChatSidebar`, `SidebarDetailsPanel`, all already existing.

- [ ] **Step 1: Read the two test files that will need updating, before touching anything**

Read `apps/web/src/chat/ChatShell.test.tsx` and `apps/web/src/chat/ChatShellDetailsFocus.test.tsx` in full. Note exactly how they render `<ChatShell/>` (what router wrapper, what routes, what they assert on `data-testid="chat-shell"` / `data-nav-open` / `data-details-open`). These attributes are moving to `AppShell`'s root div — the tests must render `AppShell` as the route ancestor of `ChatShell`, e.g.:

```tsx
<MemoryRouter initialEntries={["/chat"]}>
  <Routes>
    <Route element={<AppShell />}>
      <Route path="/chat" element={<ChatShell />}>
        <Route index element={<div>placeholder</div>} />
      </Route>
    </Route>
  </Routes>
</MemoryRouter>
```

Write down the diff needed; apply it in Step 6 below (after the split exists), not now.

- [ ] **Step 2: Create `AppShell.tsx` by moving (not copying) the chrome out of `ChatShell.tsx`**

Move these from `ChatShell.tsx` into the new file, verbatim, unless noted:

- `readySidebar` helper — **stays in `ChatShell.tsx`** (only `ChatShell`'s outlet-context construction needs `currentUserId`/`channels`/`dms`/`attachmentLimits` in that exact shape; `AppShell` only needs the raw `state` to hand to `ChatSidebar` and to the outlet context).
- `resolveDetailsTarget`, `detailsTargetExists`, `restoreDetailsFocus`, `dataFlag`, `ChatNavBar`, `useRootScrollLock`, `ROOT_LOCK_CLASS` — move as-is.
- `handleShellKeyDown` — move as-is (Escape-closes-drawer is chrome, not chat logic).

New `AppShell.tsx`:

```tsx
import { useCallback, useRef, useState, type KeyboardEvent } from "react";
import { Outlet, useLocation } from "react-router";

import "./AppShell.css";
import ChatSidebar, { chatNavigationId } from "./ChatSidebar";
import { useNavDrawer } from "./useNavDrawer";
import SidebarDetailsPanel, { type SidebarDetailsTarget } from "./SidebarDetailsPanel";
import type { Channel, DMConversation } from "./chatTypes";
import { useChatSidebar } from "./useChatSidebar";

// [move resolveDetailsTarget, detailsTargetExists, restoreDetailsFocus, dataFlag,
//  ChatNavBar, useRootScrollLock, ROOT_LOCK_CLASS here verbatim from ChatShell.tsx]

export type AppShellOutletContext = ReturnType<typeof useChatSidebar>;

/** "/profile" or "/profile/..." gets the settings label; everything else (today, only "/chat/...") gets the chat one. */
function mainAriaLabel(pathname: string): string {
  return pathname === "/profile" || pathname.startsWith("/profile/")
    ? "Configurações da conta"
    : "Área de mensagens";
}

export default function AppShell() {
  useRootScrollLock();
  const sidebar = useChatSidebar();
  const {
    state,
    retry,
    setPinned,
    markRead,
    renameChannel,
    renameGroup,
    setMuted,
    leaveConversation,
  } = sidebar;
  const { pathname } = useLocation();

  const [sidebarDetails, setSidebarDetails] = useState<
    (SidebarDetailsTarget & { pathname: string }) | null
  >(null);
  const dms = state.status === "ready" ? state.dms : [];
  const openDetailsTarget =
    sidebarDetails?.pathname === pathname &&
    detailsTargetExists(sidebarDetails, {
      channels: state.status === "ready" ? state.channels : [],
      dms,
    })
      ? sidebarDetails
      : null;

  const {
    open: navOpen,
    modal: navModal,
    toggle: toggleNav,
    close: setNavClosed,
  } = useNavDrawer(pathname);
  const navToggleRef = useRef<HTMLButtonElement>(null);
  const closeNav = useCallback(() => {
    setNavClosed();
    navToggleRef.current?.focus();
  }, [setNavClosed]);
  const detailsOpenerRef = useRef<HTMLElement | null>(null);
  const openSidebarDetails = useCallback(
    (kind: "channel" | "dm", targetId: string, opener: HTMLElement | null) => {
      const resolved = resolveDetailsTarget(kind, targetId, dms);
      if (!resolved) return;
      detailsOpenerRef.current = opener;
      setSidebarDetails({ ...resolved, pathname });
      closeNav();
    },
    [dms, pathname, closeNav],
  );
  const closeSidebarDetails = useCallback(() => {
    setSidebarDetails(null);
    restoreDetailsFocus(detailsOpenerRef.current, navToggleRef.current);
    detailsOpenerRef.current = null;
  }, []);

  function handleShellKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (!navModal || event.key !== "Escape") return;
    if (!event.currentTarget.contains(event.target as Node)) return;
    closeNav();
  }

  return (
    <div
      className="chat-app"
      data-testid="chat-shell"
      data-nav-open={dataFlag(navOpen)}
      data-details-open={dataFlag(openDetailsTarget !== null)}
      onKeyDown={handleShellKeyDown}
    >
      <ChatNavBar open={navOpen} onToggle={toggleNav} toggleRef={navToggleRef} />
      <ChatSidebar
        state={state}
        retry={retry}
        setPinned={setPinned}
        markRead={markRead}
        renameChannel={renameChannel}
        renameGroup={renameGroup}
        setMuted={setMuted}
        leaveConversation={leaveConversation}
        onOpenDetails={openSidebarDetails}
      />
      {navModal && (
        <button
          type="button"
          className="chat-app__nav-backdrop"
          aria-label="Fechar a navegação"
          onClick={closeNav}
          data-testid="chat-nav-backdrop"
        />
      )}
      <main className="chat-app__main" aria-label={mainAriaLabel(pathname)} inert={navModal}>
        <Outlet context={sidebar} />
      </main>
      <SidebarDetailsPanel
        target={openDetailsTarget}
        currentUserId={state.status === "ready" ? state.currentUserId : ""}
        onClose={closeSidebarDetails}
      />
    </div>
  );
}
```

Note `data-testid="chat-shell"` is kept unchanged (existing tests and any e2e selectors target it) even though the component is now `AppShell` — renaming the test id is unnecessary churn.

`git mv apps/web/src/chat/ChatShell.css apps/web/src/chat/AppShell.css` (content unchanged for this step; content is still exactly what `ChatShell.css` had).

- [ ] **Step 3: Shrink `ChatShell.tsx` to call-session wiring only**

```tsx
import { useCallback, useEffect } from "react";
import { Outlet, useOutletContext } from "react-router";

import { useCallSession } from "../calls/CallSessionProvider";
import type { ParticipantMedia } from "./useCallMedia";
import type { AppShellOutletContext } from "./AppShell";
import type { ResourceCallKind } from "./callApi";
import type { Call, CallType } from "./callState";
import type { ResourceCallTarget } from "./useResourceCallSession";
import type { Channel, DMConversation } from "./chatTypes";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import type { SidebarState } from "./useChatSidebar";

// readySidebar, ActiveResourceCallSession, ActiveDirectCallSession, ChatOutletContext:
// UNCHANGED, keep exactly as they are today.

export default function ChatShell() {
  const { state, retry } = useOutletContext<AppShellOutletContext>();
  const ready = readySidebar(state);
  const {
    calls,
    resource: resourceCall,
    joinResourceParticipation,
    registerDirectory,
    registerIdentity,
    getResourceCall,
    media,
    expand,
    leaveResourceParticipation,
    localIdentity,
    resourcePresentationCall,
    directPresentationCall,
  } = useCallSession();
  useEffect(() => registerIdentity(state.status, retry), [registerIdentity, retry, state.status]);
  useEffect(() => {
    if (state.status === "ready") {
      registerDirectory({
        currentUserId: state.currentUserId,
        channels: state.channels,
        dms: state.dms,
      });
    }
  }, [registerDirectory, state]);

  // directCallBusy, activeResourceTarget, isParticipatingIn, resourceCallSession,
  // directCallSession, outletContext: UNCHANGED, keep exactly as they are today
  // (they only read `ready`, `state`, and the useCallSession() destructure above —
  // none of them reference navOpen/navModal/sidebarDetails, which no longer exist here).

  return <Outlet context={outletContext} />;
}
```

Delete from `ChatShell.tsx`: the `useRootScrollLock`/`ROOT_LOCK_CLASS`/`dataFlag`/`ChatNavBar`/`resolveDetailsTarget`/`detailsTargetExists`/`restoreDetailsFocus` definitions (now in `AppShell.tsx`), the `useNavDrawer`/`sidebarDetails`/`openSidebarDetails`/`closeSidebarDetails`/`handleShellKeyDown` state and handlers, the `import "./ChatShell.css"` line, and the entire returned JSX tree except the final `<Outlet context={outletContext} />`.

- [ ] **Step 4: Update `App.tsx`'s route tree**

Change the `/chat` route from a top-level child of the `RequireAuth`/`CallSessionProvider` route to being nested one level deeper under a new `AppShell` layout route, per the "Route tree, before → after" diagram above. `/profile` becomes `<ProfileSettingsShell/>` in this same step's route tree shape, but leave its `element` as the still-existing `ProfilePage` for now — Task 2 swaps it to `ProfileSettingsShell`. Import `AppShell` from `./chat/AppShell`.

- [ ] **Step 5: `pnpm typecheck` in `apps/web`**

Run: `pnpm --filter web typecheck` (or `cd apps/web && pnpm typecheck`, whichever the repo's `package.json` scripts define — check `apps/web/package.json` for the exact script name first).
Expected: no errors related to `AppShell`/`ChatShell`/`App.tsx`. Fix any import path issues before continuing.

- [ ] **Step 6: Apply the test-file diff planned in Step 1**

Update `ChatShell.test.tsx` and `ChatShellDetailsFocus.test.tsx` to wrap `<ChatShell/>` inside a parent `<Route element={<AppShell/>}>` as sketched in Step 1. Anything asserting `data-nav-open`/`data-details-open`/`data-testid="chat-shell"`/nav toggle/backdrop/details-panel behavior now targets `AppShell`'s rendered output (which is still what's on screen — the assertions themselves should not need to change, only the render setup).

- [ ] **Step 7: Run the chat test suite**

Run: `pnpm --filter web test -- ChatShell AppShell ChatSidebar ChatShellDetailsFocus`
Expected: all pass. `AppShell.test.tsx` doesn't exist with real content yet (Task 1 doesn't require new coverage beyond what moved with the code — the moved code's existing test coverage via `ChatShell.test.tsx` now exercises `AppShell` through the same assertions). If coverage tooling flags `AppShell.tsx` as under-covered because no test file targets it directly, add `apps/web/src/chat/AppShell.test.tsx` with a minimal smoke test asserting it renders `ChatSidebar` + `<main>` + honors `data-nav-open`, reusing fixtures from `ChatShell.test.tsx`.

- [ ] **Step 8: Commit**

```bash
git add apps/web/src/chat/AppShell.tsx apps/web/src/chat/AppShell.css apps/web/src/chat/ChatShell.tsx apps/web/src/chat/ChatShell.test.tsx apps/web/src/chat/ChatShellDetailsFocus.test.tsx apps/web/src/App.tsx
git rm apps/web/src/chat/ChatShell.css
git commit -m "refactor(chat): extract AppShell from ChatShell for shared shell reuse"
```

---

### Task 2: Route restructure + `ProfileSettingsShell` + `ProfileTabs`

**Files:**

- Create: `apps/web/src/profile/ProfileSettingsShell.tsx`
- Create: `apps/web/src/profile/ProfileTabs.tsx`, `apps/web/src/profile/ProfileTabs.css`
- Create: `apps/web/src/profile/ProfileSettingsShell.css`
- Modify: `apps/web/src/App.tsx`
- Test: `apps/web/src/profile/ProfileTabs.test.tsx`, `apps/web/src/profile/ProfileSettingsShell.test.tsx`

**Interfaces:**

- Consumes: `AppShellOutletContext` is available via `useOutletContext()` at this level but **not used** — `ProfileSettingsShell` and its children get identity from `useSelfProfile()` (Task 5+), never from the chat sidebar context.
- Produces: `export default function ProfileSettingsShell(): JSX.Element` — renders `ProfileTabs` + `<Outlet/>`.
- Produces: `export default function ProfileTabs(): JSX.Element` — a `<nav>` with four `NavLink`s.

- [ ] **Step 1: Write `ProfileTabs.tsx`**

Real routes, not an ARIA `tablist` — each item is a full navigation (URL changes, back/forward works), so a `nav`/`aria-current="page"` pattern is the correct accessible mapping, not a roving-tabindex `role="tablist"` (that pattern is for panel-switching without navigation). `end` on the `/profile` link so it isn't marked active on `/profile/sessions` too.

```tsx
import { NavLink } from "react-router";

import "./ProfileTabs.css";

const sections = [
  { to: "/profile", label: "Perfil", end: true },
  { to: "/profile/notifications", label: "Notificações", end: false },
  { to: "/profile/security", label: "Segurança", end: false },
  { to: "/profile/sessions", label: "Sessões", end: false },
] as const;

export default function ProfileTabs() {
  return (
    <nav className="profile-tabs" aria-label="Seções da conta">
      <ul className="profile-tabs__list">
        {sections.map((section) => (
          <li key={section.to} className="profile-tabs__item">
            <NavLink
              to={section.to}
              end={section.end}
              className={({ isActive }) =>
                isActive ? "profile-tabs__link profile-tabs__link--active" : "profile-tabs__link"
              }
            >
              {section.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}
```

- [ ] **Step 2: Write `ProfileTabs.test.tsx`**

```tsx
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";

import ProfileTabs from "./ProfileTabs";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/profile/*" element={<ProfileTabs />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("ProfileTabs", () => {
  it("marks Perfil active on the exact /profile route", () => {
    renderAt("/profile");
    expect(screen.getByRole("link", { name: "Perfil" })).toHaveClass("profile-tabs__link--active");
    expect(screen.getByRole("link", { name: "Sessões" })).not.toHaveClass(
      "profile-tabs__link--active",
    );
  });

  it("marks Sessões active on /profile/sessions and not Perfil", () => {
    renderAt("/profile/sessions");
    expect(screen.getByRole("link", { name: "Sessões" })).toHaveClass("profile-tabs__link--active");
    expect(screen.getByRole("link", { name: "Perfil" })).not.toHaveClass(
      "profile-tabs__link--active",
    );
  });

  it("renders all four sections as links", () => {
    renderAt("/profile");
    for (const label of ["Perfil", "Notificações", "Segurança", "Sessões"]) {
      expect(screen.getByRole("link", { name: label })).toHaveAttribute(
        "href",
        label === "Perfil"
          ? "/profile"
          : `/profile/${label === "Notificações" ? "notifications" : label === "Segurança" ? "security" : "sessions"}`,
      );
    }
  });
});
```

- [ ] **Step 3: Run it, verify it fails only on the not-yet-existing import**

Run: `pnpm --filter web test -- ProfileTabs`
Expected: FAIL (`ProfileTabs.tsx` doesn't exist yet if Step 1 wasn't saved — reorder if needed) then PASS once Step 1's file is saved.

- [ ] **Step 4: Write `ProfileSettingsShell.tsx`**

```tsx
import { Outlet } from "react-router";

import "./ProfileSettingsShell.css";
import ProfileTabs from "./ProfileTabs";

export default function ProfileSettingsShell() {
  return (
    <div className="profile-settings" data-testid="profile-settings-shell">
      <header className="profile-settings__header">
        <h1 className="profile-settings__title">Configurações da conta</h1>
      </header>
      <ProfileTabs />
      <div className="profile-settings__content">
        <Outlet />
      </div>
    </div>
  );
}
```

CSS (`ProfileSettingsShell.css`): content max-width ~1000px per the issue ("900–1050px"), centered, tabs bar `position: sticky; top: 0;` within the scrollable content region (not the page). Mirror the breakpoint values already established in `AppShell.css`/`ChatShell.css` for the tablet/mobile collapse (from Task 1's recon: wide ≥1280px, compact 1024–1279.98px, tablet/phone <1024px/<768px) rather than inventing new ones.

- [ ] **Step 5: Write `ProfileSettingsShell.test.tsx`**

```tsx
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";

import ProfileSettingsShell from "./ProfileSettingsShell";

describe("ProfileSettingsShell", () => {
  it("renders the tabs and the matched child route", () => {
    render(
      <MemoryRouter initialEntries={["/profile/notifications"]}>
        <Routes>
          <Route path="/profile" element={<ProfileSettingsShell />}>
            <Route path="notifications" element={<div>Notifications content</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByRole("navigation", { name: "Seções da conta" })).toBeInTheDocument();
    expect(screen.getByText("Notifications content")).toBeInTheDocument();
  });
});
```

- [ ] **Step 6: Update `App.tsx`**

Replace the placeholder `<Route path="/profile" element={<ProfilePage />} />` from Task 1 Step 4 with the nested route tree:

```tsx
<Route path="/profile" element={<ProfileSettingsShell />}>
  <Route index element={<ProfileOverviewPage />} />
  <Route
    path="notifications"
    element={
      <Suspense fallback={null}>
        <NotificationsSettingsPage />
      </Suspense>
    }
  />
  <Route
    path="security"
    element={
      <Suspense fallback={null}>
        <SecuritySettingsPage />
      </Suspense>
    }
  />
  <Route
    path="sessions"
    element={
      <Suspense fallback={null}>
        <SessionsSettingsPage />
      </Suspense>
    }
  />
</Route>
```

with imports:

```tsx
import ProfileSettingsShell from "./profile/ProfileSettingsShell";
import ProfileOverviewPage from "./profile/ProfileOverviewPage";
const NotificationsSettingsPage = lazy(() => import("./profile/NotificationsSettingsPage"));
const SecuritySettingsPage = lazy(() => import("./profile/SecuritySettingsPage"));
const SessionsSettingsPage = lazy(() => import("./profile/SessionsSettingsPage"));
```

`ProfileOverviewPage` (Task 9), `NotificationsSettingsPage` (Task 10), `SecuritySettingsPage` (Task 13), `SessionsSettingsPage` (Task 12) don't exist yet — this step will not typecheck until those tasks create them. That's expected; note it and move on, or stub each with `export default function X() { return null; }` temporarily if you want `App.tsx` to typecheck standalone before those tasks land. Remove the stub once the real file exists.

- [ ] **Step 7: Run `pnpm typecheck` and the full profile+chat suite, fix, commit**

```bash
git add apps/web/src/profile/ProfileTabs.tsx apps/web/src/profile/ProfileTabs.css apps/web/src/profile/ProfileTabs.test.tsx apps/web/src/profile/ProfileSettingsShell.tsx apps/web/src/profile/ProfileSettingsShell.css apps/web/src/profile/ProfileSettingsShell.test.tsx apps/web/src/App.tsx
git commit -m "feat(profile): add ProfileSettingsShell and route-based section tabs"
```

---

### Task 3: Fix the sidebar footer menu (`SidebarUserMenu`)

The current footer (`ChatSidebar.tsx:830-889`, function `SidebarUser`) renders two sibling links: the avatar/name link to `/profile` (correct, keep), and a gear icon labeled `aria-label="Configurações"`/`title="Configurações"` that actually navigates to `/admin/users` — a mislabeling bug. Issue #672 §9 asks for a real user menu ("Meu perfil", "Administração", "Sair"). Fix both at once: replace the gear link with a small menu button.

Product decision (document in the PR description, not in code comments): the mockup's "Configurações" item and "Meu perfil" item both point at the same place now that `/profile` covers identity + notifications + security + sessions — so this menu has **three** items, not four: "Meu perfil" (→ `/profile`), "Administração" (→ `/admin/users`, shown unconditionally like every other admin link in this codebase today — no client-side capability gate exists anywhere in `apps/web`; the destination enforces authorization server-side, per the pattern already used for `/admin/anti-spam` and `/admin/upload-limit`), and "Sair" (logout — does not exist as a UI control anywhere in the app today; `logout()` in `authApi.ts` is called from tests only).

**Files:**

- Create: `apps/web/src/chat/SidebarUserMenu.tsx`
- Modify: `apps/web/src/chat/ChatSidebar.tsx` (swap the gear `<Link>` for `<SidebarUserMenu/>`)
- Modify: `apps/web/src/chat/ChatSidebar.css` (small additions, reusing `.chat-sidebar__actions-*` class names already defined for `ConversationActionsMenu`)
- Test: `apps/web/src/chat/SidebarUserMenu.test.tsx`
- Modify: `apps/web/src/chat/ChatSidebar.test.tsx` (any assertion targeting the old gear link's `aria-label="Configurações"` / href)

**Interfaces:**

- Produces: `export default function SidebarUserMenu(): JSX.Element`. No props — it owns its own open/closed state and calls `useNavigate()` + `logout()` + `clearTokens()` internally.

- [ ] **Step 1: Write the failing test**

```tsx
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import * as authApi from "../auth/authApi";
import * as authSession from "../lib/authSession";
import SidebarUserMenu from "./SidebarUserMenu";

vi.mock("../auth/authApi");
vi.mock("../lib/authSession", async (importOriginal) => ({
  ...(await importOriginal<typeof authSession>()),
  clearTokens: vi.fn(),
}));

afterEach(() => vi.clearAllMocks());

function renderMenu() {
  return render(
    <MemoryRouter>
      <SidebarUserMenu />
    </MemoryRouter>,
  );
}

describe("SidebarUserMenu", () => {
  it("opens a menu with Meu perfil, Administração and Sair", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    const menu = screen.getByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Meu perfil" })).toHaveAttribute(
      "href",
      "/profile",
    );
    expect(within(menu).getByRole("menuitem", { name: "Administração" })).toHaveAttribute(
      "href",
      "/admin/users",
    );
    expect(within(menu).getByRole("menuitem", { name: "Sair" })).toBeInTheDocument();
  });

  it("closes on Escape and returns focus to the trigger", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: /menu da conta/i });
    await user.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("Sair calls logout, clears tokens, and never throws on a failed request", async () => {
    vi.mocked(authApi.logout).mockRejectedValueOnce(new Error("network"));
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /menu da conta/i }));
    await user.click(screen.getByRole("menuitem", { name: "Sair" }));
    await waitFor(() => expect(authSession.clearTokens).toHaveBeenCalledTimes(1));
  });
});
```

- [ ] **Step 2: Run it, confirm it fails** (`SidebarUserMenu.tsx` doesn't exist)

Run: `pnpm --filter web test -- SidebarUserMenu`
Expected: FAIL with a module-not-found error.

- [ ] **Step 3: Write `SidebarUserMenu.tsx`**

Mirrors `ConversationActionsMenu.tsx`'s trigger+portal+`role="menu"`+keyboard-nav mechanics (Escape closes and restores focus, ArrowUp/ArrowDown/Home/End move between items, outside click/focus closes), simplified for one static trigger instead of N per-row triggers (no dynamic `itemCount`-based flip math needed — three fixed items, footer sits at the bottom of the sidebar so the menu opens **above** the trigger by default).

```tsx
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Link, useNavigate } from "react-router";

import { logout } from "../auth/authApi";
import { clearTokens } from "../lib/authSession";

function IconSettings() {
  return (
    <svg
      viewBox="0 0 24 24"
      className="chat-sidebar__more-icon"
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
    </svg>
  );
}

const menuId = "chat-sidebar-user-menu";
const menuWidth = 200;
const menuGap = 6;
const viewportMargin = 8;

export default function SidebarUserMenu() {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef(false);
  const navigate = useNavigate();
  const domId = useId();

  const close = useCallback((restoreFocus: boolean) => {
    restoreFocusRef.current = restoreFocus;
    setOpen(false);
  }, []);

  const applyPosition = useCallback(() => {
    const node = menuRef.current;
    const trigger = triggerRef.current?.getBoundingClientRect();
    if (!node || !trigger) return;
    const height = node.offsetHeight || 120;
    const opensAbove = trigger.top - menuGap - height >= viewportMargin;
    node.style.top = opensAbove
      ? `${trigger.top - menuGap - height}px`
      : `${trigger.bottom + menuGap}px`;
    const maxLeft = window.innerWidth - menuWidth - viewportMargin;
    node.style.left = `${Math.min(Math.max(viewportMargin, trigger.left), Math.max(viewportMargin, maxLeft))}px`;
  }, []);

  const attachMenu = useCallback(
    (node: HTMLDivElement | null) => {
      menuRef.current = node;
      if (node) applyPosition();
    },
    [applyPosition],
  );

  useEffect(() => {
    if (open) {
      menuRef.current
        ?.querySelector<HTMLAnchorElement | HTMLButtonElement>("[role='menuitem']")
        ?.focus();
      return;
    }
    if (restoreFocusRef.current) {
      restoreFocusRef.current = false;
      triggerRef.current?.focus();
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const closeIfOutside = (event: Event) => {
      const target = event.target as Node | null;
      if (menuRef.current?.contains(target) || triggerRef.current?.contains(target)) return;
      close(false);
    };
    document.addEventListener("mousedown", closeIfOutside);
    document.addEventListener("focusin", closeIfOutside);
    return () => {
      document.removeEventListener("mousedown", closeIfOutside);
      document.removeEventListener("focusin", closeIfOutside);
    };
  }, [open, close]);

  function moveFocus(delta: number) {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLElement>("[role='menuitem']") ?? [],
    );
    if (items.length === 0) return;
    const current = items.findIndex((item) => item === document.activeElement);
    const next =
      ((((current < 0 ? -1 : current) + delta) % items.length) + items.length) % items.length;
    items[next]?.focus();
  }

  function handleMenuKeyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      close(true);
      return;
    }
    if (event.key === "Tab") {
      event.preventDefault();
      close(true);
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      moveFocus(1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveFocus(-1);
    }
  }

  async function handleLogout() {
    close(true);
    try {
      await logout();
    } catch {
      // Best-effort server-side revoke. The client is the source of truth for
      // "am I logged out" — clearTokens() below fires unconditionally, and
      // RequireAuth's onAuthChange subscription redirects to /login from that.
    } finally {
      clearTokens();
    }
  }

  return (
    <div className="chat-sidebar__actions">
      <button
        ref={triggerRef}
        type="button"
        className="chat-sidebar__actions-trigger chat-sidebar__user-settings"
        aria-label="Menu da conta"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? `${menuId}-${domId}` : undefined}
        onClick={() => {
          restoreFocusRef.current = false;
          setOpen((current) => !current);
        }}
      >
        <IconSettings />
      </button>
      {open &&
        createPortal(
          <div
            ref={attachMenu}
            id={`${menuId}-${domId}`}
            role="menu"
            aria-label="Menu da conta"
            className="chat-sidebar__actions-menu"
            onKeyDown={handleMenuKeyDown}
          >
            <Link
              to="/profile"
              role="menuitem"
              tabIndex={-1}
              className="chat-sidebar__actions-item"
              onClick={() => close(false)}
            >
              <span className="chat-sidebar__actions-label">Meu perfil</span>
            </Link>
            <Link
              to="/admin/users"
              role="menuitem"
              tabIndex={-1}
              className="chat-sidebar__actions-item"
              onClick={() => close(false)}
            >
              <span className="chat-sidebar__actions-label">Administração</span>
            </Link>
            <span className="chat-sidebar__actions-separator" role="none" />
            <button
              type="button"
              role="menuitem"
              tabIndex={-1}
              className="chat-sidebar__actions-item chat-sidebar__actions-item--destructive"
              onClick={() => void handleLogout()}
            >
              <span className="chat-sidebar__actions-label">Sair</span>
            </button>
          </div>,
          document.body,
        )}
    </div>
  );
}
```

`navigate` import is unused in this version (Link handles navigation) — remove the `useNavigate` import if the linter flags it unused; it's only needed if a later revision needs programmatic navigation after logout, which it doesn't (RequireAuth's redirect handles that).

- [ ] **Step 4: Swap the gear link for the new menu in `ChatSidebar.tsx`**

Replace lines 879-886 (the `<Link to="/admin/users" ...><IconSettings/></Link>`) with `<SidebarUserMenu />`, and remove the now-unused local `IconSettings` function (`ChatSidebar.tsx:104`) and its now-dangling import if nothing else in the file uses it — check with a grep for `IconSettings` in the file before deleting.

```tsx
import SidebarUserMenu from "./SidebarUserMenu";
// ...
      <SidebarUserMenu />
    </div>
  );
}
```

- [ ] **Step 5: Run the test, verify it passes**

Run: `pnpm --filter web test -- SidebarUserMenu ChatSidebar`
Expected: PASS. Fix any `ChatSidebar.test.tsx` assertion that still expects the old `aria-label="Configurações"` gear link.

- [ ] **Step 6: Commit**

```bash
git add apps/web/src/chat/SidebarUserMenu.tsx apps/web/src/chat/SidebarUserMenu.test.tsx apps/web/src/chat/ChatSidebar.tsx apps/web/src/chat/ChatSidebar.test.tsx apps/web/src/chat/ChatSidebar.css
git commit -m "fix(chat): replace mislabeled sidebar gear with a real account menu"
```

---

### Task 4: Consolidate `profileApi.ts` into a single `updateProfile` call

Today `ProfilePage` saves `display_name` and `job_title`/`bio`/`timezone`/`custom_status` through two separate `PATCH /auth/me` calls (`updateDisplayName`, `updateProfileFields`) because it has two separate forms. `ProfileEditDialog` (Task 6) is a single form for all five fields, so it needs a single call.

**Files:**

- Modify: `apps/web/src/profile/profileApi.ts`
- Modify: `apps/web/src/profile/profileApi.test.ts` (find/read the existing test file first — recon didn't confirm its exact name; if it's colocated inside `ProfilePage.test.tsx` instead, add the new tests there and note that in the task instead)

**Interfaces:**

- Produces: `export interface UpdateProfileInput { displayName: string; jobTitle: string; bio: string; timezone: string; customStatus: string; }`
- Produces: `export async function updateProfile(fields: UpdateProfileInput, signal?: AbortSignal): Promise<SelfProfile>`
- Removes: `updateDisplayName`, `UpdateDisplayNameError`, `UpdateDisplayNameErrorReason`, `updateProfileFields`, `UpdateProfileFieldsError`, `UpdateProfileFieldsErrorReason`, `ProfileFieldsInput` (all superseded — `ProfilePage.tsx` is deleted in Task 14, so nothing else calls them after this task's consumer, `ProfileEditDialog`, lands in Task 6). Keep them until Task 6 actually replaces the call sites, to avoid an intermediate broken build — or remove now and let `ProfilePage.tsx` fail to typecheck until Task 14 deletes it. **Do the latter is riskier; instead, keep both old functions until Task 14 deletes `ProfilePage.tsx`, and only remove them in Task 14's cleanup step.** Add `updateProfile` alongside them in this task.

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it, vi } from "vitest";

import { authenticatedFetch } from "../lib/authClient";
import { ApiRequestError } from "../lib/api";
import { updateProfile } from "./profileApi";

vi.mock("../lib/authClient");

describe("updateProfile", () => {
  it("sends all five fields in one PATCH and maps the response", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce({
      data: {
        id: "u1",
        display_name: "Ana",
        job_title: "Eng",
        bio: "bio",
        timezone: "America/Sao_Paulo",
        custom_status: "🚀 Focada",
      },
    });
    const result = await updateProfile({
      displayName: "Ana",
      jobTitle: "Eng",
      bio: "bio",
      timezone: "America/Sao_Paulo",
      customStatus: "🚀 Focada",
    });
    expect(authenticatedFetch).toHaveBeenCalledWith(
      "/api/auth/me",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({
          display_name: "Ana",
          job_title: "Eng",
          bio: "bio",
          timezone: "America/Sao_Paulo",
          custom_status: "🚀 Focada",
        }),
      }),
    );
    expect(result.displayName).toBe("Ana");
  });

  it("maps a 400 to an invalid UpdateProfileError and a 403 to forbidden", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(new ApiRequestError(400, "bad"));
    await expect(
      updateProfile({ displayName: "x", jobTitle: "", bio: "", timezone: "", customStatus: "" }),
    ).rejects.toMatchObject({ reason: "invalid" });
  });
});
```

- [ ] **Step 2: Run it, confirm it fails**

Run: `pnpm --filter web test -- profileApi` — FAIL, `updateProfile` is not exported.

- [ ] **Step 3: Implement `updateProfile` in `profileApi.ts`**

Add after the existing `updateProfileFields`:

```ts
export interface UpdateProfileInput {
  displayName: string;
  jobTitle: string;
  bio: string;
  timezone: string;
  customStatus: string;
}

export type UpdateProfileErrorReason = "invalid" | "forbidden" | "unknown";

export class UpdateProfileError extends Error {
  readonly reason: UpdateProfileErrorReason;
  constructor(reason: UpdateProfileErrorReason, message: string) {
    super(message);
    this.name = "UpdateProfileError";
    this.reason = reason;
  }
}

/**
 * Saves display_name, job_title, bio, timezone and custom_status in one PATCH,
 * replacing ProfilePage's former two-form/two-request flow now that a single
 * edit dialog owns all five fields together.
 */
export async function updateProfile(
  fields: UpdateProfileInput,
  signal?: AbortSignal,
): Promise<SelfProfile> {
  try {
    const res = await authenticatedFetch<SelfProfileResponse>(`${AUTH_BASE}/me`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        display_name: fields.displayName,
        job_title: fields.jobTitle,
        bio: fields.bio,
        timezone: fields.timezone,
        custom_status: fields.customStatus,
      }),
      signal,
    });
    return selfProfileFromResponse(res);
  } catch (error) {
    throw mapUpdateProfileError(error);
  }
}

function mapUpdateProfileError(error: unknown): UpdateProfileError {
  if (error instanceof ApiRequestError) {
    switch (error.status) {
      case 400:
        return new UpdateProfileError("invalid", "Dados inválidos.");
      case 403:
        return new UpdateProfileError("forbidden", "Conta indisponível para esta ação.");
    }
  }
  return new UpdateProfileError("unknown", "Não foi possível atualizar o perfil.");
}
```

- [ ] **Step 4: Run it, verify it passes; commit**

```bash
git add apps/web/src/profile/profileApi.ts apps/web/src/profile/profileApi.test.ts
git commit -m "feat(profile): add unified updateProfile PATCH for the new edit dialog"
```

---

### Task 5: `ProfileIdentityCard`

**Files:**

- Create: `apps/web/src/profile/ProfileIdentityCard.tsx`, `apps/web/src/profile/ProfileIdentityCard.css`
- Test: `apps/web/src/profile/ProfileIdentityCard.test.tsx`

**Interfaces:**

- Consumes: `SelfProfile` (from `profileApi.ts`), `usePresence`/`presenceLabel` (from `../chat/presence`), `PersonAvatarImage` (from `../chat/PersonAvatarImage`), `initialsFrom`/`avatarColorFor` (from `../chat/messageDisplay`).
- Produces: `export default function ProfileIdentityCard({ profile, onEdit, onChangePhoto }: { profile: SelfProfile; onEdit: () => void; onChangePhoto: () => void }): JSX.Element`

- [ ] **Step 1: Write the failing test**

```tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ProfileIdentityCard from "./ProfileIdentityCard";
import type { SelfProfile } from "./profileApi";

vi.mock("../chat/presence", () => ({
  usePresence: () => "online",
  presenceLabel: (state: string) => (state === "online" ? "Online" : state),
}));

const profile: SelfProfile = {
  id: "u1",
  displayName: "Ana Costa",
  avatarUrl: "/api/files/avatar.png",
  jobTitle: "Infraestrutura & Segurança",
  bio: "Trabalho com plataforma.",
  timezone: "America/Sao_Paulo",
  customStatus: "🚀 Focada no deploy",
};

describe("ProfileIdentityCard", () => {
  it("shows name, cargo, presence, timezone, bio and custom status", () => {
    render(<ProfileIdentityCard profile={profile} onEdit={vi.fn()} onChangePhoto={vi.fn()} />);
    expect(screen.getByRole("heading", { name: "Ana Costa" })).toBeInTheDocument();
    expect(screen.getByText("Infraestrutura & Segurança")).toBeInTheDocument();
    expect(screen.getByText("Online")).toBeInTheDocument();
    expect(screen.getByText("America/Sao_Paulo")).toBeInTheDocument();
    expect(screen.getByText("Trabalho com plataforma.")).toBeInTheDocument();
    expect(screen.getByText("🚀 Focada no deploy")).toBeInTheDocument();
  });

  it("omits empty optional fields instead of rendering a placeholder", () => {
    render(
      <ProfileIdentityCard
        profile={{ ...profile, jobTitle: "", bio: "", timezone: "", customStatus: "" }}
        onEdit={vi.fn()}
        onChangePhoto={vi.fn()}
      />,
    );
    expect(screen.queryByText("Infraestrutura & Segurança")).not.toBeInTheDocument();
    expect(screen.queryByTestId("profile-identity-timezone")).not.toBeInTheDocument();
  });

  it("Editar and Trocar foto call their handlers", async () => {
    const user = userEvent.setup();
    const onEdit = vi.fn();
    const onChangePhoto = vi.fn();
    render(<ProfileIdentityCard profile={profile} onEdit={onEdit} onChangePhoto={onChangePhoto} />);
    await user.click(screen.getByRole("button", { name: "Editar" }));
    await user.click(screen.getByRole("button", { name: "Trocar foto" }));
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onChangePhoto).toHaveBeenCalledTimes(1);
  });
});
```

- [ ] **Step 2: Run it, confirm it fails** (module doesn't exist)

- [ ] **Step 3: Implement `ProfileIdentityCard.tsx`**

```tsx
import "./ProfileIdentityCard.css";
import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import { presenceLabel, usePresence } from "../chat/presence";
import type { SelfProfile } from "./profileApi";

interface ProfileIdentityCardProps {
  profile: SelfProfile;
  onEdit: () => void;
  onChangePhoto: () => void;
}

export default function ProfileIdentityCard({
  profile,
  onEdit,
  onChangePhoto,
}: ProfileIdentityCardProps) {
  const presence = usePresence(profile.id);
  const initials = profile.displayName ? initialsFrom(profile.displayName) : "";

  return (
    <section className="profile-identity" aria-label="Identidade">
      <div className="profile-identity__avatar" style={{ color: avatarColorFor(profile.id) }}>
        <PersonAvatarImage
          src={profile.avatarUrl}
          initials={initials}
          imgClassName="profile-identity__avatar-img"
        />
      </div>
      <div className="profile-identity__info">
        <h1 className="profile-identity__name">{profile.displayName || "Sem nome"}</h1>
        {profile.jobTitle && <p className="profile-identity__job-title">{profile.jobTitle}</p>}
        <div className="profile-identity__meta">
          <span className="profile-identity__presence">
            <PresenceDotInline state={presence} /> {presenceLabel(presence)}
          </span>
          {profile.timezone && (
            <span data-testid="profile-identity-timezone" className="profile-identity__timezone">
              {profile.timezone}
            </span>
          )}
        </div>
        {profile.customStatus && (
          <p className="profile-identity__custom-status">{profile.customStatus}</p>
        )}
        {profile.bio && <p className="profile-identity__bio">{profile.bio}</p>}
        <div className="profile-identity__actions">
          <button
            type="button"
            className="profile-identity__btn profile-identity__btn--primary"
            onClick={onEdit}
          >
            Editar
          </button>
          <button type="button" className="profile-identity__btn" onClick={onChangePhoto}>
            Trocar foto
          </button>
        </div>
      </div>
    </section>
  );
}

function PresenceDotInline({ state }: { state: string }) {
  return (
    <span
      className={`profile-identity__presence-dot profile-identity__presence-dot--${state}`}
      aria-hidden="true"
    />
  );
}
```

Reuse `../chat/PresenceDot` directly instead of `PresenceDotInline` if its props line up (check `PresenceDot.tsx`'s exact prop signature before writing this step for real — it wasn't read during recon). Prefer the existing component over a new one-off if it fits without contortion.

No e-mail field: `SelfProfile` has no `email` — the identity card intentionally never renders one (see plan header: not in the backend contract, so it is not invented here).

- [ ] **Step 4: Run test, verify pass; commit**

```bash
git add apps/web/src/profile/ProfileIdentityCard.tsx apps/web/src/profile/ProfileIdentityCard.css apps/web/src/profile/ProfileIdentityCard.test.tsx
git commit -m "feat(profile): add ProfileIdentityCard"
```

---

### Task 6: `ProfileEditDialog`

**Files:**

- Create: `apps/web/src/profile/ProfileEditDialog.tsx`, `apps/web/src/profile/ProfileEditDialog.css`
- Test: `apps/web/src/profile/ProfileEditDialog.test.tsx`

**Interfaces:**

- Consumes: `updateProfile`, `UpdateProfileError` (Task 4); `validateDisplayName`, `validateBio`, `validateShortProfileField`, `validateTimezone`, `supportedTimezones` (existing `profileForm.ts`, unchanged).
- Produces: `export default function ProfileEditDialog({ profile, onClose, onSaved }: { profile: SelfProfile; onClose: () => void; onSaved: (profile: SelfProfile) => void }): JSX.Element`

Dialog shell (portal, `role="dialog"`, `aria-modal`, Escape, Tab focus trap, `submittingRef` single-submit guard) mirrors `RenameChannelDialog.tsx` exactly — copy that shell, not `LeaveConversationDialog.tsx`'s (this is a form, not a plain confirm). Fields: Nome de exibição, Cargo (documented exception — see comment below), Biografia, Status customizado, Fuso horário. All five submit together in one `updateProfile` call.

- [ ] **Step 1: Write the failing test** (covering dirty-state-disables-save, validation-blocks-save, single-submit, network-error-preserves-draft, success calls onSaved+onClose)

```tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ProfileEditDialog from "./ProfileEditDialog";
import * as profileApi from "./profileApi";
import type { SelfProfile } from "./profileApi";

vi.mock("./profileApi", async (importOriginal) => ({
  ...(await importOriginal<typeof profileApi>()),
  updateProfile: vi.fn(),
}));

const profile: SelfProfile = {
  id: "u1",
  displayName: "Ana",
  jobTitle: "Eng",
  bio: "",
  timezone: "",
  customStatus: "",
};

describe("ProfileEditDialog", () => {
  it("disables Salvar alterações until a field changes", async () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    expect(screen.getByRole("button", { name: /salvar alterações/i })).toBeDisabled();
    await userEvent.setup().type(screen.getByLabelText("Nome de exibição"), "!");
    expect(screen.getByRole("button", { name: /salvar alterações/i })).toBeEnabled();
  });

  it("blocks save and shows inline error on an invalid name", async () => {
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    const nameInput = screen.getByLabelText("Nome de exibição");
    await user.clear(nameInput);
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/informe um nome/i);
    expect(profileApi.updateProfile).not.toHaveBeenCalled();
  });

  it("saves once even on a double click, and calls onSaved+onClose with the server response", async () => {
    let resolveUpdate!: (p: SelfProfile) => void;
    vi.mocked(profileApi.updateProfile).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveUpdate = resolve;
      }),
    );
    const onSaved = vi.fn();
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={onClose} onSaved={onSaved} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    const saveButton = screen.getByRole("button", { name: /salvar alterações/i });
    await user.click(saveButton);
    await user.click(saveButton);
    expect(profileApi.updateProfile).toHaveBeenCalledTimes(1);
    resolveUpdate({ ...profile, displayName: "Ana Costa" });
    await waitFor(() =>
      expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ displayName: "Ana Costa" })),
    );
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps the dialog open and the draft intact on a network error", async () => {
    vi.mocked(profileApi.updateProfile).mockRejectedValueOnce(
      new profileApi.UpdateProfileError("unknown", "Não foi possível atualizar o perfil."),
    );
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={onClose} onSaved={vi.fn()} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/não foi possível/i));
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Nome de exibição")).toHaveValue("Ana Costa");
  });
});
```

- [ ] **Step 2: Run, confirm fail**

- [ ] **Step 3: Implement `ProfileEditDialog.tsx`**

```tsx
import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./ProfileEditDialog.css";
import {
  supportedTimezones,
  validateBio,
  validateDisplayName,
  validateShortProfileField,
  validateTimezone,
} from "./profileForm";
import { updateProfile, UpdateProfileError, type SelfProfile } from "./profileApi";

const TIMEZONE_OPTION_ELEMENTS = supportedTimezones().map((tz) => (
  <option key={tz} value={tz}>
    {tz}
  </option>
));

interface ProfileEditDialogProps {
  profile: SelfProfile;
  onClose: () => void;
  onSaved: (profile: SelfProfile) => void;
}

const titleId = "profile-edit-title";

export default function ProfileEditDialog({ profile, onClose, onSaved }: ProfileEditDialogProps) {
  const [displayName, setDisplayName] = useState(profile.displayName);
  const [jobTitle, setJobTitle] = useState(profile.jobTitle ?? "");
  const [bio, setBio] = useState(profile.bio ?? "");
  const [timezone, setTimezone] = useState(profile.timezone ?? "");
  const [customStatus, setCustomStatus] = useState(profile.customStatus ?? "");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);
  const dialogRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    mountedRef.current = true;
    nameInputRef.current?.focus();
    return () => {
      mountedRef.current = false;
    };
  }, []);

  function requestClose() {
    if (!submittingRef.current) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
      "button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled)",
    );
    if (!focusable?.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  const trimmedName = displayName.trim();
  const nameError = trimmedName === "" ? null : validateDisplayName(trimmedName);
  const jobTitleError = validateShortProfileField(jobTitle, "Cargo");
  const bioError = validateBio(bio);
  const timezoneError = validateTimezone(timezone);
  const customStatusError = validateShortProfileField(customStatus, "Status");
  const dirty =
    displayName !== profile.displayName ||
    jobTitle !== (profile.jobTitle ?? "") ||
    bio !== (profile.bio ?? "") ||
    timezone !== (profile.timezone ?? "") ||
    customStatus !== (profile.customStatus ?? "");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const message = validateDisplayName(trimmedName);
    if (message || jobTitleError || bioError || timezoneError || customStatusError) {
      setError(message ?? jobTitleError ?? bioError ?? timezoneError ?? customStatusError);
      nameInputRef.current?.focus();
      return;
    }
    if (submittingRef.current) return;
    submittingRef.current = true;
    setPending(true);
    setError(null);
    try {
      const saved = await updateProfile({
        displayName: trimmedName,
        jobTitle: jobTitle.trim(),
        bio: bio.trim(),
        timezone,
        customStatus: customStatus.trim(),
      });
      if (!mountedRef.current) return;
      onSaved(saved);
      onClose();
    } catch (failure) {
      if (!mountedRef.current) return;
      const message =
        failure instanceof UpdateProfileError
          ? failure.message
          : "Não foi possível salvar o perfil.";
      setError(message);
      nameInputRef.current?.focus();
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  return createPortal(
    <div className="profile-edit__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="profile-edit"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="profile-edit__title">
          Editar perfil
        </h2>
        <form onSubmit={submit}>
          <label className="profile-edit__label" htmlFor="profile-edit-name">
            Nome de exibição
          </label>
          <input
            ref={nameInputRef}
            id="profile-edit-name"
            type="text"
            value={displayName}
            disabled={pending}
            aria-invalid={nameError !== null}
            onChange={(e) => {
              setDisplayName(e.target.value);
              setError(null);
            }}
          />

          {/*
           * job_title stays user-editable: PATCH /auth/me's request DTO
           * (services/auth-service/internal/http/profile_handler.go) accepts it
           * from the client today and there is no admin-authority field/source
           * for it anywhere in the backend — no department/role/email exists in
           * the self-profile contract at all. This is the documented exception
           * issue #672 §1.3 asks for: kept editable by deliberate reading of the
           * actual contract, not by defaulting to the prototype's read-only mock.
           */}
          <label className="profile-edit__label" htmlFor="profile-edit-job-title">
            Cargo
          </label>
          <input
            id="profile-edit-job-title"
            type="text"
            value={jobTitle}
            disabled={pending}
            aria-invalid={jobTitleError !== null}
            onChange={(e) => setJobTitle(e.target.value)}
          />

          <label className="profile-edit__label" htmlFor="profile-edit-timezone">
            Fuso horário
          </label>
          <select
            id="profile-edit-timezone"
            value={timezone}
            disabled={pending}
            onChange={(e) => setTimezone(e.target.value)}
          >
            <option value="">Não definido</option>
            {TIMEZONE_OPTION_ELEMENTS}
          </select>

          <label className="profile-edit__label" htmlFor="profile-edit-custom-status">
            Status customizado
          </label>
          <input
            id="profile-edit-custom-status"
            type="text"
            value={customStatus}
            disabled={pending}
            onChange={(e) => setCustomStatus(e.target.value)}
          />

          <label className="profile-edit__label" htmlFor="profile-edit-bio">
            Biografia
          </label>
          <textarea
            id="profile-edit-bio"
            value={bio}
            disabled={pending}
            onChange={(e) => setBio(e.target.value)}
          />

          {error && (
            <p role="alert" className="profile-edit__error">
              {error}
            </p>
          )}

          <div className="profile-edit__actions">
            <button
              type="button"
              className="profile-edit__cancel"
              disabled={pending}
              onClick={requestClose}
            >
              Cancelar
            </button>
            <button
              type="submit"
              className="profile-edit__submit"
              disabled={!dirty || pending}
              aria-busy={pending}
            >
              {pending ? "Salvando…" : "Salvar alterações"}
            </button>
          </div>
        </form>
      </div>
    </div>,
    document.body,
  );
}
```

- [ ] **Step 4: Run, verify pass; commit**

```bash
git add apps/web/src/profile/ProfileEditDialog.tsx apps/web/src/profile/ProfileEditDialog.css apps/web/src/profile/ProfileEditDialog.test.tsx
git commit -m "feat(profile): add unified ProfileEditDialog"
```

---

### Task 7: `AvatarDialog`

**Files:**

- Create: `apps/web/src/profile/AvatarDialog.tsx`, `apps/web/src/profile/AvatarDialog.css`
- Test: `apps/web/src/profile/AvatarDialog.test.tsx`

**Interfaces:**

- Consumes: `uploadAvatar`, `removeAvatar`, `AvatarUploadError`, `AVATAR_ACCEPTED_TYPES`, `AVATAR_MAX_BYTES` (existing `profileApi.ts`, unchanged), `refreshSelfProfile` (existing `selfProfile.ts`, unchanged), `PersonAvatarImage`.
- Produces: `export default function AvatarDialog({ currentAvatarUrl, onClose }: { currentAvatarUrl?: string; onClose: () => void }): JSX.Element`

This is `ProfilePage.tsx`'s existing avatar-card logic (`persistedAvatarUrl`/`selectedFile`/`previewUrl`/`onSelect`/`onUpload`/`onRemove`/`discardSelection`, lines 92-311 and 534-609 of the current file), relocated into a dialog shell and driven by its own local state instead of the page's. Behavior is identical — same validation, same `refreshSelfProfile()` call after a confirmed mutation, same object-URL revocation — only the container changes from an inline `<section>` to a portal dialog, and the native `<input type="file">` becomes visually hidden (triggered by a real "Selecionar arquivo" button) instead of being the primary visible control, per issue #672 §1.5.

- [ ] **Step 1: Write the failing test** (port the existing avatar tests from `ProfilePage.test.tsx` — read that file first to copy its exact mock setup for `profileApi`, then adapt selectors to the dialog's own labels)

Cover: file type rejection, file size rejection, preview shown before upload, cancel discards preview only (no network call), upload calls `uploadAvatar` + `refreshSelfProfile` + `onClose`, remove calls `removeAvatar` + `refreshSelfProfile`, preview object URL is revoked on unmount and on re-selection, network error on upload preserves the selection for retry.

- [ ] **Step 2: Run, confirm fail**

- [ ] **Step 3: Implement `AvatarDialog.tsx`**

Port the relevant state/handlers from `ProfilePage.tsx` (`persistedAvatarUrl` becomes a `currentAvatarUrl` prop instead of page-owned state — the dialog doesn't own the "what's persisted" truth, `ProfileOverviewPage` does, via `useSelfProfile()`), wrap in the `RenameChannelDialog`-style portal shell, hide the `<input type="file">` visually (`className="profile-avatar-dialog__file-input sr-only"` or `hidden` + a visible trigger button that calls `.click()` on a ref to the hidden input):

```tsx
import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";

import "./AvatarDialog.css";
import {
  AVATAR_ACCEPTED_TYPES,
  AVATAR_MAX_BYTES,
  AvatarUploadError,
  removeAvatar,
  uploadAvatar,
} from "./profileApi";
import { refreshSelfProfile } from "./selfProfile";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";

interface AvatarDialogProps {
  currentAvatarUrl?: string;
  onClose: () => void;
}

const titleId = "avatar-dialog-title";

export default function AvatarDialog({ currentAvatarUrl, onClose }: AvatarDialogProps) {
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [selectionError, setSelectionError] = useState<string | null>(null);
  const [networkError, setNetworkError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const uploadingRef = useRef(false);
  const removingRef = useRef(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    return () => {
      if (previewUrl) URL.revokeObjectURL(previewUrl);
    };
  }, [previewUrl]);

  const clearSelectedAvatarState = useCallback(() => {
    setPreviewUrl(null);
    setSelectedFile(null);
  }, []);

  const clearFileInput = useCallback(() => {
    if (fileInputRef.current) fileInputRef.current.value = "";
  }, []);

  const discardSelection = useCallback(() => {
    clearSelectedAvatarState();
    clearFileInput();
  }, [clearSelectedAvatarState, clearFileInput]);

  function requestClose() {
    if (!uploadingRef.current && !removingRef.current) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled)");
    if (!focusable?.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  const onSelect = useCallback(
    (event: React.ChangeEvent<HTMLInputElement>) => {
      const input = event.currentTarget;
      const file = input.files?.[0] ?? null;
      setSelectionError(null);
      setNetworkError(null);
      clearSelectedAvatarState();
      clearFileInput();
      if (!file) return;
      if (!AVATAR_ACCEPTED_TYPES.includes(file.type as (typeof AVATAR_ACCEPTED_TYPES)[number])) {
        setSelectionError("Escolha uma imagem JPEG ou PNG.");
        return;
      }
      if (file.size > AVATAR_MAX_BYTES) {
        setSelectionError("A imagem é muito grande (máx. 5 MB).");
        return;
      }
      setSelectedFile(file);
      setPreviewUrl(URL.createObjectURL(file));
    },
    [clearSelectedAvatarState, clearFileInput],
  );

  const onUpload = useCallback(async () => {
    if (!selectedFile || uploadingRef.current) return;
    uploadingRef.current = true;
    setUploading(true);
    setNetworkError(null);
    try {
      await uploadAvatar(selectedFile);
      refreshSelfProfile();
      if (mountedRef.current) onClose();
    } catch (error) {
      if (!mountedRef.current) return;
      setNetworkError(
        error instanceof AvatarUploadError ? error.message : "Não foi possível enviar o avatar.",
      );
    } finally {
      uploadingRef.current = false;
      if (mountedRef.current) setUploading(false);
    }
  }, [selectedFile, onClose]);

  const onRemove = useCallback(async () => {
    if (removingRef.current) return;
    removingRef.current = true;
    setRemoving(true);
    setNetworkError(null);
    try {
      await removeAvatar();
      refreshSelfProfile();
      discardSelection();
      if (mountedRef.current) onClose();
    } catch (error) {
      if (!mountedRef.current) return;
      setNetworkError(
        error instanceof AvatarUploadError ? error.message : "Não foi possível remover o avatar.",
      );
    } finally {
      removingRef.current = false;
      if (mountedRef.current) setRemoving(false);
    }
  }, [discardSelection, onClose]);

  const busy = uploading || removing;
  const hasPersistedAvatar = Boolean(currentAvatarUrl);
  const shownSrc = previewUrl ?? currentAvatarUrl;

  return createPortal(
    <div className="avatar-dialog__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="avatar-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="avatar-dialog__title">
          Trocar foto
        </h2>
        <div className="avatar-dialog__preview">
          {previewUrl ? (
            <img
              src={previewUrl}
              alt="Pré-visualização do avatar"
              className="avatar-dialog__preview-img"
            />
          ) : (
            <PersonAvatarImage
              src={shownSrc}
              initials=""
              imgClassName="avatar-dialog__preview-img"
            />
          )}
        </div>
        <p className="avatar-dialog__hint">JPEG ou PNG, até 5 MB.</p>
        <input
          ref={fileInputRef}
          type="file"
          accept="image/jpeg,image/png"
          className="avatar-dialog__file-input"
          hidden
          aria-hidden="true"
          tabIndex={-1}
          onChange={onSelect}
          disabled={busy}
        />
        <div className="avatar-dialog__actions">
          <button
            type="button"
            className="avatar-dialog__btn"
            onClick={() => fileInputRef.current?.click()}
            disabled={busy}
          >
            Selecionar arquivo
          </button>
          {selectedFile && (
            <button
              type="button"
              className="avatar-dialog__btn"
              onClick={discardSelection}
              disabled={busy}
            >
              Cancelar seleção
            </button>
          )}
          <button
            type="button"
            className="avatar-dialog__btn avatar-dialog__btn--primary"
            onClick={() => void onUpload()}
            disabled={!selectedFile || busy}
            aria-busy={uploading}
          >
            {uploading ? "Enviando…" : "Enviar avatar"}
          </button>
          <button
            type="button"
            className="avatar-dialog__btn avatar-dialog__btn--destructive"
            onClick={() => void onRemove()}
            disabled={busy || !hasPersistedAvatar}
            aria-busy={removing}
          >
            {removing ? "Removendo…" : "Remover avatar"}
          </button>
          <button
            type="button"
            className="avatar-dialog__btn"
            onClick={requestClose}
            disabled={busy}
          >
            Fechar
          </button>
        </div>
        <div role="status" aria-live="polite" className="avatar-dialog__status">
          {selectionError && <span className="avatar-dialog__error">{selectionError}</span>}
          {networkError && <span className="avatar-dialog__error">{networkError}</span>}
        </div>
      </div>
    </div>,
    document.body,
  );
}
```

- [ ] **Step 4: Run, verify pass; commit**

```bash
git add apps/web/src/profile/AvatarDialog.tsx apps/web/src/profile/AvatarDialog.css apps/web/src/profile/AvatarDialog.test.tsx
git commit -m "feat(profile): add dedicated AvatarDialog replacing the exposed file input"
```

---

### Task 8: `ProfileInfoCard`

**Files:**

- Create: `apps/web/src/profile/ProfileInfoCard.tsx`, `apps/web/src/profile/ProfileInfoCard.css`
- Test: `apps/web/src/profile/ProfileInfoCard.test.tsx`

Per the Task 6 backend-contract finding, the self-profile API returns exactly `id`, `display_name`, `avatar_url`, `job_title`, `bio`, `timezone`, `custom_status` — no `email`, `department`, `role`, or `locale`. So this card has exactly two rows: Cargo (editable — same field as the identity card and edit dialog, shown again here for the "two-column info" layout the issue asks for) and Fuso horário. It does **not** invent an e-mail/department/role row. `WorkingHoursCard` is intentionally **not built** in this plan — no backend source of truth exists for it (see plan header); document this omission in the final PR description, don't silently skip it.

**Interfaces:**

- Produces: `export default function ProfileInfoCard({ profile }: { profile: SelfProfile }): JSX.Element`

- [ ] **Step 1: Write the failing test**

```tsx
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import ProfileInfoCard from "./ProfileInfoCard";
import type { SelfProfile } from "./profileApi";

const profile: SelfProfile = {
  id: "u1",
  displayName: "Ana",
  jobTitle: "Engenheira",
  timezone: "America/Sao_Paulo",
  bio: "",
  customStatus: "",
};

describe("ProfileInfoCard", () => {
  it("renders job title and timezone as a two-column definition list", () => {
    render(<ProfileInfoCard profile={profile} />);
    expect(screen.getByText("Cargo")).toBeInTheDocument();
    expect(screen.getByText("Engenheira")).toBeInTheDocument();
    expect(screen.getByText("Fuso horário")).toBeInTheDocument();
    expect(screen.getByText("America/Sao_Paulo")).toBeInTheDocument();
  });

  it("omits a row entirely when its value is unset, rather than showing a placeholder", () => {
    render(<ProfileInfoCard profile={{ ...profile, timezone: "" }} />);
    expect(screen.queryByText("Fuso horário")).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run, confirm fail**

- [ ] **Step 3: Implement**

```tsx
import "./ProfileInfoCard.css";
import type { SelfProfile } from "./profileApi";

export default function ProfileInfoCard({ profile }: { profile: SelfProfile }) {
  const rows: Array<{ label: string; value: string }> = [];
  if (profile.jobTitle) rows.push({ label: "Cargo", value: profile.jobTitle });
  if (profile.timezone) rows.push({ label: "Fuso horário", value: profile.timezone });
  if (rows.length === 0) return null;

  return (
    <section className="profile-info" aria-label="Informações">
      <h2 className="profile-info__title">Informações</h2>
      <dl className="profile-info__grid">
        {rows.map((row) => (
          <div className="profile-info__row" key={row.label}>
            <dt className="profile-info__label">{row.label}</dt>
            <dd className="profile-info__value">{row.value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
```

CSS: `.profile-info__grid { display: grid; grid-template-columns: repeat(2, 1fr); }` collapsing to one column under the mobile breakpoint already established in Task 2.

- [ ] **Step 4: Run, verify pass; commit**

```bash
git add apps/web/src/profile/ProfileInfoCard.tsx apps/web/src/profile/ProfileInfoCard.css apps/web/src/profile/ProfileInfoCard.test.tsx
git commit -m "feat(profile): add ProfileInfoCard with only real, non-invented fields"
```

---

### Task 9: `ProfileOverviewPage`

**Files:**

- Create: `apps/web/src/profile/ProfileOverviewPage.tsx`, `apps/web/src/profile/ProfileOverviewPage.css`
- Test: `apps/web/src/profile/ProfileOverviewPage.test.tsx`

**Interfaces:**

- Consumes: `useSelfProfile` (existing `selfProfile.ts`, **unchanged** — this is the switch from `ProfilePage`'s own local `fetchMyProfile` call to the shared cache, eliminating the duplicate GET this page and the sidebar both used to make independently), `ProfileIdentityCard`, `ProfileInfoCard`, `ProfileEditDialog`, `AvatarDialog`.
- Produces: `export default function ProfileOverviewPage(): JSX.Element`

- [ ] **Step 1: Write the failing test**

```tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ProfileOverviewPage from "./ProfileOverviewPage";
import * as selfProfile from "./selfProfile";

vi.mock("./selfProfile");
vi.mock("../chat/presence", () => ({ usePresence: () => "online", presenceLabel: () => "Online" }));

describe("ProfileOverviewPage", () => {
  it("shows a loading state, then the identity card once ready", () => {
    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({ status: "loading" });
    const { rerender } = render(<ProfileOverviewPage />);
    expect(screen.getByRole("status")).toBeInTheDocument();

    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({
      status: "ready",
      profile: {
        id: "u1",
        displayName: "Ana",
        jobTitle: "",
        bio: "",
        timezone: "",
        customStatus: "",
      },
    });
    rerender(<ProfileOverviewPage />);
    expect(screen.getByRole("heading", { name: "Ana" })).toBeInTheDocument();
  });

  it("shows a retry-capable error state independent of the sidebar/other sections", () => {
    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({ status: "error" });
    render(<ProfileOverviewPage />);
    expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument();
  });

  it("opens ProfileEditDialog from Editar and AvatarDialog from Trocar foto", async () => {
    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({
      status: "ready",
      profile: {
        id: "u1",
        displayName: "Ana",
        jobTitle: "",
        bio: "",
        timezone: "",
        customStatus: "",
      },
    });
    const user = userEvent.setup();
    render(<ProfileOverviewPage />);
    await user.click(screen.getByRole("button", { name: "Editar" }));
    expect(screen.getByRole("dialog", { name: "Editar perfil" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Trocar foto" }));
    expect(screen.getByRole("dialog", { name: "Trocar foto" })).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run, confirm fail**

- [ ] **Step 3: Implement**

```tsx
import { useState } from "react";

import "./ProfileOverviewPage.css";
import { ensureSelfProfile, useSelfProfile } from "./selfProfile";
import ProfileIdentityCard from "./ProfileIdentityCard";
import ProfileInfoCard from "./ProfileInfoCard";
import ProfileEditDialog from "./ProfileEditDialog";
import AvatarDialog from "./AvatarDialog";

type OpenDialog = "edit" | "avatar" | null;

export default function ProfileOverviewPage() {
  const self = useSelfProfile();
  const [openDialog, setOpenDialog] = useState<OpenDialog>(null);

  if (self.status === "loading") {
    return (
      <div className="profile-overview" role="status" aria-label="Carregando perfil">
        <span className="profile-overview__loading" />
      </div>
    );
  }

  if (self.status === "error") {
    return (
      <div className="profile-overview profile-overview__error">
        <p>Não foi possível carregar seu perfil.</p>
        <button type="button" onClick={() => ensureSelfProfile()}>
          Tentar novamente
        </button>
      </div>
    );
  }

  const { profile } = self;

  return (
    <div className="profile-overview">
      <header className="profile-overview__header">
        <h1 className="profile-overview__title">Perfil</h1>
        <p className="profile-overview__description">
          Suas informações, disponibilidade e preferências pessoais.
        </p>
      </header>
      <ProfileIdentityCard
        profile={profile}
        onEdit={() => setOpenDialog("edit")}
        onChangePhoto={() => setOpenDialog("avatar")}
      />
      <ProfileInfoCard profile={profile} />
      {openDialog === "edit" && (
        <ProfileEditDialog
          profile={profile}
          onClose={() => setOpenDialog(null)}
          onSaved={() => {
            /* the server response is already reflected because updateProfile's
               caller (ProfileEditDialog) resolves with it, but the source of
               truth this page reads is useSelfProfile() — force a refetch so
               this page shows exactly what the server persisted, not a second,
               page-local copy of it. */
            ensureSelfProfile();
          }}
        />
      )}
      {openDialog === "avatar" && (
        <AvatarDialog currentAvatarUrl={profile.avatarUrl} onClose={() => setOpenDialog(null)} />
      )}
    </div>
  );
}
```

Note: `ensureSelfProfile()` only refetches when the cached entry doesn't belong to the current session generation (see `selfProfile.ts`'s doc comment) — after a confirmed save the generation hasn't changed, so this would be a no-op. Use `refreshSelfProfile()` instead (unconditional refetch) in the `onSaved` callback so the page picks up the just-saved values immediately:

```tsx
import { refreshSelfProfile, useSelfProfile } from "./selfProfile";
// ...
onSaved={() => refreshSelfProfile()}
```

(`ensureSelfProfile` is still correct for the Retry button — a failed load has no fresh generation to force, so the conditional refetch there could be a no-op too; use `refreshSelfProfile()` for retry as well, since "error" state should always attempt again unconditionally.) Fix both call sites to `refreshSelfProfile()` before finishing this step.

- [ ] **Step 4: Run, verify pass; commit**

```bash
git add apps/web/src/profile/ProfileOverviewPage.tsx apps/web/src/profile/ProfileOverviewPage.css apps/web/src/profile/ProfileOverviewPage.test.tsx
git commit -m "feat(profile): add ProfileOverviewPage backed by the shared self-profile cache"
```

---

### Task 10: `NotificationsSettingsPage`

**Files:**

- Create: `apps/web/src/profile/NotificationsSettingsPage.tsx`, `apps/web/src/profile/NotificationsSettingsPage.css`
- Test: `apps/web/src/profile/NotificationsSettingsPage.test.tsx`

This is `ProfilePage.tsx`'s existing `<section className="profile-page__notifications-card">` block (current lines 783-904) plus its backing state/handlers (`soundMode`/`onChangeSoundMode`, `incomingCallRingtoneEnabled`/`onChangeIncomingCallRingtone`, `browserPermission`/`showBrowserNotificationHelp`/`onEnableBrowserNotifications`/the focus+visibilitychange effect), relocated verbatim — same helpers (`getSoundNotificationMode`, `setSoundNotificationMode`, `getIncomingCallRingtoneEnabled`, `setIncomingCallRingtoneEnabled`, `playIncomingCallRingtonePreview`, `getBrowserNotificationPermission`, `requestBrowserNotificationPermission`, `isBrowserNotificationSecureContext`), only restyled and given its own heading/description per issue #672 §2.1.

- [ ] **Step 1: Copy the existing tests**

Read `ProfilePage.test.tsx` in full and copy every test that exercises sound mode, ringtone, and browser-notification-permission behavior (identify them by the `describe`/`it` names — likely something like "sound preference", "incoming call ringtone", "browser notifications"). Paste them into the new `NotificationsSettingsPage.test.tsx`, updating only the component under test and any selectors that assumed the old page's DOM structure (e.g., if the old page's heading was absent, this one needs `screen.getByRole("heading", { name: "Notificações" })`). Do **not** rewrite the assertions' intent — the behavior under test is unchanged.

- [ ] **Step 2: Run, confirm fail**

- [ ] **Step 3: Implement `NotificationsSettingsPage.tsx`**

```tsx
import { useCallback, useEffect, useState } from "react";

import "./NotificationsSettingsPage.css";
import {
  type BrowserNotificationPermission,
  getBrowserNotificationPermission,
  isBrowserNotificationSecureContext,
  requestBrowserNotificationPermission,
} from "../chat/browserNotification";
import {
  getSoundNotificationMode,
  setSoundNotificationMode,
  type SoundNotificationMode,
} from "../chat/soundPreference";
import {
  getIncomingCallRingtoneEnabled,
  playIncomingCallRingtonePreview,
  setIncomingCallRingtoneEnabled,
} from "../calls/incomingCallRingtone";

export default function NotificationsSettingsPage() {
  const [soundMode, setSoundModeState] = useState<SoundNotificationMode>(() =>
    getSoundNotificationMode(),
  );
  const [incomingCallRingtoneEnabled, setIncomingCallRingtoneEnabledState] = useState(() =>
    getIncomingCallRingtoneEnabled(),
  );
  const [browserPermission, setBrowserPermission] = useState<BrowserNotificationPermission>(() =>
    getBrowserNotificationPermission(),
  );
  const [showBrowserNotificationHelp, setShowBrowserNotificationHelp] = useState(false);

  const onChangeSoundMode = useCallback((event: React.ChangeEvent<HTMLInputElement>) => {
    const next = event.currentTarget.value as SoundNotificationMode;
    setSoundNotificationMode(next);
    setSoundModeState(next);
  }, []);

  const onChangeIncomingCallRingtone = useCallback((event: React.ChangeEvent<HTMLInputElement>) => {
    const enabled = event.currentTarget.checked;
    setIncomingCallRingtoneEnabled(enabled);
    setIncomingCallRingtoneEnabledState(enabled);
  }, []);

  const onEnableBrowserNotifications = useCallback(async () => {
    const result = await requestBrowserNotificationPermission();
    setBrowserPermission(result);
  }, []);

  useEffect(() => {
    const refreshBrowserPermission = () => setBrowserPermission(getBrowserNotificationPermission());
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") refreshBrowserPermission();
    };
    window.addEventListener("focus", refreshBrowserPermission);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      window.removeEventListener("focus", refreshBrowserPermission);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, []);

  return (
    <div className="notifications-settings">
      <header className="notifications-settings__header">
        <h1 className="notifications-settings__title">Notificações</h1>
        <p className="notifications-settings__description">
          Gerencie como você é avisado sobre mensagens, menções e chamadas.
        </p>
      </header>

      {/* [paste ProfilePage.tsx's existing <fieldset className="profile-page__sound-modes">
           block here verbatim, renaming the wrapper className prefix from
           profile-page__ to notifications-settings__ throughout — behavior unchanged] */}

      {/* [paste the existing incoming-call-ringtone <div> block here verbatim, same rename] */}

      {/* [paste the existing browser-notifications <div> block here verbatim, same rename,
           including all four permission branches: granted / denied+help / unsupported / default] */}
    </div>
  );
}
```

Copy the three JSX blocks exactly from `ProfilePage.tsx` lines 784-830 (sound modes fieldset), 832-849 (ringtone), 851-903 (browser notifications) — same markup, same conditionals, same copy strings, only the CSS class prefix changes from `profile-page__` to `notifications-settings__` (and update `NotificationsSettingsPage.css` to define those classes, copied from the relevant rules in `ProfilePage.css`).

- [ ] **Step 4: Run, verify pass; commit**

```bash
git add apps/web/src/profile/NotificationsSettingsPage.tsx apps/web/src/profile/NotificationsSettingsPage.css apps/web/src/profile/NotificationsSettingsPage.test.tsx
git commit -m "feat(profile): move notification preferences into their own settings page"
```

---

### Task 11: `sessionsApi.ts`

**Files:**

- Create: `apps/web/src/profile/sessionsApi.ts`
- Test: `apps/web/src/profile/sessionsApi.test.ts`

Backend contract confirmed in `services/auth-service/internal/http/session_handler.go`: `GET /auth/me/sessions` → `{ data: SessionRow[], pagination: {...} }` where each row already has `ip_address` IP-masked (`maskIPAddress`) and `user_agent` sanitized (`sanitizeUserAgent`) server-side, plus a `current: boolean`. `DELETE /auth/me/sessions/{id}` → 204, 404 for unknown/cross-user (never distinguishes the two). `DELETE /auth/me/sessions` → 204, revokes all but the caller's current session (401 if the request has no current session bound).

**Interfaces:**

- Produces:

```ts
export interface Session {
  id: string;
  createdAt: string;
  lastSeenAt: string;
  ipAddress: string; // already masked server-side, e.g. "187.10.x.x"
  userAgent: string; // already sanitized server-side; raw string, not parsed into browser/OS
  current: boolean;
  revokedAt?: string;
}
export async function listSessions(signal?: AbortSignal): Promise<Session[]>;
export async function revokeSession(sessionId: string, signal?: AbortSignal): Promise<void>;
export async function revokeAllOtherSessions(signal?: AbortSignal): Promise<void>;
export class SessionsApiError extends Error {
  reason: "forbidden" | "unauthorized" | "unknown";
}
```

Deliberately no UA parsing into "Firefox · Linux"-style labels: the backend hands back one sanitized string, not structured browser/OS fields, and a client-side regex guesser risks mislabeling a session's browser (a false "Chrome" on an actual Firefox session is worse than an honest raw string) — display the sanitized `user_agent` text as-is. Note this decision in the PR description.

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it, vi } from "vitest";

import { authenticatedFetch } from "../lib/authClient";
import { ApiRequestError } from "../lib/api";
import {
  listSessions,
  revokeAllOtherSessions,
  revokeSession,
  SessionsApiError,
} from "./sessionsApi";

vi.mock("../lib/authClient");

describe("sessionsApi", () => {
  it("listSessions maps the envelope into Session[]", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce({
      data: [
        {
          id: "s1",
          device_id: null,
          created_at: "2026-08-01T00:00:00Z",
          last_seen_at: "2026-08-27T00:00:00Z",
          idle_expires_at: "2026-08-27T01:00:00Z",
          absolute_expires_at: null,
          revoked_at: null,
          ip_address: "187.10.x.x",
          user_agent: "Mozilla/5.0 (X11; Linux x86_64) Firefox",
          current: true,
        },
      ],
      pagination: { limit: 50 },
    });
    const sessions = await listSessions();
    expect(sessions).toEqual([
      {
        id: "s1",
        createdAt: "2026-08-01T00:00:00Z",
        lastSeenAt: "2026-08-27T00:00:00Z",
        ipAddress: "187.10.x.x",
        userAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox",
        current: true,
        revokedAt: undefined,
      },
    ]);
    expect(authenticatedFetch).toHaveBeenCalledWith("/api/auth/me/sessions", {
      method: "GET",
      signal: undefined,
    });
  });

  it("revokeSession calls DELETE on the session's own path", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce(undefined);
    await revokeSession("s2");
    expect(authenticatedFetch).toHaveBeenCalledWith("/api/auth/me/sessions/s2", {
      method: "DELETE",
      signal: undefined,
    });
  });

  it("revokeAllOtherSessions calls DELETE on the collection endpoint", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce(undefined);
    await revokeAllOtherSessions();
    expect(authenticatedFetch).toHaveBeenCalledWith("/api/auth/me/sessions", {
      method: "DELETE",
      signal: undefined,
    });
  });

  it("maps a 401 on revokeAllOtherSessions to SessionsApiError('unauthorized')", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(401, "no current session"),
    );
    await expect(revokeAllOtherSessions()).rejects.toMatchObject({ reason: "unauthorized" });
    expect(SessionsApiError).toBeDefined();
  });
});
```

- [ ] **Step 2: Run, confirm fail**

- [ ] **Step 3: Implement `sessionsApi.ts`**

```ts
import { authenticatedFetch } from "../lib/authClient";
import { ApiRequestError } from "../lib/api";

const AUTH_BASE = import.meta.env.VITE_AUTH_API_BASE_URL ?? "/api/auth";

export interface Session {
  id: string;
  createdAt: string;
  lastSeenAt: string;
  ipAddress: string;
  userAgent: string;
  current: boolean;
  revokedAt?: string;
}

interface SessionRowResponse {
  id: string;
  device_id: string | null;
  created_at: string;
  last_seen_at: string;
  idle_expires_at: string;
  absolute_expires_at: string | null;
  revoked_at: string | null;
  ip_address?: string;
  user_agent?: string;
  current: boolean;
}

interface SessionsListResponse {
  data: SessionRowResponse[];
  pagination: { limit: number };
}

export type SessionsApiErrorReason = "unauthorized" | "forbidden" | "unknown";

export class SessionsApiError extends Error {
  readonly reason: SessionsApiErrorReason;
  constructor(reason: SessionsApiErrorReason, message: string) {
    super(message);
    this.name = "SessionsApiError";
    this.reason = reason;
  }
}

function mapSessionsError(error: unknown): SessionsApiError {
  if (error instanceof ApiRequestError) {
    switch (error.status) {
      case 401:
        return new SessionsApiError("unauthorized", "Sua sessão atual não pôde ser confirmada.");
      case 403:
        return new SessionsApiError("forbidden", "Você não tem permissão para esta ação.");
    }
  }
  return new SessionsApiError("unknown", "Não foi possível concluir a operação.");
}

function fromResponse(row: SessionRowResponse): Session {
  return {
    id: row.id,
    createdAt: row.created_at,
    lastSeenAt: row.last_seen_at,
    ipAddress: row.ip_address ?? "",
    userAgent: row.user_agent ?? "",
    current: row.current,
    revokedAt: row.revoked_at ?? undefined,
  };
}

/** Lists the authenticated user's own active sessions, newest first. Never accepts a user id — identity is the session's own. */
export async function listSessions(signal?: AbortSignal): Promise<Session[]> {
  try {
    const res = await authenticatedFetch<SessionsListResponse>(`${AUTH_BASE}/me/sessions`, {
      method: "GET",
      signal,
    });
    return res.data.map(fromResponse);
  } catch (error) {
    throw mapSessionsError(error);
  }
}

/** Revokes one session. Idempotent from the caller's perspective: a 404 (already gone / not this user's) is not surfaced as a distinct case here — the list is always revalidated after, so the row converges to "gone" either way. */
export async function revokeSession(sessionId: string, signal?: AbortSignal): Promise<void> {
  try {
    await authenticatedFetch<void>(`${AUTH_BASE}/me/sessions/${sessionId}`, {
      method: "DELETE",
      signal,
    });
  } catch (error) {
    throw mapSessionsError(error);
  }
}

/** Revokes every session except the caller's current one. */
export async function revokeAllOtherSessions(signal?: AbortSignal): Promise<void> {
  try {
    await authenticatedFetch<void>(`${AUTH_BASE}/me/sessions`, { method: "DELETE", signal });
  } catch (error) {
    throw mapSessionsError(error);
  }
}
```

- [ ] **Step 4: Run, verify pass; commit**

```bash
git add apps/web/src/profile/sessionsApi.ts apps/web/src/profile/sessionsApi.test.ts
git commit -m "feat(profile): add sessionsApi client for GET/DELETE /auth/me/sessions"
```

---

### Task 12: `RevokeSessionDialog`, `SessionRow`, `SessionsSettingsPage`

**Files:**

- Create: `apps/web/src/profile/RevokeSessionDialog.tsx`, `.css`
- Create: `apps/web/src/profile/SessionRow.tsx`, `.css`
- Create: `apps/web/src/profile/SessionsSettingsPage.tsx`, `.css`
- Test: three matching `.test.tsx` files

**Interfaces:**

- `RevokeSessionDialog`: mirrors `LeaveConversationDialog.tsx`'s shell exactly (confirm-only, no form field, focus starts on Cancel). `export default function RevokeSessionDialog({ target, onClose, onConfirm }: { target: "single" | "others"; onClose: () => void; onConfirm: () => Promise<void> }): JSX.Element` — one component parameterized by which action it confirms, rather than two near-identical dialogs.
- `SessionRow`: `export default function SessionRow({ session, onRevoke }: { session: Session; onRevoke: (id: string) => void }): JSX.Element` — pure display + a "Revogar sessão" button (hidden entirely for `session.current`, per issue §4.3: no revoke control on the current session — that's what app logout is for).
- `SessionsSettingsPage`: owns the list fetch (loading/error/retry, independent of Profile/Notifications/Security per issue's "falha de uma seção não derruba as demais"), the two confirm-dialog open states, and revalidation after a mutation.

- [ ] **Step 1: Write `RevokeSessionDialog.test.tsx`, run, confirm fail**

```tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import RevokeSessionDialog from "./RevokeSessionDialog";

describe("RevokeSessionDialog", () => {
  it("focuses Cancelar first for the single-session variant", () => {
    render(<RevokeSessionDialog target="single" onClose={vi.fn()} onConfirm={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Cancelar" })).toHaveFocus();
    expect(screen.getByRole("heading")).toHaveTextContent(/revogar sessão\?/i);
  });

  it("shows the 'others' copy and calls onConfirm once, then onClose", async () => {
    let resolveConfirm!: () => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveConfirm = resolve;
        }),
    );
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<RevokeSessionDialog target="others" onClose={onClose} onConfirm={onConfirm} />);
    expect(screen.getByRole("heading")).toHaveTextContent(/revogar outras sessões\?/i);
    const confirmButton = screen.getByRole("button", { name: /revogar sessões/i });
    await user.click(confirmButton);
    await user.click(confirmButton);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    resolveConfirm();
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("keeps the dialog open on a rejected onConfirm", async () => {
    const onConfirm = vi.fn().mockRejectedValueOnce(new Error("fail"));
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<RevokeSessionDialog target="single" onClose={onClose} onConfirm={onConfirm} />);
    await user.click(screen.getByRole("button", { name: /revogar sessão/i }));
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    expect(onClose).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Implement `RevokeSessionDialog.tsx`**

Copy `LeaveConversationDialog.tsx`'s shell (portal, `dialogRef`, `cancelRef` focused first, `handleKeyDown` with Tab-trap over `button:not(:disabled)`, `submittingRef`, `mountedRef`, `confirm()`), replacing its content with:

```tsx
const copy = {
  single: {
    title: "Revogar sessão?",
    description: "Este dispositivo será desconectado do NChat e precisará autenticar novamente.",
    confirmLabel: "Revogar sessão",
    pendingLabel: "Revogando…",
  },
  others: {
    title: "Revogar outras sessões?",
    description:
      "Esses dispositivos serão desconectados do NChat e precisarão autenticar novamente.",
    confirmLabel: "Revogar sessões",
    pendingLabel: "Revogando…",
  },
} as const;
```

with the same `error`/`pending`/`onConfirm().catch()` flow as `LeaveConversationDialog`'s `confirm()`, and a generic error message ("Não foi possível concluir a revogação. Tente novamente.") since the caller (`SessionsSettingsPage`) already knows the specific reason from `SessionsApiError` and can pass a pre-formatted message down instead if a step here decides that's cleaner — keep it simple and let the dialog show one static fallback message on any rejection, matching `LeaveConversationDialog`'s pattern of a single generic message rather than status-code branching (session revoke errors don't have the same "which specific thing went wrong" nuance renaming does).

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Write `SessionRow.test.tsx`, run, confirm fail**

```tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import SessionRow from "./SessionRow";
import type { Session } from "./sessionsApi";

const base: Session = {
  id: "s1",
  createdAt: "2026-08-01T00:00:00Z",
  lastSeenAt: "2026-08-27T10:00:00Z",
  ipAddress: "187.10.x.x",
  userAgent: "Mozilla/5.0 Firefox",
  current: false,
};

describe("SessionRow", () => {
  it("shows a 'Sessão atual' badge and no revoke button for the current session", () => {
    render(<SessionRow session={{ ...base, current: true }} onRevoke={vi.fn()} />);
    expect(screen.getByText("Sessão atual")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /revogar/i })).not.toBeInTheDocument();
  });

  it("shows Revogar sessão for a remote session and calls onRevoke with its id", async () => {
    const onRevoke = vi.fn();
    const user = userEvent.setup();
    render(<SessionRow session={base} onRevoke={onRevoke} />);
    await user.click(screen.getByRole("button", { name: "Revogar sessão" }));
    expect(onRevoke).toHaveBeenCalledWith("s1");
  });

  it("shows the masked IP and raw sanitized user agent as-is", () => {
    render(<SessionRow session={base} onRevoke={vi.fn()} />);
    expect(screen.getByText("187.10.x.x")).toBeInTheDocument();
    expect(screen.getByText("Mozilla/5.0 Firefox")).toBeInTheDocument();
  });
});
```

- [ ] **Step 5: Implement `SessionRow.tsx`**

```tsx
import "./SessionRow.css";
import type { Session } from "./sessionsApi";

export default function SessionRow({
  session,
  onRevoke,
}: {
  session: Session;
  onRevoke: (id: string) => void;
}) {
  return (
    <li className="session-row" data-testid="session-row">
      <div className="session-row__info">
        <p className="session-row__agent">{session.userAgent || "Dispositivo desconhecido"}</p>
        <p className="session-row__meta">
          {session.ipAddress && <span>{session.ipAddress} (aproximado)</span>}
          <span>
            {session.current
              ? "Ativa agora"
              : `Última atividade em ${new Date(session.lastSeenAt).toLocaleString("pt-BR")}`}
          </span>
        </p>
      </div>
      {session.current ? (
        <span className="session-row__current-badge">Sessão atual</span>
      ) : (
        <button type="button" className="session-row__revoke" onClick={() => onRevoke(session.id)}>
          Revogar sessão
        </button>
      )}
    </li>
  );
}
```

- [ ] **Step 6: Run, verify pass**

- [ ] **Step 7: Write `SessionsSettingsPage.test.tsx`, run, confirm fail**

Cover: loading state, error+retry state (independent of other sections — this test doesn't need to prove the others don't break, that's structural via routing, just that this page has its own error UI rather than crashing the whole app), empty-after-load isn't a distinct empty state since there's always at least the current session, revoke-single opens `RevokeSessionDialog`+on confirm calls `revokeSession`+relists, revoke-all-others opens `RevokeSessionDialog target="others"`+on confirm calls `revokeAllOtherSessions`+relists, a component unmount mid-fetch doesn't call `setState` after unmount (assert via not throwing / no act() warning).

```tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import SessionsSettingsPage from "./SessionsSettingsPage";
import * as sessionsApi from "./sessionsApi";

vi.mock("./sessionsApi");

const sessions: sessionsApi.Session[] = [
  {
    id: "current",
    createdAt: "",
    lastSeenAt: "",
    ipAddress: "1.2.x.x",
    userAgent: "Firefox",
    current: true,
  },
  {
    id: "other",
    createdAt: "",
    lastSeenAt: "",
    ipAddress: "3.4.x.x",
    userAgent: "Chrome",
    current: false,
  },
];

describe("SessionsSettingsPage", () => {
  it("loads and lists sessions, current session first with its badge", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions);
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    expect(screen.getByText("Sessão atual")).toBeInTheDocument();
  });

  it("shows a retry-capable error independent of other sections", async () => {
    vi.mocked(sessionsApi.listSessions).mockRejectedValueOnce(
      new sessionsApi.SessionsApiError("unknown", "x"),
    );
    render(<SessionsSettingsPage />);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument(),
    );
  });

  it("revokes a single session through the confirm dialog and relists", async () => {
    vi.mocked(sessionsApi.listSessions)
      .mockResolvedValueOnce(sessions)
      .mockResolvedValueOnce([sessions[0]]);
    vi.mocked(sessionsApi.revokeSession).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    await user.click(screen.getByRole("button", { name: "Revogar sessão" }));
    await user.click(screen.getByRole("button", { name: "Revogar sessão", hidden: false }));
    await waitFor(() => expect(sessionsApi.revokeSession).toHaveBeenCalledWith("other"));
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(1));
  });

  it("revokes all others and preserves the current session", async () => {
    vi.mocked(sessionsApi.listSessions)
      .mockResolvedValueOnce(sessions)
      .mockResolvedValueOnce([sessions[0]]);
    vi.mocked(sessionsApi.revokeAllOtherSessions).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    await user.click(screen.getByRole("button", { name: /revogar todas as outras/i }));
    await user.click(screen.getByRole("button", { name: /revogar sessões/i }));
    await waitFor(() => expect(sessionsApi.revokeAllOtherSessions).toHaveBeenCalledTimes(1));
    await waitFor(() => {
      const rows = screen.getAllByTestId("session-row");
      expect(rows).toHaveLength(1);
      expect(screen.getByText("Sessão atual")).toBeInTheDocument();
    });
  });
});
```

- [ ] **Step 8: Implement `SessionsSettingsPage.tsx`**

```tsx
import { useCallback, useEffect, useRef, useState } from "react";

import "./SessionsSettingsPage.css";
import { listSessions, revokeAllOtherSessions, revokeSession, type Session } from "./sessionsApi";
import RevokeSessionDialog from "./RevokeSessionDialog";
import SessionRow from "./SessionRow";

type LoadState =
  | { status: "loading" }
  | { status: "error" }
  | { status: "ready"; sessions: Session[] };
type ConfirmState = { target: "single"; sessionId: string } | { target: "others" } | null;

export default function SessionsSettingsPage() {
  const [state, setState] = useState<LoadState>({ status: "loading" });
  const [confirm, setConfirm] = useState<ConfirmState>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback((signal?: AbortSignal) => {
    setState({ status: "loading" });
    listSessions(signal)
      .then((sessions) => {
        if (signal?.aborted || !mountedRef.current) return;
        setState({ status: "ready", sessions });
      })
      .catch(() => {
        if (signal?.aborted || !mountedRef.current) return;
        setState({ status: "error" });
      });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  async function handleConfirm() {
    if (!confirm) return;
    if (confirm.target === "single") {
      await revokeSession(confirm.sessionId);
    } else {
      await revokeAllOtherSessions();
    }
    if (mountedRef.current) load();
  }

  if (state.status === "loading") {
    return (
      <div className="sessions-settings" role="status" aria-label="Carregando sessões">
        <span className="sessions-settings__loading" />
      </div>
    );
  }

  if (state.status === "error") {
    return (
      <div className="sessions-settings sessions-settings__error">
        <p>Não foi possível carregar suas sessões.</p>
        <button type="button" onClick={() => load()}>
          Tentar novamente
        </button>
      </div>
    );
  }

  const { sessions } = state;
  const hasOtherSessions = sessions.some((s) => !s.current);

  return (
    <div className="sessions-settings">
      <header className="sessions-settings__header">
        <h1 className="sessions-settings__title">Sessões</h1>
        {hasOtherSessions && (
          <button
            type="button"
            className="sessions-settings__revoke-all"
            onClick={() => setConfirm({ target: "others" })}
          >
            Revogar todas as outras
          </button>
        )}
      </header>
      <ul className="sessions-settings__list">
        {sessions.map((session) => (
          <SessionRow
            key={session.id}
            session={session}
            onRevoke={(id) => setConfirm({ target: "single", sessionId: id })}
          />
        ))}
      </ul>
      {confirm && (
        <RevokeSessionDialog
          target={confirm.target}
          onClose={() => setConfirm(null)}
          onConfirm={handleConfirm}
        />
      )}
    </div>
  );
}
```

- [ ] **Step 9: Run, verify pass; commit**

```bash
git add apps/web/src/profile/RevokeSessionDialog.tsx apps/web/src/profile/RevokeSessionDialog.css apps/web/src/profile/RevokeSessionDialog.test.tsx apps/web/src/profile/SessionRow.tsx apps/web/src/profile/SessionRow.css apps/web/src/profile/SessionRow.test.tsx apps/web/src/profile/SessionsSettingsPage.tsx apps/web/src/profile/SessionsSettingsPage.css apps/web/src/profile/SessionsSettingsPage.test.tsx
git commit -m "feat(profile): add Sessions settings page backed by real /auth/me/sessions"
```

---

### Task 13: `SecuritySettingsPage` + Keycloak account URL config

**Files:**

- Create: `apps/web/src/profile/SecuritySettingsPage.tsx`, `.css`
- Test: `apps/web/src/profile/SecuritySettingsPage.test.tsx`
- Modify: `apps/web/.env.example` (or wherever `VITE_AUTH_API_BASE_URL` is documented — check for an existing `.env.example`/`vite-env.d.ts` first)

**Interfaces:**

- Produces: `export default function SecuritySettingsPage(): JSX.Element`
- New env var: `VITE_KEYCLOAK_ACCOUNT_URL` — trusted build-time config, same convention as `VITE_AUTH_API_BASE_URL` in `profileApi.ts:13`. **Never** constructed from any user input or route param.

- [ ] **Step 1: Find the env var documentation/typing convention**

Grep for `VITE_AUTH_API_BASE_URL` across `apps/web` (look for `vite-env.d.ts` / `.env.example` / `env.d.ts`) to find where env vars are typed/documented, and mirror that exact pattern for the new one.

- [ ] **Step 2: Write the failing test**

```tsx
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import SecuritySettingsPage from "./SecuritySettingsPage";

describe("SecuritySettingsPage", () => {
  const originalUrl = import.meta.env.VITE_KEYCLOAK_ACCOUNT_URL;

  afterEach(() => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", originalUrl ?? "");
  });

  it("never implements a local password/MFA form", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "https://id.nchat.local/realms/nchat/account");
    render(<SecuritySettingsPage />);
    expect(screen.queryByLabelText(/senha/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/totp|autenticador|passkey/i)).not.toBeInTheDocument();
  });

  it("links to the configured Keycloak account URL, opened in a new tab safely", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "https://id.nchat.local/realms/nchat/account");
    render(<SecuritySettingsPage />);
    const link = screen.getByRole("link", { name: /gerenciar segurança da conta/i });
    expect(link).toHaveAttribute("href", "https://id.nchat.local/realms/nchat/account");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  it("shows an honest message with no dead link when the URL is not configured", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "");
    render(<SecuritySettingsPage />);
    expect(screen.queryByRole("link", { name: /gerenciar segurança/i })).not.toBeInTheDocument();
    expect(
      screen.getByText(/gerencie senha e autenticação no provedor de identidade/i),
    ).toBeInTheDocument();
  });

  it("does not claim MFA is enabled or disabled — no fabricated status", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "https://id.nchat.local/realms/nchat/account");
    render(<SecuritySettingsPage />);
    expect(screen.queryByText(/ativada|desativada/i)).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 3: Run, confirm fail**

- [ ] **Step 4: Implement**

```tsx
import "./SecuritySettingsPage.css";

const KEYCLOAK_ACCOUNT_URL = import.meta.env.VITE_KEYCLOAK_ACCOUNT_URL;

export default function SecuritySettingsPage() {
  return (
    <div className="security-settings">
      <header className="security-settings__header">
        <h1 className="security-settings__title">Segurança</h1>
      </header>
      <section className="security-settings__card" aria-label="Segurança da conta">
        <dl className="security-settings__grid">
          <div className="security-settings__row">
            <dt>Provedor de identidade</dt>
            <dd>Keycloak</dd>
          </div>
        </dl>
        <p className="security-settings__note">
          Gerencie senha e autenticação no provedor de identidade.
        </p>
        {KEYCLOAK_ACCOUNT_URL ? (
          <a
            href={KEYCLOAK_ACCOUNT_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="security-settings__manage-btn"
          >
            Gerenciar segurança da conta
          </a>
        ) : (
          <p className="security-settings__unavailable">
            O gerenciamento de conta do provedor de identidade não está configurado neste ambiente.
          </p>
        )}
      </section>
    </div>
  );
}
```

No MFA status, no methods list, no security-activity log — per the recon, no backend surfaces any of that to the user themselves, so none of it is rendered (issue #672 §3.3/§3.5).

- [ ] **Step 5: Add `VITE_KEYCLOAK_ACCOUNT_URL` to the env typing/example file found in Step 1**, matching whatever convention `VITE_AUTH_API_BASE_URL` uses there (comment explaining it's the Keycloak Account Console URL for the realm, unset disables the "Gerenciar segurança da conta" link gracefully).

- [ ] **Step 6: Run, verify pass; commit**

```bash
git add apps/web/src/profile/SecuritySettingsPage.tsx apps/web/src/profile/SecuritySettingsPage.css apps/web/src/profile/SecuritySettingsPage.test.tsx <the env file found in Step 1>
git commit -m "feat(profile): add Security settings page linking to the Keycloak account console"
```

---

### Task 14: Delete `ProfilePage`, clean up dead code, finish CSS

**Files:**

- Delete: `apps/web/src/profile/ProfilePage.tsx`, `apps/web/src/profile/ProfilePage.css`, `apps/web/src/profile/ProfilePage.test.tsx`
- Modify: `apps/web/src/App.tsx` (remove the now-unused `ProfilePage` import if any stub remains from Task 2 Step 6)
- Modify: `apps/web/src/profile/profileApi.ts` (remove `updateDisplayName`, `UpdateDisplayNameError`, `UpdateDisplayNameErrorReason`, `updateProfileFields`, `UpdateProfileFieldsError`, `UpdateProfileFieldsErrorReason`, `ProfileFieldsInput` — superseded by `updateProfile` from Task 4, and `ProfilePage.tsx`, their only caller, no longer exists after this task)
- Modify: `apps/web/src/profile/profileApi.test.ts` (remove the now-dead tests for the deleted functions)

- [ ] **Step 1: Confirm nothing else references the functions being deleted**

Run: `grep -rn "updateDisplayName\|updateProfileFields\|UpdateDisplayNameError\|UpdateProfileFieldsError" apps/web/src` — expect matches only inside `profileApi.ts` and `profileApi.test.ts` themselves (and the about-to-be-deleted `ProfilePage.tsx`/`ProfilePage.test.tsx`). If anything else references them, stop and resolve that before deleting.

- [ ] **Step 2: Delete the files, remove the dead exports**

```bash
git rm apps/web/src/profile/ProfilePage.tsx apps/web/src/profile/ProfilePage.css apps/web/src/profile/ProfilePage.test.tsx
```

Edit `profileApi.ts` to remove the four dead exports and their helper `mapUpdateDisplayNameError`/`mapUpdateProfileFieldsError` functions. Edit `profileApi.test.ts` to remove their tests.

- [ ] **Step 3: Run full frontend typecheck + test + build**

```bash
pnpm --filter web typecheck
pnpm --filter web test
pnpm --filter web build
```

Expected: all pass, zero references to the deleted page/functions remain, no unused-import lint errors.

- [ ] **Step 4: Finish responsive CSS pass**

Verify (manually, in the browser — see Task 17) at 1920×1080, 1366×768, 768×1024, 390×844:

- `ProfileSettingsShell`'s content max-width is ~900–1050px and doesn't stretch edge-to-edge on the wide viewport.
- `ProfileTabs` doesn't overflow horizontally on 390×844 — if it does, add `overflow-x: auto` with visible focus rings preserved (never `overflow: hidden` that clips focus).
- `ProfileEditDialog`/`AvatarDialog`/`RevokeSessionDialog` don't get obscured by the mobile virtual keyboard — use `max-height: 90dvh; overflow-y: auto` on the dialog body if not already inherited from the shared dialog CSS pattern.
- `ProfileInfoCard`'s two-column grid collapses to one column under the existing tablet/mobile breakpoint.

Fix any overflow/clipping found; there's no separate code task for this beyond the CSS files already created in Tasks 2/5–13 — this step is the verification pass, not new component work.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore(profile): remove superseded ProfilePage and dead profileApi exports"
```

---

### Task 15: Update `ChatSidebar.test.tsx` for the menu change, add `AppShell.test.tsx` coverage gaps, full suite pass

**Files:**

- Modify: `apps/web/src/chat/ChatSidebar.test.tsx`
- Modify/Create: `apps/web/src/chat/AppShell.test.tsx` (fill in any coverage gap flagged by Step 3 below)

- [ ] **Step 1: Run the full frontend suite with coverage**

```bash
pnpm --filter web test:coverage
```

- [ ] **Step 2: Fix every failing test surfaced by the Task 1/3 refactors**

Expect failures in `ChatSidebar.test.tsx` (old gear link assertions — fixed in Task 3, verify here) and possibly `App.test.tsx` (route tree shape changed — check it doesn't assert the old flat `/profile` route path in a way that breaks; update if so).

- [ ] **Step 3: Check the coverage report for `AppShell.tsx`, `ChatShell.tsx`, `SidebarUserMenu.tsx`, `ProfileSettingsShell.tsx` specifically**

If any file is under the 90% lines/functions/branches/statements threshold, add the missing test case(s) directly (not a blanket "add more tests" — identify the specific uncovered branch from the coverage report and write one targeted test for it).

- [ ] **Step 4: Run `pnpm --filter web lint` and `pnpm --filter web typecheck`**

Fix any lint/type errors.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "test(profile): close coverage gaps from the shell extraction and menu fix"
```

---

### Task 16: E2E specs (Playwright)

Real Playwright infra already exists at `apps/web/e2e/` (`apps/web/playwright.config.ts`), with an established mocking convention: `page.route("**/api/...", (route) => route.fulfill({...}))` per endpoint, helper functions like `mockLoginSuccess`/`mockChatSidebarApi` in `apps/web/e2e/auth.spec.ts`, and `sessionStorage` keys `nchat_at`/`nchat_rt` for token seeding. Mirror that file's helper style, and `apps/web/e2e/messaging/responsive-layout.spec.ts` for the viewport-resize pattern, rather than inventing new conventions.

**Files:**

- Create: `apps/web/e2e/profile-settings.spec.ts`

- [ ] **Step 1: Read `apps/web/e2e/auth.spec.ts` in full and `apps/web/e2e/messaging/responsive-layout.spec.ts` in full**

Copy their exact login-seeding helper (however the suite gets an authenticated `page` — either via the `mockLoginSuccess`+UI-login flow, or a shared fixture/`storageState` if one exists; check for a `apps/web/e2e/fixtures.ts` or similar shared setup file first and reuse it if present instead of duplicating `auth.spec.ts`'s helpers) and the exact viewport values used for the responsive assertions elsewhere, so this new spec uses the same 1920×1080/1366×768/768×1024/390×844 set consistently with the rest of the suite.

- [ ] **Step 2: Write `apps/web/e2e/profile-settings.spec.ts`** covering, at minimum, the issue's own E2E checklist (§ "Testes esperados > E2E"):

```ts
import { expect, test } from "@playwright/test";
// import the shared auth/session helpers found in Step 1

test.describe("Profile & account settings (#672)", () => {
  test.beforeEach(async ({ page }) => {
    // seed an authenticated session + mock GET /api/auth/me, GET /api/chat/sidebar
    // (mirroring auth.spec.ts's mockLoginSuccess/mockChatSidebarApi), then
    // page.goto("/profile")
  });

  test("opens /profile with the sidebar still present, edits and saves the display name without a reload", async ({
    page,
  }) => {
    await expect(page.getByTestId("chat-shell")).toBeVisible();
    await page.getByRole("button", { name: "Editar" }).click();
    await page.getByLabel("Nome de exibição").fill("Novo Nome");
    // mock PATCH /api/auth/me to return the new name
    await page.getByRole("button", { name: /salvar alterações/i }).click();
    await expect(page.getByRole("heading", { name: "Novo Nome" })).toBeVisible();
  });

  test("changes avatar via AvatarDialog and it reflects in the sidebar footer without reload", async ({
    page,
  }) => {
    // mock POST /api/auth/me/avatar; assert sidebar footer <img> src updates
  });

  test("removing the avatar falls back to initials", async ({ page }) => {
    // mock DELETE /api/auth/me/avatar; assert initials fallback renders
  });

  test("navigates all four sections via tabs, and each is a real deep link surviving reload", async ({
    page,
  }) => {
    for (const path of [
      "/profile",
      "/profile/notifications",
      "/profile/security",
      "/profile/sessions",
    ]) {
      await page.goto(path);
      await expect(page).toHaveURL(path);
      // one distinguishing assertion per section's own heading
    }
  });

  test("back/forward preserves the active section", async ({ page }) => {
    await page.goto("/profile/notifications");
    await page.goto("/profile/sessions");
    await page.goBack();
    await expect(page).toHaveURL("/profile/notifications");
    await page.goForward();
    await expect(page).toHaveURL("/profile/sessions");
  });

  test("notifications: sound mode and call ringtone are independent toggles", async ({ page }) => {
    await page.goto("/profile/notifications");
    // toggle each, assert the other's state is unaffected
  });

  test("security: no local password/MFA form exists, and the Keycloak link is present when configured", async ({
    page,
  }) => {
    await page.goto("/profile/security");
    await expect(page.getByLabel(/senha/i)).toHaveCount(0);
  });

  test("sessions: identifies current session, revokes a remote one, and revoke-all-others preserves current", async ({
    page,
  }) => {
    await page.goto("/profile/sessions");
    // mock GET/DELETE /api/auth/me/sessions per the sessionsApi.ts contract from Task 11
    // revoke one row, confirm dialog, assert it's gone from the list
    // revoke-all-others, confirm dialog, assert only "Sessão atual" remains
  });

  test("cannot fetch or revoke another user's session id (BOLA)", async ({ page }) => {
    await page.goto("/profile/sessions");
    // mock DELETE /api/auth/me/sessions/other-users-session-id -> 404 per the
    // real handler's cross-user behavior (session_handler.go never distinguishes
    // "not found" from "not yours"); assert the UI surfaces a generic failure,
    // never a message implying the session existed under someone else
  });

  test("responsive: no horizontal overflow at 1920x1080, 1366x768, 768x1024, 390x844", async ({
    page,
  }) => {
    for (const viewport of [
      { width: 1920, height: 1080 },
      { width: 1366, height: 768 },
      { width: 768, height: 1024 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/profile");
      const hasOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
      );
      expect(hasOverflow).toBe(false);
    }
  });

  test("full keyboard navigation: tabs, edit dialog open/close/Escape, focus returns to trigger", async ({
    page,
  }) => {
    await page.keyboard.press("Tab"); // ... walk to the tabs and the Editar button entirely via keyboard
    // open ProfileEditDialog with Enter, Escape to close, assert focus is back on "Editar"
  });
});
```

Fill in each mocked route with the exact request/response shapes defined in Tasks 4/7/11 (`PATCH /auth/me`, `POST/DELETE /auth/me/avatar`, `GET/DELETE /auth/me/sessions[/id]`) — don't invent different shapes here than what the real client code sends.

- [ ] **Step 3: Run the new spec**

Run: `pnpm --filter web exec playwright test profile-settings.spec.ts` (or whatever script `apps/web/package.json` defines for its Playwright suite — check first).
Expected: all pass locally against the dev build. If the suite requires a running dev server via `playwright.config.ts`'s `webServer` block, let Playwright manage that (don't hand-start `make dev-web` first unless the config requires it).

- [ ] **Step 4: Commit**

```bash
git add apps/web/e2e/profile-settings.spec.ts
git commit -m "test(e2e): add Playwright coverage for the profile/settings redesign"
```

---

### Task 17: Manual verification + quality gates

No code changes in this task — verification only.

- [ ] **Step 1: Launch the app and walk the golden path**

Use the `run` skill (or `make dev-web` directly) to start the dev server. In a real browser:

1. Log in, confirm `/chat` still shows the sidebar, WebSocket connects, an existing conversation opens normally.
2. Click the account menu in the sidebar footer → "Meu perfil" → lands on `/profile` with the sidebar still present and still scrolled/selected where it was.
3. Confirm no visible WS reconnect / loading flash of the channel list when navigating `/chat` ↔ `/profile` back and forth a few times.
4. Click "Editar", change the display name, save, confirm the sidebar footer's name updates without a reload.
5. Click "Trocar foto", upload a new avatar, confirm it updates in the identity card, the sidebar footer, and (open a channel) message author avatars.
6. Navigate to `/profile/notifications`, toggle sound mode and the call ringtone independently, click "Testar som de chamada".
7. Navigate to `/profile/security`, confirm no password/MFA form exists, and the "Gerenciar segurança da conta" link (if `VITE_KEYCLOAK_ACCOUNT_URL` is set in the local `.env`) opens Keycloak in a new tab.
8. Navigate to `/profile/sessions`, confirm the current session is badged, revoke a different session (open a second browser/incognito session first to have one to revoke), confirm the list updates and that second session gets logged out on its next authenticated action.
9. Reload on `/profile/sessions` directly (deep link) — confirms tab + sidebar both render correctly from a cold load.
10. Use browser back/forward across `/profile` → `/profile/notifications` → `/chat` and confirm each lands correctly.
11. Resize to 1920×1080, 1366×768, 768×1024, and use device toolbar at 390×844 — check for horizontal overflow, drawer behavior, and that dialogs remain usable.
12. Keyboard-only pass: Tab through the sidebar footer menu (Escape closes, focus returns to trigger), Tab through `ProfileTabs`, open and close each dialog with keyboard only, confirm focus starts on Cancel in `RevokeSessionDialog`.

Fix anything broken before proceeding — this step exists to catch what unit tests structurally can't (real WS behavior, real layout, real focus behavior across real browser paint timing).

- [ ] **Step 2: Run the full frontend quality gate**

```bash
pnpm lint
pnpm typecheck
pnpm test
pnpm build
pnpm run ci
```

- [ ] **Step 3: Run the repo-level aggregate gates**

```bash
make format-check
make lint
make test
make build
make ci
```

(`go test ./...`, `go vet ./...`, `govulncheck ./...` apply to backend packages only — this PR touches zero Go files, so these should be no-ops/pass trivially; run them anyway since `make ci`/`make test` likely invoke them as part of the aggregate, and confirm they report no regression.)

- [ ] **Step 4: Run security gates**

```bash
make security
```

(covers `gitleaks`/Trivy per `SECURITY.md`; this PR adds no new dependencies and no secrets, so expect a clean pass.)

- [ ] **Step 5: Final diff review against `develop`**

```bash
git diff develop... --stat
```

Confirm: no backend files touched, no dead code left (`ProfilePage.*` gone, `updateDisplayName`/`updateProfileFields` gone), no `console.log`/debug leftovers, no TODO placeholders. Read through the full diff once end to end.

- [ ] **Step 6: Do not commit or open the PR without the user's explicit go-ahead** — report back per the Issue #672 "Entrega" checklist (files changed, architectural decisions, what was and wasn't implemented and why, test/gate results, remaining risks) before creating the PR.

---

## Deliberately not built in this plan (and why)

- **Working hours card** — no backend/admin source of truth found for it anywhere in the codebase. Per issue §1.9, omitting entirely is an explicitly allowed choice (the other allowed choice, an honest "Não configurado" placeholder card, was not taken to avoid shipping a permanently-empty card with no path to ever showing real data in this PR).
- **Per-channel notification preferences, e-mail digest** — no backend persistence exists (issue §2.5/§2.6 explicitly forbid building UI without one).
- **MFA status / methods / security activity log** — no accessible source of truth (Keycloak Admin API access was not found wired into any NChat backend service); issue §3.3/§3.5 explicitly forbid inventing this.
- **Device management UI** (`GET/DELETE/PATCH /auth/me/devices`) — real backend exists but issue #672's acceptance criteria only asks for session revocation, not device renaming/management; adding it would be scope creep beyond the issue.
- **"Encerrar sessão ao fechar o navegador"** — explicitly forbidden by issue §4.7 (no `beforeunload`-based security).
- **Locale/idioma field** — not in the `PATCH /auth/me` contract; not invented.
- **Client-side capability gate for "Administração"** — no such mechanism exists anywhere in `apps/web` today; the existing admin links (`/admin/anti-spam`, `/admin/upload-limit`) rely entirely on server-side enforcement, and this plan follows that same established pattern rather than inventing a new one for just this menu item.
