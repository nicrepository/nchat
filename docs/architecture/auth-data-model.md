# Auth Service — Data Model

Migration: `migrations/auth/000001_auth_identity_schema`
Schema: `auth`

## Table summary

```
auth.users
├── auth.user_password_credentials  (1:1, CASCADE delete)
├── auth.user_invites               (invited_by / accepted_by, SET NULL on user delete)
├── auth.user_sessions              (CASCADE delete; device must belong to same user)
│   └── auth.user_devices           (SET NULL on device delete)
├── auth.user_devices               (CASCADE delete)
├── auth.login_attempts             (SET NULL on user delete)
└── auth.password_reset_tokens      (CASCADE delete)

auth.auth_policy_settings           (singleton, no FK)
```

## Schema reference

### users

| Column                  | Type        | Notes                                           |
| ----------------------- | ----------- | ----------------------------------------------- |
| id                      | uuid        | PK, gen_random_uuid()                           |
| email                   | citext      | Unique, case-insensitive                        |
| display_name            | text        | Required                                        |
| full_name               | text        | Optional, PII                                   |
| avatar_url              | text        | Optional, PII                                   |
| status                  | text        | active / invited / suspended / locked / deleted |
| auth_source             | text        | manual / oidc / imported                        |
| external_subject        | text        | Required and unique for OIDC users (RF-44)      |
| email_verified_at       | timestamptz |                                                 |
| last_login_at           | timestamptz |                                                 |
| deleted_at              | timestamptz | Soft delete (RF-55)                             |
| anonymized_at           | timestamptz | LGPD anonymisation timestamp                    |
| created_at / updated_at | timestamptz |                                                 |

### user_password_credentials

| Column               | Type        | Notes                   |
| -------------------- | ----------- | ----------------------- |
| user_id              | uuid        | PK + FK → users         |
| password_hash        | text        | Bcrypt/Argon2 hash only |
| password_changed_at  | timestamptz |                         |
| password_expires_at  | timestamptz | RF-47                   |
| must_change_password | boolean     | Admin force-reset       |

### user_invites

| Column                            | Type        | Notes                                  |
| --------------------------------- | ----------- | -------------------------------------- |
| id                                | uuid        | PK                                     |
| email                             | citext      | Invitee email                          |
| invited_by_user_id                | uuid        | FK → users (SET NULL)                  |
| token_hash                        | text        | Unique; raw token never stored (RF-46) |
| status                            | text        | pending / accepted / expired / revoked |
| expires_at                        | timestamptz |                                        |
| accepted_at / accepted_by_user_id |             |                                        |
| revoked_at                        | timestamptz |                                        |

### user_sessions

| Column                      | Type        | Notes                              |
| --------------------------- | ----------- | ---------------------------------- |
| id                          | uuid        | PK                                 |
| user_id                     | uuid        | FK → users (CASCADE)               |
| device_id                   | uuid        | FK → same user's device (SET NULL) |
| refresh_token_hash          | text        | Unique; raw token never stored     |
| ip_address                  | inet        |                                    |
| idle_expires_at             | timestamptz | RF-51                              |
| absolute_expires_at         | timestamptz | RF-51                              |
| revoked_at / revoked_reason |             |                                    |

### user_devices

| Column                  | Type                               | Notes                                |
| ----------------------- | ---------------------------------- | ------------------------------------ |
| id                      | uuid                               | PK                                   |
| user_id                 | uuid                               | FK → users (CASCADE)                 |
| device_fingerprint_hash | text                               | Raw fingerprint never stored (RF-53) |
| display_name / platform | text                               | User-visible                         |
| last_ip                 | inet                               |                                      |
| trusted_at / revoked_at | timestamptz                        | RF-53                                |
| UNIQUE                  | (user_id, device_fingerprint_hash) | RF-52                                |

### login_attempts

| Column                  | Type      | Notes                    |
| ----------------------- | --------- | ------------------------ |
| id                      | bigserial | PK                       |
| user_id                 | uuid      | FK → users (SET NULL)    |
| email                   | citext    | Kept after user deletion |
| success                 | boolean   |                          |
| failure_reason          | text      |                          |
| ip_address / user_agent |           | RF-49, RF-50             |

### password_reset_tokens

| Column     | Type        | Notes                                  |
| ---------- | ----------- | -------------------------------------- |
| id         | uuid        | PK                                     |
| user_id    | uuid        | FK → users (CASCADE)                   |
| token_hash | text        | Unique; raw token never stored (RF-48) |
| expires_at | timestamptz |                                        |
| used_at    | timestamptz | Single-use enforcement                 |

### auth_policy_settings

| Column                                          | Type     | Default | Notes                             |
| ----------------------------------------------- | -------- | ------- | --------------------------------- |
| id                                              | smallint | 1       | CHECK (id = 1) enforces singleton |
| min_password_length                             | int      | 12      | RF-47                             |
| require_uppercase / lowercase / number / symbol | boolean  | true    | RF-47                             |
| password_expiration_days                        | int      | NULL    | NULL = no expiry                  |
| failed_login_limit                              | int      | 5       | RF-49                             |
| session_idle_timeout_minutes                    | int      | 60      | RF-51                             |
| max_devices_per_user                            | int      | 5       | RF-52                             |

## Indexes

| Index                               | Columns                    | Rationale                    |
| ----------------------------------- | -------------------------- | ---------------------------- |
| idx_users_status                    | status                     | Filter active/suspended      |
| idx_users_deleted_at                | deleted_at                 | Soft-delete filter           |
| idx_users_oidc_subject_unique       | external_subject           | OIDC identity uniqueness     |
| idx_user_sessions_user_revoked      | (user_id, revoked_at)      | Active-session list per user |
| idx_user_sessions_user_device       | (user_id, device_id)       | Session-device FK checks     |
| idx_user_devices_user_revoked       | (user_id, revoked_at)      | Device list per user         |
| idx_user_invites_email_status       | (email, status)            | Pending invite lookup        |
| idx_login_attempts_email_time       | (email, created_at DESC)   | Brute-force detection        |
| idx_login_attempts_user_time        | (user_id, created_at DESC) | User-visible attempt history |
| idx_password_reset_tokens_user_used | (user_id, used_at)         | Valid token lookup           |

## Security decisions

1. **Hash-only storage**: `token_hash`, `password_hash`, `device_fingerprint_hash`
   — raw values are never persisted. The application layer hashes before write.

2. **citext for email**: avoids silent duplicates from case variation without
   requiring application-side normalisation.

3. **inet for IP**: native PostgreSQL type; supports IPv4/IPv6; avoids injection
   via string parsing.

4. **Schema namespace**: auth tables are created in the existing `auth` schema,
   not in `public`.

5. **Cascade strategy**: sensitive child data (sessions, devices, credentials,
   reset tokens) cascade on user delete. Audit data (login_attempts) and
   relational references (invites) use SET NULL to preserve audit trail.

6. **Soft delete first (RF-55)**: `deleted_at` + `status = 'deleted'`. Hard
   delete deferred to V1.0 (RF-56); FK cascades are already correct for it.

## V1.0 hard delete note (RF-56)

No schema changes are needed. The existing `ON DELETE CASCADE` constraints on
`user_id` foreign keys ensure a `DELETE FROM auth.users WHERE id = $1` propagates
correctly. Hard-delete implementation requires only application-layer code in V1.0.
