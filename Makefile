.DEFAULT_GOAL := help

BIN_DIR := bin
BINARY := $(BIN_DIR)/poly-txt
CSS_INPUT := internal/app/static/input.css
CSS_OUTPUT := internal/app/static/style.css

.PHONY: help
help: ## Show this help
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: dev
dev: ## Run development server
	go run ./cmd/server

.PHONY: build
build: css ## Build production binary
	@mkdir -p $(BIN_DIR)
	go build -o $(BINARY) ./cmd/server

.PHONY: css
css: ## Compile Tailwind CSS for production
	npx tailwindcss -i $(CSS_INPUT) -o $(CSS_OUTPUT) --minify

.PHONY: css-watch
css-watch: ## Compile Tailwind CSS with hot reload
	npx tailwindcss -i $(CSS_INPUT) -o $(CSS_OUTPUT) --watch

.PHONY: format
format: ## Format Go code
	gofmt -w -s .

.PHONY: lint
lint: ## Lint Go code with staticcheck and vet
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "  (skip staticcheck: install via 'go install honnef.co/go/tools/cmd/staticcheck@latest')"; \
	fi

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: check
check: format lint test ## Run format, lint, and test

.PHONY: ci
ci: check build ## Run all checks then build

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	rm -f $(CSS_OUTPUT)
	rm -f data/latex.db
