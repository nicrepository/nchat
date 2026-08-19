import { lazy, Suspense } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";

import "./App.css";
import AdminUsersPage from "./admin/AdminUsersPage";
import AcceptInvitePage from "./auth/AcceptInvitePage";
import ForgotPasswordPage from "./auth/ForgotPasswordPage";
import LoginPage from "./auth/LoginPage";
import OIDCCallbackPage from "./auth/OIDCCallbackPage";
import RequireAuth from "./auth/RequireAuth";
import CallSessionProvider from "./calls/CallSessionProvider";
import DedicatedCallPage from "./calls/DedicatedCallPage";
import ResetPasswordPage from "./auth/ResetPasswordPage";
import ChatPlaceholder from "./chat/ChatPlaceholder";
import ChatShell from "./chat/ChatShell";
import ProfilePage from "./profile/ProfilePage";

const AdminAntiSpamPage = lazy(() => import("./admin/AdminAntiSpamPage"));
const AdminUploadLimitPage = lazy(() => import("./admin/AdminUploadLimitPage"));
const ChatMessageArea = lazy(() => import("./chat/ChatMessageArea"));
const FavoritesPage = lazy(() => import("./chat/FavoritesPage"));
const GlobalSearchPage = lazy(() => import("./search/GlobalSearchPage"));
const LiveKitSpikePage = import.meta.env.DEV
  ? lazy(() => import("./mediaSpike/LiveKitSpikePage"))
  : null;

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/forgot-password" element={<ForgotPasswordPage />} />
        <Route path="/reset-password" element={<ResetPasswordPage />} />
        <Route path="/accept-invite" element={<AcceptInvitePage />} />
        <Route path="/oidc-callback" element={<OIDCCallbackPage />} />

        {LiveKitSpikePage && (
          <Route
            path="/spike/livekit-1to1"
            element={
              <Suspense fallback={<p>Carregando Spike LiveKit…</p>}>
                <LiveKitSpikePage />
              </Suspense>
            }
          />
        )}

        <Route
          element={
            <RequireAuth>
              <CallSessionProvider />
            </RequireAuth>
          }
        >
          <Route path="/call/:callId" element={<DedicatedCallPage />} />
          {/* ── Chat shell (authenticated) ─────────────────────────────── */}
          <Route path="/chat" element={<ChatShell />}>
            <Route index element={<ChatPlaceholder />} />
            <Route
              path="channel/:id"
              element={
                <Suspense fallback={null}>
                  <ChatMessageArea kind="channel" />
                </Suspense>
              }
            />
            <Route
              path="dm/:id"
              element={
                <Suspense fallback={null}>
                  <ChatMessageArea kind="dm" />
                </Suspense>
              }
            />
            <Route
              path="favorites"
              element={
                <Suspense fallback={null}>
                  <FavoritesPage />
                </Suspense>
              }
            />
          </Route>

          {/* ── Profile (authenticated) ───────────────────────────────── */}
          <Route path="/profile" element={<ProfilePage />} />

          {/* ── Admin ─────────────────────────────────────────────────── */}
          <Route path="/admin/users" element={<AdminUsersPage />} />
          {/* RF-19 (issue #419). RequireAuth is the same session gate the other
            admin route uses; the admin-only data behind this page is authorized
            server-side, so reaching the route without the role shows errors
            rather than the policy. */}
          <Route
            path="/admin/anti-spam"
            element={
              <Suspense fallback={null}>
                <AdminAntiSpamPage />
              </Suspense>
            }
          />
          {/* RF-32 (issue #458). Same session gate and same reasoning as the
            anti-spam route above: the policy behind this page is authorized
            server-side, so reaching the route without the role shows errors
            rather than the configuration. */}
          <Route
            path="/admin/upload-limit"
            element={
              <Suspense fallback={null}>
                <AdminUploadLimitPage />
              </Suspense>
            }
          />
          <Route
            path="search"
            element={
              <Suspense fallback={null}>
                <GlobalSearchPage />
              </Suspense>
            }
          />
        </Route>

        {/* ── Redirects ─────────────────────────────────────────────── */}
        <Route path="/" element={<Navigate to="/chat" replace />} />
        <Route path="*" element={<Navigate to="/chat" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
