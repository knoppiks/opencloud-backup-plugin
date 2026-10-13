#!/usr/bin/env bash
# Proves the trust-boundary lint rules in .golangci.yml still fire (#93).
#
# depguard and forbidigo encode the ciphertext-only and offline-decrypt
# boundaries. A rule that is weakened, or whose file scope is narrowed by
# mistake, does not fail lint; it simply stops reporting. So this copies the
# Go sources into a throwaway directory, adds one file per rule that breaks
# it from a package the rule covers, runs golangci-lint there and fails
# unless every probe is reported by the expected linter.
#
# A new depguard rule or forbidigo pattern needs a probe here.
#
# Usage: lint-probe.sh   (GOLANGCI_LINT overrides the binary)
set -euo pipefail

GOLANGCI_LINT=${GOLANGCI_LINT:-golangci-lint}
MODULE=github.com/knoppiks/opencloud-backup-plugin

root=$(git rev-parse --show-toplevel)
# A fixed path and a cache of its own: golangci-lint caches results by
# package content, and a cached result keeps the path it was produced under.
# Under a fresh directory each run, packages come back pointing at an earlier
# run's (deleted) files, whose nolint directives can then not be read, and a
# probe may "pass" on a stale path. dist/ is git-ignored; the copy has its
# own go.mod, so `./...` in the repository never descends into it.
work="$root/dist/lint-probe/src"
export GOLANGCI_LINT_CACHE="$root/dist/lint-probe/cache"
rm -rf "$work"
mkdir -p "$work" "$GOLANGCI_LINT_CACHE"
trap 'rm -rf "$work"' EXIT

# Tracked and untracked-but-not-ignored Go sources, as in the working tree.
# Files deleted from the working tree but still tracked are skipped.
copy_sources() {
	(cd "$root" && git ls-files -z --cached --others --exclude-standard -- \
		go.mod go.sum .golangci.yml cmd internal pkg) |
		while IFS= read -r -d '' f; do
			[ -e "$root/$f" ] || continue
			mkdir -p "$work/$(dirname "$f")"
			cp "$root/$f" "$work/$f"
		done
}

probes=0
expectations=()
dirs=()

# probe DIR LINTER NEEDLE: writes stdin to DIR/zz_lintprobe_N.go and expects
# LINTER to report a line in that file containing NEEDLE. forbidigo names a
# method call by its receiver variable (`s.Open`), so method probes match on
# the rule's message. Keep the violation on a line of its own: the probe
# functions are unused, and golangci-lint otherwise reports one issue per
# line (hence also --uniq-by-line=false).
probe() {
	local dir=$1 linter=$2 needle=$3
	probes=$((probes + 1))
	local file="$dir/zz_lintprobe_$probes.go"
	cat >"$work/$file"
	expectations+=("$file|$linter|$needle")
	dirs+=("./$dir")
}

copy_sources

# --- depguard ---------------------------------------------------------------

probe internal/api depguard "'decrypt-is-offline-only'" <<EOF
package api

import "$MODULE/pkg/takeout/decrypt"

var _ = decrypt.Decrypt
EOF

probe cmd/backupd depguard "'test-support-in-tests-only'" <<EOF
package main

import "$MODULE/internal/testutil"

var _ = testutil.NewFakeClock
EOF

probe pkg/keys depguard "'decrypt-path-has-no-network'" <<EOF
package keys

import "net"

var _ = net.Dial
EOF

# --- forbidigo: ciphertext-only ---------------------------------------------

probe internal/api forbidigo "keys.UnwrapRK" <<EOF
package api

import "$MODULE/pkg/keys"

func lintProbeUnwrapRK() {
	_, _ = keys.UnwrapRK(keys.WrappedDK{}, nil)
}
EOF

probe internal/takeout/remote forbidigo "Data Keys are unwrapped by the backup worker only" <<EOF
package remote

import "$MODULE/pkg/keys"

func lintProbeUnwrapSRW(s *keys.SRWWrapper) {
	_, _ = s.UnwrapSRW(keys.WrappedDK{})
}
EOF

probe cmd/takeout forbidigo "target credentials are opened in worker memory only" <<EOF
package main

import "$MODULE/pkg/keys"

func lintProbeTWOpen(s *keys.TWSealer) {
	_, _ = s.Open(nil)
}
EOF

probe internal/api forbidigo "target credentials are opened in worker memory only" <<EOF
package api

import "$MODULE/internal/targets"

func lintProbeCredOpen(s targets.CredSealer) {
	_, _ = s.Open(nil)
}
EOF

# --- forbidigo: library code ------------------------------------------------

probe internal/backup forbidigo "fmt.Println" <<EOF
package backup

import "fmt"

func lintProbePrint() {
	fmt.Println()
}
EOF

probe internal/backup forbidigo "os.Exit" <<EOF
package backup

import "os"

func lintProbeExit() {
	os.Exit(1)
}
EOF

probe internal/backup forbidigo "log.Printf" <<EOF
package backup

import "log"

func lintProbeLog() {
	log.Printf("")
}
EOF

# --- run --------------------------------------------------------------------

mapfile -t pkgs < <(printf '%s\n' "${dirs[@]}" | sort -u)
out="$work/lint.out"
status=0
(cd "$work" && "$GOLANGCI_LINT" run --max-issues-per-linter=0 --max-same-issues=0 \
	--uniq-by-line=false "${pkgs[@]}") >"$out" 2>&1 || status=$?

missing=0
for e in "${expectations[@]}"; do
	IFS='|' read -r file linter needle <<<"$e"
	if grep -E "^$file:[0-9]+:" "$out" | grep -F "($linter)" | grep -qF -- "$needle"; then
		echo "ok      $file ($linter: $needle)"
	else
		echo "MISSING $file ($linter: $needle)" >&2
		missing=$((missing + 1))
	fi
done

if [ "$status" -eq 0 ] || [ "$missing" -gt 0 ]; then
	echo >&2
	echo "lint-probe: $missing of $probes probes not reported (golangci-lint exit $status)." >&2
	echo "A trust-boundary rule in .golangci.yml stopped firing, or a probe no longer compiles." >&2
	echo "golangci-lint output:" >&2
	cat "$out" >&2
	exit 1
fi
echo "lint-probe: all $probes probes reported"
