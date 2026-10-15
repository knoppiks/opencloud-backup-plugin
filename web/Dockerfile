# Container image carrying the Backup Vault web bundle, for an initContainer.
#
# This image serves nothing. It exists to put the built extension into
# OpenCloud's apps directory, the way OpenCloud's own `web-extensions` images
# do: the bundle sits at the same path those images use,
# /usr/share/nginx/html/backup-vault/, and the image's entrypoint copies it to
# /extensions/backup-vault, as the documented initContainer expects. From
# there it goes into `<OC_DATA_DIR>/web/assets/apps/backup-vault/`. OpenCloud
# scans that directory at startup only, so the pod must restart after the
# copy, which an initContainer gets for free.
#
# There is no shell in the image (phase-11 §11.1, #99): the copy is
# cmd/install-bundle, a static Go program, on distroless. An initContainer
# must therefore not override the command with the `sh -c "mkdir … && cp …"`
# one-liner OpenCloud's own images are run with; the default does that job.
#
# Build context is the repository root, for the Go program:
#
#   docker build -f web/Dockerfile .
#
# (see web/Dockerfile.dockerignore). Base images are pinned by digest
# (AGENTS.md: pin external versions).

# --- bundle -----------------------------------------------------------------
# Keep the Node version in step with CI's NODE_VERSION. The bundle is the same
# on every platform, so it is built once, on the builder's own. The build
# checks the bundle's third-party licenses and writes THIRD_PARTY_NOTICES
# into it (web/build/third-party-licenses.ts).
FROM --platform=$BUILDPLATFORM node:24.17.0-bookworm-slim@sha256:862263c612aa437e3037674b85419622a9d93bff80aa1eee5398dfe686375532 AS bundle
# The build stamp, as in the service image. The bundle reports VERSION as
# its own (vite.config.ts) and compares it with the service's.
ARG VERSION=""

WORKDIR /src/web

# pnpm at the version pinned in package.json's packageManager, via Corepack.
RUN corepack enable

# Dependencies first: a source-only change then reuses this layer.
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY web/ ./
RUN BACKUP_VAULT_VERSION="$VERSION" pnpm build

# --- install-bundle ---------------------------------------------------------
# Keep the Go version in step with go.mod and CI's GO_VERSION
# (internal/buildinfo/toolchain_test.go). Cross-compiled on the builder's
# platform, like the service image.
FROM --platform=$BUILDPLATFORM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS installer
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=""
ARG COMMIT=""
ARG DATE=""

WORKDIR /src

COPY go.mod go.sum ./
COPY cmd/ cmd/
COPY internal/ internal/
COPY scripts/ scripts/

ENV CGO_ENABLED=0
ENV BUILDINFO=github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X $BUILDINFO.Version=$VERSION -X $BUILDINFO.Commit=$COMMIT -X $BUILDINFO.Date=$DATE" \
    -o /out/install-bundle ./cmd/install-bundle
# The notices for the program itself: the Go standard library.
RUN scripts/third-party-notices.sh -o /out/THIRD_PARTY_NOTICES -p "$TARGETOS/$TARGETARCH" ./cmd/install-bundle

# --- runtime ----------------------------------------------------------------
# distroless/static: no shell, no package manager, no GPL-licensed busybox.
# The :nonroot variant runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG VERSION=""
ARG COMMIT=""
ARG DATE=""
LABEL org.opencontainers.image.title="opencloud-backup-vault-web" \
      org.opencontainers.image.description="Backup Vault web extension for OpenCloud, for an initContainer that installs it" \
      org.opencontainers.image.source="https://github.com/knoppiks/opencloud-backup-plugin" \
      org.opencontainers.image.licenses="Apache-2.0 AND AGPL-3.0" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT" \
      org.opencontainers.image.created="$DATE"

COPY --from=bundle /src/web/dist/ /usr/share/nginx/html/backup-vault/
COPY --from=installer /out/install-bundle /install-bundle

# Licenses: the project's, the program's third-party notices, and the
# bundle's (which also travel inside the bundle).
COPY LICENSE /licenses/LICENSE
COPY --from=installer /out/THIRD_PARTY_NOTICES /licenses/THIRD_PARTY_NOTICES
COPY --from=bundle /src/web/dist/THIRD_PARTY_NOTICES /licenses/backup-vault/THIRD_PARTY_NOTICES

USER 65532:65532
ENTRYPOINT ["/install-bundle"]
