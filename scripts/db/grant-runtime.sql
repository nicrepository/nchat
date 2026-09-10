\set ON_ERROR_STOP on

REVOKE ALL ON SCHEMA auth, chat, files FROM nchat_app;
GRANT USAGE ON SCHEMA auth, chat, files TO nchat_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA auth, chat, files TO nchat_app;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA auth, chat, files TO nchat_app;

ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA auth
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA chat
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA files
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA auth
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA chat
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA files
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO nchat_app;

-- The applied-migration ledger, readable — and only readable — by the runtime
-- role (CICD-08).
--
-- scripts/deploy/nchat-prod/rollback-schema-gate.sh has to answer one question
-- before production traffic is returned to an older slot: which migrations have
-- actually been applied. Pods, Deployments and the release a slot declares are
-- all proxies for that, and all of them are wrong in the same way — deploy.sh
-- runs the migration before it applies the workloads, so a release whose
-- migration completed and whose rollout then failed leaves the schema advanced
-- with no workload anywhere carrying it. Reading the schema back off the
-- workloads would then report that release as never having happened.
--
-- public.schema_migrations is the record of what ran. SELECT and nothing else:
-- the rollback path must be able to read the ledger and must never be able to
-- change it, and the role it reads as is the one the application already runs
-- as rather than the migrator that owns the table.
GRANT USAGE ON SCHEMA public TO nchat_app;
GRANT SELECT ON public.schema_migrations TO nchat_app;
