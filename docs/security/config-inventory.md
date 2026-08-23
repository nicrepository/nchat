# Inventario de configuracoes (issue #580)

Documento canonico do que e configuravel no NChat, de onde vem cada valor e o
que e preciso fazer para muda-lo. Vive em `docs/security/` pelo mesmo motivo que
a matriz de permissoes: descreve politica e fonte de verdade, nao o contrato de
um endpoint. O contrato da Admin API esta em
[`docs/api/admin-endpoints.md`](../api/admin-endpoints.md).

Escopo: as configuracoes que **existem hoje no codigo**. Nenhuma linha descreve
um parametro que nao e lido por algum servico.

## Classes

Toda configuracao tem uma classe explicita. A classe descreve como uma mudanca
chega a plataforma, e nao o quanto seria conveniente edita-la.

| Classe | Significado                      | Editavel pelo console |
| ------ | -------------------------------- | --------------------- |
| A      | Runtime                          | **sim**               |
| B      | Runtime com credencial           | nao existe            |
| C      | Exige restart/rollout            | nao                   |
| D      | Infraestrutura / somente leitura | nao                   |

**Nao existe classe B.** O NChat nao tem backend de secret que a Admin API possa
escrever: credenciais chegam como variavel de ambiente vinda de Sealed Secrets,
e rotacionar uma e um manifesto selado mais um rollout
([`sealed-secrets-rotation.md`](../runbooks/sealed-secrets-rotation.md)). Um campo
"substituir secret" no console ou gravaria um valor que ninguem le, ou empurraria
credencial de cluster para um processo que nao pode te-la. O console nao oferece
nenhum dos dois, e
`domain.ValidateConfigCatalog` recusa uma definicao que se declare classe B — de
modo que a primeira delas obriga a desenhar o caminho de escrita antes.

## Fontes de verdade

| Fonte           | Onde vive                                 | Quem escreve                       |
| --------------- | ----------------------------------------- | ---------------------------------- |
| `database`      | `auth.auth_policy_settings` (linha unica) | **Admin Console** (unico escritor) |
| `gitops`        | ConfigMap `nchat-config` via Kustomize    | commit + rollout                   |
| `sealed_secret` | Secrets `nchat-*` via Sealed Secrets      | operador, pelo runbook             |

O console **le** o estado efetivo das duas ultimas a partir do proprio ambiente
do pod do `admin-service`, que monta `nchat-config` e `nchat-secrets` com
`envFrom` como todos os outros servicos. Ele **nunca** escreve nelas: nao existe
PATCH em recurso do Kubernetes, e o browser jamais recebe credencial de cluster.

---

## Classe A — editaveis pelo Admin Console

Todas em `auth.auth_policy_settings`, linha unica fixada por
`CHECK (id = 1)`. O `auth-service` le essa linha **na propria requisicao que a
aplica** (`login_store`, `session_store`, `user_store`, `invite_store`,
`password_reset_store`, `device_session_store`), entao persistir e aplicar: nao
ha restart, nao ha rollout e nao ha cache a invalidar.

Capability de leitura: `admin.config.read`. Capability de escrita:
`admin.config.manage`, e `admin.superuser` quando o valor resultante e marcado
como perigoso.

| Chave (Admin API)                          | Coluna                             | Tipo | Unidade      | Faixa aceita | Default | Perigoso quando | Impacto                                                                                                                                        |
| ------------------------------------------ | ---------------------------------- | ---- | ------------ | ------------ | ------- | --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `auth.password.min_length`                 | `min_password_length`              | int  | caracteres   | 8–128        | 12      | `< 12`          | Forca minima de toda senha local                                                                                                               |
| `auth.password.require_uppercase`          | `require_uppercase`                | bool | —            | —            | `true`  | `false`         | Complexidade de senha                                                                                                                          |
| `auth.password.require_lowercase`          | `require_lowercase`                | bool | —            | —            | `true`  | `false`         | Complexidade de senha                                                                                                                          |
| `auth.password.require_number`             | `require_number`                   | bool | —            | —            | `true`  | `false`         | Complexidade de senha                                                                                                                          |
| `auth.password.require_symbol`             | `require_symbol`                   | bool | —            | —            | `true`  | `false`         | Complexidade de senha                                                                                                                          |
| `auth.password.expiration_days`            | `password_expiration_days`         | int? | dias         | 1–3650       | `null`  | —               | Expiracao de senha; `null` significa nao expira. Aplicada no login pelo `auth-service`, contra `user_password_credentials.password_changed_at` |
| `auth.login.failed_attempt_limit`          | `failed_login_limit`               | int  | tentativas   | 3–20         | 5       | `> 10`          | Bloqueio temporario apos falhas de login                                                                                                       |
| `auth.login.failed_attempt_window_minutes` | `failed_login_window_minutes`      | int  | minutos      | 1–1440       | 15      | —               | Janela de contagem das falhas                                                                                                                  |
| `auth.login.lockout_minutes`               | `failed_login_lockout_minutes`     | int  | minutos      | 1–1440       | 15      | `< 5`           | Duracao do bloqueio                                                                                                                            |
| `auth.session.idle_timeout_minutes`        | `session_idle_timeout_minutes`     | int  | minutos      | 5–1440       | 60      | `> 240`         | Inatividade da sessao **do chat** (nao a do console)                                                                                           |
| `auth.device.max_per_user`                 | `max_devices_per_user`             | int  | dispositivos | 1–50         | 5       | —               | Sessoes de dispositivo simultaneas                                                                                                             |
| `auth.password_reset.ttl_minutes`          | `password_reset_token_ttl_minutes` | int  | minutos      | 5–1440       | 60      | `> 240`         | Validade do link de redefinicao                                                                                                                |
| `auth.invite.ttl_hours`                    | `invite_token_ttl_hours`           | int  | horas        | 1–720        | 72      | `> 168`         | Validade do convite                                                                                                                            |

Notas que valem registrar:

- **A faixa aceita e mais estreita que o `CHECK` da coluna.** O `CHECK` existe
  para que um bug nao grave besteira; a faixa existe para que um administrador
  nao receba a oferta de um valor tecnicamente armazenavel e operacionalmente
  indefensavel. Nada e arredondado nem truncado: valor fora da faixa e recusado.
- **Nao ha dependencia entre campos.** Nenhuma invariante liga uma coluna a
  outra hoje, e nenhuma foi inventada para preencher a secao — a validacao e por
  campo: tipo, faixa e nulidade.
- **Nenhuma validacao acessa a rede.** Nenhuma definicao editavel e do tipo URL,
  entao nao existe "testar endpoint" e nao ha superficie de SSRF a policiar.
  `ValidateConfigCatalog` exige `source = database` em toda definicao editavel,
  o que mantem essa propriedade estrutural.
- **Health posterior:** a verificacao e o proprio proximo login. Nao existe
  sonda dedicada e o console nao inventa uma.

---

## Classe C — lidas no boot a partir do ConfigMap (`nchat-config`)

Fonte de verdade: Git (`infra/k8s/base/configmap.yaml` e os patches de overlay).
Mudar qualquer uma e commit + rollout. O console mostra o valor efetivo
observado pelo pod do `admin-service` e diz explicitamente que a alteracao exige
rollout.

| Chave (Admin API)                 | Variavel                         | Servico dono         | Impacto                                                 |
| --------------------------------- | -------------------------------- | -------------------- | ------------------------------------------------------- |
| `platform.environment`            | `APP_ENV`                        | todos                | Rotulo do ambiente; decide o aviso no topo do console   |
| `platform.log_level`              | `LOG_LEVEL`                      | todos                | Verbosidade do log                                      |
| `auth.jwt.issuer`                 | `AUTH_JWT_ISSUER`                | auth-service         | `iss` exigido em todo access token                      |
| `auth.jwt.audience`               | `AUTH_JWT_AUDIENCE`              | auth-service         | `aud` exigido em todo access token                      |
| `auth.trusted_proxy_cidrs`        | `AUTH_TRUSTED_PROXY_CIDRS`       | auth-service         | Quais proxies podem declarar o endereco do cliente      |
| `oidc.enabled`                    | `OIDC_ENABLED`                   | auth-service         | **Perigoso**: desligar deixa so o login local           |
| `oidc.provider_name`              | `OIDC_PROVIDER_NAME`             | auth-service         | Identificador do provedor nas rotas de SSO              |
| `oidc.scopes`                     | `OIDC_SCOPES`                    | auth-service         | Escopos pedidos na autorizacao                          |
| `oidc.auto_provision_enabled`     | `OIDC_AUTO_PROVISION_ENABLED`    | auth-service         | Cria conta local no primeiro login federado             |
| `oidc.allowed_email_domains`      | `OIDC_ALLOWED_EMAIL_DOMAINS`     | auth-service         | Restringe o provisionamento automatico                  |
| `oidc.admin_acr_values`           | `OIDC_ADMIN_ACR_VALUES`          | auth-service         | ACR exigido no SSO do console                           |
| `calls.livekit.enabled`           | `LIVEKIT_ENABLED`                | media-service        | Emissao de token de chamada                             |
| `calls.livekit.token_ttl_seconds` | `LIVEKIT_TTL_SECONDS`            | media-service        | Validade do token de sala                               |
| `email.smtp.worker_enabled`       | `SMTP_WORKER_ENABLED`            | notification-service | **Perigoso**: desligar acumula a fila sem entregar nada |
| `admin.allowed_origins`           | `ADMIN_ALLOWED_ORIGINS`          | admin-service        | Allowlist de origem da Admin API                        |
| `admin.session.idle_minutes`      | `ADMIN_SESSION_IDLE_MINUTES`     | admin-service        | Inatividade da sessao administrativa                    |
| `admin.session.absolute_minutes`  | `ADMIN_SESSION_ABSOLUTE_MINUTES` | admin-service        | Prazo absoluto da sessao administrativa                 |

### Classe C que o `admin-service` nao observa

Estas sao lidas no boot pelo servico dono, que as recebe por `envFrom` do mesmo
ConfigMap, mas nao aparecem na Admin API porque o pod do `admin-service` nao
carrega nenhuma delas com valor proprio a reportar. Estao aqui porque o
inventario e do NChat, nao do console.

| Variavel                                                                                           | Servico dono         | Impacto                                      |
| -------------------------------------------------------------------------------------------------- | -------------------- | -------------------------------------------- |
| `CHAT_LINK_SAFETY_ENABLED`                                                                         | chat-service         | RF-21; sem credencial o servico nao sobe     |
| `CHAT_LINK_SAFETY_WORKSPACE_BUDGET`                                                                | chat-service         | Orcamento de URLs novas por janela           |
| `CHAT_LINK_SAFETY_BUDGET_WINDOW_SECONDS`                                                           | chat-service         | Janela do orcamento                          |
| `CHAT_LINK_SAFETY_MAX_PENDING_JOBS`                                                                | chat-service         | Fila maxima de verificacao                   |
| `CHAT_LINK_SAFETY_PROVIDER_SUBMIT_LIMIT`                                                           | chat-service         | Teto de submissoes ao provedor               |
| `CHAT_LINK_SAFETY_PROVIDER_SUBMIT_WINDOW_SECONDS`                                                  | chat-service         | Janela do teto acima                         |
| `CHAT_LINK_SAFETY_SUBMIT_UNCERTAIN_TIMEOUT_SECONDS`                                                | chat-service         | Prazo do veredito indefinido                 |
| `REACTION_RATE_LIMIT_MAX_ACTIONS` / `_WINDOW_SECONDS`                                              | chat-service         | Limite de reacoes                            |
| `TYPING_RATE_LIMIT_MAX_ACTIONS` / `_WINDOW_SECONDS`                                                | chat-service         | Limite de "digitando"                        |
| `CALL_RING_TIMEOUT_SECONDS`                                                                        | chat-service         | Timeout de toque de chamada                  |
| `CALL_START_RATE_LIMIT_MAX_ACTIONS` / `_WINDOW_SECONDS`                                            | chat-service         | Limite de inicio de chamada                  |
| `WS_INBOUND_MESSAGES_PER_MINUTE` / `WS_INBOUND_BURST`                                              | chat-service         | Limite de entrada no WebSocket               |
| `WS_MAX_CONNECTIONS_PER_USER`                                                                      | chat-service         | Conexoes simultaneas por usuario             |
| `WS_MAX_INVALID_MESSAGES`                                                                          | chat-service         | Tolerancia a mensagem invalida               |
| `MENTION_LABEL_CACHE_TTL_SECONDS`                                                                  | chat-service         | TTL do cache de mencoes                      |
| `VALKEY_WS_BROADCAST_ENABLED`                                                                      | chat-service         | Broadcast entre instancias                   |
| `FILE_UPLOADS_ENABLED`                                                                             | file-service         | Liga/desliga upload de anexos                |
| `FILE_MAX_UPLOAD_BYTES`                                                                            | file-service         | Teto de upload do servico                    |
| `FILE_MALWARE_SCAN_REQUIRED`                                                                       | file-service         | **Perigoso**: desligar aceita anexo sem scan |
| `FILE_MALWARE_SCANNER_ADDRESS` / `_TIMEOUT_SECONDS`                                                | file-service         | Endereco e prazo do ClamAV                   |
| `FILE_UPLOAD_MAX_CONCURRENT` / `_PER_USER`                                                         | file-service         | Admissao de upload                           |
| `FILE_UPLOAD_RETRY_AFTER_SECONDS`                                                                  | file-service         | `Retry-After` na recusa por concorrencia     |
| `FILE_LINK_PREVIEW_ENABLED` / `_TIMEOUT_SECONDS` / `_CACHE_TTL_SECONDS`                            | file-service         | Previa de link                               |
| `FILE_LINK_SAFETY_ENABLED` e `FILES_LINK_SAFETY_*`                                                 | file-service         | RF-21 no file-service                        |
| `FILE_DB_MAX_CONNECTIONS`                                                                          | file-service         | Pool de conexoes                             |
| `SEAWEEDFS_TIMEOUT_SECONDS`                                                                        | file-service         | Prazo do armazenamento                       |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USERNAME` / `SMTP_FROM` / `SMTP_FROM_NAME`                       | notification-service | Relay de e-mail                              |
| `SMTP_TLS_MODE`                                                                                    | notification-service | **Perigoso**: enfraquecer o TLS do relay     |
| `SMTP_TIMEOUT_SECONDS` / `SMTP_MAX_ATTEMPTS` / `SMTP_BACKOFF_SECONDS` / `SMTP_WORKER_POLL_SECONDS` | notification-service | Politica de entrega                          |
| `AUTH_PUBLIC_WEB_BASE_URL`                                                                         | notification-service | Base dos links enviados por e-mail           |
| `AUTH_ACCESS_TOKEN_TTL_SECONDS` / `AUTH_REFRESH_TOKEN_TTL_SECONDS`                                 | auth-service         | Vida dos tokens de chat                      |
| `AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE` / `_BURST`                                             | auth-service         | Limite do endpoint de token                  |
| `AUTH_AVATAR_DIR` / `AUTH_AVATAR_BASE_URL`                                                         | auth-service         | Armazenamento de avatar                      |
| `OIDC_HTTP_TIMEOUT_SECONDS` / `OIDC_STATE_TTL_MINUTES` / `OIDC_FRONTEND_CALLBACK_URL`              | auth-service         | Fluxo OIDC                                   |
| `LIVEKIT_API_URL`                                                                                  | media-service        | Endpoint do LiveKit                          |
| `READ_HEADER_TIMEOUT_SECONDS` / `READ_TIMEOUT_SECONDS` / `WRITE_TIMEOUT_SECONDS`                   | todos                | Prazos do servidor HTTP                      |
| `DB_CONNECT_TIMEOUT_SECONDS`                                                                       | todos                | Prazo de conexao ao banco                    |
| `PORT` / `SERVICE_NAME` / `WS_INSTANCE_ID`                                                         | todos                | Identidade do processo                       |
| `OTEL_*` / `PROMETHEUS_METRICS_ENABLED`                                                            | todos                | Observabilidade                              |
| `VITE_*_API_BASE_URL` / `VITE_CHAT_WS_URL`                                                         | apps/web             | Configuracao de build do frontend            |

Politicas por workspace (`chat.workspaces.message_rate_limit_per_minute` e
`max_upload_bytes`) sao runtime e **ja tem tela propria** desde a issue #579
(`/security` e `/files`). Nao foram duplicadas nesta tela: sao por workspace, e
esta e a configuracao de plataforma.

---

## Classe D — infraestrutura e credenciais

### Infraestrutura (somente leitura, valor observavel)

| Chave (Admin API)           | Variavel                | Impacto                        |
| --------------------------- | ----------------------- | ------------------------------ |
| `infra.postgres.host`       | `POSTGRES_HOST`         | Endereco do banco              |
| `infra.postgres.port`       | `POSTGRES_PORT`         | Porta do banco                 |
| `infra.postgres.database`   | `POSTGRES_DB`           | Base de dados                  |
| `infra.valkey.host`         | `VALKEY_HOST`           | Cache e broadcast de WebSocket |
| `infra.storage.filer_url`   | `SEAWEEDFS_FILER_URL`   | Filer de anexos                |
| `infra.storage.s3_endpoint` | `SEAWEEDFS_S3_ENDPOINT` | Endpoint S3 de anexos          |

### Credenciais (somente `configurado` / `nao configurado`)

O valor **nunca** sai do servico. A Admin API responde apenas se a variavel
esta presente e nao vazia; nenhum campo do payload carrega o conteudo, nenhum
diff o mostra e o historico de versoes e estruturalmente incapaz de armazena-lo
(`CHECK jsonb_typeof(...) IN ('number','boolean','null')`).

| Chave (Admin API)                   | Variavel                           | Secret                  | Servico dono               |
| ----------------------------------- | ---------------------------------- | ----------------------- | -------------------------- |
| `secret.database_url`               | `DATABASE_URL`                     | `nchat-secrets`         | plataforma                 |
| `secret.valkey_url`                 | `VALKEY_URL`                       | `nchat-secrets`         | chat-service               |
| `secret.auth_jwt_hmac`              | `AUTH_JWT_HMAC_SECRET`             | `nchat-secrets`         | auth-service               |
| `secret.auth_email_outbox_key`      | `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` | `nchat-secrets`         | auth-service               |
| `secret.oidc_issuer_url`            | `OIDC_ISSUER_URL`                  | `nchat-secrets`         | auth-service               |
| `secret.oidc_client_id`             | `OIDC_CLIENT_ID`                   | `nchat-secrets`         | auth-service               |
| `secret.oidc_client_secret`         | `OIDC_CLIENT_SECRET`               | `nchat-secrets`         | auth-service               |
| `secret.oidc_redirect_url`          | `OIDC_REDIRECT_URL`                | `nchat-secrets`         | auth-service               |
| `secret.oidc_admin_redirect_url`    | `OIDC_ADMIN_REDIRECT_URL`          | `nchat-secrets`         | auth-service               |
| `secret.smtp_password`              | `SMTP_PASSWORD`                    | `nchat-secrets`         | notification-service       |
| `secret.livekit_api_key`            | `LIVEKIT_API_KEY`                  | `nchat-secrets`         | media-service              |
| `secret.livekit_api_secret`         | `LIVEKIT_API_SECRET`               | `nchat-secrets`         | media-service              |
| `secret.file_encryption_master_key` | —                                  | `nchat-file-encryption` | file-service               |
| `secret.link_safety_api_token`      | —                                  | `nchat-link-safety`     | chat-service, file-service |

As duas ultimas sao montadas **apenas** pelos servicos donos, por decisao
deliberada da issue de criptografia de anexos e da RF-21. O `admin-service` nao
as ve, e o console relata isso como **nao observavel** em vez de "nao
configurado": os dois fatos sao diferentes, e confundi-los mandaria um operador
consertar algo que esta correto.

Ownership e cadencia de rotacao continuam em
[`secrets-owners.md`](secrets-owners.md); o procedimento continua em
[`sealed-secrets-rotation.md`](../runbooks/sealed-secrets-rotation.md).

---

## Como manter este documento

O registry em
`services/admin-service/internal/domain/config_catalog.go` e a autoridade sobre
o que a Admin API expoe. Uma definicao nova entra la primeiro; este documento e
a versao narrativa, e cobre tambem o que o console nao expoe.
`ValidateConfigCatalog` (testado em `config_test.go`) impede as combinacoes que
tornariam o registry inconsistente — chave duplicada, definicao editavel fora do
banco, credencial editavel, definicao somente leitura sem motivo.
