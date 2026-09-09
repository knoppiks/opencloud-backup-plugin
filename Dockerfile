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
#              Builds for every desktop OS come from `make decrypt-release`.
#
# Base images are pinned by digest (AGENTS.md: pin external versions). Bump them
# deliberately; a moving tag would mean the image an operator deploys is not the
# image that was tested.

# --- build ------------------------------------------------------------------
# Keep the Go version in step with go.mod and CI's GO_VERSION.
FROM golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS build

WORKDIR /src

# Dependencies first: a source-only change then reuses this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off, for the same reason CI asserts it: a single static binary that does
# not care what is in the runtime image (decisions.md, success criterion 1).
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w" -o /out/backupd ./cmd/backupd && \
    go build -trimpath -ldflags "-s -w" -o /out/takeout ./cmd/takeout

# --- runtime ----------------------------------------------------------------
# distroless/static, not scratch: the service makes outbound HTTPS calls (JWKS
# discovery, the OpenCloud graph API, S3 targets), so it needs CA roots. It does
# not need tzdata — the timezone database is compiled into the binary, so
# TZ and SCHEDULE_TIMEZONE work in an image with no /usr/share/zoneinfo.
#
# The :nonroot variant runs as uid 65532, which is what deploy/'s securityContext
# pins. There is no shell and no package manager in here by design.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/backupd /backupd
COPY --from=build /out/takeout /takeout

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
