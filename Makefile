.PHONY: help install dev-web dev-env-up dev-env-down dev-env-reset dev-env-status dev-env-logs dev-env-validate dev-env-config-check dev-gateway-up dev-gateway-down dev-gateway-status dev-gateway-logs dev-gateway-validate dev-tls-generate dev-tls-status dev-tls-clean tls-config-check k8s-render k8s-validate k8s-render-staging k8s-validate-staging k8s-apply-dev k8s-delete-dev k8s-status-dev k8s-ci health-contract-check ci-config-check gateway-config-check sealed-secrets-validate sealed-secrets-policy-check sealed-secrets-install-controller sealed-secrets-fetch-cert build-web test-web lint-web test-go vet-go fmt-go format format-check lint-go go-coverage go-coverage-check web-coverage coverage lint test build security security-secrets security-govulncheck security-trivy-fs security-trivy-config poc-seaweedfs poc-valkey poc-config-check observability-config-check grafana-dashboard-check migrations-check migrations-up migrations-down migrations-status migrations-reset migrations-smoke dev-observability-up dev-observability-down dev-observability-status dev-observability-logs dev-observability-validate dev-media-up dev-media-down dev-media-status dev-media-logs dev-media-validate media-config-check ci

help:
	@echo "NChat development commands"
	@echo "  make install     Install frontend dependencies"
	@echo "  make dev-web     Run web app locally"
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
	@echo "  make test-web    Run frontend tests"
	@echo "  make lint-web    Run frontend lint"
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
	@echo "  make dev-observability-up    Start Prometheus, Grafana, Jaeger"
	@echo "  make dev-observability-down  Stop observability stack"
	@echo "  make dev-observability-status Show observability stack status"
	@echo "  make dev-observability-validate Validate observability stack"
	@echo "  make dev-media-up    Start LiveKit + coturn dev stack (profile: media)"
	@echo "  make dev-media-status Show LiveKit + coturn dev stack status"
	@echo "  make dev-media-validate Create a room and connect a real participant"
	@echo "  make dev-media-down  Stop LiveKit + coturn dev stack"
	@echo "  make media-config-check Validate LiveKit/coturn config (CI-safe)"

install:
	pnpm install

dev-web:
	pnpm dev:web

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

gateway-config-check:
	pnpm gateway:config-check

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

test-web:
	pnpm test:web

lint-web:
	pnpm lint:web

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

security-trivy-fs:
	pnpm security:trivy:fs

security-trivy-config:
	pnpm security:trivy:config

ci:
	pnpm run ci

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
