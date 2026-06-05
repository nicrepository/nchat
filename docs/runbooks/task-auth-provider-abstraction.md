# Auth: Identity Provider Abstraction

## What this introduces

`IdentityProviderSlug`, `IdentityProviderKind`, and `ProviderRegistry` — a typed abstraction
that lets multiple identity providers coexist in the auth-service without changing any existing
Keycloak behaviour.

## Provider status

| Slug               | Kind    | Status |
|--------------------|---------|--------|
| `keycloak`         | OIDC    | Fully implemented and enabled |
| `azure_ad`         | OIDC    | Config adapters only — constructor exists, not wired to env vars yet |
| `google_workspace` | OIDC    | Config adapters only — constructor exists, not wired to env vars yet |
| `samba_ad`         | LDAP/AD | `DirectoryProvider` interface placeholder only — cannot be used as OIDC |

Azure AD and Google Workspace providers are **abstraction-ready but not fully enabled**
unless their dedicated env vars are added and the app wiring is extended.

## Existing Keycloak env vars (unchanged)

```
OIDC_ENABLED=true
OIDC_PROVIDER_NAME=keycloak          # also validates as azure_ad, google_workspace
OIDC_ISSUER_URL=https://...
OIDC_CLIENT_ID=...
OIDC_CLIENT_SECRET=...
OIDC_REDIRECT_URL=...
OIDC_FRONTEND_CALLBACK_URL=/oidc-callback
OIDC_SCOPES=openid email profile
OIDC_HTTP_TIMEOUT_SECONDS=10
OIDC_STATE_TTL_MINUTES=10
OIDC_AUTO_PROVISION_ENABLED=false
OIDC_ALLOWED_EMAIL_DOMAINS=
```

## Enabling Azure AD in a future release

1. Add env vars: `AZURE_AD_TENANT_ID`, `AZURE_AD_CLIENT_ID`, `AZURE_AD_CLIENT_SECRET`, `AZURE_AD_REDIRECT_URL`
2. In `app.go`, call `service.NewAzureADProvider(service.AzureADProviderConfig{TenantID: ..., ClientID: ..., ...})`
3. Register the result: `registry.Register(domain.IdentityProviderSlugAzureAD, azureProvider)`
4. Set `OIDC_PROVIDER_NAME=azure_ad` in the deployment

The existing route `GET /auth/oidc/keycloak/login` etc. delegates to whatever provider slug
is configured — the `/keycloak/` in the URL is a naming artifact from the MVP; it does not
restrict which provider is active.

## Enabling Google Workspace in a future release

Same pattern as Azure AD using `service.NewGoogleWorkspaceProvider` and
`domain.IdentityProviderSlugGoogleWorkspace`. Issuer URL is pre-set to
`https://accounts.google.com`.

## SambaAD

SambaAD uses LDAP/Kerberos, not OIDC. It implements `service.DirectoryProvider`, not
`service.OIDCProvider`. Attempting to register `samba_ad` as an OIDC provider returns an
error. Full LDAP bind/login is out of scope for this abstraction PR.

## Migration notes

Existing deployments using `OIDC_PROVIDER_NAME=keycloak` (or omitting the variable entirely)
are unaffected. The only breaking change is that `OIDC_PROVIDER_NAME=samba_ad` now fails
config validation — it was previously invalid ("not keycloak") and would also have failed.
