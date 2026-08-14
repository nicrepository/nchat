# Local Project Launcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide one Linux command that starts the complete NChat development environment locally and opens it in the default browser.

**Architecture:** A Bash launcher orchestrates the repository's existing Docker, migration, gateway, Go-service, and pnpm commands. Runtime PIDs and logs live under ignored `.nchat-local/`; secrets and environment overrides remain local and are generated only when absent.

**Tech Stack:** Bash, Docker Compose v2, Go 1.25+, Node 24, pnpm 11, Vite, Traefik.

**Spec:** User request in the 2026-08-13 session.

## Global Constraints

- Linux only.
- All application services run locally.
- Preserve existing source changes and existing local environment files.
- Never commit generated secrets.
- Reuse existing project scripts instead of duplicating their internals.

---

### Task 1: Launcher contract tests

**Files:**
- Create: `scripts/dev/nchat-local-test.sh`

**Interfaces:**
- Consumes: `scripts/dev/nchat-local.sh --help`, `status`, and `check`.
- Produces: a repeatable shell test for launcher syntax and command contract.

- [ ] **Step 1: Write a test that requires the launcher commands and safe check mode**
- [ ] **Step 2: Run it and verify it fails because the launcher is absent**

### Task 2: Complete local launcher

**Files:**
- Create: `scripts/dev/nchat-local.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: existing `scripts/dev/dev-env-*.sh`, `scripts/db/migrate.sh`, `scripts/dev/dev-gateway-*.sh`, seven Go entrypoints, and `pnpm dev:web`.
- Produces: `up`, `down`, `restart`, `status`, `logs`, `check`, and `--help` commands.

- [ ] **Step 1: Implement prerequisite and port checks**
- [ ] **Step 2: Generate missing ignored local configuration and cryptographic development secrets**
- [ ] **Step 3: Start infrastructure, migrations, backends, frontend, gateway, and browser in order**
- [ ] **Step 4: Implement PID-safe stop, status, and log commands**
- [ ] **Step 5: Add Makefile entrypoints**
- [ ] **Step 6: Run the launcher contract test and shell syntax checks**

### Task 3: End-to-end verification

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: completed launcher.
- Produces: documented single-command startup and verified local endpoints.

- [ ] **Step 1: Document the single-command workflow**
- [ ] **Step 2: Run `check` and repository config checks**
- [ ] **Step 3: Run `up --no-browser`, verify health endpoints, then report exact local URLs**

