# Runbook — Fundação do Admin Console (issue #578)

Como o console administrativo do NChat é operado: onde ele roda, como o primeiro
administrador nasce, como uma permissão é concedida e revogada, e o que olhar
quando alguém não consegue entrar.

## O que existe

| Peça                  | Onde                                                                                                      |
| --------------------- | --------------------------------------------------------------------------------------------------------- |
| Console (frontend)    | `apps/admin-web` — bundle próprio, host próprio, CSP própria                                              |
| Admin API             | `services/admin-service` — `/api/admin/*`                                                                 |
| Modelo de autorização | `auth.admin_principals`, `auth.admin_roles`, `auth.admin_role_capabilities`, `auth.admin_principal_roles` |
| Sessão administrativa | `auth.admin_sessions`                                                                                     |
| Auditoria             | `auth.admin_audit_events`                                                                                 |
| Contrato da API       | `docs/api/admin-endpoints.md`                                                                             |
| Política              | `SECURITY.md`, `docs/security/rbac-matrix.md`                                                             |

## Hosts

| Ambiente         | Console                             | Origem do host                          |
| ---------------- | ----------------------------------- | --------------------------------------- |
| Local            | `https://admin.nchat.local`         | fixo em `infra/traefik/local`           |
| k3s-dev          | `http://admin.nchat.local`          | fixo no overlay                         |
| k3s-staging      | `https://admin.staging.nchat.local` | fixo no overlay                         |
| nchat-dev-server | `admin.<NCHAT_DEV_HOST>`            | derivado de `topology.env` pelo overlay |

No `nchat-dev-server` o host administrativo **não é configurado**: ele é o
rótulo fixo `admin` sob o `NCHAT_DEV_HOST` que o operador já informa, derivado
pela substituição do kustomize — do mesmo jeito que `turn.<host>` já era. O
certificado `nchat-dev-tls` cobre os dois nomes, e um host longo demais para
comportar o rótulo (acima de 247 caracteres) é recusado na validação de
topologia.

O host administrativo serve exatamente três coisas: o bundle do console,
`/api/admin` (admin-service) e `/api/auth` (auth-service, usado no login).
Nenhuma outra rota da plataforma é roteada para ele.

Nada no código do frontend conhece esses hostnames. O ambiente exibido no
console vem do payload de bootstrap, que por sua vez vem do `APP_ENV` do
deployment — não do `window.location`.

## Subir localmente

```bash
make dev-tls-generate     # inclui admin.nchat.local no certificado
make dev-gateway-up
make dev-admin-web        # Vite em :5174, servido pelo Traefik em admin.nchat.local
```

`/etc/hosts`:

```text
127.0.0.1 nchat.local
127.0.0.1 admin.nchat.local
```

admin-service precisa de `DATABASE_URL` e `AUTH_JWT_HMAC_SECRET` — os mesmos
valores que o auth-service usa. Sem eles o serviço sobe, responde `/healthz` e
`/version`, e devolve `503` em toda rota privilegiada. Isso é proposital: um pod
mal configurado recusa a Admin API em vez de servi-la sem os guards.

## Single sign-on (Keycloak/OIDC)

O console usa o **mesmo** fluxo OIDC do chat — mesmo provider, mesmo client,
mesmo state/nonce/PKCE. O que muda é uma coisa só: para qual origem o Keycloak
devolve o navegador.

O login aceita um rótulo, nunca uma URL:

```
GET /api/auth/oidc/keycloak/login            -> aplicação chat (padrão)
GET /api/auth/oidc/keycloak/login?app=admin  -> console administrativo
```

`app` é uma enum fechada (`chat` | `admin`). Qualquer outro valor recebe `400` e
não inicia login algum. O rótulo escolhe uma URI de callback que vive na
configuração do servidor; ele nunca vira destino. O contexto é gravado em
`auth.oidc_auth_requests.app_context`, ao lado dos hashes de state e nonce, e o
callback o relê dali — não da requisição que volta.

O redirecionamento final continua sendo o caminho relativo `/oidc-callback`, que
por ser relativo mantém o navegador na origem em que o callback rodou. Não existe
`returnTo`, `redirect_uri` ou qualquer parâmetro de destino aceito do cliente.

### Configuração

| Variável                     | Onde vive       | Significado                                            |
| ---------------------------- | --------------- | ------------------------------------------------------ |
| `OIDC_REDIRECT_URL`          | `nchat-secrets` | callback do provider para o host do chat (obrigatório) |
| `OIDC_ADMIN_REDIRECT_URL`    | `nchat-secrets` | callback do provider para o host do console (opcional) |
| `OIDC_FRONTEND_CALLBACK_URL` | `nchat-secrets` | `/oidc-callback` — relativo, comum às duas aplicações  |

As duas URIs ficam no mesmo Secret e são lidas por `secretKeyRef` no Deployment
do auth-service. Não é confidencialidade — são URLs públicas que o provider já
conhece — e sim proximidade: elas precisam ser cadastradas juntas no mesmo
client Keycloak, e um operador que configura uma e esquece a outra é exatamente
a falha que essa co-locação evita.

Ambas as URIs de callback são **absolutas** e precisam de HTTPS, exceto em
loopback (`localhost`, `127.0.0.1`), que existe para desenvolvimento local. Uma
`OIDC_ADMIN_REDIRECT_URL` malformada faz o auth-service recusar a configuração
OIDC inteira no boot, em vez de falhar no primeiro login de um administrador.

Deixar `OIDC_ADMIN_REDIRECT_URL` vazia significa **sem SSO no host do console** —
o botão devolve `503 oidc_unavailable`. Ela nunca cai de volta na URI do chat:
isso autenticaria o administrador e o deixaria na origem errada, onde a sessão
administrativa dele não pode existir. O login por senha continua funcionando.

### O que cadastrar no Keycloak

No **mesmo client** já usado pelo chat (`OIDC_CLIENT_ID`), acrescente a segunda
Valid Redirect URI:

```
https://nchat.nic-labs.com/api/auth/oidc/keycloak/callback
https://admin.nchat.nic-labs.com/api/auth/oidc/keycloak/callback
```

Local:

```
https://nchat.local/api/auth/oidc/keycloak/callback
https://admin.nchat.local/api/auth/oidc/keycloak/callback
```

Nenhum segredo novo entra nisso: as duas são URLs públicas que o provider já
conhece. Nada disso vai para `VITE_*`, ConfigMap de frontend ou bundle.

### MFA administrativo

O NChat **não implementa segundo fator**. Quem autentica é o Keycloak; o que o
NChat faz é pedir o contexto de autenticação certo e **recusar um token que não
volte com ele**. Nenhum TOTP, senha administrativa paralela ou segredo de
segundo fator é armazenado aqui.

| Variável                           | Efeito                                     |
| ---------------------------------- | ------------------------------------------ |
| `OIDC_ADMIN_ACR_VALUES` vazio      | sem requisito; o console entra como o chat |
| `OIDC_ADMIN_ACR_VALUES` preenchido | requisito real e fail-closed               |

Com valor configurado:

1. `GET /api/auth/oidc/keycloak/login?app=admin` envia `acr_values` ao Keycloak;
2. o Keycloak executa o Authentication Flow correspondente;
3. o callback compara o claim `acr` do ID token validado contra a allowlist;
4. `acr` ausente ou fora da lista ⇒ `403 oidc_insufficient_assurance`, **sem
   criar sessão**.

Comparação exata, sem prefixo ou substring: um nível de assurance é um
identificador, não uma escala que este serviço possa interpretar. `acr` ausente
é recusa — o claim é opcional em OIDC, e "o provedor não disse nada" nunca é
"usou segundo fator". O requisito vale **somente** para `app=admin`; o login do
chat não muda.

O valor não é inventado por este repositório: é exatamente o que o seu realm
emite.

### Configurar no Keycloak

Fazer no realm usado pelo ambiente (nenhum secret entra nesta documentação):

1. **Authentication → Flows**: duplicar o browser flow como, por exemplo,
   `nchat-admin-browser`, e tornar o passo de OTP/WebAuthn **Required** em vez
   de Conditional.
2. **Authentication → Policies → OTP Policy** (ou WebAuthn Policy): definir o
   fator exigido.
3. **Realm settings → Sessions/Tokens → ACR to LoA Mapping**: mapear o nome que
   você usará — por exemplo `nchat-admin-mfa` — para o Level of Authentication
   correspondente ao fluxo acima.
4. **Clients → `<OIDC_CLIENT_ID>` → Advanced → ACR to LoA Mapping**: repetir o
   mapeamento no client, se o realm não o herdar.
5. Cadastrar a segunda Valid Redirect URI (host administrativo) no mesmo client.
6. Definir `OIDC_ADMIN_ACR_VALUES` com exatamente esse nome.

Aplicação da política: quem administra a plataforma precisa estar no grupo/role
a que o fluxo exige o fator. Se a política for por realm, vale para todos.

### Como verificar

Com MFA exigido:

```bash
# 1. o login administrativo deve pedir o contexto
curl -sD - -o /dev/null "https://<admin-host>/api/auth/oidc/keycloak/login?app=admin" \
  | grep -i '^location:' | tr '&' '\n' | grep acr_values

# 2. decodificar o acr do ID token emitido (payload, sem assinatura)
#    e conferir contra OIDC_ADMIN_ACR_VALUES
```

Teste **sem** MFA: usar uma conta fora do grupo/policy, ou apontar
`OIDC_ADMIN_ACR_VALUES` para um valor que o realm não emite. Esperado:
`403 oidc_insufficient_assurance` e nenhuma sessão administrativa criada.

Teste **com** MFA: conta sujeita à policy, segundo fator concluído. Esperado:
login conclui e `POST /api/admin/session` funciona normalmente.

Impacto de desabilitar a policy: esvaziar `OIDC_ADMIN_ACR_VALUES` remove o
requisito no NChat, e afrouxar o Authentication Flow remove no Keycloak. As duas
pontas precisam estar configuradas — só uma delas não garante nada.

### Por que o SSO não concede acesso administrativo

Um login OIDC bem-sucedido produz uma sessão NChat comum e nada mais. O console
ainda precisa trocá-la por uma sessão administrativa em
`POST /api/admin/session`, que consulta `auth.admin_principals`. Uma pessoa sem
principal ativo recebe `403` ali — o SSO prova identidade, nunca autoridade.

## Criar o primeiro administrador

O bootstrap é **server-side apenas**. Não existe token, header ou variável de
ambiente que conceda acesso administrativo pelo navegador, e o
`X-NChat-Admin-Token` (`ADMIN_BOOTSTRAP_TOKEN`) continua restrito às três rotas
CLI do auth-service — ele nunca chega ao browser, ao bundle, ao `localStorage`,
a um cookie ou a um log.

A concessão é uma transação no banco, executada por quem já tem acesso ao
PostgreSQL:

```sql
BEGIN;

-- 1. A pessoa precisa existir e estar ativa em auth.users.
SELECT id, email, status FROM auth.users WHERE email = 'admin@nic-labs.com';

-- 2. Torná-la um principal administrativo.
INSERT INTO auth.admin_principals (user_id)
SELECT id FROM auth.users WHERE email = 'admin@nic-labs.com'
ON CONFLICT (user_id) DO UPDATE SET status = 'active', updated_at = now();

-- 3. Conceder um papel. platform-superuser implica todas as capabilities;
--    platform-auditor concede somente leitura e auditoria.
INSERT INTO auth.admin_principal_roles (user_id, role_slug)
SELECT id, 'platform-superuser' FROM auth.users WHERE email = 'admin@nic-labs.com'
ON CONFLICT DO NOTHING;

COMMIT;
```

Conferir:

```sql
SELECT u.email, pr.role_slug, rc.capability
FROM auth.admin_principals AS p
JOIN auth.users AS u ON u.id = p.user_id
JOIN auth.admin_principal_roles AS pr ON pr.user_id = p.user_id
JOIN auth.admin_role_capabilities AS rc ON rc.role_slug = pr.role_slug
WHERE p.status = 'active'
ORDER BY u.email, rc.capability;
```

## Revogar

Três níveis, do mais estreito ao mais amplo. Todos passam a valer **na próxima
requisição** — as capabilities e o estado do principal são relidos do banco a
cada request, nunca lidos de uma claim antiga.

```sql
-- Remover um papel.
DELETE FROM auth.admin_principal_roles
WHERE user_id = (SELECT id FROM auth.users WHERE email = 'admin@nic-labs.com')
  AND role_slug = 'platform-superuser';

-- Suspender o principal, mantendo o histórico.
UPDATE auth.admin_principals SET status = 'suspended', updated_at = now()
WHERE user_id = (SELECT id FROM auth.users WHERE email = 'admin@nic-labs.com');

-- Encerrar as sessões administrativas abertas agora.
UPDATE auth.admin_sessions SET revoked_at = now(), revoked_reason = 'operator_revoked'
WHERE user_id = (SELECT id FROM auth.users WHERE email = 'admin@nic-labs.com')
  AND revoked_at IS NULL;
```

Revogar o login normal da pessoa (`auth.user_sessions.revoked_at`) também encerra
a sessão administrativa criada a partir dele. O contrário não vale: sair do
console não desloga a pessoa do chat.

## Política de sessão

| Propriedade   | Sessão do chat                                    | Sessão administrativa                                               |
| ------------- | ------------------------------------------------- | ------------------------------------------------------------------- |
| Credencial    | access token em `sessionStorage` + refresh cookie | cookie `__Host-` opaco, HttpOnly                                    |
| Idle          | 60 min (configurável no banco)                    | 15 min (`ADMIN_SESSION_IDLE_MINUTES`)                               |
| Vida absoluta | opcional                                          | 8 h (`ADMIN_SESSION_ABSOLUTE_MINUTES`)                              |
| Renovação     | `/auth/refresh`                                   | janela de idle renovada a cada request, limitada pela vida absoluta |
| Rotação       | rotação de refresh token                          | valor novo a cada handshake                                         |

O host administrativo também recebe o cookie de refresh do chat (`nchat_rt`),
emitido pelo auth-service no login. Ele é `HttpOnly` e tem `Path=/api/auth`,
então nunca é enviado para `/api/admin` e não pode ser lido por script. Ele
representa exatamente o mesmo login do qual a sessão administrativa foi
derivada — não concede nada além do que essa sessão já concede — e expira
sozinho pela política de sessão do chat.

O console **não** chama `/auth/refresh`. O token de acesso do chat é usado uma
única vez, no handshake, e descartado; ele não é persistido em lugar nenhum do
navegador. Consequência deliberada: a revalidação por requisição verifica se o
login de origem foi revogado ou passou da vida absoluta, mas não aplica a janela
de _inatividade_ do chat — senão um administrador trabalhando no console seria
expulso por um cronômetro de uma aba que ele não está usando.

## Diagnóstico

| Sintoma                             | Causa provável                                                                |
| ----------------------------------- | ----------------------------------------------------------------------------- |
| `503` em toda rota `/api/admin/*`   | admin-service sem `DATABASE_URL`/`AUTH_JWT_HMAC_SECRET`, ou banco fora        |
| `404` em `/api/admin/bootstrap`     | middleware de rewrite ausente no Ingress (`admin-api-prefix`)                 |
| Login correto e `403` no handshake  | pessoa não é principal ativo, ou não tem nenhuma capability                   |
| Console pede login logo após entrar | cookie descartado — verificar HTTPS e o prefixo `__Host-`                     |
| `403` só em uma seção               | falta a capability daquela seção; a negação fica em `auth.admin_audit_events` |

Últimos eventos:

```sql
SELECT occurred_at, action, result, correlation_id, metadata
FROM auth.admin_audit_events
ORDER BY occurred_at DESC, id DESC
LIMIT 20;
```

O `correlation_id` é o `X-Request-ID` da requisição, então ele liga a linha de
auditoria ao log e ao trace da mesma chamada.

## O que a auditoria nunca registra

Access token, refresh token, bootstrap token, senha, client secret, conteúdo de
mensagem, header `Authorization` e header `Cookie`. `metadata` é um objeto JSON
montado campo a campo pelo produtor do evento — não há caminho que copie um
header ou um corpo de requisição para dentro dele.
