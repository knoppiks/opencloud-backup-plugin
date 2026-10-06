#!/usr/bin/env bash
# Tear down the test OpenCloud fixture.
#   ./down.sh          stop + remove the container (keeps config/data)
#   ./down.sh --purge  also delete generated config/, data/, apps/, fixture.env and
#                      .env (the OpenCloud version up.sh picked)
set -euo pipefail
cd "$(dirname "$0")"

# Compose interpolates the whole file even to stop it, and OC_IMAGE normally
# comes from the .env up.sh writes. A fixture started before that file existed
# has none; the image does not matter for tearing down, so any value will do.
OC_IMAGE="${OC_IMAGE:-unused-by-down}" docker compose down -v || true

if [ "${1:-}" = "--purge" ]; then
  # config/data are created inside the container as root; use a helper container
  # to remove them so we don't need host root.
  docker run --rm -v "$(pwd):/work" alpine:3 \
    sh -c 'rm -rf /work/config /work/data /work/apps /work/fixture.env /work/ca.crt /work/.env' || \
    rm -rf config data apps fixture.env ca.crt .env
  echo "purged config/, data/, apps/, fixture.env, ca.crt, .env"
fi
