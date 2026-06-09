# Demo Interna — Auth MVP (nchat)

> **Audience:** Internal demo presenters and developers validating the auth MVP.
> **Scope:** Manual and SSO login, password recovery, invite activation, session/device
> management, and admin users screen — as implemented.
>
> ⚠️ This runbook covers the **current state** of the MVP. Several flows are partial.
> Read the _"O que NÃO demonstrar ainda"_ section before the demo.

---

## Pré-requisitos

### Ambiente

| Item          | Requisito                                                                     |
| ------------- | ----------------------------------------------------------------------------- |
| Ambiente alvo | Staging (nunca produção)                                                      |
| Auth service  | Rodando e acessível via HTTPS staging                                         |
| Frontend web  | Rodando, configurado com `VITE_AUTH_API_BASE_URL` apontando para auth service |
| PostgreSQL    | Migrations aplicadas (`auth` schema presente)                                 |
| Keycloak      | Realm `nchat` configurado (necessário apenas para demo SSO)                   |
| Email         | Serviço de email handoff configurado (necessário para recuperação e convite)  |

### Variáveis de ambiente necessárias (auth-service)

Nunca inserir valores reais aqui. Consultar secrets do ambiente de staging.

| Variável                         | Descrição                                                                                    |
| -------------------------------- | -------------------------------------------------------------------------------------------- |
| `DATABASE_URL`                   | Conexão PostgreSQL staging                                                                   |
| `AUTH_JWT_HMAC_SECRET`           | Secret HMAC para assinar JWTs                                                                |
| `AUTH_JWT_ISSUER`                | Issuer do JWT                                                                                |
| `AUTH_JWT_AUDIENCE`              | Audience do JWT                                                                              |
| `AUTH_ACCESS_TOKEN_TTL_SECONDS`  | TTL do access token (padrão: 900)                                                            |
| `AUTH_REFRESH_TOKEN_TTL_SECONDS` | TTL do refresh token (padrão: 604800)                                                        |
| `ADMIN_BOOTSTRAP_TOKEN`          | Token do guard admin bootstrap (apenas CLI/servidor — nunca no browser)                      |
| `OIDC_ENABLED`                   | `true` para habilitar SSO Keycloak                                                           |
| `OIDC_PROVIDER_NAME`             | `keycloak`                                                                                   |
| `OIDC_ISSUER_URL`                | URL do realm Keycloak (ex: `https://keycloak.example.com/realms/nchat`)                      |
| `OIDC_CLIENT_ID`                 | Client ID do Keycloak                                                                        |
| `OIDC_CLIENT_SECRET`             | Client secret do Keycloak (apenas em secrets)                                                |
| `OIDC_REDIRECT_URL`              | URL de callback backend (ex: `https://auth.staging.example.com/auth/oidc/keycloak/callback`) |
| `OIDC_FRONTEND_CALLBACK_URL`     | Deve ser `/oidc-callback` (fixo)                                                             |
| `EMAIL_FROM`                     | Endereço de envio para recovery e convites                                                   |

### Contas de teste

> **Regra de nomenclatura obrigatória:**
> O local-part do email deve começar com `nchat-smoke-` ou `nchat-test-`,
> ou o domínio deve ser um domínio de teste configurado em `STAGING_TEST_ACCOUNT_DOMAIN`.
> Nunca usar contas reais de usuários.

| Conta                                 | Propósito                                                                        |
| ------------------------------------- | -------------------------------------------------------------------------------- |
| `nchat-test-demo@<dominio-staging>`   | Conta principal para login manual, recovery, sessões                             |
| `nchat-test-demo2@<dominio-staging>`  | Conta secundária para multi-device / sessão paralela                             |
| `nchat-test-invite@<dominio-staging>` | Endereço para receber convite (não precisa existir antes)                        |
| `nchat-test-sso@<dominio-keycloak>`   | Conta no Keycloak para SSO (domínio configurado em `OIDC_ALLOWED_EMAIL_DOMAINS`) |

**Criação das contas de teste** (uma vez, via bootstrap — nunca via browser):

```bash
# Criar conta principal de teste
curl -s -X POST \
  -H "X-NChat-Admin-Token: <ADMIN_BOOTSTRAP_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "nchat-test-demo@<dominio-staging>",
    "display_name": "Demo User",
    "initial_password": "<senha_temporaria_forte>",
    "must_change_password": false
  }' \
  https://auth.staging.example.com/admin/users
```

Substituir `<ADMIN_BOOTSTRAP_TOKEN>` pelo valor do secret de staging (nunca hardcodar).

### Pressupostos Keycloak / staging

- Realm `nchat` existe no Keycloak staging.
- Client confidencial configurado com `Authorization Code` flow habilitado.
- URI de redirect exatamente igual a `OIDC_REDIRECT_URL`.
- A conta SSO de teste já existe no Keycloak e tem email verificado.
- `OIDC_AUTO_PROVISION_ENABLED=true` OU a conta SSO já foi importada/criada localmente.
- `OIDC_ALLOWED_EMAIL_DOMAINS` configurado com o domínio da conta de teste SSO (se aplicável).

---

## Script de demo — passo a passo

### 1. Login manual

**Objetivo:** demonstrar autenticação email/senha, geração de tokens, guard de rota frontend.

**Fluxo:**

1. Abrir `https://staging.example.com/login` no browser.
2. Preencher:
   - E-mail: `nchat-test-demo@<dominio-staging>`
   - Senha: `<senha_da_conta_de_teste>`
3. Clicar em **Entrar**.
4. Aguardar redirecionamento para `/` (tela principal do chat).

**Resultado esperado:**

- Redirecionamento para `/` sem erro.
- Tokens armazenados em `sessionStorage` (ou equivalente).
- `RequireAuth.tsx` não redireciona de volta para `/login`.

**O que mostrar no DevTools (opcional):**

- Network → `POST /api/auth/login` → 200 com `access_token`, `refresh_token`, `user`.
- Nenhum token aparece na URL.

**Erro esperado se credenciais erradas:**

- Mensagem _"E-mail ou senha inválidos. Tente novamente."_ na tela.
- `POST /api/auth/login` → 401 com `"error": "invalid_credentials"`.

---

### 2. Logout

**Objetivo:** demonstrar invalidação do refresh token e limpeza de sessão no frontend.

**Fluxo** (a partir de uma sessão autenticada):

1. Acionar logout na interface (botão ou menu).
2. Aguardar redirecionamento para `/login`.

**Resultado esperado:**

- `POST /api/auth/logout` → 204.
- Tokens removidos do storage.
- Tentativa de navegar para rota protegida redireciona para `/login`.

**Nota:** Mesmo com um refresh token inválido/já revogado, o logout retorna 204 (idempotente).

---

### 3. Recuperação de senha

**Objetivo:** demonstrar o fluxo RF-48 — esqueci senha → email → reset.

> ⚠️ **Dependência de email:** O serviço de email handoff deve estar configurado no ambiente
> de staging. Sem ele, `POST /auth/password/forgot` retorna 503.

**Fluxo:**

1. Na tela de login, clicar em **"Esqueci minha senha"** → abre `/forgot-password`.
2. Preencher o e-mail da conta de teste: `nchat-test-demo@<dominio-staging>`.
3. Clicar em **Enviar**.
4. Resultado esperado: mensagem de confirmação na tela (202 Accepted).
5. Verificar caixa de entrada do endereço de teste.
6. Clicar no link de reset no email → abre `/reset-password?token=<token>`.
7. Preencher nova senha (mínimo 12 caracteres, letras maiúsculas, minúsculas, número e símbolo).
8. Confirmar reset → 204 → tela de sucesso → redirecionar para `/login`.
9. Fazer login com a nova senha para confirmar.

**Resultado esperado:**

- `POST /api/auth/password/forgot` → 202 (mesmo se email não existir — anti-enumeração).
- `POST /api/auth/password/reset` → 204 na primeira chamada com token válido.
- Segunda tentativa com o mesmo token → 401 `invalid_token` (single-use).

**Nota de segurança:** O token nunca aparece no corpo de resposta da API; só chega via email.

---

### 4. Convite e ativação de conta

**Objetivo:** demonstrar o fluxo RF-46 — admin cria convite → usuário ativa conta.

> ⚠️ **Dependência de email:** Igual à recuperação de senha.
> **Requer `ADMIN_BOOTSTRAP_TOKEN`:** A criação do convite usa o endpoint bootstrap,
> que **não** é acessível pelo browser. Use o script CLI abaixo antes do demo.

**Passo 1 — Criar convite (CLI, pré-demo):**

```bash
curl -s -X POST \
  -H "X-NChat-Admin-Token: <ADMIN_BOOTSTRAP_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "nchat-test-invite@<dominio-staging>",
    "display_name": "Usuário Convidado"
  }' \
  https://auth.staging.example.com/admin/invites
```

Anotar o `id` retornado para referência. Não exibir o token (ele vai por email).

**Passo 2 — Ativar conta (browser):**

1. Verificar caixa de entrada de `nchat-test-invite@<dominio-staging>`.
2. Clicar no link de convite no email → abre `/accept-invite?token=<token>`.
3. Preencher:
   - Nome de exibição (obrigatório)
   - Nome completo (opcional)
   - Senha (política: ≥12 chars, uppercase, lowercase, número, símbolo)
   - Confirmar senha
4. Clicar em **Ativar conta**.
5. Resultado: mensagem de sucesso → clicar em **Ir para entrar**.
6. Fazer login com o email e senha definidos.

**Resultado esperado:**

- `POST /api/auth/invites/accept` → 201 com `id`, `email`, `display_name`, `created_at`.
- Segunda tentativa com o mesmo token → 401 `invalid_invite_token` (single-use).
- Login com as credenciais recém-criadas funciona normalmente.

---

### 5. SSO Keycloak — redirect / callback / exchange

**Objetivo:** demonstrar o fluxo RF-44 — login via Keycloak Authorization Code + PKCE.

> ⚠️ **Dependência de Keycloak:** Requer Keycloak staging configurado. Se `OIDC_ENABLED=false`,
> o botão pode estar visível mas o endpoint retorna 404.

**Fluxo:**

1. Abrir `/login` no browser.
2. Clicar em **"Entrar com Keycloak"**.
3. Browser redireciona para Keycloak (`GET /auth/oidc/keycloak/login` → 302 → Keycloak).
4. Fazer login no Keycloak com a conta de teste SSO: `nchat-test-sso@<dominio-keycloak>`.
5. Keycloak redireciona para `GET /auth/oidc/keycloak/callback?code=...&state=...`.
6. Auth service valida state/nonce/ID token, cria sessão NChat, redireciona para `/oidc-callback?code=<one-time-code>`.
7. Frontend (`OIDCCallbackPage.tsx`) detecta o `code`, chama `POST /api/auth/oidc/keycloak/exchange`.
8. Browser vai para `/` após tokens recebidos.

**Resultado esperado:**

- `/auth/oidc/keycloak/login` → 302 para Keycloak.
- `/auth/oidc/keycloak/callback` → 302 para `/oidc-callback?code=<opaque_code>`.
- `POST /api/auth/oidc/keycloak/exchange` → 200 com tokens (mesma shape do login manual).
- O `?code=` é removido da URL do browser **antes** do POST (via `history.replaceState`).
- Login bem-sucedido, redirecionamento para `/`.

**O que mostrar no DevTools (opcional):**

- Network → exchange → 200 com `access_token` e `refresh_token`.
- URL do browser mostra `/oidc-callback` limpo (sem `code=`) após o exchange.

**Nota:** `OIDC_AUTO_PROVISION_ENABLED=true` cria automaticamente a conta NChat se o email
Keycloak não tiver um usuário local correspondente. Se `false`, o usuário precisa já existir
localmente com o mesmo email e `auth_source = oidc`.

---

### 6. Refresh automático / persistência de sessão

**Objetivo:** demonstrar RF-51 — tokens se renovam automaticamente; sessão persiste sem re-login.

**Fluxo (opção A — aguardar expiração, requer TTL curto):**

> Requer ambiente de staging com `AUTH_ACCESS_TOKEN_TTL_SECONDS` reduzido (ex: 60s).

1. Fazer login.
2. Aguardar o access token expirar (observar Network no DevTools).
3. Qualquer ação autenticada dispara refresh automático silencioso.
4. Verificar: `POST /api/auth/refresh` → 200 com novos tokens, sem re-login.

**Fluxo (opção B — inspecionar via DevTools):**

1. Fazer login.
2. DevTools → Application → Session Storage → copiar `refresh_token` (valor opaco).
3. Aguardar qualquer ação autenticada ou forçar via `authClient.ts` hook.
4. Verificar Network: `POST /api/auth/refresh` → 200.
5. O access token anterior não funciona mais após rotação.

**Resultado esperado:**

- `POST /api/auth/refresh` → 200 com novos `access_token` e `refresh_token`.
- Token anterior é revogado automaticamente (rotação).
- Usuário continua logado sem interrupção.

**Nota:** O smoke test de staging
(`scripts/staging/auth-smoke.mjs` com `STAGING_EXPECT_SHORT_TTL=true`)
valida esse comportamento automaticamente.

---

### 7. Revogação de sessão e dispositivo

**Objetivo:** demonstrar RF-53 — listar sessões/dispositivos e revogar remotamente.

> **Nota:** No MVP, a tela de gerenciamento de sessões/dispositivos não está implementada
> no frontend web. Esta demo usa `curl` diretamente para mostrar o contrato de API.

**Passo 1 — Fazer login e capturar access token:**

```bash
ACCESS_TOKEN="<access_token_do_login>"
```

(Copiar do DevTools → Network → login → response body. Não commitar.)

**Passo 2 — Listar sessões:**

```bash
curl -s \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  https://auth.staging.example.com/auth/me/sessions | jq .
```

Resposta esperada: array com pelo menos a sessão atual (`"current": true`).

**Passo 3 — Revogar sessão específica:**

```bash
curl -s -X DELETE \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  https://auth.staging.example.com/auth/me/sessions/<session_id>
```

Resposta esperada: 204.

**Passo 4 — Revogar todas as outras sessões (manter a atual):**

```bash
curl -s -X DELETE \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  https://auth.staging.example.com/auth/me/sessions
```

Resposta esperada: 204.

**Passo 5 — Listar dispositivos:**

```bash
curl -s \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  https://auth.staging.example.com/auth/me/devices | jq .
```

Resposta esperada: array de dispositivos com `meta.max_devices_per_user`.

**Passo 6 — Revogar dispositivo:**

```bash
curl -s -X DELETE \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  https://auth.staging.example.com/auth/me/devices/<device_id>
```

Resposta esperada: 204.

**Resultado esperado:**

- Sessão revogada via DELETE → token de refresh da sessão invalidado.
- Revogação de dispositivo invalida todas as sessões desse dispositivo.
- IP retornado mascarado: IPv4 `a.b.*.*`, IPv6 `prefix:*`.

**RF mapping:** RF-51 (expiração), RF-53 (sessões/dispositivos)

---

### 8. Tela de usuários admin — fundação e status

**Objetivo:** demonstrar a tela de admin (`/admin/users`) e o estado atual da fundação de status.

**Fluxo:**

1. Fazer login com conta que tenha acesso à rota `/admin` (qualquer usuário autenticado no MVP).
2. Navegar para `/admin/users`.
3. A tela carrega a lista de usuários via `GET /api/admin/users` (Bearer JWT — **endpoint diferente** do bootstrap).
4. Mostrar filtros (Todos / Ativos / Suspensos) e busca por nome/email.
5. Mostrar badges de status (Ativo / Suspenso) e origem (manual / oidc).
6. Mostrar que os botões **Suspender / Ativar** estão desabilitados (`disabled`, `aria-disabled="true"`).

**O que explicar:**

- A leitura da lista de usuários funciona (GET /admin/users com Bearer JWT).
- A mutação de status (`PATCH /admin/users/{id}/status`) existe no backend,
  mas está protegida pelo `X-NChat-Admin-Token` (bootstrap guard), que **não é seguro para uso
  no browser**. Os botões ficam desabilitados até que um guard JWT/RBAC de admin (RF-74)
  seja implementado.
- Filtros "Admins" e "Convites pendentes" mostram estado vazio — dados de role/invite
  não são retornados pelo endpoint atual.
- O botão **"Convidar usuário"** também está desabilitado no frontend (funcionalidade não
  disponível nesta versão da tela).

**Resultado esperado:**

- Tela renderiza lista de usuários com skeleton de carregamento.
- Status e origem exibidos corretamente.
- Busca filtra por nome e email na lista carregada (client-side).
- Botões de ação permanecem desabilitados — não há click handler.

---

## Resultados esperados por fluxo

| Fluxo                 | Endpoint principal                  | Resposta esperada      |
| --------------------- | ----------------------------------- | ---------------------- |
| Login manual          | `POST /auth/login`                  | 200 com tokens + user  |
| Logout                | `POST /auth/logout`                 | 204                    |
| Forgot password       | `POST /auth/password/forgot`        | 202                    |
| Reset password        | `POST /auth/password/reset`         | 204                    |
| Criar convite (CLI)   | `POST /admin/invites`               | 201 com invite id      |
| Aceitar convite       | `POST /auth/invites/accept`         | 201 com user id        |
| SSO login             | `GET /auth/oidc/keycloak/login`     | 302 → Keycloak         |
| SSO callback          | `GET /auth/oidc/keycloak/callback`  | 302 → `/oidc-callback` |
| SSO exchange          | `POST /auth/oidc/keycloak/exchange` | 200 com tokens + user  |
| Refresh               | `POST /auth/refresh`                | 200 com novos tokens   |
| Listar sessões        | `GET /auth/me/sessions`             | 200 com array          |
| Revogar sessão        | `DELETE /auth/me/sessions/{id}`     | 204                    |
| Revogar todas         | `DELETE /auth/me/sessions`          | 204                    |
| Listar dispositivos   | `GET /auth/me/devices`              | 200 com array          |
| Revogar dispositivo   | `DELETE /auth/me/devices/{id}`      | 204                    |
| Admin users (leitura) | `GET /admin/users`                  | 200 com array          |

---

## Limitações conhecidas

| Limitação                                                 | Impacto                                                                                                       |
| --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| `must_change_password` flow não implementado              | Login com `must_change_password: true` mostra aviso e impede sessão; não redireciona para troca de senha real |
| Tela de sessões/dispositivos não implementada no frontend | Revogação só via `curl` / API direta                                                                          |
| Mutação de status de usuário não disponível no browser    | Botões desabilitados; requer `X-NChat-Admin-Token` via CLI                                                    |
| Convite pelo browser não disponível                       | Botão "Convidar usuário" desabilitado; requer CLI                                                             |
| Filtros "Admins" e "Convites pendentes" sem dados         | API não retorna dados de role/invite na listagem                                                              |
| SSO somente Keycloak                                      | Azure AD e Google Workspace não implementados                                                                 |
| Refresh de TTL curto requer ambiente especial             | Demo de expiração requer `AUTH_ACCESS_TOKEN_TTL_SECONDS` baixo                                                |

---

## O que NÃO demonstrar ainda

> Demonstrar os itens abaixo pode gerar expectativas incorretas. Evitar ou
> deixar claro que estão fora do escopo atual.

- ❌ **Full RBAC** — não implementado. Não há papéis de admin diferenciados por JWT.
- ❌ **RF-75 — admin browser flow completo** — não implementado. A tela existe em forma de fundação.
- ❌ **Azure AD / Google Workspace SSO** — não implementados. Keycloak é o único provedor.
- ❌ **Troca obrigatória de senha** (`must_change_password`) — fluxo de UI não implementado.
- ❌ **Notificação de novo dispositivo** (RF-54) — não implementado.
- ❌ **Soft-delete de usuário** (RF-55 / RF-56) — não implementado.
- ❌ **Mutação de status via browser** — botões desabilitados intencionalmente.
- ❌ **Convidar usuário via browser** — botão desabilitado intencionalmente.
- ❌ **Teste de carga em produção** — fora de escopo.

---

## Rollback e limpeza pós-demo

**Revogar todas as sessões de contas de teste:**

```bash
# Login para obter token
RESP=$(curl -s -X POST \
  -H "Content-Type: application/json" \
  -d '{"email":"nchat-test-demo@<dominio-staging>","password":"<senha_teste>"}' \
  https://auth.staging.example.com/auth/login)

ACCESS_TOKEN=$(echo "$RESP" | jq -r .access_token)
REFRESH_TOKEN=$(echo "$RESP" | jq -r .refresh_token)

# Revogar todas as sessões exceto a atual
curl -s -X DELETE \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  https://auth.staging.example.com/auth/me/sessions

# Logout (invalida a sessão atual)
curl -s -X POST \
  -H "Content-Type: application/json" \
  -d "{\"refresh_token\":\"$REFRESH_TOKEN\"}" \
  https://auth.staging.example.com/auth/logout
```

**Remover conta de convite de teste** (se criada):

Use `PATCH /admin/users/{id}/status` com `status: "suspended"` via CLI para
desativar a conta sem deletá-la (hard-delete não implementado no MVP).

**Após limpeza:** verificar que nenhuma sessão ativa permanece para as contas de teste.

---

## Executar o smoke test automatizado

O script `scripts/staging/auth-smoke.mjs` valida o contrato do backend contra
o ambiente de staging. Consultar o cabeçalho do script para a lista completa de
variáveis de ambiente necessárias.

```bash
# Configurar variáveis antes de executar (ver scripts/staging/auth-smoke.mjs)
export STAGING_API_BASE_URL="https://auth.staging.example.com"
export STAGING_ALLOWED_ORIGIN="https://auth.staging.example.com"
export STAGING_AUTH_EMAIL="nchat-test-demo@<dominio-staging>"
# ... demais variáveis obrigatórias

node scripts/staging/auth-smoke.mjs
```

> Nunca executar contra produção. O script tem guard que rejeita hosts `prod`/`production`/`api.nic-labs.com`.
