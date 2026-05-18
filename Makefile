.PHONY: help install dev-web build-web test-web lint-web test-go vet-go fmt-go lint test build

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
	@echo "  make lint        Run lint checks"
	@echo "  make test        Run all tests"
	@echo "  make build       Build all buildable targets"

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

lint:
	pnpm lint

test:
	pnpm test

build:
	pnpm build
