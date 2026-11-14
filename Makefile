SHELL := /bin/bash
export GOTOOLCHAIN ?= auto
SQLC ?= $(shell go env GOPATH)/bin/sqlc
COMPOSE ?= podman compose
-include .env
export

.PHONY: run worker build test test-int lint fmt db-up db-down migrate-up migrate-down sqlc seed
run:
	go run ./cmd/api
worker:
	go run ./cmd/worker
build:
	go build -o bin/api ./cmd/api
	go build -o bin/worker ./cmd/worker
	go build -o bin/ops ./cmd/ops
test:
	go test -race ./...
test-int:
	go test -race -tags=integration ./...
lint:
	@test -z "$$(gofmt -l cmd internal)" || (echo 'Run make fmt'; exit 1)
	go vet ./...
fmt:
	gofmt -w cmd internal
db-up:
	$(COMPOSE) up -d --wait postgres
db-down:
	$(COMPOSE) down
migrate-up:
	migrate -path db/migrations -database "$(DATABASE_URL)" up
migrate-down:
	migrate -path db/migrations -database "$(DATABASE_URL)" down 1
sqlc:
	$(SQLC) generate
seed:
	@test -n "$(DATABASE_URL)" || (echo 'DATABASE_URL is required'; exit 1)
	@test -n "$(ADMIN_EMAIL)" || (echo 'ADMIN_EMAIL is required (bootstrap school email)'; exit 1)
	@test -n "$(OPERATOR)" || (echo 'OPERATOR is required (verified internal operator)'; exit 1)
	@test -n "$(TICKET)" || (echo 'TICKET is required (verification reference)'; exit 1)
	psql "$(DATABASE_URL)" -v ON_ERROR_STOP=1 -v admin_email="$(ADMIN_EMAIL)" -v operator="$(OPERATOR)" -v ticket="$(TICKET)" -f db/seed/seed.sql
