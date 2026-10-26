# Container image for the backup service.
#
# Two binaries ship here and one deliberately does not:
#
#   /backupd   the service (HTTP API + worker + scheduler), and — when given an
#              argument — the operator commands rotate-srw / rotate-tw, which
#              need exactly this configuration, state Space and custody keys.
#   /takeout   the admin Take-Out extractor, so a Path A extraction can run as a
#              Job from the image the deployment already has.
#   decrypt    ABSENT ON PURPOSE. It is the user's tool: it runs on the user's
#              own machine with the user's own Recovery Key, and the server is
#              the one place it should never need to be (decisions.md #2, #15).
#              Builds for every desktop OS are GitHub release assets
#              (.goreleaser.yaml; locally `make decrypt-release`).
#
# Base images are pinned by digest (AGENTS.md: pin external versions). Bump them
# deliberately; a moving tag would mean the image an operator deploys is not the
# image that was tested.

# --- build ------------------------------------------------------------------
# Keep the Go version in step with go.mod and CI's GO_VERSION.
# The build runs on the builder's own platform and cross-compiles, so a
# multi-arch release does not run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ARG TARGETOS
ARG TARGETARCH
# The build stamp (`make image` passes `make stamp`'s values; the release
# passes the tag's). Unset, the binaries report "dev".
ARG VERSION=""
ARG COMMIT=""
ARG DATE=""

WORKDIR /src

# Dependencies first: a source-only change then reuses this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off, for the same reason CI asserts it: a single static binary that does
# not care what is in the runtime image (decisions.md, success criterion 1).
ENV CGO_ENABLED=0
ENV BUILDINFO=github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo
RUN LDFLAGS="-s -w -X $BUILDINFO.Version=$VERSION -X $BUILDINFO.Commit=$COMMIT -X $BUILDINFO.Date=$DATE" && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "$LDFLAGS" -o /out/backupd ./cmd/backupd && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "$LDFLAGS" -o /out/takeout ./cmd/takeout

# The third-party notices for exactly these two programs on this platform,
# after the check that refuses a dependency under a license outside the
# allow-list (scripts/third-party-notices.sh, phase-11 §11.1).
RUN scripts/third-party-notices.sh -o /out/THIRD_PARTY_NOTICES -p "$TARGETOS/$TARGETARCH" ./cmd/backupd ./cmd/takeout

# --- runtime ----------------------------------------------------------------
# distroless/static, not scratch: the service makes outbound HTTPS calls (JWKS
# discovery, the OpenCloud graph API, S3 targets), so it needs CA roots. It does
# not need tzdata — the timezone database is compiled into the binary, so
# TZ and SCHEDULE_TIMEZONE work in an image with no /usr/share/zoneinfo.
#
# The :nonroot variant runs as uid 65532, which is what deploy/'s securityContext
# pins. There is no shell and no package manager in here by design.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG VERSION=""
ARG COMMIT=""
ARG DATE=""
# In the Dockerfile rather than only in the release, so a local build is
# labelled too. `created` is the commit's time, like the binaries' date.
LABEL org.opencontainers.image.title="opencloud-backupd" \
      org.opencontainers.image.description="Backup service for OpenCloud Spaces: encrypted, deduplicated, per-Space backups to S3" \
      org.opencontainers.image.source="https://github.com/knoppiks/opencloud-backup-plugin" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT" \
      org.opencontainers.image.created="$DATE"

COPY --from=build /out/backupd /backupd
COPY --from=build /out/takeout /takeout
COPY LICENSE /licenses/LICENSE
COPY --from=build /out/THIRD_PARTY_NOTICES /licenses/THIRD_PARTY_NOTICES

USER 65532:65532
EXPOSE 8080

# NO ARGUMENTS. The service is `/backupd` with an empty argument list; the first
# argument, if there is one, is read as an operator subcommand. That contract is
# what lets a rotation run as
#
#   kubectl run ... --image <this> -- rotate-srw -service-stopped
#
# and it is why deploy/deployment-backupd.yaml sets no `args`. A Take-Out
# overrides the entrypoint instead: `--entrypoint /takeout`.
ENTRYPOINT ["/backupd"]
