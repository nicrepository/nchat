import { configDefaults, defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// O SonarQube resolve os caminhos do LCOV a partir da raiz do repositorio, e o
// lcovonly os escreve relativos a este projectRoot. Sem isto o relatorio sairia
// com "src/..." e nenhum arquivo seria reconhecido na analise do monorepo.
const repositoryRoot = fileURLToPath(new URL("../..", import.meta.url));

export default defineConfig({
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
    allowedHosts: ["nchat.local"],
    proxy: {
      "/api/media": {
        target: "http://127.0.0.1:8087",
        changeOrigin: false,
        rewrite: (path) => path.replace(/^\/api\/media/, ""),
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/setupTests.ts",
    exclude: [...configDefaults.exclude, "e2e/**", "**/e2e/**", "**/*.e2e.*"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html", ["lcovonly", { projectRoot: repositoryRoot }]],
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
