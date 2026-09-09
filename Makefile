# EpicPanel — developer commands
#
# Most common:
#   make test          — full backend test suite (serialized; safe every time)
#   make test-one PK=internal/billing
#   make build         — backend binaries
#   make frontend      — typecheck + build all three apps
#   make dev-api       — run the API locally against epicpanel_dev
#   make reset-testdb  — drop+recreate the shared test DB (fixes dirty state)
#   make release V=v1.0.0 [UPLOAD=1]

SHELL := /bin/bash
.PHONY: test test-one build frontend dev-api reset-testdb release vet fmt lint

# Test DB: override with your own, or set EPICPANEL_TEST_DATABASE_URL globally.
TESTDB ?= postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test?sslmode=disable

# IMPORTANT: -p 1 serializes test packages. The test DBs share the Postgres
# instance; parallel package tests race on migrations (pg_type catalog locks).
# Migrations also take a pg advisory lock now (belt and suspenders).
export EPICPANEL_TEST_DATABASE_URL = $(TESTDB)

test:
	cd backend && go build ./... && go vet ./... && go test ./... -p 1 -count=1

# Single package: make test-one PK=internal/billing
test-one:
	cd backend && go test ./$(PK)/... -p 1 -count=1

vet:
	cd backend && go vet ./...

fmt:
	cd backend && gofmt -l -w .

build:
	cd backend && go build -o bin/epicpanel-api ./cmd/api
	cd backend && go build -o bin/epicpanel-agent ./cmd/agent

frontend:
	cd frontend && npx tsc -b --force
	cd frontend && npm run build:customer
	cd frontend && npx vite build
	cd frontend && npx vite build --config apps/admin/vite.config.ts

dev-api:
	cd backend && EPICPANEL_DATABASE_URL=postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_dev?sslmode=disable go run ./cmd/api

# Fixes "relation does not exist"/"already exists"/pg_type failures caused by
# interrupted runs: drop + recreate the shared test DB, then re-run make test.
reset-testdb:
	sudo -u postgres psql -c "DROP DATABASE IF EXISTS epicpanel_test WITH (FORCE)"
	sudo -u postgres createdb -O epicpanel epicpanel_test
	sudo -u postgres psql -c "GRANT ALL ON SCHEMA public TO epicpanel"
	@echo "test DB reset — run: make test"

release:
	cd backend && ./build-release.sh $(V) $(if $(UPLOAD),upload $(V),)

lint: vet fmt
	@cd frontend && npx tsc -b --force
