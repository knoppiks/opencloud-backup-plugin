#!/usr/bin/env bash
# Tear down the test OpenCloud fixture.
#   ./down.sh          stop + remove the container (keeps config/data)
#   ./down.sh --purge  also delete generated config/, data/, apps/, fixture.env
set -euo pipefail
cd "$(dirname "$0")"

docker compose down -v || true

if [ "${1:-}" = "--purge" ]; then
  # config/data are created inside the container as root; use a helper container
  # to remove them so we don't need host root.
  docker run --rm -v "$(pwd):/work" alpine:3 sh -c 'rm -rf /work/config /work/data /work/apps /work/fixture.env' || \
    rm -rf config data apps fixture.env
  echo "purged config/, data/, apps/, fixture.env"
fi
