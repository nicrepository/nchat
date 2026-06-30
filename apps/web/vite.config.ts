import { configDefaults, defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
    allowedHosts: ["nchat.local"],
  },
  build: {
    rollupOptions: {
      output: {
        // ponytail: TipTap individual extensions; re-add manualChunks when
        // bundle measurement justifies splitting.
        manualChunks: {
          "tiptap-vendor": [
            "@tiptap/core",
            "@tiptap/react",
            "@tiptap/extension-bold",
            "@tiptap/extension-italic",
            "@tiptap/extension-code",
            "@tiptap/extension-code-block",
            "@tiptap/extension-bullet-list",
            "@tiptap/extension-ordered-list",
            "@tiptap/extension-list-item",
            "@tiptap/extension-hard-break",
            "@tiptap/extension-history",
            "@tiptap/extension-document",
            "@tiptap/extension-paragraph",
            "@tiptap/extension-text",
          ],
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/setupTests.ts",
    exclude: [...configDefaults.exclude, "e2e/**", "**/e2e/**", "**/*.e2e.*"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html"],
      reportsDirectory: "../../coverage/web",
      thresholds: {
        lines: 90,
        functions: 90,
        branches: 90,
        statements: 90,
      },
    },
  },
});
