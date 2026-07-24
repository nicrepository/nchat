import { lazy, Suspense } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";

import "./App.css";
import AdminUsersPage from "./admin/AdminUsersPage";
import AcceptInvitePage from "./auth/AcceptInvitePage";
import ForgotPasswordPage from "./auth/ForgotPasswordPage";
import LoginPage from "./auth/LoginPage";
import OIDCCallbackPage from "./auth/OIDCCallbackPage";
import RequireAuth from "./auth/RequireAuth";
import ResetPasswordPage from "./auth/ResetPasswordPage";
import ChatPlaceholder from "./chat/ChatPlaceholder";
import ChatShell from "./chat/ChatShell";
import ProfilePage from "./profile/ProfilePage";

const ChatMessageArea = lazy(() => import("./chat/ChatMessageArea"));
const FavoritesPage = lazy(() => import("./chat/FavoritesPage"));
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

        {/* ── Chat shell (authenticated) ─────────────────────────────── */}
        <Route
          path="/chat"
          element={
            <RequireAuth>
              <ChatShell />
            </RequireAuth>
          }
        >
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
        <Route
          path="/profile"
          element={
            <RequireAuth>
              <ProfilePage />
            </RequireAuth>
          }
        />

        {/* ── Admin ─────────────────────────────────────────────────── */}
        <Route
          path="/admin/users"
          element={
            <RequireAuth>
              <AdminUsersPage />
            </RequireAuth>
          }
        />

        {/* ── Redirects ─────────────────────────────────────────────── */}
        <Route path="/" element={<Navigate to="/chat" replace />} />
        <Route path="*" element={<Navigate to="/chat" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
