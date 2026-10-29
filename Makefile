# ==============================================================================
# Project: Echo
# A complete Makefile for Go Backend, Next.js Frontend, and Docker
# ==============================================================================

# Directories & Settings
BACKEND_DIR := .
WEB_DIR     := ./web
BIN_DIR     := ./bin
SERVER_MAIN := ./internal/api/server.go
BINARY_NAME := server

# Tools & Variables
GOPATH      := $(shell go env GOPATH)
GOBIN       := $(if $(shell go env GOBIN),$(shell go env GOBIN),$(GOPATH)/bin)
PATH        := $(GOBIN):$(PATH)

# Unset GOROOT so Go auto-matches the toolchain compiler instead of conflicting with /usr/local/go
unexport GOROOT

.PHONY: all help dev run build clean tidy fmt lint test \
        docker-up docker-down docker-logs docker-reset db-shell \
        web-install web-dev web-build web-lint tools

# Default target
all: help

# ------------------------------------------------------------------------------
# Help
# ------------------------------------------------------------------------------
help: ## Show this help message
	@echo "Usage: make [target]"
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ------------------------------------------------------------------------------
# Backend (Go)
# ------------------------------------------------------------------------------
run: ## Run the Go backend server directly
	go run $(SERVER_MAIN)

dev: run ## Alias for run

build: ## Build the Go backend binary
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY_NAME) $(SERVER_MAIN)
	@echo "Built binary at $(BIN_DIR)/$(BINARY_NAME)"

clean: ## Clean build artifacts
	rm -rf $(BIN_DIR)

tidy: ## Download and clean up Go modules
	go mod tidy

fmt: ## Format all Go code (gofmt + goimports)
	@echo "Formatting Go files..."
	gofmt -s -w .
	@if command -v goimports > /dev/null 2>&1; then \
		goimports -w .; \
	else \
		echo "goimports not found, run 'make tools' to install it."; \
	fi

lint: ## Run Go linters (go vet + golangci-lint)
	@echo "Running go vet..."
	go vet ./...
	@if command -v golangci-lint > /dev/null 2>&1; then \
		echo "Running golangci-lint..."; \
		golangci-lint run; \
	else \
		echo "golangci-lint not found, run 'make tools' to install it."; \
	fi

test: ## Run Go tests
	go test -v ./...

# ------------------------------------------------------------------------------
# Docker & Database
# ------------------------------------------------------------------------------
docker-up: ## Start Postgres Docker container in background
	docker compose up -d

docker-down: ## Stop Postgres Docker container
	docker compose down

docker-logs: ## Follow Postgres Docker logs
	docker compose logs -f postgres

docker-reset: ## Wipe Postgres volume and re-run migrations fresh
	docker compose down -v
	docker compose up -d

db-shell: ## Open interactive psql shell inside running Postgres container
	docker exec -it echo_postgres psql -U echo_admin -d echo_db

# ------------------------------------------------------------------------------
# Frontend (Next.js)
# ------------------------------------------------------------------------------
web-install: ## Install frontend dependencies
	pnpm --prefix $(WEB_DIR) install

web-dev: ## Start Next.js frontend in development mode
	pnpm --prefix $(WEB_DIR) dev

web-build: ## Build Next.js frontend for production
	pnpm --prefix $(WEB_DIR) build

web-lint: ## Run frontend ESLint
	pnpm --prefix $(WEB_DIR) lint

# ------------------------------------------------------------------------------
# Tooling Installation
# ------------------------------------------------------------------------------
tools: ## Install goimports and golangci-lint locally into GOPATH/bin
	@echo "Installing goimports..."
	go install golang.org/x/tools/cmd/goimports@latest
	@echo "Installing golangci-lint..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@echo "Tools installed to $(GOBIN). Ensure $(GOBIN) is in your PATH."