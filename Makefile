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

# The build stamp (internal/buildinfo, #102): every artifact of a build
# reports the same version and commit. VERSION is `git describe` against the
# release tags (v1.2.3 on a tag, v1.2.3-4-gabcdef012345 after it, -dirty for
# uncommitted changes); DATE is the commit's time, not the build's, so the
# same commit builds the same binary. Outside a git checkout all three are
# empty and the binaries fall back to what the toolchain recorded ("dev").
# The release overrides nothing: on the tag, describe is the tag.
VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --abbrev=12 --always --dirty 2>/dev/null)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null)
BUILDINFO := github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo
STAMP_LDFLAGS := -X $(BUILDINFO).Version=$(VERSION) -X $(BUILDINFO).Commit=$(COMMIT) -X $(BUILDINFO).Date=$(DATE)
# Stripped (no symbol table, no DWARF) as the images and the release build
# their binaries; `make build DEBUG=1` keeps both for a debugger.
BUILD_LDFLAGS := $(if $(DEBUG),,-s -w )$(STAMP_LDFLAGS)
# The same stamp for an image build.
DOCKER_STAMP := --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE)

# Platforms the offline recovery CLI must build for. It is the family's last
# resort, so it ships for every desktop OS (phase-5 exit criteria).
DECRYPT_PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show this help.
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build all binaries into $(DIST), stripped (DEBUG=1 keeps symbols).
	@mkdir -p $(DIST)
	@for b in $(BINARIES); do \
		echo "building $$b"; \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(BUILD_LDFLAGS)" -o $(DIST)/$$b ./cmd/$$b || exit 1; \
	done

# KEY=value lines, for CI ($$GITHUB_OUTPUT) and anyone asking what a build
# from this tree would report.
.PHONY: stamp
stamp: ## Print the build stamp (VERSION, COMMIT, DATE) this tree builds with.
	@echo "version=$(VERSION)"
	@echo "commit=$(COMMIT)"
	@echo "date=$(DATE)"

# The release's CLI binaries are defined once, in .goreleaser.yaml, and built
# from there everywhere: here, in CI on every pull request, and by the
# release (phase-11 §11.3, #104). GoReleaser and syft (for the SBOMs) must be
# on PATH, at the versions CI pins.
GORELEASER     ?= goreleaser
RELEASE_DIST   := $(DIST)/release
# What earlier release jobs add to the release (web bundle archive, image
# SBOMs, image digests); .goreleaser.yaml checksums and attaches all of it.
RELEASE_ASSETS := $(DIST)/release-assets
GORELEASER_ENV := VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE)

.PHONY: decrypt-release
decrypt-release: ## Cross-build the offline decrypt CLI for every supported OS (into dist/release).
	$(GORELEASER_ENV) $(GORELEASER) build --snapshot --clean --id decrypt

# Everything the release builds, signed by nobody and published nowhere,
# then checked the way the release is checked. Builds the web bundle archive
# first (web-archive needs `pnpm install` done) into an emptied
# $(RELEASE_ASSETS), so nothing left from an earlier run is attached.
.PHONY: release-snapshot
release-snapshot: ## Build every release asset locally (no signing, no upload) and check them.
	rm -rf $(RELEASE_ASSETS)
	$(MAKE) --no-print-directory web-archive
	$(GORELEASER_ENV) $(GORELEASER) release --snapshot --clean --skip=sign
	DECRYPT_PLATFORMS="$(DECRYPT_PLATFORMS)" scripts/check-release-assets.sh $(RELEASE_DIST) $(RELEASE_ASSETS) "$(VERSION)" "$(COMMIT)"
	test/release/release-scripts-test.sh $(RELEASE_DIST) $(RELEASE_ASSETS) "$(VERSION)" "$(COMMIT)"
	test/release/release-issues-test.sh

# The release's shell scripts (tags, notes, verification), and that the
# asset check still refuses damaged assets; the latter part runs inside
# release-snapshot, which has a snapshot to damage.
.PHONY: release-scripts-test
release-scripts-test: ## Test the release's shell scripts.
	test/release/release-scripts-test.sh
	test/release/release-issues-test.sh

# The issues TAG ships, and the release notes' "What changed" for it, from
# GitHub (needs gh signed in). Shows what a release would say before tagging.
.PHONY: release-issues
release-issues: ## Print the issues TAG ships (a tag, or HEAD to preview; queries GitHub).
	@[ -n "$(TAG)" ] || { echo "release-issues: set TAG=vX.Y.Z (or HEAD)" >&2; exit 1; }
	scripts/release-issues.sh $(TAG)

# The release itself (release.yml): VERSION must be the tag on HEAD, the
# tree clean, RELEASE_NOTES rendered, GITHUB_TOKEN and cosign's OIDC
# identity available. Creates or replaces the draft release.
.PHONY: release
release:
	@[ -n "$(RELEASE_NOTES)" ] || { echo "release: RELEASE_NOTES is not set" >&2; exit 1; }
	$(GORELEASER_ENV) $(GORELEASER) release --clean --release-notes "$(RELEASE_NOTES)"
	DECRYPT_PLATFORMS="$(DECRYPT_PLATFORMS)" scripts/check-release-assets.sh $(RELEASE_DIST) $(RELEASE_ASSETS) "$(VERSION)" "$(COMMIT)"
	scripts/check-release-notes.sh "$(VERSION)" "$(RELEASE_NOTES)"

# The web bundle as a release asset, for an install without the
# initContainer image: backup-vault/ holding the bundle, its notices and the
# project's LICENSE. Owner, order and times are fixed (the commit's), so the
# same commit gives the same archive.
WEB_ARCHIVE := $(RELEASE_ASSETS)/backup-vault_$(patsubst v%,%,$(VERSION)).tar.gz

.PHONY: web-archive
web-archive: web-build ## Build the web bundle and archive it as a release asset.
	@rm -rf $(DIST)/web-archive
	@mkdir -p $(DIST)/web-archive/backup-vault $(RELEASE_ASSETS)
	cp -R $(WEB_DIR)/dist/. $(DIST)/web-archive/backup-vault/
	cp LICENSE $(DIST)/web-archive/backup-vault/LICENSE
	tar -C $(DIST)/web-archive --sort=name --mtime="$(DATE)" --owner=0 --group=0 --numeric-owner \
		-cf - backup-vault | gzip -n >$(WEB_ARCHIVE)
	@rm -rf $(DIST)/web-archive

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
	cd $(WEB_DIR) && BACKUP_VAULT_VERSION=$(VERSION) $(PNPM) build

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

# The Go tools CI uses, at the versions CI uses (phase-11 §11.7, #125). Each
# is a `tool` directive in a module of its own under tools/<name>/, so its
# go.sum pins every byte of it and its dependencies stay out of this
# module's graph (and out of the license and vulnerability audits). Renovate
# bumps them like any Go module. Built into $(TOOLS) with the toolchain the
# project's go.mod names (the tools' modules name none, so there is one pin
# to move), and rebuilt when it or the tool's module files change.
# golangci-lint refuses to lint a module that targets a newer Go than it was
# built with.
TOOLS := $(abspath $(DIST)/tools)
GO_TOOLS := $(notdir $(wildcard tools/*))
GO_TOOLCHAIN := $(shell sed -n 's/^toolchain //p' go.mod)

$(addprefix $(TOOLS)/,$(GO_TOOLS)): $(TOOLS)/%: tools/%/go.mod tools/%/go.sum go.mod
	GOBIN=$(TOOLS) GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) -C tools/$* install tool
	@touch $@

.PHONY: tools
tools: $(addprefix $(TOOLS)/,$(GO_TOOLS)) ## Build the pinned Go tools into $(DIST)/tools.

.PHONY: lint
lint: $(TOOLS)/golangci-lint ## Run golangci-lint.
	$(TOOLS)/golangci-lint run ./...

# A weakened depguard or forbidigo rule does not fail `make lint`; it just
# stops reporting. This breaks each trust-boundary rule in a copy of the
# sources and fails unless lint reports every one (#93).
.PHONY: lint-probe
lint-probe: $(TOOLS)/golangci-lint ## Prove the trust-boundary lint rules still fire (one violation per rule).
	GOLANGCI_LINT=$(TOOLS)/golangci-lint test/lintprobe/lint-probe.sh

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

# Known vulnerabilities (phase-11 §11.6, #116). The scanners are pinned like
# every other tool: govulncheck in tools/, the others as images by digest;
# Renovate bumps them (the `# renovate:` lines).
# renovate: datasource=docker
OSV_SCANNER_IMAGE ?= ghcr.io/google/osv-scanner:v2.7.0@sha256:4f3b7e2c64b9d38a5febc5a7e4a3dfef1d1362f726dea7a2ef4762b46469fcd0
# renovate: datasource=docker
TRIVY_IMAGE ?= aquasec/trivy:0.76.0@sha256:2416d1f74045b57274df1a340eee6b510cacd73c66a6ccb8da092b117eb7d222

# The shipped programs, on every platform anything ships for, in source mode:
# a vulnerability fails only when the code can reach it. The standard
# library's count too, so this is also what moves the Go toolchain pin.
# Built for the host and run per platform.
.PHONY: vulncheck
vulncheck: $(TOOLS)/govulncheck ## Fail on a known vulnerability the Go programs can reach (govulncheck, every shipped platform).
	@for p in $(LICENSE_PLATFORMS); do \
		echo "govulncheck ./cmd/... ($$p)"; \
		GOOS=$${p%/*} GOARCH=$${p#*/} $(TOOLS)/govulncheck ./cmd/... || exit 1; \
	done

# The whole lockfile is scanned; what blocks is decided against the packages
# the bundle holds (web/build/vulnerability-audit.ts). osv-scanner exits 1
# when it found anything, which is not yet a verdict.
.PHONY: web-audit
web-audit: web-build ## Fail on a high/critical known vulnerability in a package the web bundle ships (osv-scanner).
	@mkdir -p $(DIST)
	docker run --rm -v "$(CURDIR)/$(WEB_DIR):/src:ro" $(OSV_SCANNER_IMAGE) \
		scan source --format json -L /src/pnpm-lock.yaml >$(DIST)/osv-web.json || [ $$? -eq 1 ]
	cd $(WEB_DIR) && $(PNPM) exec jiti build/vulnerability-audit.ts $(abspath $(DIST))/osv-web.json

# Both local builds by default; the release scans its pushed images by digest.
SCAN_IMAGES ?= $(IMAGE):$(IMAGE_TAG) $(WEB_IMAGE):$(IMAGE_TAG)

.PHONY: image-scan
image-scan: ## Fail on a fixable critical vulnerability in either image (Trivy; after image, web-image).
	TRIVY_IMAGE=$(TRIVY_IMAGE) scripts/scan-image.sh $(SCAN_IMAGES)

# An image scan that stops refusing looks like a clean image. This scans a
# stand-in with a known fixable critical vulnerability and fails unless it
# is refused, and its patched twin unless it passes.
.PHONY: image-scan-probe
image-scan-probe: ## Prove the image scan still refuses a fixable critical vulnerability.
	TRIVY_IMAGE=$(TRIVY_IMAGE) test/scanprobe/scan-probe.sh

# A renovate.json Renovate cannot read stops every update; the hosted app
# then opens an issue, and nothing else notices.
# renovate: datasource=npm depName=renovate
RENOVATE_VERSION ?= 44.171.7

.PHONY: renovate-config
renovate-config: ## Validate renovate.json with the pinned Renovate.
	npx --yes --package renovate@$(RENOVATE_VERSION) -- renovate-config-validator --strict

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format code.
	$(GO) fmt ./...

.PHONY: image
image: ## Build the service container image ($(IMAGE):$(IMAGE_TAG)).
	docker build $(DOCKER_STAMP) -t $(IMAGE):$(IMAGE_TAG) .

.PHONY: web-image
web-image: ## Build the web bundle image for an initContainer ($(WEB_IMAGE):$(IMAGE_TAG)).
	docker build $(DOCKER_STAMP) -t $(WEB_IMAGE):$(IMAGE_TAG) -f $(WEB_DIR)/Dockerfile .

.PHONY: image-stamp-check
image-stamp-check: ## Check both images report this tree's version everywhere (after image, web-image).
	scripts/check-image-stamp.sh $(IMAGE):$(IMAGE_TAG) $(WEB_IMAGE):$(IMAGE_TAG) "$(VERSION)" "$(COMMIT)"

.PHONY: k8s-validate
k8s-validate: $(TOOLS)/kubeconform ## Validate K8s manifests offline (kubeconform).
	$(TOOLS)/kubeconform -strict -summary deploy/*.yaml

# The compose add-on's first-install helper (#157), against a fake docker.
.PHONY: compose-test
compose-test: ## Test the compose add-on's setup.sh (no Docker needed).
	test/compose/setup-test.sh

# deploy/ names only published versions of this project's images (#110);
# needs the registry (docker buildx). The check's own test needs nothing.
.PHONY: deploy-images
deploy-images: ## Check deploy/ names only published image tags (queries ghcr.io).
	test/release/deploy-images-test.sh
	scripts/check-deploy-images.sh deploy/*.yaml

# HEAD's whole history (what CI fails on), then the files of the working
# tree git would commit (AGENTS.md, "Secret scanning").
.PHONY: secret-scan
secret-scan: $(TOOLS)/gitleaks ## Scan HEAD's history and the working tree for secrets (gitleaks).
	scripts/secret-scan.sh $(TOOLS)/gitleaks

# The pipeline's own files (#125): workflows (actionlint, with shellcheck on
# their `run:` scripts, and zizmor's security audits), shell scripts
# (shellcheck), Dockerfiles (hadolint). Non-Go linters run as images pinned
# by digest. zizmor's online audits (impostor commits, known-vulnerable
# actions) need GH_TOKEN; without it they are skipped, as locally they may be.
# renovate: datasource=docker
ZIZMOR_IMAGE ?= ghcr.io/zizmorcore/zizmor:1.31.1@sha256:42d409dbb851d4c5d35a058cf042b828bea1e3f2884378c526eb0974780e18d9
# renovate: datasource=docker
SHELLCHECK_IMAGE ?= koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d
# renovate: datasource=docker
HADOLINT_IMAGE ?= hadolint/hadolint:v2.15.1@sha256:32dac94127fd60b7b7e3fbfc65e1383b9b5e25c9bfd7b8536de7a539fe68a12d
SHELL_SCRIPTS = git ls-files -z --cached --others --exclude-standard -- '*.sh'
DOCKERFILES := Dockerfile $(WEB_DIR)/Dockerfile

.PHONY: pipeline-lint
pipeline-lint: $(TOOLS)/actionlint ## Lint workflows, shell scripts and Dockerfiles (actionlint, zizmor, shellcheck, hadolint).
	SHELLCHECK_IMAGE=$(SHELLCHECK_IMAGE) $(TOOLS)/actionlint -shellcheck=scripts/shellcheck.sh
	docker run --rm -v "$(CURDIR):/repo:ro" -w /repo -e GH_TOKEN $(ZIZMOR_IMAGE) \
		$(if $(GH_TOKEN),,--no-online-audits) .github/workflows
	$(SHELL_SCRIPTS) | SHELLCHECK_IMAGE=$(SHELLCHECK_IMAGE) xargs -0 scripts/shellcheck.sh
	docker run --rm -v "$(CURDIR):/repo:ro" -w /repo $(HADOLINT_IMAGE) hadolint $(DOCKERFILES)

# Every file git would commit against .editorconfig (#137): charset, line
# endings, final newline, trailing whitespace. Indentation is left to gofmt,
# prettier and review; raw strings and heredocs carry their own.
.PHONY: editorconfig
editorconfig: $(TOOLS)/editorconfig-checker ## Check every tracked file against .editorconfig.
	git ls-files -z --cached --others --exclude-standard \
		| xargs -0 $(TOOLS)/editorconfig-checker -no-color -disable-indentation

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
