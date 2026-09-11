import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import eslintConfigPrettier from "eslint-config-prettier";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist", "coverage"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
    },
  },
  {
    // Only the live WebSocket path may reach the presentation layer (issue
    // #750). Announcing a message — a toast, a chime, an OS notification —
    // means "this just happened", so a path that is recovering state must not
    // be able to reach it: hydration, pagination, the sidebar's reconnect
    // refetch and the subscription resync are state ingestion only.
    //
    // This is the enforcement of that boundary rather than a convention about
    // it. useChatSidebar.ts holds the one live handler and is the only allowed
    // importer; everything else fails the build. Tests are exempt because the
    // module's own suite has to call it.
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/chat/useChatSidebar.ts", "src/chat/notificationPresentation.ts", "**/*.test.*"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              group: ["**/notificationPresentation"],
              message:
                "Only the live WebSocket handler (useChatSidebar.ts) may present a notification (#750). Recovered state — hydration, pagination, reconnect refetch, resync — is ingested, never announced.",
            },
          ],
        },
      ],
    },
  },
  {
    // The Service Worker is a classic script with worker globals, not a
    // module in the application bundle (issue #747).
    files: ["public/sw.js"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "script",
      globals: globals.serviceworker,
    },
  },
  eslintConfigPrettier,
);
