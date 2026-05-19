.PHONY: help install dev-web build-web test-web lint-web test-go vet-go fmt-go format format-check lint-go go-coverage web-coverage coverage lint test build ci

help:
	@echo "NChat development commands"
	@echo "  make install     Install frontend dependencies"
	@echo "  make dev-web     Run web app locally"
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
	@echo "  make lint        Run lint checks"
	@echo "  make test        Run all tests"
	@echo "  make build       Build all buildable targets"
	@echo "  make ci          Run local CI gate"

install:
	pnpm install

dev-web:
	pnpm dev:web

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

ci:
	pnpm run ci
