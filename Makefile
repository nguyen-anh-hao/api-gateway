.DEFAULT_GOAL := help
SHELL := /bin/bash

ifneq (,$(wildcard .env))
include .env
export
endif

GO ?= go
IMAGE ?= api-gateway:local
SPEC ?= internal/swagger/openapi.yaml

.PHONY: help env run test lint fmt build check generate-check openapi docker clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

env: ## Create .env from .env.example when missing
	@test -f .env || cp .env.example .env

run: env ## Run the gateway locally
	$(GO) run ./cmd

test: ## Run unit tests
	$(GO) test ./...

lint: ## Vet packages
	$(GO) vet ./...

fmt: ## Format Go sources
	gofmt -w .

generate-check: ## Verify generated code matches the OpenAPI document
	$(GO) generate ./...
	@git diff --exit-code -- internal/api internal/swagger || { echo "generated code is stale"; exit 1; }

openapi: ## Lint the OpenAPI document
	npx --yes @redocly/cli@1 lint $(SPEC)

build: ## Compile all packages
	$(GO) build ./...

check: lint test openapi ## Lint, test and validate the API contract

docker: ## Build the container image
	docker build -t $(IMAGE) .

clean: ## Remove build artifacts
	$(GO) clean
