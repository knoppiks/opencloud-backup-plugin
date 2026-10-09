# opencloud-backup-plugin — developer task runner.
#
# Targets mirror the CI pipeline so local == CI. See .github/workflows/ci.yml.

GO            ?= go
DIST          ?= dist
BINARIES      := backupd takeout decrypt
IMAGE         ?= opencloud-backupd
IMAGE_TAG     ?= dev
WEB_IMAGE     ?= opencloud-backup-vault-web
OPENCLOUD_DIR := test/fixtures/opencloud
DEV_COMPOSE   := docker-compose.dev.yml
WEB_DIR       := web
# Corepack reads the pnpm version from web/package.json, so the toolchain is
# pinned by the same file CI uses rather than by whatever is on $PATH.
PNPM          ?= corepack pnpm

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

# Fuzz targets are found by name, so a new one joins `make fuzz` (and CI) by
# being written. `go test -fuzz` takes one target in one package per run.
FUZZTIME ?= 10s
FUZZ_TARGETS = grep -rHE --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(' pkg internal cmd | \
	sed -E 's\#^(.*)/[^/]*:func (Fuzz[A-Za-z0-9_]*)\(.*\#./\1 \2\#' | sort -u

# Every target runs even after one fails, so one finding does not hide the
# next; the failed targets are listed at the end.
.PHONY: fuzz
fuzz: ## Run every fuzz target for FUZZTIME each (default 10s; crashers land in testdata/fuzz/).
	@$(FUZZ_TARGETS) | { n=0; failed=""; while read -r pkg target; do \
		n=$$((n+1)); echo "fuzz $$pkg $$target ($(FUZZTIME))"; \
		$(GO) test -run '^$$' -fuzz "^$$target\$$" -fuzztime $(FUZZTIME) "$$pkg" </dev/null || failed="$$failed $$pkg:$$target"; \
	done; test $$n -gt 0 || { echo "no fuzz targets found" >&2; exit 1; }; \
	test -z "$$failed" || { echo "fuzz targets failed:$$failed" >&2; exit 1; }; }

.PHONY: fuzz-list
fuzz-list: ## List the fuzz targets `make fuzz` runs.
	@$(FUZZ_TARGETS)

# A failing input is the one file `go test -fuzz` adds under testdata/fuzz/;
# the committed corpus is tracked, so untracked files there are the findings.
FUZZ_CRASHERS ?= $(DIST)/fuzz-crashers

.PHONY: fuzz-crashers
fuzz-crashers: ## Copy the failing inputs `make fuzz` found to FUZZ_CRASHERS, keeping their paths.
	@rm -rf $(FUZZ_CRASHERS)
	@git ls-files --others --exclude-standard -- '*/testdata/fuzz/*' | while read -r f; do \
		mkdir -p "$(FUZZ_CRASHERS)/$$(dirname "$$f")" && cp "$$f" "$(FUZZ_CRASHERS)/$$f" && echo "$$f"; \
	done

.PHONY: test-integration
test-integration: ## Run integration tests (Garage fixture; needs Docker).
	$(GO) test -tags integration ./...

.PHONY: test-opencloud
test-opencloud: ## Run the OpenCloud-fixture tests (run dev-up first; failures, not skips).
	@test -f $(OPENCLOUD_DIR)/fixture.env || \
		{ echo "no $(OPENCLOUD_DIR)/fixture.env — run 'make dev-up' and $(OPENCLOUD_DIR)/seed.sh first" >&2; exit 1; }
	set -a; . $(OPENCLOUD_DIR)/fixture.env; set +a; \
	OPENCLOUD_FIXTURE_REQUIRED=1 $(GO) test -tags integration -count=1 \
		./pkg/cs3/... ./pkg/cs3state/... ./pkg/api/... ./pkg/ocversion/... ./pkg/restore/... ./pkg/backup/...

.PHONY: web-install
web-install: ## Install the web extension's dependencies (frozen lockfile).
	cd $(WEB_DIR) && $(PNPM) install --frozen-lockfile

.PHONY: web-lint
web-lint: ## Lint and format-check the web extension.
	cd $(WEB_DIR) && $(PNPM) lint
	cd $(WEB_DIR) && $(PNPM) format:check

.PHONY: web-format
web-format: ## Reformat the web extension in place (prettier).
	cd $(WEB_DIR) && $(PNPM) format

.PHONY: web-test
web-test: ## Run the web extension's unit tests (includes the Go interop vectors).
	cd $(WEB_DIR) && $(PNPM) test

.PHONY: web-typecheck
web-typecheck: ## Typecheck the web extension.
	cd $(WEB_DIR) && $(PNPM) typecheck

.PHONY: web-build
web-build: ## Build the web extension bundle into $(WEB_DIR)/dist.
	cd $(WEB_DIR) && $(PNPM) build

.PHONY: web-install-fixture
web-install-fixture: ## Build the extension into the OpenCloud fixture and verify it loaded.
	$(OPENCLOUD_DIR)/install-webapp.sh

.PHONY: web-verify-fixture
web-verify-fixture: ## Re-verify the installed extension without rebuilding it.
	$(OPENCLOUD_DIR)/install-webapp.sh --no-build

.PHONY: e2e
e2e: ## Run the browser end-to-end tests (dev-up and web-install-fixture first; starts its own backupd).
	cd $(WEB_DIR) && $(PNPM) e2e

.PHONY: web-vectors
web-vectors: ## Regenerate the browser-produced interop vectors, then verify Go opens them.
	cd $(WEB_DIR) && $(PNPM) vectors
	$(GO) test ./pkg/keys -run TestBrowserVectors -count=1

.PHONY: generate
generate: ## Regenerate checked-in generated files (environment references).
	$(GO) test ./cmd/backupd ./cmd/takeout -run '^TestEnvironmentReference$$' -count=1 -update

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run ./...

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format code.
	$(GO) fmt ./...

.PHONY: image
image: ## Build the service container image ($(IMAGE):$(IMAGE_TAG)).
	docker build -t $(IMAGE):$(IMAGE_TAG) .

.PHONY: web-image
web-image: ## Build the web bundle image for an initContainer ($(WEB_IMAGE):$(IMAGE_TAG)).
	docker build -t $(WEB_IMAGE):$(IMAGE_TAG) $(WEB_DIR)

.PHONY: k8s-validate
k8s-validate: ## Validate K8s manifests offline (kubeconform).
	kubeconform -strict -summary deploy/

.PHONY: secret-scan
secret-scan: ## Scan HEAD's full git history for committed secrets (gitleaks).
	gitleaks git . --log-opts="HEAD" --redact --no-banner

.PHONY: dev-up
dev-up: ## Start dev Garage, then the test OpenCloud fixture (and seed it).
	docker compose -f $(DEV_COMPOSE) up -d
	cd $(OPENCLOUD_DIR) && ./up.sh
	# up.sh rewrites fixture.env from scratch, so the seeded ids have to be
	# appended again afterwards or `make test-opencloud` fails on a variable it
	# reports as "not set" rather than as "not seeded" — which reads like a
	# broken test rather than an unseeded fixture.
	cd $(OPENCLOUD_DIR) && ./seed.sh

.PHONY: dev-down
dev-down: ## Stop the OpenCloud fixture and dev Garage.
	-cd $(OPENCLOUD_DIR) && ./down.sh
	docker compose -f $(DEV_COMPOSE) down

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(DIST)
