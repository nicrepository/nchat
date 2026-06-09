#!/usr/bin/env node
/**
 * Staging auth smoke test — nchat
 *
 * Validates the auth-service backend contract against a live staging environment.
 * All interactions use environment variables; no credentials are hardcoded or printed.
 *
 * Usage:
 *   node scripts/staging/auth-smoke.mjs
 *   STAGING_EXPECT_SHORT_TTL=true node scripts/staging/auth-smoke.mjs
 *
 * Required env vars:
 *   STAGING_API_BASE_URL             auth-service base URL
 *   STAGING_ALLOWED_ORIGIN           exact expected API origin (mandatory for all runs)
 *   STAGING_AUTH_EMAIL               primary test account e-mail
 *   STAGING_AUTH_PASSWORD            primary test account password
 *   STAGING_TEST_ACCOUNT_CONFIRM     must equal STAGING_AUTH_EMAIL exactly
 *   STAGING_AUTH_DESTRUCTIVE_CONFIRM must equal I_UNDERSTAND_THIS_REVOKES_TEST_SESSIONS
 *
 * Optional env vars:
 *   STAGING_ALLOW_LOCAL                  "true" to allow http://localhost / http://127.0.0.1
 *   STAGING_TEST_ACCOUNT_DOMAIN          allowed test account domain (alternative to prefix check)
 *   STAGING_SECOND_AUTH_EMAIL            second test account e-mail (must be paired with password)
 *   STAGING_SECOND_AUTH_PASSWORD         second test account password (must be paired with email)
 *   STAGING_SECOND_TEST_ACCOUNT_CONFIRM  must equal STAGING_SECOND_AUTH_EMAIL exactly (required when used)
 *   STAGING_OIDC_ISSUER_URL              OIDC issuer base URL; enables suite E when set
 *   STAGING_EXPECT_SHORT_TTL             "true" to run expiry assertions (requires short TTL env)
 *   STAGING_REQUEST_TIMEOUT_MS           per-request timeout in ms, integer 1000–30000 (default: 15000)
 *
 * This script never prints token values; all assertions use only structural checks.
 * Destructive operations only affect sessions created by this script.
 * Cleanup failures are reported and cause a non-zero exit.
 */

// ---------------------------------------------------------------------------
// Raw env reads — never log these directly
// ---------------------------------------------------------------------------

const RAW_API_URL = process.env.STAGING_API_BASE_URL ?? "";
const RAW_ALLOWED_ORIGIN = process.env.STAGING_ALLOWED_ORIGIN ?? "";
const EMAIL = process.env.STAGING_AUTH_EMAIL ?? "";
const PASSWORD = process.env.STAGING_AUTH_PASSWORD ?? "";
const EMAIL2 = process.env.STAGING_SECOND_AUTH_EMAIL ?? "";
const PASSWORD2 = process.env.STAGING_SECOND_AUTH_PASSWORD ?? "";
const OIDC_ISSUER_RAW = (process.env.STAGING_OIDC_ISSUER_URL ?? "").replace(/\/$/, "");
const SHORT_TTL = process.env.STAGING_EXPECT_SHORT_TTL === "true";

// Set by validateEnvironment(); use only these in fetch calls and console output.
let API_ORIGIN = "";
let OIDC_ISSUER_ORIGIN = "";
let TIMEOUT_MS = 15_000;

// Credential-bearing URL parameter names, built programmatically.
const CRED_QUERY_KEYS = [
  ...["access", "refresh", "id", "code"].map((p) => p + "_" + "token"),
  "token",
  "jwt",
];

// ---------------------------------------------------------------------------
// Fail-closed environment guard
// ---------------------------------------------------------------------------

function abort(msg) {
  console.error(`\nERROR: ${msg}`);
  process.exit(1);
}

/**
 * Validate a test account email.
 * Local-part must start with "nchat-smoke-" or "nchat-test-", OR the domain must
 * equal STAGING_TEST_ACCOUNT_DOMAIN. Both the naming policy AND explicit confirmation
 * are required — neither alone is sufficient.
 */
function validateTestAccount(email, emailVar, confirmVar) {
  const atIdx = email.indexOf("@");
  if (atIdx < 1) {
    abort(`${emailVar} must be a valid email address.`);
  }
  const localPart = email.slice(0, atIdx).toLowerCase();
  const domain = email.slice(atIdx + 1).toLowerCase();
  const testDomain = (process.env.STAGING_TEST_ACCOUNT_DOMAIN ?? "").toLowerCase().trim();

  const meetsNamingPolicy =
    localPart.startsWith("nchat-smoke-") ||
    localPart.startsWith("nchat-test-") ||
    (testDomain !== "" && domain === testDomain);

  if (!meetsNamingPolicy) {
    abort(
      `${emailVar} local-part must start with 'nchat-smoke-' or 'nchat-test-', ` +
        `or set STAGING_TEST_ACCOUNT_DOMAIN to an allowed test domain.`,
    );
  }

  const confirm = process.env[confirmVar] ?? "";
  if (!confirm) {
    abort(`${confirmVar} must be set to the exact value of ${emailVar}.`);
  }
  if (confirm !== email) {
    abort(
      `${confirmVar} does not match ${emailVar}. Both naming policy and exact confirmation are required.`,
    );
  }
}

function validateEnvironment() {
  if (!RAW_API_URL || !EMAIL || !PASSWORD) {
    abort("STAGING_API_BASE_URL, STAGING_AUTH_EMAIL, and STAGING_AUTH_PASSWORD must be set.");
  }

  // --- Parse and validate API URL ---
  let apiUrl;
  try {
    apiUrl = new URL(RAW_API_URL);
  } catch {
    abort("STAGING_API_BASE_URL is not a valid URL.");
  }
  if (apiUrl.username || apiUrl.password) {
    abort("STAGING_API_BASE_URL must not contain userinfo (user:password@host).");
  }
  if (apiUrl.search) {
    abort("STAGING_API_BASE_URL must not contain a query string.");
  }
  if (apiUrl.hash) {
    abort("STAGING_API_BASE_URL must not contain a fragment.");
  }

  const allowLocal = process.env.STAGING_ALLOW_LOCAL === "true";
  const isLocal = apiUrl.hostname === "localhost" || apiUrl.hostname === "127.0.0.1";

  // Only https: is allowed for non-local. For local, only http: when STAGING_ALLOW_LOCAL=true.
  const protocolOk =
    apiUrl.protocol === "https:" || (apiUrl.protocol === "http:" && isLocal && allowLocal);
  if (!protocolOk) {
    abort(
      "STAGING_API_BASE_URL must use https:. " +
        "Only http: is accepted for localhost/127.0.0.1 with STAGING_ALLOW_LOCAL=true. " +
        "Protocols ftp:, file:, ws:, wss: and all others are rejected.",
    );
  }

  // Only the sanitized origin (protocol + host) is ever logged or used in fetch.
  API_ORIGIN = apiUrl.origin;

  // --- Production origin guard (independent of allowlist) ---
  const host = apiUrl.hostname.toLowerCase();
  const allowProdLike =
    process.env.STAGING_ALLOW_PRODUCTION_LIKE_ORIGIN === "I_UNDERSTAND_THIS_IS_DANGEROUS";
  const isProdLike =
    /(?:^|\.)prod(?:\.|$)/.test(host) ||
    /(?:^|\.)production(?:\.|$)/.test(host) ||
    host === "api.nic-labs.com";
  if (isProdLike && !allowProdLike) {
    abort(
      "STAGING_API_BASE_URL appears to be a production host. " +
        "Refusing to run against production. " +
        "If this is intentional, set STAGING_ALLOW_PRODUCTION_LIKE_ORIGIN=I_UNDERSTAND_THIS_IS_DANGEROUS.",
    );
  }

  // --- STAGING_ALLOWED_ORIGIN: mandatory for all runs, including local ---
  if (!RAW_ALLOWED_ORIGIN) {
    abort(
      "STAGING_ALLOWED_ORIGIN must be set to the exact expected API origin (e.g. https://api.staging.example.com).",
    );
  }
  let allowedOriginUrl;
  try {
    allowedOriginUrl = new URL(RAW_ALLOWED_ORIGIN);
  } catch {
    abort("STAGING_ALLOWED_ORIGIN is not a valid URL.");
  }
  if (allowedOriginUrl.username || allowedOriginUrl.password) {
    abort("STAGING_ALLOWED_ORIGIN must not contain userinfo.");
  }
  if (allowedOriginUrl.search) {
    abort("STAGING_ALLOWED_ORIGIN must not contain a query string.");
  }
  if (allowedOriginUrl.hash) {
    abort("STAGING_ALLOWED_ORIGIN must not contain a fragment.");
  }
  if (allowedOriginUrl.pathname !== "/" && allowedOriginUrl.pathname !== "") {
    abort("STAGING_ALLOWED_ORIGIN must contain only an origin (protocol + host), no path.");
  }
  const ALLOWED_ORIGIN = allowedOriginUrl.origin;
  if (API_ORIGIN !== ALLOWED_ORIGIN) {
    // Log only sanitized origins — never log raw env var values.
    abort(`API origin mismatch. Expected: ${ALLOWED_ORIGIN}. Got: ${API_ORIGIN}.`);
  }

  // --- Timeout: full integer regex, then bounds check ---
  const rawTimeoutStr = process.env.STAGING_REQUEST_TIMEOUT_MS ?? "15000";
  if (!/^\d+$/.test(rawTimeoutStr)) {
    abort(
      "STAGING_REQUEST_TIMEOUT_MS must be a positive integer string (no decimals, no suffixes).",
    );
  }
  const rawTimeout = parseInt(rawTimeoutStr, 10);
  if (rawTimeout < 1000 || rawTimeout > 30_000) {
    abort("STAGING_REQUEST_TIMEOUT_MS must be between 1000 and 30000 (default: 15000).");
  }
  TIMEOUT_MS = rawTimeout;

  // --- Explicit destructive opt-in ---
  const CONFIRM_VALUE = "I_UNDERSTAND_THIS_REVOKES_TEST_SESSIONS";
  if (process.env.STAGING_AUTH_DESTRUCTIVE_CONFIRM !== CONFIRM_VALUE) {
    abort(`Set STAGING_AUTH_DESTRUCTIVE_CONFIRM=${CONFIRM_VALUE} to confirm destructive tests.`);
  }

  // --- Test account validation (naming policy + explicit confirmation, both required) ---
  validateTestAccount(EMAIL, "STAGING_AUTH_EMAIL", "STAGING_TEST_ACCOUNT_CONFIRM");

  if (EMAIL2 || PASSWORD2) {
    if (!EMAIL2 || !PASSWORD2) {
      abort(
        "STAGING_SECOND_AUTH_EMAIL and STAGING_SECOND_AUTH_PASSWORD must both be set or both absent.",
      );
    }
    validateTestAccount(EMAIL2, "STAGING_SECOND_AUTH_EMAIL", "STAGING_SECOND_TEST_ACCOUNT_CONFIRM");
  }

  // --- OIDC issuer (optional; validated here so suite E can use OIDC_ISSUER_ORIGIN) ---
  if (OIDC_ISSUER_RAW) {
    let u;
    try {
      u = new URL(OIDC_ISSUER_RAW);
    } catch {
      abort("STAGING_OIDC_ISSUER_URL is not a valid URL.");
    }
    if (u.protocol !== "https:") abort("STAGING_OIDC_ISSUER_URL must use HTTPS.");
    if (u.username || u.password) abort("STAGING_OIDC_ISSUER_URL must not contain userinfo.");
    OIDC_ISSUER_ORIGIN = u.origin;
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** @typedef {{ access_token: string, refresh_token: string, token_type: string, expires_in: number }} TokenPair */
/** @typedef {{ id: string, email: string, display_name: string, must_change_password: boolean }} UserInfo */
/** @typedef {{ id: string, current: boolean }} SessionInfo */

let passed = 0;
let failed = 0;
const failures = [];

// Cleanup errors are collected here; reported at the end and cause non-zero exit.
const cleanupErrors = [];

/** @param {string} name @param {() => Promise<void>} fn */
async function test(name, fn) {
  process.stdout.write(`  ${name} ... `);
  try {
    await fn();
    console.log("PASS");
    passed++;
  } catch (/** @type {unknown} */ err) {
    const msg = err instanceof Error ? err.message : String(err);
    console.log(`FAIL — ${msg}`);
    failed++;
    failures.push({ name, msg });
  }
}

/** @param {string} path @param {RequestInit} [init] @returns {Promise<Response>} */
async function api(path, init) {
  return fetch(`${API_ORIGIN}${path}`, {
    signal: AbortSignal.timeout(TIMEOUT_MS),
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

/** @param {Response} res @param {number} expected */
function assertStatus(res, expected) {
  if (res.status !== expected) throw new Error(`expected HTTP ${expected}, got ${res.status}`);
}

/** @param {unknown} value @param {string} msg */
function assert(value, msg) {
  if (!value) throw new Error(msg);
}

/**
 * Returns true if any credential-bearing parameter key appears in the URL's
 * decoded query string or fragment. Fails closed: malformed URL returns true.
 * Keys are normalized to lowercase before comparison.
 * @param {string} rawUrl
 */
function containsCredentialBearingUrlParam(rawUrl) {
  let u;
  try {
    u = new URL(rawUrl);
  } catch {
    return true; // Malformed URL — fail closed.
  }
  const qKeys = [...u.searchParams.keys()].map((k) => k.toLowerCase());
  const fKeys = [...new URLSearchParams(u.hash.replace(/^#/, "")).keys()].map((k) =>
    k.toLowerCase(),
  );
  return CRED_QUERY_KEYS.some((k) => qKeys.includes(k) || fKeys.includes(k));
}

/** @param {string} email @param {string} password @param {string} [label] @returns {Promise<TokenPair & { user: UserInfo }>} */
async function login(email, password, label = "smoke") {
  const res = await api("/auth/login", {
    method: "POST",
    body: JSON.stringify({ email, password, device_name: label }),
  });
  assertStatus(res, 200);
  return /** @type {any} */ (res.json());
}

/** @param {string} refreshToken @returns {Promise<TokenPair>} */
async function refresh(refreshToken) {
  const res = await api("/auth/refresh", {
    method: "POST",
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
  assertStatus(res, 200);
  return /** @type {any} */ (res.json());
}

/** @param {string} refreshToken */
async function logout(refreshToken) {
  const res = await api("/auth/logout", {
    method: "POST",
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
  assertStatus(res, 204);
}

/**
 * Attempt logout for cleanup; collect failure without silently discarding it.
 * @param {string} rt
 * @param {string} label  Human-readable label for error reporting (no token values).
 */
async function cleanupLogout(rt, label) {
  if (!rt) return;
  try {
    await logout(rt);
  } catch (err) {
    cleanupErrors.push({
      label,
      msg: err instanceof Error ? err.message : String(err),
    });
  }
}

/** @param {string} accessToken @returns {Promise<Response>} */
async function getSessions(accessToken) {
  return api("/auth/me/sessions", {
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${accessToken}` },
  });
}

/** @param {string} accessToken @param {string} sessionId @returns {Promise<Response>} */
async function deleteSession(accessToken, sessionId) {
  return api(`/auth/me/sessions/${sessionId}`, {
    method: "DELETE",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${accessToken}` },
  });
}

/** @param {string} accessToken @returns {Promise<SessionInfo[]>} */
async function listSessionIds(accessToken) {
  const res = await getSessions(accessToken);
  assertStatus(res, 200);
  const body = /** @type {any} */ (await res.json());
  return body.data;
}

/** @param {number} ms */
function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

// ---------------------------------------------------------------------------
// Suite A — Basic auth smoke
// ---------------------------------------------------------------------------

async function suiteA() {
  console.log("\n[A] Basic auth smoke");

  let at = "";
  let rt = "";

  try {
    await test("login with valid credentials returns 200 + token pair + user", async () => {
      const r = await login(EMAIL, PASSWORD, "smoke-a");
      // Register for cleanup immediately before any shape assertions can throw.
      if (typeof r.refresh_token === "string") rt = r.refresh_token;
      assert(typeof r.access_token === "string" && r.access_token, "access_token missing");
      assert(typeof r.refresh_token === "string" && r.refresh_token, "refresh_token missing");
      assert(r.token_type === "Bearer", "token_type must be Bearer");
      assert(typeof r.expires_in === "number", "expires_in must be a number");
      assert(r.user?.email, "user.email missing");
      at = r.access_token;
    });

    await test("access token calls GET /auth/me/sessions (active-session protected) → 200", async () => {
      const res = await getSessions(at);
      assertStatus(res, 200);
      const body = /** @type {any} */ (await res.json());
      assert(Array.isArray(body.data), "sessions.data must be an array");
    });

    await test("unauthenticated request to protected endpoint returns 401", async () => {
      const res = await api("/auth/me/sessions", {
        headers: { "Content-Type": "application/json" },
      });
      assertStatus(res, 401);
    });

    await test("wrong password returns 401 with generic error code", async () => {
      const res = await api("/auth/login", {
        method: "POST",
        body: JSON.stringify({ email: EMAIL, password: "smoke-placeholder" }),
      });
      assertStatus(res, 401);
      const body = /** @type {any} */ (await res.json());
      assert(body.error?.code === "invalid_credentials", "error code must be invalid_credentials");
      assert(typeof body.error?.message === "string", "error message must be a string");
    });
  } finally {
    await cleanupLogout(rt, "suite-A");
  }
}

// ---------------------------------------------------------------------------
// Suite B — Refresh flow with family-revocation assertions
// ---------------------------------------------------------------------------

async function suiteB() {
  console.log("\n[B] Refresh flow");

  let originalRT = "";
  let rotatedRT = "";
  let rotatedAT = "";

  try {
    const s = await login(EMAIL, PASSWORD, "smoke-b");
    originalRT = s.refresh_token; // registered for cleanup immediately

    await test("login returns both access and refresh tokens", async () => {
      assert(s.access_token, "access_token missing");
      assert(s.refresh_token, "refresh_token missing");
    });

    await test("valid refresh token returns new rotated token pair", async () => {
      const r = await refresh(originalRT);
      // Register new tokens for cleanup before any shape assertions can throw.
      if (typeof r.refresh_token === "string") rotatedRT = r.refresh_token;
      if (typeof r.access_token === "string") rotatedAT = r.access_token;
      assert(r.access_token, "new access_token missing");
      assert(r.refresh_token, "new refresh_token missing");
      assert(r.refresh_token !== originalRT, "refresh token must rotate on use");
    });

    await test("replaying rotated (old) refresh token returns 401", async () => {
      const res = await api("/auth/refresh", {
        method: "POST",
        body: JSON.stringify({ refresh_token: originalRT }),
      });
      assertStatus(res, 401);
    });

    // Reuse detection revokes the session family; current rotated token must also be rejected.
    await test("after reuse, current rotated refresh token is also rejected (family revoked)", async () => {
      const res = await api("/auth/refresh", {
        method: "POST",
        body: JSON.stringify({ refresh_token: rotatedRT }),
      });
      assertStatus(res, 401);
    });

    await test("after family revocation, access token from rotated pair returns 401 on active-session endpoint", async () => {
      const res = await getSessions(rotatedAT);
      assertStatus(res, 401);
    });

    await test("arbitrary invalid refresh token returns 401", async () => {
      const res = await api("/auth/refresh", {
        method: "POST",
        body: JSON.stringify({ refresh_token: "smoke-placeholder" }),
      });
      assertStatus(res, 401);
    });
  } finally {
    // Session already revoked by reuse detection; logout is idempotent.
    await cleanupLogout(rotatedRT || originalRT, "suite-B");
  }
}

// ---------------------------------------------------------------------------
// Suite C — Logout / session revocation
// ---------------------------------------------------------------------------

async function suiteC() {
  console.log("\n[C] Logout and session revocation");

  // C1: logout invalidates RT and AT (active-session check).
  await (async () => {
    let rt = "";
    let at = "";
    try {
      const s = await login(EMAIL, PASSWORD, "smoke-c1");
      rt = s.refresh_token;
      at = s.access_token;

      await test("GET /auth/me/sessions returns current session in list", async () => {
        const sessions = await listSessionIds(at);
        assert(sessions.length > 0, "session list must not be empty");
        assert(
          sessions.some((x) => x.current),
          "at least one session must be marked current",
        );
      });

      await test("logout returns 204", async () => {
        await logout(rt);
      });

      await test("after logout, refresh token returns 401", async () => {
        const res = await api("/auth/refresh", {
          method: "POST",
          body: JSON.stringify({ refresh_token: rt }),
        });
        assertStatus(res, 401);
      });

      await test("after logout, access token returns 401 on active-session endpoint", async () => {
        const res = await getSessions(at);
        assertStatus(res, 401);
      });
    } finally {
      await cleanupLogout(rt, "suite-C1"); // idempotent
    }
  })();

  // C2: cross-session revocation with hard prerequisites outside test() wrapper.
  await (async () => {
    console.log("\n  [C2] Cross-session revocation (same primary account)");

    let s1RT = "";
    let s2RT = "";
    let s2AT = "";

    try {
      const s1 = await login(EMAIL, PASSWORD, "smoke-c2");
      s1RT = s1.refresh_token;

      const beforeIds = new Set((await listSessionIds(s1.access_token)).map((x) => x.id));

      const s2 = await login(EMAIL, PASSWORD, "smoke-c2b");
      s2RT = s2.refresh_token;
      s2AT = s2.access_token;

      // Hard prerequisite: session diff must yield exactly one new session.
      // Not wrapped in test() — failure aborts the suite rather than continuing with stale state.
      const after = await listSessionIds(s1.access_token);
      const newSessions = after.filter((x) => !beforeIds.has(x.id));
      if (newSessions.length !== 1) {
        throw new Error(
          `C2 prereq: expected exactly 1 new session after second login, got ${newSessions.length}`,
        );
      }
      const targetSessionId = newSessions[0].id;

      // Hard prerequisite: session deletion must succeed before refresh assertions.
      const deleteRes = await deleteSession(s1.access_token, targetSessionId);
      if (deleteRes.status !== 204) {
        throw new Error(`C2 prereq: session deletion returned HTTP ${deleteRes.status}`);
      }

      // Soft verifications — failure is reported but does not abort.
      await test("revoked session: refresh token returns 401", async () => {
        const res = await api("/auth/refresh", {
          method: "POST",
          body: JSON.stringify({ refresh_token: s2RT }),
        });
        assertStatus(res, 401);
      });

      await test("revoked session: access token returns 401 on active-session endpoint", async () => {
        const res = await getSessions(s2AT);
        assertStatus(res, 401);
      });

      await test("revoking session does not invalidate current (s1) session", async () => {
        const res = await getSessions(s1.access_token);
        assertStatus(res, 200);
      });
    } finally {
      await cleanupLogout(s2RT, "suite-C2-s2");
      await cleanupLogout(s1RT, "suite-C2-s1");
    }
  })();

  // C3 (optional): cross-user revocation must be rejected.
  if (EMAIL2 && PASSWORD2) {
    console.log("\n  [C3] Cross-user revocation attempt (second account available)");

    let primaryRT = "";
    let otherRT = "";

    try {
      const primary = await login(EMAIL, PASSWORD, "smoke-c3");
      primaryRT = primary.refresh_token;

      const other = await login(EMAIL2, PASSWORD2, "smoke-c3b");
      otherRT = other.refresh_token;

      await test("cross-user DELETE /auth/me/sessions/{id} returns 404", async () => {
        const primarySessions = await listSessionIds(primary.access_token);
        const primaryCurrent = primarySessions.find((x) => x.current);
        assert(primaryCurrent, "primary current session not found");
        const res = await deleteSession(other.access_token, primaryCurrent.id);
        assert(
          res.status === 404 || res.status === 401,
          `expected 404 or 401 for cross-user revocation, got ${res.status}`,
        );
      });
    } finally {
      await cleanupLogout(primaryRT, "suite-C3-primary");
      await cleanupLogout(otherRT, "suite-C3-other");
    }
  } else {
    console.log("  [C3] SKIPPED — STAGING_SECOND_AUTH_EMAIL not set");
  }
}

// ---------------------------------------------------------------------------
// Suite D — Token expiry (requires STAGING_EXPECT_SHORT_TTL=true)
// ---------------------------------------------------------------------------

async function suiteD() {
  console.log("\n[D] Token expiration");

  if (!SHORT_TTL) {
    console.log("  SKIPPED — set STAGING_EXPECT_SHORT_TTL=true to enable.");
    console.log("  Prerequisite: deploy staging with AUTH_ACCESS_TOKEN_TTL_SECONDS <= 5,");
    console.log("    run this suite, then restore TTL config. See runbook for details.");
    return;
  }

  let rt = "";

  try {
    const s = await login(EMAIL, PASSWORD, "smoke-d");
    rt = s.refresh_token;
    const capturedAT = s.access_token;

    await test("after short TTL wait, expired access token returns 401", async () => {
      console.log("\n    Waiting 12 s for short-TTL access token to expire...");
      await sleep(12_000);
      const res = await getSessions(capturedAT);
      assertStatus(res, 401);
    });

    await test("valid refresh token after AT expiry returns new token pair", async () => {
      const r = await refresh(rt);
      assert(r.access_token, "new access_token missing");
      assert(r.refresh_token, "new refresh_token missing");
      rt = r.refresh_token; // update for cleanup
      await logout(rt);
      rt = ""; // already cleaned up
    });
  } finally {
    await cleanupLogout(rt, "suite-D");
  }
}

// ---------------------------------------------------------------------------
// Suite E — SSO/OIDC smoke (requires STAGING_OIDC_ISSUER_URL)
// ---------------------------------------------------------------------------

async function suiteE() {
  console.log("\n[E] SSO/OIDC smoke");

  if (!OIDC_ISSUER_ORIGIN) {
    console.log("  SKIPPED — set STAGING_OIDC_ISSUER_URL to enable.");
    return;
  }

  await test("OIDC login redirects to configured issuer with HTTPS and expected OIDC params", async () => {
    const res = await api("/auth/oidc/keycloak/login", { redirect: "manual" });
    assert(
      res.status === 302 || res.status === 307 || res.status === 308,
      `expected redirect, got ${res.status}`,
    );
    const location = res.headers.get("location") ?? "";
    assert(location.length > 0, "Location header must be present");

    let redirectUrl;
    try {
      redirectUrl = new URL(location);
    } catch {
      throw new Error("Location header is not a valid URL");
    }

    assert(redirectUrl.protocol === "https:", "OIDC redirect must use HTTPS");
    assert(
      !redirectUrl.username && !redirectUrl.password,
      "OIDC redirect must not contain userinfo",
    );
    assert(
      redirectUrl.origin === OIDC_ISSUER_ORIGIN,
      `redirect origin must match issuer ${OIDC_ISSUER_ORIGIN}`,
    );

    const qp = redirectUrl.searchParams;
    assert(qp.has("state"), "OIDC redirect must include 'state' param");
    assert(
      qp.has("client_id") || qp.has("response_type"),
      "OIDC redirect must include OIDC params",
    );

    assert(
      !containsCredentialBearingUrlParam(location),
      "OIDC redirect must not contain credential-bearing token fields in query or fragment",
    );
  });

  console.log("  NOTE: Full OIDC callback/exchange flow requires manual verification.");
  console.log("        See runbook for step-by-step instructions.");
}

// ---------------------------------------------------------------------------
// Suite F — Security surface checks
// ---------------------------------------------------------------------------

async function suiteF() {
  console.log("\n[F] Security surface checks");

  await test("login response contains no X-NChat-Admin-Token header", async () => {
    const res = await api("/auth/login", {
      method: "POST",
      body: JSON.stringify({ email: EMAIL, password: PASSWORD }),
    });
    const body = /** @type {any} */ (await res.json());
    const rt =
      res.status === 200 && typeof body.refresh_token === "string" ? body.refresh_token : "";
    try {
      assert(!res.headers.get("x-nchat-admin-token"), "X-NChat-Admin-Token must not appear");
    } finally {
      // Cleanup in finally so assertion failure cannot leak the session.
      await cleanupLogout(rt, "suite-F-login");
    }
  });

  await test("login error response Content-Type is application/json", async () => {
    const res = await api("/auth/login", {
      method: "POST",
      body: JSON.stringify({ email: EMAIL, password: "smoke-placeholder" }),
    });
    const ct = res.headers.get("content-type") ?? "";
    assert(ct.includes("application/json"), `Content-Type must be JSON, got: ${ct}`);
  });

  await test("OIDC login redirect does not contain credential-bearing token fields in URL", async () => {
    const res = await api("/auth/oidc/keycloak/login", { redirect: "manual" });
    const location = res.headers.get("location") ?? "";
    if (location) {
      assert(
        !containsCredentialBearingUrlParam(location),
        "OIDC redirect must not contain credential-bearing token fields",
      );
    }
  });
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main() {
  validateEnvironment();

  console.log("nchat staging auth smoke test");
  console.log(`Target: ${API_ORIGIN}`);
  console.log("─".repeat(60));

  await suiteA();
  await suiteB();
  await suiteC();
  await suiteD();
  await suiteE();
  await suiteF();

  console.log("\n" + "─".repeat(60));
  console.log(`Results: ${passed} passed, ${failed} failed`);

  if (failures.length > 0) {
    console.log("\nFailed tests:");
    for (const f of failures) {
      console.log(`  ✗ ${f.name}`);
      console.log(`    ${f.msg}`);
    }
  }

  if (cleanupErrors.length > 0) {
    console.error("\nCleanup failures (sessions may remain active):");
    for (const e of cleanupErrors) {
      console.error(`  ✗ [${e.label}]: ${e.msg}`);
    }
  }

  if (failures.length > 0 || cleanupErrors.length > 0) {
    process.exit(1);
  }

  console.log("\nAll checks passed.");
}

main().catch((err) => {
  console.error("Unexpected error:", err instanceof Error ? err.message : err);
  process.exit(1);
});
