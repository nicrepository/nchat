import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import App from "./App";
import { registerNotificationServiceWorker } from "./notifications/serviceWorkerRegistration";
import { startWebPushLifecycle } from "./notifications/webPushReconciler";

// Registered before render and never from a component: the worker exists to
// run when no page does. It resolves null on any browser that cannot have
// one, and never rejects, so the app boots the same either way (issue #747).
void registerNotificationServiceWorker();

// One set of global listeners for the whole page, started outside React for the
// same reason: focus and visibility describe the tab, not a component tree. It
// diagnoses and repairs Web Push, and never prompts for permission (issue #748).
startWebPushLifecycle();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
