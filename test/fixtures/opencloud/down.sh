#!/usr/bin/env bash
# Tear down the OpenCloud fixture.
#   ./down.sh          stop and remove its containers; OpenCloud's data, the
#                      stack and its .env stay
#   ./down.sh --purge  also delete OpenCloud's volumes, the stack, fixture.env
#                      and ca.crt
set -euo pipefail
# shellcheck source=/dev/null # lib.sh: CI runs shellcheck without -x
. "$(dirname "$0")/lib.sh"
cd "${FIXTURE_DIR}"

purge=
[ "${1:-}" != "--purge" ] || purge=-v

if [ -f "${STACK_DIR}/.env" ]; then
  fixture_compose down ${purge:+"${purge}"} || true
fi

if [ -n "${purge}" ]; then
  # The apps directory was handed to OpenCloud's uid, which need not be the
  # host user's: a root helper removes what the host user may not.
  # config/, data/, apps/ and .env are the bespoke fixture's, from before
  # #158.
  docker run --rm -v "${FIXTURE_DIR}:/work" alpine:3 \
    sh -c 'rm -rf /work/stack /work/fixture.env /work/ca.crt /work/config /work/data /work/apps /work/.env'
  echo "purged the stack, OpenCloud's volumes, fixture.env and ca.crt"
fi
