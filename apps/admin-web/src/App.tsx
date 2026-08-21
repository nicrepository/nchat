import { BrowserRouter, Route, Routes } from "react-router";

import "./styles.css";
import LoginPage from "./auth/LoginPage";
import OIDCCallbackPage from "./auth/OIDCCallbackPage";
import AuditPage from "./pages/AuditPage";
import ChannelsPage from "./pages/ChannelsPage";
import FilesPage from "./pages/FilesPage";
import NotFoundPage from "./pages/NotFoundPage";
import OverviewPage from "./pages/OverviewPage";
import SecurityPage from "./pages/SecurityPage";
import UsersPage from "./pages/UsersPage";
import AdminSessionProvider from "./session/AdminSessionProvider";
import { useAdminSession } from "./session/useAdminSession";
import AdminLayout from "./shell/AdminLayout";

/**
 * The console's one gate.
 *
 * Every administrative route is behind it, and it renders from the session
 * state alone — never from the URL. That is what makes a deep link into
 * `/audit` identical to clicking through to it: both mount this component
 * first, and an unauthenticated visitor gets the sign-in screen either way.
 *
 * It is a user-experience gate, not a security boundary. Nothing here protects
 * data; the Admin API refuses the same requests whether or not this component
 * ever rendered.
 */
function AdminGate() {
  const { status, message, reload } = useAdminSession();

  switch (status) {
    case "loading":
      return (
        <main className="admin-login" id="admin-main">
          <p role="status">Carregando console administrativo…</p>
        </main>
      );
    case "unauthenticated":
    case "forbidden":
    case "unavailable":
      return <LoginPage />;
    case "error":
      return (
        <main className="admin-login" id="admin-main">
          <h1>Console indisponível</h1>
          <p role="alert" className="admin-alert">
            {message}
          </p>
          <button type="button" className="admin-button" onClick={reload}>
            Tentar novamente
          </button>
        </main>
      );
    case "ready":
      return <AdminLayout />;
  }
}

export default function App() {
  return (
    <BrowserRouter>
      <AdminSessionProvider>
        <Routes>
          {/* The SSO return is the one route that must render before a session
              exists: it is what creates one. */}
          <Route path="/oidc-callback" element={<OIDCCallbackPage />} />
          <Route element={<AdminGate />}>
            <Route index element={<OverviewPage />} />
            {/* Routing is not authorization. Each of these renders a page that
                asks the API, and the API refuses a principal without the
                capability whether or not the sidebar drew the link. */}
            <Route path="users" element={<UsersPage />} />
            <Route path="channels" element={<ChannelsPage />} />
            <Route path="security" element={<SecurityPage />} />
            <Route path="files" element={<FilesPage />} />
            <Route path="audit" element={<AuditPage />} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Routes>
      </AdminSessionProvider>
    </BrowserRouter>
  );
}
