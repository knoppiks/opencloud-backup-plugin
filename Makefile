# opencloud-backup-plugin — developer task runner.
#
# Targets mirror the CI pipeline so local == CI. See .github/workflows/ci.yml.

GO            ?= go
DIST          ?= dist
BINARIES      := backupd takeout decrypt install-bundle
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

# Unit tests build with kopia's own `testing` tag: repositories they create
# derive their key with kopia's fast test derivation instead of scrypt, which
# was most of the run under -race (#94). The integration runs under -race set
# it too (#93): scrypt is pure Go, so the race detector multiplies its cost.
# The release derivation stays covered by `make test-integration-release`,
# built without the tag and without -race, and by the frozen Take-Out every
# run opens. Never set this tag on a build that ships.
UNIT_TEST_TAGS := testing

.PHONY: test
test: ## Run unit tests with the race detector (kopia's fast test key derivation).
	$(GO) test -race -tags $(UNIT_TEST_TAGS) ./...

# Integration tests are the Test functions in files built only with the
# `integration` tag. The unit tests in the same packages already ran in
# `make test`, so the integration runs select these by name. Derived from the
# sources: a new integration test joins by being written.
INTEGRATION_FILES = grep -rlE --include='*_test.go' '^//go:build integration$$' pkg internal cmd
# Plus the tests only a build without kopia's `testing` tag compiles.
RELEASE_KDF_FILES = grep -rlE --include='*_test.go' '^//go:build (integration|!testing)$$' pkg internal cmd
test_names = $(shell $(1) | xargs grep -hoE '^func Test[A-Za-z0-9_]*' | sed 's/^func //' | sort -u | paste -sd'|' -)
test_pkgs  = $(shell $(1) | xargs -n1 dirname | sort -u | sed 's|^|./|')
INTEGRATION_RUN  = ^($(call test_names,$(INTEGRATION_FILES)))$$
INTEGRATION_PKGS = $(call test_pkgs,$(INTEGRATION_FILES))
RELEASE_KDF_RUN  = ^($(call test_names,$(RELEASE_KDF_FILES)))$$
RELEASE_KDF_PKGS = $(call test_pkgs,$(RELEASE_KDF_FILES))
INTEGRATION_TIMEOUT ?= 20m

# Coverage is measured across the whole module (-coverpkg=./...), so code
# exercised only through another package's tests counts. Reported, not gated
# (phase-10-hardening.md §10.8; the gate is Phase 14's).
COVERPROFILE             ?= $(DIST)/coverage.out
INTEGRATION_COVERPROFILE ?= $(DIST)/coverage-integration.out
COVERTITLE               ?= unit tests
GO_TEST_COVER = $(GO) test -race -covermode=atomic -coverpkg=./...

.PHONY: cover
cover: ## `make test` with module-wide coverage written to COVERPROFILE.
	@mkdir -p $(dir $(COVERPROFILE))
	$(GO_TEST_COVER) -tags $(UNIT_TEST_TAGS) -coverprofile=$(COVERPROFILE) ./...
	@$(GO) tool cover -func=$(COVERPROFILE) | tail -n 1

.PHONY: cover-integration
cover-integration: ## `make test-integration` with module-wide coverage written to INTEGRATION_COVERPROFILE.
	@mkdir -p $(dir $(INTEGRATION_COVERPROFILE))
	$(GO_TEST_COVER) -tags integration,$(UNIT_TEST_TAGS) -timeout $(INTEGRATION_TIMEOUT) \
		-run '$(INTEGRATION_RUN)' -coverprofile=$(INTEGRATION_COVERPROFILE) $(INTEGRATION_PKGS)
	@$(GO) tool cover -func=$(INTEGRATION_COVERPROFILE) | tail -n 1

.PHONY: integration-list
integration-list: ## List the packages and tests the integration targets select.
	@echo "integration packages: $(INTEGRATION_PKGS)"
	@echo "integration tests:    $(INTEGRATION_RUN)"
	@echo "release-KDF packages: $(RELEASE_KDF_PKGS)"
	@echo "release-KDF tests:    $(RELEASE_KDF_RUN)"

# Markdown for a CI job summary: the total, then statement coverage per
# package. Every test binary reports the blocks it was built with, so a block
# appears once per binary; it counts once, as covered if any binary reached it.
.PHONY: cover-summary
cover-summary: ## Print COVERPROFILE as a Markdown summary (total and per package).
	@echo "### Go coverage ($(COVERTITLE))"; echo; \
	echo "Total: **$$($(GO) tool cover -func=$(COVERPROFILE) | awk '/^total:/ {print $$NF}')** of statements, \
	module-wide (\`-coverpkg=./...\`). Reported only; no gate yet."; echo; \
	echo "<details><summary>Per package</summary>"; echo; \
	echo "| Package | Coverage |"; echo "|---|---:|"; \
	LC_ALL=C awk 'NR > 1 { \
		pkg = $$1; sub(/:.*/, "", pkg); sub(/\/[^\/]*$$/, "", pkg); \
		sub(/^github\.com\/knoppiks\/opencloud-backup-plugin\/?/, "", pkg); \
		if (pkg == "") pkg = "."; \
		if (!($$1 in stmts)) { stmts[$$1] = $$2; owner[$$1] = pkg } \
		if ($$3 > 0) hit[$$1] = 1 } \
	END { for (b in stmts) { total[owner[b]] += stmts[b]; if (b in hit) covered[owner[b]] += stmts[b] } \
		for (p in total) if (total[p] > 0) printf "| `%s` | %.1f %% |\n", p, 100 * covered[p] / total[p] }' \
		$(COVERPROFILE) | sort; \
	echo; echo "</details>"

.PHONY: mod-check
mod-check: ## Fail when go.mod/go.sum are not tidy or a downloaded module does not verify.
	$(GO) mod tidy -diff
	$(GO) mod verify

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
test-integration: ## Run the integration tests under -race (Garage fixture; needs Docker).
	$(GO) test -race -tags integration,$(UNIT_TEST_TAGS) -timeout $(INTEGRATION_TIMEOUT) \
		-run '$(INTEGRATION_RUN)' $(INTEGRATION_PKGS)

# The same tests without kopia's `testing` tag, so every repository they
# create derives its key with the release default (scrypt), plus the tests
# only such a build compiles. Without -race: under it scrypt dominates.
.PHONY: test-integration-release
test-integration-release: ## Run the integration tests with kopia's release key derivation (no -race; needs Docker).
	$(GO) test -tags integration -timeout $(INTEGRATION_TIMEOUT) \
		-run '$(RELEASE_KDF_RUN)' $(RELEASE_KDF_PKGS)

.PHONY: test-opencloud
test-opencloud: ## Run the OpenCloud-fixture tests (run dev-up first; failures, not skips).
	@test -f $(OPENCLOUD_DIR)/fixture.env || \
		{ echo "no $(OPENCLOUD_DIR)/fixture.env — run 'make dev-up' and $(OPENCLOUD_DIR)/seed.sh first" >&2; exit 1; }
	set -a; . $(OPENCLOUD_DIR)/fixture.env; set +a; \
	OPENCLOUD_FIXTURE_REQUIRED=1 $(GO) test -race -tags integration,$(UNIT_TEST_TAGS) -count=1 \
		./internal/cs3/... ./internal/cs3state/... ./internal/api/... ./internal/ocversion/... ./internal/restore/... ./internal/backup/...

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

# A weakened depguard or forbidigo rule does not fail `make lint`; it just
# stops reporting. This breaks each trust-boundary rule in a copy of the
# sources and fails unless lint reports every one (#93).
.PHONY: lint-probe
lint-probe: ## Prove the trust-boundary lint rules still fire (one violation per rule).
	test/lintprobe/lint-probe.sh

# Every Go program on every platform anything ships for: the images carry
# backupd and takeout (and install-bundle) for linux, the release carries
# decrypt for DECRYPT_PLATFORMS. Checking all of ./cmd/... on all of them
# is a superset, and a dependency only some platform links is still seen.
# The images generate their own notices for what they contain.
LICENSE_PLATFORMS := $(DECRYPT_PLATFORMS)

.PHONY: licenses
licenses: ## Check Go dependency licenses against the allow-list; write $(DIST)/THIRD_PARTY_NOTICES.
	@mkdir -p $(DIST)
	scripts/third-party-notices.sh -o $(DIST)/THIRD_PARTY_NOTICES -p "$(LICENSE_PLATFORMS)" ./cmd/...

# A license check that stops refusing looks like a clean dependency tree.
# This feeds it a stand-in dependency without a license and one under GPL-3.0
# and fails unless both are refused (#99).
.PHONY: license-probe
license-probe: ## Prove the license check still refuses unlicensed and disallowed dependencies.
	test/licenseprobe/license-probe.sh

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
	docker build -t $(WEB_IMAGE):$(IMAGE_TAG) -f $(WEB_DIR)/Dockerfile .

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
