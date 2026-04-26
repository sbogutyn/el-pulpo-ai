SHELL := /usr/bin/env bash

DATABASE_URL ?= postgres://pulpo:pulpo@localhost:5432/pulpo?sslmode=disable
MIGRATE      ?= go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate

# --- Docker build config ----------------------------------------------------
DOCKER                 ?= docker
DOCKER_REGISTRY        ?=
DOCKER_NAMESPACE       ?= sbogutyn
DOCKER_BUILDKIT_EXPORT := DOCKER_BUILDKIT=1
VERSION                ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT                 ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE             ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PLATFORMS              ?= linux/amd64,linux/arm64

# Prefix tags with registry/ when provided.
image_ref = $(if $(DOCKER_REGISTRY),$(DOCKER_REGISTRY)/,)$(DOCKER_NAMESPACE)/el-pulpo-$(1)

MASTERMIND_IMAGE ?= $(call image_ref,mastermind)
WORKER_IMAGE     ?= $(call image_ref,worker)
MCP_IMAGE        ?= $(call image_ref,mastermind-mcp)

DOCKER_BUILD_ARGS = \
	--build-arg VERSION=$(VERSION) \
	--build-arg COMMIT=$(COMMIT) \
	--build-arg BUILD_DATE=$(BUILD_DATE)

.PHONY: dev-up dev-down migrate-up migrate-down migrate-new \
        proto run-mastermind run-worker run-mcp run-cli test tidy build build-mcp build-cli \
        demo-up demo-down demo-logs demo-build \
        docker-build docker-build-mastermind docker-build-worker docker-build-mcp \
        docker-buildx docker-buildx-mastermind docker-buildx-worker docker-buildx-mcp \
        docker-push docker-push-mastermind docker-push-worker docker-push-mcp \
        docker-run-mastermind docker-run-worker docker-run-mcp docker-clean

dev-up:
	docker compose up -d

dev-down:
	docker compose down

# --- Dashboard demo stack ---------------------------------------------------
# `demo-up` builds and starts: postgres + mastermind + 4 synthetic workers
# (orca-01, finch-02, mole-03, newt-04) + a seeder that keeps the queue full.
# Then browse to http://localhost:8080/dashboard (admin / admin).
demo-up:
	docker compose --profile demo up --build -d
	@echo
	@echo "  dashboard ready at: http://localhost:8080/dashboard"
	@echo "  basic-auth:         admin / admin"

demo-down:
	docker compose --profile demo down

demo-logs:
	docker compose --profile demo logs -f --tail=100

demo-build:
	docker compose --profile demo build

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

run-mcp:
	MASTERMIND_ADDR=localhost:50051 \
	ADMIN_TOKEN=devtoken \
	go run ./cmd/mastermind-mcp

test:
	go test ./... -race -count=1

tidy:
	go mod tidy

build:
	CGO_ENABLED=0 go build -o bin/mastermind ./cmd/mastermind
	CGO_ENABLED=0 go build -o bin/worker ./cmd/worker
	CGO_ENABLED=0 go build -o bin/mastermind-mcp ./cmd/mastermind-mcp
	CGO_ENABLED=0 go build -o bin/elpulpo ./cmd/elpulpo

build-mcp:
	CGO_ENABLED=0 go build -o bin/mastermind-mcp ./cmd/mastermind-mcp

build-cli:
	CGO_ENABLED=0 go build -o bin/elpulpo ./cmd/elpulpo

run-cli:
	MASTERMIND_ADDR=localhost:50051 \
	ADMIN_TOKEN=devtoken \
	go run ./cmd/elpulpo $(ARGS)

# --- Docker targets ---------------------------------------------------------

docker-build: docker-build-mastermind docker-build-worker docker-build-mcp

docker-build-mastermind:
	$(DOCKER_BUILDKIT_EXPORT) $(DOCKER) build \
		-f Dockerfile.mastermind \
		-t $(MASTERMIND_IMAGE):$(VERSION) \
		-t $(MASTERMIND_IMAGE):latest \
		$(DOCKER_BUILD_ARGS) \
		.

docker-build-worker:
	$(DOCKER_BUILDKIT_EXPORT) $(DOCKER) build \
		-f Dockerfile.worker \
		-t $(WORKER_IMAGE):$(VERSION) \
		-t $(WORKER_IMAGE):latest \
		$(DOCKER_BUILD_ARGS) \
		.

docker-build-mcp:
	$(DOCKER_BUILDKIT_EXPORT) $(DOCKER) build \
		-f Dockerfile.mcp \
		-t $(MCP_IMAGE):$(VERSION) \
		-t $(MCP_IMAGE):latest \
		$(DOCKER_BUILD_ARGS) \
		.

docker-buildx: docker-buildx-mastermind docker-buildx-worker docker-buildx-mcp

docker-buildx-mastermind:
	$(DOCKER) buildx build \
		--platform $(PLATFORMS) \
		-f Dockerfile.mastermind \
		-t $(MASTERMIND_IMAGE):$(VERSION) \
		-t $(MASTERMIND_IMAGE):latest \
		$(DOCKER_BUILD_ARGS) \
		$(if $(PUSH),--push,--load) \
		.

docker-buildx-worker:
	$(DOCKER) buildx build \
		--platform $(PLATFORMS) \
		-f Dockerfile.worker \
		-t $(WORKER_IMAGE):$(VERSION) \
		-t $(WORKER_IMAGE):latest \
		$(DOCKER_BUILD_ARGS) \
		$(if $(PUSH),--push,--load) \
		.

docker-buildx-mcp:
	$(DOCKER) buildx build \
		--platform $(PLATFORMS) \
		-f Dockerfile.mcp \
		-t $(MCP_IMAGE):$(VERSION) \
		-t $(MCP_IMAGE):latest \
		$(DOCKER_BUILD_ARGS) \
		$(if $(PUSH),--push,--load) \
		.

docker-push: docker-push-mastermind docker-push-worker docker-push-mcp

docker-push-mastermind:
	$(DOCKER) push $(MASTERMIND_IMAGE):$(VERSION)
	$(DOCKER) push $(MASTERMIND_IMAGE):latest

docker-push-worker:
	$(DOCKER) push $(WORKER_IMAGE):$(VERSION)
	$(DOCKER) push $(WORKER_IMAGE):latest

docker-push-mcp:
	$(DOCKER) push $(MCP_IMAGE):$(VERSION)
	$(DOCKER) push $(MCP_IMAGE):latest

docker-run-mastermind:
	$(DOCKER) run --rm --name mastermind \
		-p 50051:50051 -p 8080:8080 \
		--env-file .env \
		$(MASTERMIND_IMAGE):$(VERSION)

docker-run-worker:
	$(DOCKER) run --rm --name worker \
		--env-file .env \
		$(WORKER_IMAGE):$(VERSION)

docker-run-mcp:
	$(DOCKER) run --rm -i --name mastermind-mcp \
		--env-file .env \
		$(MCP_IMAGE):$(VERSION)

docker-clean:
	-$(DOCKER) rmi $(MASTERMIND_IMAGE):$(VERSION) $(MASTERMIND_IMAGE):latest 2>/dev/null
	-$(DOCKER) rmi $(WORKER_IMAGE):$(VERSION) $(WORKER_IMAGE):latest 2>/dev/null
	-$(DOCKER) rmi $(MCP_IMAGE):$(VERSION) $(MCP_IMAGE):latest 2>/dev/null
