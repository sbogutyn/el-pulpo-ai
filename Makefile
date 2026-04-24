SHELL := /usr/bin/env bash

DATABASE_URL ?= postgres://pulpo:pulpo@localhost:5432/pulpo?sslmode=disable
MIGRATE      ?= go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate

.PHONY: dev-up dev-down migrate-up migrate-down migrate-new \
        proto run-mastermind run-worker test tidy build

dev-up:
	docker compose up -d

dev-down:
	docker compose down

migrate-up:
	$(MIGRATE) -path ./migrations -database "$(DATABASE_URL)" up

migrate-down:
	$(MIGRATE) -path ./migrations -database "$(DATABASE_URL)" down 1

migrate-new:
	@test -n "$(NAME)" || (echo "usage: make migrate-new NAME=add_xxx" && exit 1)
	$(MIGRATE) create -dir ./migrations -ext sql -seq $(NAME)

proto:
	protoc \
	  --go_out=. --go_opt=module=github.com/sbogutyn/el-pulpo-ai \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/sbogutyn/el-pulpo-ai \
	  internal/proto/tasks.proto

run-mastermind:
	DATABASE_URL=$(DATABASE_URL) \
	WORKER_TOKEN=devtoken \
	ADMIN_TOKEN=devtoken \
	ADMIN_USER=admin ADMIN_PASSWORD=admin \
	go run ./cmd/mastermind

run-worker:
	MASTERMIND_ADDR=localhost:50051 \
	WORKER_TOKEN=devtoken \
	go run ./cmd/worker

test:
	go test ./... -race -count=1

tidy:
	go mod tidy

build:
	CGO_ENABLED=0 go build -o bin/mastermind ./cmd/mastermind
	CGO_ENABLED=0 go build -o bin/worker ./cmd/worker
