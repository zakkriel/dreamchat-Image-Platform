SHELL := /bin/bash

POSTGRES_DSN ?= postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable
APP_PORT     ?= 8080

.PHONY: help up down dev migrate migrate-down migrate-status seed seed-admin test build generate fmt vet lint wait-ready

help:
	@echo "Targets:"
	@echo "  make up            - docker compose up -d"
	@echo "  make down          - docker compose down -v"
	@echo "  make migrate       - apply migrations via goose (cmd/migrate up)"
	@echo "  make migrate-down  - roll back the latest migration (cmd/migrate down)"
	@echo "  make migrate-status- show goose migration status"
	@echo "  make seed          - insert one dev API token (raw value printed once)"
	@echo "  make seed-admin    - insert one dev admin token (admin:costs only)"
	@echo "  make dev           - up + wait-for-ready + migrate + seed"
	@echo "  make test          - go test ./..."
	@echo "  make build         - go build ./..."
	@echo "  make generate      - run oapi-codegen + sqlc generate"
	@echo "  make fmt           - gofmt -w ."
	@echo "  make vet           - go vet ./..."
	@echo "  make lint          - golangci-lint run"

up:
	docker compose up -d --build

down:
	docker compose down -v

wait-ready:
	@echo "Waiting for Postgres..."
	@for i in $$(seq 1 60); do \
	  if docker compose exec -T postgres pg_isready -U image_platform >/dev/null 2>&1; then \
	    echo "Postgres ready"; break; \
	  fi; sleep 1; \
	done
	@echo "Waiting for API..."
	@for i in $$(seq 1 60); do \
	  if curl -fsS "http://localhost:$(APP_PORT)/health" >/dev/null 2>&1; then \
	    echo "API ready"; break; \
	  fi; sleep 1; \
	done

migrate:
	POSTGRES_DSN=$(POSTGRES_DSN) go run ./cmd/migrate up

migrate-down:
	POSTGRES_DSN=$(POSTGRES_DSN) go run ./cmd/migrate down

migrate-status:
	POSTGRES_DSN=$(POSTGRES_DSN) go run ./cmd/migrate status

seed:
	@bash scripts/seed_dev_token.sh

seed-admin:
	@bash scripts/seed_admin_token.sh

dev: up wait-ready migrate seed
	@echo ""
	@echo "DreamChat Image Platform is up."
	@echo "Health: curl -i http://localhost:$(APP_PORT)/health"

test:
	go test ./...

build:
	go build ./...

generate:
	@echo "Running oapi-codegen..."
	go tool oapi-codegen -config oapi-codegen.yaml api/openapi.yaml
	@echo "Running sqlc generate..."
	sqlc generate

fmt:
	gofmt -w .

vet:
	go vet ./...

lint:
	golangci-lint run
