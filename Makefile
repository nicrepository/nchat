.PHONY: help install dev-web dev-admin-web dev-env-up dev-env-down dev-env-reset dev-env-status dev-env-logs dev-env-validate dev-env-config-check dev-gateway-up dev-gateway-down dev-gateway-status dev-gateway-logs dev-gateway-validate dev-tls-generate dev-tls-status dev-tls-clean tls-config-check k8s-render k8s-validate k8s-render-staging k8s-validate-staging k8s-apply-dev k8s-delete-dev k8s-status-dev k8s-ci health-contract-check ci-config-check images-module-inputs-check images-module-inputs-check-test gateway-config-check web-security-headers-check web-livekit-integration-check sealed-secrets-validate sealed-secrets-policy-check sealed-secrets-install-controller sealed-secrets-fetch-cert build-web build-admin-web test-web test-admin-web lint-web lint-admin-web test-go vet-go fmt-go format format-check lint-go go-coverage go-coverage-check web-coverage coverage lint test build security security-secrets security-govulncheck security-govulncheck-gate-test security-trivy-fs security-trivy-config poc-seaweedfs poc-valkey poc-config-check observability-config-check grafana-dashboard-check migrations-check migrations-blue-green-test prod-blue-green-check prod-blue-green-check-test prod-stateful-check prod-stateful-check-test prod-stateful-preflight-test prod-stateful-apply prod-blue-green-test prod-blue-green-query-test prod-capacity-test prod-release-manifest-test cd-workflows-check cd-workflows-test prod-lifecycle-test prod-rollback-schema-test dev-smoke-test prod-runner-guard-test prod-capacity-evidence prod-blue-green-status prod-blue-green-bootstrap prod-blue-green-deploy prod-blue-green-smoke prod-blue-green-stable-smoke prod-blue-green-record-traffic-smoke prod-blue-green-rollback-schema-gate dev-smoke prod-blue-green-cutover prod-blue-green-rollback prod-blue-green-drain-old prod-notification-levels migrations-up migrations-down migrations-status migrations-reset migrations-smoke db-restore-test dev-observability-up dev-observability-down dev-observability-status dev-observability-logs dev-observability-validate dev-media-up dev-media-down dev-media-status dev-media-logs dev-media-validate media-config-check qa-webrtc-office-network webrtc-office-network-config-check ci ci-static-web ci-static-admin-web ci-static-go ci-static-repository ci-architecture-check ci-infra-config ci-infra-kubernetes ci-infra-migrations ci-infra-release-safety test-integration-go

help:
	@echo "NChat development commands"
	@echo "  make install     Install frontend dependencies"
	@echo "  make dev-web     Run web app locally"
	@echo "  make dev-admin-web Run admin console locally"
	@echo "  make dev-env-up  Start local data services"
	@echo "  make dev-env-down Stop local data services"
	@echo "  make dev-env-validate Validate local data services"
	@echo "  make dev-gateway-up Start local Traefik gateway"
	@echo "  make dev-gateway-status Show local Traefik gateway status"
	@echo "  make dev-gateway-validate Validate local Traefik gateway"
	@echo "  make dev-gateway-down Stop local Traefik gateway"
	@echo "  make dev-tls-generate Generate local HTTPS certificate"
	@echo "  make dev-tls-status Show local HTTPS certificate metadata"
	@echo "  make tls-config-check Run TLS config validation"
	@echo "  make web-security-headers-check Validate web security headers/CSP"
	@echo "  make web-livekit-integration-check Build+run web image, validate real LiveKit CSP (requires Docker)"
	@echo "  make k8s-render  Render k3s-dev manifests"
	@echo "  make k8s-validate Validate k3s-dev manifests"
	@echo "  make k8s-render-staging Render k3s-staging manifests"
	@echo "  make k8s-validate-staging Validate k3s-staging manifests"
	@echo "  make k8s-apply-dev Apply k3s-dev manifests"
	@echo "  make k8s-status-dev Show k3s-dev resources"
	@echo "  make k8s-delete-dev Delete k3s-dev manifests"
	@echo "  make k8s-ci      Run Kubernetes manifest CI check"
	@echo "  make health-contract-check Run health endpoint contract check"
	@echo "  make ci-config-check Run CI config validation"
	@echo "  make gateway-config-check Run gateway config validation"
	@echo "  make sealed-secrets-policy-check Run Sealed Secrets policy validation"
	@echo "  make build-web   Build web app"
	@echo "  make build-admin-web Build admin console app"
	@echo "  make test-web    Run frontend tests"
	@echo "  make test-admin-web Run admin console tests"
	@echo "  make lint-web    Run frontend lint"
	@echo "  make lint-admin-web Run admin console lint"
	@echo "  make test-go     Run Go tests"
	@echo "  make vet-go      Run Go vet"
	@echo "  make fmt-go      Check Go formatting"
	@echo "  make format      Format Go, web, docs, YAML, and JSON"
	@echo "  make format-check Check formatting"
	@echo "  make lint-go     Run golangci-lint"
	@echo "  make coverage    Run Go and web coverage"
	@echo "  make go-coverage-check Run Go coverage threshold check"
	@echo "  make lint        Run lint checks"
	@echo "  make test        Run all tests"
	@echo "  make build       Build all buildable targets"
	@echo "  make security    Run local security scans"
	@echo "  make ci          Run local CI gate"
	@echo "  make ci-static-web        Static / Web gate: format, lint, typecheck"
	@echo "  make ci-static-admin-web  Static / Admin gate: format, lint, typecheck"
	@echo "  make ci-static-go         Static / Go gate: gofmt, vet, golangci-lint"
	@echo "  make ci-static-repository Static / Repository gate: docs format, CI config/architecture"
	@echo "  make ci-architecture-check Prove every CI gate still has exactly one owner"
	@echo "  make ci-infra-config      Infra / Config gate: versioned operational config"
	@echo "  make ci-infra-kubernetes  Infra / Kubernetes gate: manifests, sealed secrets"
	@echo "  make ci-infra-migrations  Infra / Migrations gate: static check, Blue/Green gate"
	@echo "  make ci-infra-release-safety Infra / Release Safety gate: release mechanism tests"
	@echo "  make test-integration-go  Go suites that need a real PostgreSQL (CHAT_TEST_DATABASE_URL)"
	@echo "  make poc-seaweedfs  Run SeaweedFS PoC (requires Docker)"
	@echo "  make poc-valkey     Run Valkey PoC (requires Docker)"
	@echo "  make poc-config-check Validate PoC scripts and config (CI-safe)"
	@echo "  make observability-config-check Validate observability config (CI-safe)"
	@echo "  make grafana-dashboard-check Validate Grafana dashboard provisioning"
	@echo "  make migrations-check        Validate SQL migration files (no DB required)"
	@echo "  make migrations-up           Apply all pending migrations (requires DB)"
	@echo "  make migrations-down         Roll back last migration (requires DB)"
	@echo "  make migrations-status       Show migration status (requires DB)"
	@echo "  make migrations-reset        Roll back all migrations interactively (requires DB)"
	@echo "  make migrations-smoke        Run migration smoke test (requires Docker Compose)"
	@echo "  make db-restore-test         Prove backup/restore keeps role ownership (requires Docker)"
	@echo "  make migrations-blue-green-test Run Blue/Green migration gate fixtures (CI-safe)"
	@echo "  make prod-blue-green-check   Validate the production Blue/Green overlay (CI-safe)"
	@echo "  make prod-stateful-check     Validate the production stateful overlay (CI-safe)"
	@echo "  make prod-stateful-check-test Run stateful gate negative tests (CI-safe)"
	@echo "  make prod-stateful-preflight-test Run stateful preflight negative tests (CI-safe)"
	@echo "  make prod-stateful-apply     Apply the production stateful layer (requires cluster)"
	@echo "  make prod-blue-green-check-test Run manifest gate negative tests (CI-safe)"
	@echo "  make prod-blue-green-test    Run production Blue/Green script tests (CI-safe)"
	@echo "  make prod-blue-green-query-test Run manifest reader unit tests (CI-safe)"
	@echo "  make prod-capacity-test      Run capacity preflight fixtures (CI-safe)"
	@echo "  make prod-release-manifest-test Run release manifest tests (CI-safe)"
	@echo "  make cd-workflows-check     Verify the five continuous delivery workflow contracts"
	@echo "  make cd-workflows-test      Run the continuous delivery workflow contract tests (CI-safe)"
	@echo "  make prod-lifecycle-test     Run the release lifecycle/drain tests (CI-safe)"
	@echo "  make prod-rollback-schema-test Run the rollback schema compatibility gate tests (CI-safe)"
	@echo "  make dev-smoke-test          Run the nchat-dev smoke tests (CI-safe)"
	@echo "  make prod-runner-guard-test  Run production runner pre-job guard tests (CI-safe)"
	@echo "  make prod-capacity-evidence  Collect cluster capacity evidence: ARGS=\"<output-dir>\""
	@echo "  make prod-blue-green-status  Show the production release slots (requires cluster)"
	@echo "  make prod-blue-green-bootstrap Establish production with Blue as baseline (requires cluster)"
	@echo "  make prod-blue-green-deploy  Deploy the release into the candidate slot (requires cluster)"
	@echo "  make prod-blue-green-smoke   Automated smoke of a slot: ARGS=\"--target green\""
	@echo "  make prod-blue-green-cutover Promote a slot: ARGS=\"--target green\""
	@echo "  make prod-blue-green-rollback Roll back: ARGS=\"--target blue 'reason'\""
	@echo "  make prod-blue-green-drain-old Retire a slot: ARGS=\"--target blue\""
	@echo "  make prod-notification-levels Conversation notification levels: ARGS=\"--status\" | ARGS=\"--set false\""
	@echo "  make dev-observability-up    Start Prometheus, Grafana, Jaeger"
	@echo "  make dev-observability-down  Stop observability stack"
	@echo "  make dev-observability-status Show observability stack status"
	@echo "  make dev-observability-validate Validate observability stack"
	@echo "  make dev-media-up    Start LiveKit + coturn dev stack (profile: media)"
	@echo "  make dev-media-status Show LiveKit + coturn dev stack status"
	@echo "  make dev-media-validate Create a room and connect a real participant"
	@echo "  make dev-media-down  Stop LiveKit + coturn dev stack"
	@echo "  make media-config-check Validate LiveKit/coturn config (CI-safe)"
	@echo "  make qa-webrtc-office-network Validate WebRTC on the real office network (requires Docker + real network)"
	@echo "  make webrtc-office-network-config-check Validate WebRTC office network QA config (CI-safe)"

install:
	pnpm install

dev-web:
	pnpm dev:web

dev-admin-web:
	pnpm dev:admin-web

dev-env-up:
	pnpm dev:env:up

dev-env-down:
	pnpm dev:env:down

dev-env-reset:
	pnpm dev:env:reset

dev-env-status:
	pnpm dev:env:status

dev-env-logs:
	pnpm dev:env:logs

dev-env-validate:
	pnpm dev:env:validate

dev-env-config-check:
	pnpm dev:env:config-check

dev-gateway-up:
	pnpm dev:gateway:up

dev-gateway-down:
	pnpm dev:gateway:down

dev-gateway-status:
	pnpm dev:gateway:status

dev-gateway-logs:
	pnpm dev:gateway:logs

dev-gateway-validate:
	pnpm dev:gateway:validate

dev-tls-generate:
	pnpm tls:generate

dev-tls-status:
	pnpm tls:status

dev-tls-clean:
	pnpm tls:clean

tls-config-check:
	pnpm tls:config-check

k8s-render:
	pnpm k8s:render

k8s-validate:
	pnpm k8s:validate

k8s-render-staging:
	pnpm k8s:render:staging

k8s-validate-staging:
	pnpm k8s:validate:staging

k8s-apply-dev:
	pnpm k8s:apply:dev

k8s-delete-dev:
	pnpm k8s:delete:dev

k8s-status-dev:
	pnpm k8s:status:dev

k8s-ci:
	pnpm k8s:ci

health-contract-check:
	pnpm health:contract-check

ci-config-check:
	pnpm ci:config-check

# Proves every Dockerfile copies the local module-replace targets its build
# needs. Offline: reads go.mod and the Dockerfiles, builds nothing.
images-module-inputs-check:
	pnpm images:module-inputs-check

images-module-inputs-check-test:
	pnpm images:module-inputs-check-test

gateway-config-check:
	pnpm gateway:config-check

web-security-headers-check:
	pnpm web:security-headers-check

web-livekit-integration-check:
	pnpm web:livekit-integration-check

sealed-secrets-validate:
	pnpm sealed-secrets:validate

sealed-secrets-policy-check:
	pnpm sealed-secrets:policy-check

sealed-secrets-install-controller:
	pnpm sealed-secrets:install-controller

sealed-secrets-fetch-cert:
	pnpm sealed-secrets:fetch-cert

build-web:
	pnpm build:web

build-admin-web:
	pnpm build:admin-web

test-web:
	pnpm test:web

test-admin-web:
	pnpm test:admin-web

lint-web:
	pnpm lint:web

lint-admin-web:
	pnpm lint:admin-web

test-go:
	pnpm test:go

vet-go:
	pnpm vet:go

fmt-go:
	pnpm fmt:go

format:
	pnpm format

format-check:
	pnpm format:check

lint-go:
	pnpm lint:go

go-coverage:
	pnpm test:coverage:go

go-coverage-check:
	pnpm test:coverage:go:check

web-coverage:
	pnpm test:coverage:web

coverage:
	pnpm test:coverage

lint:
	pnpm lint

test:
	pnpm test

build:
	pnpm build

security:
	pnpm security

security-secrets:
	pnpm security:secrets

security-govulncheck:
	pnpm security:govulncheck

security-govulncheck-gate-test:
	pnpm security:govulncheck-gate-test

security-trivy-fs:
	pnpm security:trivy:fs

security-trivy-config:
	pnpm security:trivy:config

ci:
	pnpm run ci

# One target per CI owner. Each reproduces exactly one job of .github/workflows/ci.yml.
ci-static-web:
	pnpm ci:static:web

ci-static-admin-web:
	pnpm ci:static:admin-web

ci-static-go:
	pnpm ci:static:go

ci-static-repository:
	pnpm ci:static:repository

ci-architecture-check:
	pnpm ci:architecture-check

ci-infra-config:
	pnpm ci:infra:config

ci-infra-kubernetes:
	pnpm ci:infra:kubernetes

ci-infra-migrations:
	pnpm ci:infra:migrations

ci-infra-release-safety:
	pnpm ci:infra:release-safety

# Needs a disposable PostgreSQL: the suite drops and recreates the chat schema.
#   CHAT_TEST_DATABASE_URL=postgresql://... make test-integration-go
test-integration-go:
	pnpm test:integration:go

poc-seaweedfs:
	pnpm poc:seaweedfs

poc-valkey:
	pnpm poc:valkey

poc-config-check:
	pnpm poc:config-check

observability-config-check:
	pnpm observability:config-check

grafana-dashboard-check:
	pnpm grafana:dashboard-check

migrations-check:
	pnpm migrations:check

migrations-up:
	pnpm migrations:up

migrations-down:
	pnpm migrations:down

migrations-status:
	pnpm migrations:status

migrations-reset:
	pnpm migrations:reset

migrations-smoke:
	pnpm migrations:smoke

# Proves the production backup/restore procedure preserves the least-privilege
# role model. Starts its own PostgreSQL, so it needs Docker but no compose stack
# and no cluster. Out of `pnpm ci` for the same reason migrations-smoke is.
db-restore-test:
	pnpm db:restore-test

migrations-blue-green-test:
	pnpm migrations:blue-green:test

prod-blue-green-check:
	pnpm prod:blue-green:check

prod-stateful-check:
	pnpm prod:stateful:check

prod-stateful-check-test:
	pnpm prod:stateful:check-test

prod-stateful-preflight-test:
	pnpm prod:stateful:preflight-test

# Applies the shared stateful layer to a real cluster. Separate from the release
# targets on purpose: it writes over production storage and is run once, before
# prod-blue-green-bootstrap.
prod-stateful-apply:
	pnpm prod:stateful:apply

prod-blue-green-check-test:
	pnpm prod:blue-green:check-test

prod-blue-green-test:
	pnpm prod:blue-green:test

prod-blue-green-query-test:
	pnpm prod:blue-green:query-test

prod-capacity-test:
	pnpm prod:capacity:test

# Proves the immutable release manifest refuses an incomplete or mismatched
# set of image digests, and that its SHA-256 seals the file it names.
prod-release-manifest-test:
	pnpm prod:release-manifest:test

# Proves the production deploy workflow is candidate-only: no job, step or
# input in it can move production traffic, and the stable Service selectors it
# records before the deploy are proved unchanged after. Offline: parses the
# workflow and drives the release binding, touches no cluster.
cd-workflows-check:
	pnpm cd:workflows:check

cd-workflows-test:
	pnpm cd:workflows:test

prod-lifecycle-test:
	pnpm prod:lifecycle:test

prod-rollback-schema-test:
	pnpm prod:rollback-schema:test

dev-smoke-test:
	pnpm dev:smoke:test

# Proves the production runner's pre-job guard authorises exactly one context
# and refuses every other repository, workflow, ref and event. Offline: runs
# the guard itself, touches no runner and no cluster.
prod-runner-guard-test:
	pnpm prod:runner-guard:test

# Collects the cluster-wide half of the capacity preflight. Run from a context
# that may read Nodes and Pods across namespaces -- not the deploy identity,
# which is namespaced and refused both.
#   make prod-capacity-evidence ARGS=/secure/path/capacity-evidence
prod-capacity-evidence:
	pnpm prod:capacity:evidence $(ARGS)

# The operational targets below act on a real production cluster. Each script
# validates the kube context and refuses an unexpected one; ARGS carries the
# mandatory explicit --target for the mutating ones.
prod-blue-green-status:
	pnpm prod:blue-green:status

prod-blue-green-bootstrap:
	pnpm prod:blue-green:bootstrap

prod-blue-green-deploy:
	pnpm prod:blue-green:deploy

prod-blue-green-smoke:
	pnpm prod:blue-green:smoke $(ARGS)

# The post-traffic profile: run after a cutover or a rollback, never on a
# candidate. smoke.sh is the candidate one and requires isolation, so the two
# are separate commands rather than one with a flag.
prod-blue-green-stable-smoke:
	pnpm prod:blue-green:stable-smoke $(ARGS)

# The same smoke, with its result recorded in the lifecycle record. This is
# what the workflows run, and it is the operator's way to unblock a release
# whose post-cutover smoke failed, was investigated and now passes.
prod-blue-green-record-traffic-smoke:
	pnpm prod:blue-green:record-traffic-smoke $(ARGS)

prod-blue-green-rollback-schema-gate:
	pnpm prod:blue-green:rollback-schema-gate $(ARGS)

dev-smoke:
	pnpm dev:smoke

prod-blue-green-cutover:
	pnpm prod:blue-green:cutover $(ARGS)

prod-blue-green-rollback:
	pnpm prod:blue-green:rollback $(ARGS)

prod-blue-green-drain-old:
	pnpm prod:blue-green:drain-old $(ARGS)

prod-notification-levels:
	pnpm prod:notification-levels $(ARGS)

dev-observability-up:
	pnpm dev:observability:up

dev-observability-down:
	pnpm dev:observability:down

dev-observability-status:
	pnpm dev:observability:status

dev-observability-logs:
	pnpm dev:observability:logs

dev-observability-validate:
	pnpm dev:observability:validate

dev-media-up:
	pnpm dev:media:up

dev-media-down:
	pnpm dev:media:down

dev-media-status:
	pnpm dev:media:status

dev-media-logs:
	pnpm dev:media:logs

dev-media-validate:
	pnpm dev:media:validate

media-config-check:
	pnpm media:config-check

qa-webrtc-office-network:
	pnpm qa:webrtc-office-network

webrtc-office-network-config-check:
	pnpm webrtc-office-network:config-check
