// Unit tests of the e2e helpers' plain logic: no browser, no jsdom. The app's
// own Vitest run excludes e2e/**, and Playwright collects *.spec/*.test files,
// so these are *.vitest.ts and run with: vitest run --config e2e/vitest.config.ts
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    root: fileURLToPath(new URL(".", import.meta.url)),
    include: ["**/*.vitest.ts"],
    environment: "node",
  },
});
