.DEFAULT_GOAL := help

BIN_DIR := bin
BINARY := $(BIN_DIR)/poly-txt
CSS_INPUT := internal/app/static/input.css
CSS_OUTPUT := internal/app/static/style.css
DOCKER_IMAGE := poly-txt:latest
SECRET_FILE := .jwt_secret

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

.PHONY: docker-build
docker-build: ## Build the Docker image (poly-txt:latest)
	docker build -t $(DOCKER_IMAGE) .

.PHONY: docker-run
docker-run: docker-build ## Run the container (reuses the secret in .jwt_secret)
	@test -f $(SECRET_FILE) || { openssl rand -hex 32 > $(SECRET_FILE); \
		echo "Generated a new signing secret in $(SECRET_FILE). Keep it: it also decrypts stored SSH keys."; }
	docker run --rm -p 3000:3000 \
		-e JWT_SECRET="$$(cat $(SECRET_FILE))" \
		-v poly-txt-data:/data \
		--name poly-txt $(DOCKER_IMAGE)

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	rm -f $(CSS_OUTPUT)
	rm -f data/latex.db
