# opencloud-backup-plugin — developer task runner.
#
# Targets mirror the CI pipeline so local == CI. See .github/workflows/ci.yml.

GO            ?= go
DIST          ?= dist
BINARIES      := backupd takeout decrypt
OPENCLOUD_DIR := test/fixtures/opencloud
DEV_COMPOSE   := docker-compose.dev.yml

# Platforms the offline recovery CLI must build for. It is the family's last
# resort, so it ships for every desktop OS (phase-5 exit criteria).
DECRYPT_PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

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

.PHONY: decrypt-release
decrypt-release: ## Cross-build the offline decrypt CLI for every supported OS.
	@mkdir -p $(DIST)
	@for p in $(DECRYPT_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "building decrypt for $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "-s -w" \
			-o $(DIST)/decrypt-$$os-$$arch$$ext ./cmd/decrypt || exit 1; \
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

.PHONY: secret-scan
secret-scan: ## Scan the full git history for committed secrets (gitleaks).
	gitleaks git . --redact --no-banner

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
