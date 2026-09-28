# Container image carrying the Backup Vault web bundle, for an initContainer.
#
# This image serves nothing. It exists to put the built extension into
# OpenCloud's apps directory, the way OpenCloud's own `web-extensions` images
# do: the bundle sits at the same path those images use, so the copy step an
# operator already has for them works unchanged:
#
#   mkdir -p /extensions/backup-vault && \
#     cp -R /usr/share/nginx/html/backup-vault/ /extensions/
#
# and from there into `<OC_DATA_DIR>/web/assets/apps/backup-vault/`. OpenCloud
# scans that directory at startup only, so the pod must restart after the copy,
# which an initContainer gets for free.
#
# Build context is `web/` (see web/.dockerignore). Base images are pinned by
# digest (AGENTS.md: pin external versions).

# --- build ------------------------------------------------------------------
# Keep the Node version in step with CI's NODE_VERSION. The bundle is the same
# on every platform, so it is built once, on the builder's own.
FROM --platform=$BUILDPLATFORM node:24.17.0-bookworm-slim@sha256:862263c612aa437e3037674b85419622a9d93bff80aa1eee5398dfe686375532 AS build

WORKDIR /src

# pnpm at the version pinned in package.json's packageManager, via Corepack.
RUN corepack enable

# Dependencies first: a source-only change then reuses this layer.
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY . .
RUN pnpm build

# --- runtime ----------------------------------------------------------------
# busybox, not scratch: the initContainer needs `sh` and `cp`. Nothing runs in
# this image beyond that copy.
FROM busybox:1.38@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e

COPY --from=build /src/dist/ /usr/share/nginx/html/backup-vault/

USER 65532:65532
CMD ["sh", "-c", "mkdir -p /extensions/backup-vault && cp -R /usr/share/nginx/html/backup-vault/ /extensions/"]
