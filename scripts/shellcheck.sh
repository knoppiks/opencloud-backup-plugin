#!/usr/bin/env bash
# Runs shellcheck at the version the Makefile pins, as an image (#125). Takes
# the arguments shellcheck takes; reads a script from stdin when given `-`, which
# is how actionlint hands it a workflow's `run:` block
# (`actionlint -shellcheck=scripts/shellcheck.sh`). Paths are relative to the
# current directory, which is mounted read-only.
#
# Usage: SHELLCHECK_IMAGE=… shellcheck.sh [SHELLCHECK ARGS…]
set -euo pipefail
: "${SHELLCHECK_IMAGE:?SHELLCHECK_IMAGE is not set (the Makefile sets it)}"

exec docker run --rm -i --network none -v "$PWD:/mnt:ro" -w /mnt "$SHELLCHECK_IMAGE" "$@"
