# opencloud-backup-plugin — developer task runner.
#
# Targets mirror the CI pipeline so local == CI. See .github/workflows/ci.yml.

GO            ?= go
DIST          ?= dist
BINARIES      := backupd takeout decrypt
OPENCLOUD_DIR := test/fixtures/opencloud
DEV_COMPOSE   := docker-compose.dev.yml

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show this help.
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build all binaries into $(DIST).
	@mkdir -p $(DIST)
	@for b in $(BINARIES); do \
		echo "building $$b"; \
		CGO_ENABLED=0 $(GO) build -trimpath -o $(DIST)/$$b ./cmd/$$b || exit 1; \
	done

.PHONY: test
test: ## Run unit tests with the race detector.
	$(GO) test -race ./...

.PHONY: test-integration
test-integration: ## Run integration tests (Garage fixture; needs Docker).
	$(GO) test -tags integration ./...

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run ./...

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format code.
	$(GO) fmt ./...

.PHONY: k8s-validate
k8s-validate: ## Validate K8s manifests offline (kubeconform).
	kubeconform -strict -summary deploy/

.PHONY: dev-up
dev-up: ## Start dev Garage, then the test OpenCloud fixture.
	docker compose -f $(DEV_COMPOSE) up -d
	cd $(OPENCLOUD_DIR) && ./up.sh

.PHONY: dev-down
dev-down: ## Stop the OpenCloud fixture and dev Garage.
	-cd $(OPENCLOUD_DIR) && ./down.sh
	docker compose -f $(DEV_COMPOSE) down

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(DIST)
