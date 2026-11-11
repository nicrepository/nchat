import { defineConfig, devices } from "@playwright/test";
import base from "./playwright.config";

const baseURL = process.env.E2E_BASE_URL;
if (!baseURL) throw new Error("Set E2E_BASE_URL to http://127.0.0.1:<available-port>");
const url = new URL(baseURL);
if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || !url.port) {
  throw new Error("E2E_BASE_URL must use http://127.0.0.1 with an explicit available port");
}

export default defineConfig({
  ...base,
  testMatch: "**/resize-observer-gate.spec.ts",
  testIgnore: [],
  workers: 1,
  retries: 0,
  projects: [...(base.projects ?? []), { name: "webkit", use: devices["Desktop Safari"] }],
  use: { ...base.use, baseURL, trace: "on", screenshot: "only-on-failure" },
  webServer: {
    ...base.webServer,
    command: `pnpm dev --host 127.0.0.1 --port ${url.port} --strictPort`,
    url: baseURL,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
