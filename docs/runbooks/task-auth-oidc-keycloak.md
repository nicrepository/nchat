# Task: Auth OIDC Keycloak Provider (RF-44)

## Traceability

- RF-44: Login via SSO OIDC, with Keycloak as the first provider.
- Branch: `feat/auth-oidc-keycloak-provider`
- Target PR: `develop`

## Scope

This task adds Keycloak OIDC login beside existing email/password auth. It does not replace manual login, password recovery, invites, JWT refresh, session expiry, device/session management, brute-force protection, or failed-login audit.

## Endpoints

Auth-service exposes:

- `GET /auth/oidc/keycloak/login` - starts the Keycloak Authorization Code + PKCE flow.
- `GET /auth/oidc/keycloak/callback` - consumes the provider callback, validates state/nonce/ID token, creates an NChat session, and redirects to the fixed internal frontend callback path with a one-time opaque code.
- `POST /auth/oidc/keycloak/exchange` - consumes the one-time opaque code and returns the normal NChat login JSON shape.

The web app uses `/oidc-callback` to exchange the opaque code. NChat access and refresh tokens are not placed in URL query parameters.

## Required Environment

OIDC is disabled by default. To enable it, set:

| Variable                      | Notes                                                                                           |
| ----------------------------- | ----------------------------------------------------------------------------------------------- |
| `OIDC_ENABLED=true`           | Enables OIDC routes.                                                                            |
| `OIDC_PROVIDER_NAME=keycloak` | Only Keycloak is supported in this PR.                                                          |
| `OIDC_ISSUER_URL`             | Keycloak realm issuer, for example `https://keycloak.example.com/realms/nchat`.                 |
| `OIDC_CLIENT_ID`              | Keycloak confidential client id.                                                                |
| `OIDC_CLIENT_SECRET`          | Keycloak confidential client secret. Store only in secrets.                                     |
| `OIDC_REDIRECT_URL`           | Backend callback URL, for example `https://auth.example.com/auth/oidc/keycloak/callback`.       |
| `OIDC_FRONTEND_CALLBACK_URL`  | Fixed relative frontend callback path. Must be `/oidc-callback`.                                |
| `OIDC_SCOPES`                 | Defaults to `openid email profile`.                                                             |
| `OIDC_HTTP_TIMEOUT_SECONDS`   | Defaults to `10`.                                                                               |
| `OIDC_STATE_TTL_MINUTES`      | Defaults to `10`.                                                                               |
| `OIDC_AUTO_PROVISION_ENABLED` | Defaults to `false`; set `true` only when automatic OIDC user creation is intended.             |
| `OIDC_ALLOWED_EMAIL_DOMAINS`  | Optional comma-separated domain allowlist. Empty allows all verified emails and logs a warning. |

If `OIDC_ENABLED=false`, OIDC endpoints return disabled responses. If `OIDC_ENABLED=true` but required config, DB, or JWT dependencies are missing, the endpoints fail closed with unavailable responses.

## Local Keycloak Setup

1. Start a local Keycloak instance and create a realm named `nchat`.
2. Create a confidential OIDC client for the web app.
3. Enable Authorization Code flow.
4. Configure the backend redirect URI exactly as `OIDC_REDIRECT_URL`, for example `http://localhost:8081/auth/oidc/keycloak/callback`.
5. Configure allowed web origins for the frontend origin if Keycloak requires it.
6. Create or import test users with verified email addresses.
7. Set auth-service env vars and start auth-service plus the web app.
8. Click `Entrar com Keycloak` on `/login`.

For local web development through the gateway, `VITE_AUTH_API_BASE_URL` can stay on the default `/api/auth`; the SSO button navigates to `/api/auth/oidc/keycloak/login`.

## Kubernetes And Sealed Secrets

`infra/k8s/base/configmap.yaml` contains non-secret defaults such as `OIDC_ENABLED=false`, provider name, scopes, timeout, TTL, auto-provision, and the optional domain allowlist.

Provider-specific values are wired in `infra/k8s/base/services/auth-service/deployment.yaml` through `secretKeyRef` entries against `nchat-secrets`:

- `OIDC_ISSUER_URL`
- `OIDC_CLIENT_ID`
- `OIDC_CLIENT_SECRET`
- `OIDC_REDIRECT_URL`
- `OIDC_FRONTEND_CALLBACK_URL`

Use `infra/k8s/secrets/templates/nchat-secrets.template.yaml` as the unsealed source template and seal it with the existing Sealed Secrets workflow. Do not commit real Keycloak secrets.

## User Linking Behavior

MVP linking is conservative:

- Existing OIDC user with `(external_provider='keycloak', external_subject=sub)` logs in.
- New verified OIDC email creates an active OIDC user when `OIDC_AUTO_PROVISION_ENABLED=true`.
- New verified OIDC email is rejected when auto-provision is disabled.
- If the email already belongs to a manual user, auth-service does not silently link it. The response is generic account-link-required/conflict behavior.
- Suspended, locked, deleted, or otherwise inactive users are rejected generically.

Future admin-controlled account linking is explicitly out of scope for this PR.

### Provisioning model: JIT, not pre-provisioning

Provisioning is **just-in-time**. The NChat account is created during
`GET /auth/oidc/keycloak/callback`, in the same transaction as the session, on
the user's **first successful SSO login**.

Creating a user in Keycloak does **not** make that user appear in NChat. There
is no SCIM endpoint, no Keycloak Admin API sync, no event listener and no
polling — NChat learns a person exists only from the claims of a login they
performed themselves. A Keycloak account that has never signed in to NChat has
no row in `auth.users`.

### Concurrent first login

Two callbacks completing at the same instant for a subject with no account both
reach the provisioning insert; nothing earlier locks a row that does not exist
yet. The insert is `ON CONFLICT DO NOTHING`, so the loser gets no row back
instead of aborting its transaction, and then re-reads by
`(external_provider, external_subject)`:

- the subject is now present — it lost the race against its own first login, and
  signs in to the account the winner created;
- the subject is still absent — the constraint that fired was
  `users_email_unique`, so the address belongs to an account this subject does
  not own, and the login is refused with the same conflict as any other manual
  e-mail collision.

The partial unique index on `(external_provider, external_subject)` is what
makes a duplicate account impossible; this branch is what keeps the second
browser from seeing a 500.

### Workspace membership is provisioned with the account (issue #604)

JIT provisioning creates the `auth.users` row **and** the
`chat.workspace_members` row, inside the same transaction that creates the
session and the exchange code. Without it a successful SSO login produced a user
every chat endpoint answered 403 to, because chat authorization is the workspace
membership.

The rules:

- the workspace is resolved by the logical rule `slug = 'default' AND status =
'active'`, never by the seed UUID;
- the membership is always `role = 'member'`, `status = 'active'`. NChat RBAC
  stays internal and server-side: no Keycloak role, group or claim is mapped,
  and `owner`, `admin` and `moderator` are never granted implicitly;
- it happens **only** when this login created the account. A login by an
  existing user never writes the table, so a `suspended` or `left` membership is
  not reactivated, a deliberately removed membership is not recreated, and an
  existing role is never rewritten;
- if there is no active default workspace, or the membership cannot be
  inserted, provisioning fails and the whole transaction rolls back — no
  account, no session, no exchange code. The login returns 500; the fix is
  operational (restore the default workspace), not a retry.

The auth-service writes one table of the chat schema for this. That is
deliberate: the alternatives (a synchronous call to chat-service, or an
asynchronous event) both allow a login to complete while the user is still
unusable, and neither exists in the platform today. The runtime role `nchat_app`
already had `INSERT` on `chat` (`scripts/db/grant-runtime.sql`), so no new grant
was needed.

Manually created users are **not** covered by this: `MemberService.JoinWorkspace`
still has no HTTP caller, so an account created by an administrator continues to
need its membership created out of band.

## Security Notes

- State, nonce, and exchange codes are stored only as domain-separated HMAC-SHA-256 hashes.
- PKCE verifier and one-time exchange values are encrypted with AES-256-GCM using HKDF-derived keys from the existing JWT HMAC secret material for MVP.
- The provider authorization code, state, nonce, PKCE verifier, provider ID token, provider access token, provider refresh token, and NChat refresh token are not logged.
- Provider access and refresh tokens are not persisted.
- The callback redirect target is the fixed relative path in `OIDC_FRONTEND_CALLBACK_URL`; absolute, protocol-relative, and CR/LF-containing values are rejected.
- ID tokens are validated for issuer, audience/client id, expiration, nonce, signature via JWKS, and Keycloak MVP algorithm `RS256`.
- `email_verified=false` or absent is rejected.
- Domain allowlist failures are generic and do not enumerate accounts.
- SQL uses parameters; OIDC routes reuse the token endpoint rate limiter pattern.

## Keycloak Configuration Required

NChat does not configure Keycloak. An administrator must create the client in
the **existing corporate realm** — do not create a new realm, and do not modify
users or clients belonging to oCIS or any other service.

| Setting                     | Required value                                                     |
| --------------------------- | ------------------------------------------------------------------ |
| Realm                       | the existing corporate realm (the same one oCIS uses)              |
| Client ID                   | a new client dedicated to NChat                                    |
| Client authentication       | ON (confidential client — the code exchange sends `client_secret`) |
| Standard flow               | ON (Authorization Code)                                            |
| Direct access grants        | OFF                                                                |
| Implicit flow               | OFF                                                                |
| Service accounts            | OFF                                                                |
| Valid redirect URI          | exactly the value of `OIDC_REDIRECT_URL`, no wildcard              |
| Web origins                 | the NChat web origin only (`+` or `*` must not be used)            |
| Client scopes               | `openid`, `email`, `profile` — nothing else is read                |
| Required claims in ID token | `iss`, `sub`, `aud`, `exp`, `nonce`, `email`, `email_verified`     |
| Optional claims used        | `name`, `given_name`, `family_name`, `preferred_username`          |
| ID token signature          | RS256 (the only algorithm accepted)                                |

Users must have a **verified** e-mail: `email_verified=false` or an absent
`email` is rejected, and no account is provisioned.

`sub` is the identity key. `preferred_username` and `email` are treated as
mutable profile data and are never used to correlate an account.

### Values the administrator must supply

These cannot be derived from the repository and are environment-specific:

1. `OIDC_ISSUER_URL` — the realm issuer, e.g. `https://<keycloak-host>/realms/<realm>`.
2. `OIDC_CLIENT_ID` — the client id created above.
3. `OIDC_CLIENT_SECRET` — the client secret (secrets only; never committed).
4. `OIDC_REDIRECT_URL` — the public auth-service callback URL, which must match
   the Valid redirect URI character for character.
5. `OIDC_ALLOWED_EMAIL_DOMAINS` — the corporate domain(s). Leaving this empty
   permits every verified address in the realm and logs a startup warning.
6. Confirmation of whether `OIDC_AUTO_PROVISION_ENABLED` should be `true`
   (JIT provisioning) or stay `false` (SSO only for accounts that already exist).

## Out Of Scope

- Azure AD
- Google Workspace
- MFA enforcement
- SCIM / directory sync
- Admin provider UI
- Advanced/manual-to-OIDC account linking
- Provider refresh token persistence or lifecycle
- Dedicated OIDC encryption key rotation

## Validation

Run the standard validation suite from the PR task, including:

```bash
bash -n scripts/db/migrate.sh scripts/ci/migrations-check.sh
pnpm migrations:check
cd services/auth-service && go test -count=1 ./...
pnpm lint:web
pnpm typecheck:web
pnpm test:web
semgrep scan --config p/secrets --config p/owasp-top-ten --config p/golang services/auth-service apps/web migrations/auth docs/runbooks/task-auth-oidc-keycloak.md
```
