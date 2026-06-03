import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";

import "./App.css";
import AcceptInvitePage from "./auth/AcceptInvitePage";
import ForgotPasswordPage from "./auth/ForgotPasswordPage";
import LoginPage from "./auth/LoginPage";
import OIDCCallbackPage from "./auth/OIDCCallbackPage";
import RequireAuth from "./auth/RequireAuth";
import ResetPasswordPage from "./auth/ResetPasswordPage";
import HomePage from "./home/HomePage";

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/forgot-password" element={<ForgotPasswordPage />} />
        <Route path="/reset-password" element={<ResetPasswordPage />} />
        <Route path="/accept-invite" element={<AcceptInvitePage />} />
        <Route path="/oidc-callback" element={<OIDCCallbackPage />} />
        <Route
          path="/"
          element={
            <RequireAuth>
              <HomePage />
            </RequireAuth>
          }
        />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
