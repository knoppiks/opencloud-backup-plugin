#!/usr/bin/env bash
# docker compose on the OpenCloud fixture's stack, from anywhere:
#
#   test/fixtures/opencloud/compose.sh ps
#   test/fixtures/opencloud/compose.sh logs --tail 200 opencloud
set -euo pipefail
# shellcheck source=/dev/null # lib.sh: CI runs shellcheck without -x
. "$(dirname "$0")/lib.sh"

if [ ! -f "${STACK_DIR}/.env" ]; then
  echo "no fixture stack in ${STACK_DIR}: run up.sh first" >&2
  exit 1
fi
fixture_compose "$@"
