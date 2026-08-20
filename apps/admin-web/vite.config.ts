import { configDefaults, defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The administrative console is a separate bundle with a separate dev server.
// Nothing here imports from apps/web: the console must be able to mount, and to
// evolve, without the chat application.
export default defineConfig({
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5174,
    strictPort: true,
    allowedHosts: ["admin.nchat.local"],
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/setupTests.ts",
    exclude: [...configDefaults.exclude, "e2e/**", "**/e2e/**", "**/*.e2e.*"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html"],
      reportsDirectory: "../../coverage/admin-web",
      exclude: [...(configDefaults.coverage?.exclude ?? []), "src/main.tsx", "e2e/**"],
      thresholds: {
        lines: 90,
        functions: 90,
        branches: 90,
        statements: 90,
      },
    },
  },
});
