import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import App from "./App";
import { registerNotificationServiceWorker } from "./notifications/serviceWorkerRegistration";

// Registered before render and never from a component: the worker exists to
// run when no page does. It resolves null on any browser that cannot have
// one, and never rejects, so the app boots the same either way (issue #747).
void registerNotificationServiceWorker();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
