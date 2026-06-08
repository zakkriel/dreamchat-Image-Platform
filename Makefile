SHELL := /bin/bash

POSTGRES_DSN ?= postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable
APP_PORT     ?= 8080

.PHONY: help up down dev migrate seed test build generate fmt vet lint wait-ready

help:
	@echo "Targets:"
	@echo "  make up        - docker compose up -d"
	@echo "  make down      - docker compose down -v"
	@echo "  make migrate   - apply migrations/0001_initial.up.sql"
	@echo "  make seed      - insert one dev API token (raw value printed once)"
	@echo "  make dev       - up + wait-for-ready + migrate + seed"
	@echo "  make test      - go test ./..."
	@echo "  make build     - go build ./..."
	@echo "  make generate  - run oapi-codegen + sqlc generate (placeholder)"
	@echo "  make fmt       - gofmt -w ."
	@echo "  make vet       - go vet ./..."
	@echo "  make lint      - golangci-lint run"

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
	@echo "Applying migrations/0001_initial.up.sql..."
	docker compose exec -T postgres psql -U image_platform -d image_platform -v ON_ERROR_STOP=1 < migrations/0001_initial.up.sql
	@echo "Migration complete."

seed:
	@bash scripts/seed_dev_token.sh

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
