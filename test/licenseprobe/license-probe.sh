#!/usr/bin/env bash
# Proves the Go license check in scripts/third-party-notices.sh still refuses
# what it must and keeps what the notices need (#99).
#
# A check that silently stops refusing looks exactly like a dependency tree
# without a problem. So this copies the Go sources into a throwaway module,
# makes cmd/decrypt import a stand-in dependency, and runs the script on it:
#
#   - a dependency without a license file must be refused;
#   - a dependency under a license outside the allow-list (GPL-3.0) must be
#     refused;
#   - an Apache-2.0 dependency's NOTICE must appear in the notices, and an
#     MPL-2.0 dependency's entry must say where its source is published,
#     beside the Go standard library's license.
#
# The license texts come from the classifier go-licenses itself matches
# against, so they are recognised as what they are.
#
# Usage: license-probe.sh
set -euo pipefail

root=$(git rev-parse --show-toplevel)
notices="$root/scripts/third-party-notices.sh"
# dist/ is git-ignored; the copy has its own go.mod, so `./...` in the
# repository never descends into it.
base="$root/dist/license-probe"
work="$base/src"
rm -rf "$base"
mkdir -p "$work"
trap 'rm -rf "$base"' EXIT

(cd "$root" && git ls-files -z --cached --others --exclude-standard -- go.mod go.sum cmd internal pkg) |
	while IFS= read -r -d '' f; do
		[ -e "$root/$f" ] || continue
		mkdir -p "$work/$(dirname "$f")"
		cp "$root/$f" "$work/$f"
	done

# Outside any module, so the download touches neither go.mod nor go.sum.
classifier=$(cd / && go mod download -json github.com/google/licenseclassifier/v2@v2.0.0 |
	sed -nE 's/^\s*"Dir": "(.*)",?$/\1/p')
license_text() { cat "$classifier/assets/License/$1/license.txt"; }

# stand_in NAME [LICENSE_FILE_CONTENT_CMD...]: a local module
# example.com/licenseprobe/NAME, required by the copy and imported by
# cmd/decrypt. Without a command it has no license file.
stand_in() {
	local name=$1
	shift
	local dir="$base/mod/$name"
	mkdir -p "$dir"
	printf 'module example.com/licenseprobe/%s\n\ngo 1.26\n' "$name" >"$dir/go.mod"
	printf 'package %s\n\nconst Name = "%s"\n' "$name" "$name" >"$dir/$name.go"
	if [ $# -gt 0 ]; then "$@" >"$dir/LICENSE"; fi
	(cd "$work" && go mod edit \
		-require "example.com/licenseprobe/$name@v0.0.0" \
		-replace "example.com/licenseprobe/$name=$dir")
	printf 'package main\n\nimport _ "example.com/licenseprobe/%s"\n' "$name" \
		>"$work/cmd/decrypt/zz_licenseprobe_$name.go"
}

# clear: drop every stand-in from the copy again.
clear_stand_ins() {
	rm -f "$work"/cmd/decrypt/zz_licenseprobe_*.go
	cp "$root/go.mod" "$work/go.mod"
}

failures=0
fail() {
	echo "FAILED  $1" >&2
	failures=$((failures + 1))
}

# expect_refused NAME: the script must fail, naming the stand-in.
expect_refused() {
	local name=$1 log="$base/$1.log"
	if (cd "$work" && "$notices" -o "$base/$name.notices" ./cmd/decrypt) >"$log" 2>&1; then
		fail "$name: accepted"
	elif ! grep -q "example.com/licenseprobe/$name" "$log"; then
		fail "$name: refused, but not for the stand-in"
		cat "$log" >&2
	else
		echo "ok      $name: refused"
	fi
}

stand_in unlicensed
expect_refused unlicensed
clear_stand_ins

stand_in gpl license_text GPL-3.0
expect_refused gpl
clear_stand_ins

apache_with_notice() {
	local dir="$base/mod/apache"
	cat "$root/LICENSE"
	printf 'licenseprobe NOTICE marker\n' >"$dir/NOTICE"
}
stand_in apache apache_with_notice
stand_in mpl license_text MPL-2.0
out="$base/accepted.notices"
if ! (cd "$work" && "$notices" -o "$out" ./cmd/decrypt) >"$base/accepted.log" 2>&1; then
	fail "apache + mpl: refused"
	cat "$base/accepted.log" >&2
else
	grep -q '^Go standard library and runtime' "$out" || fail "notices: no Go standard library entry"
	grep -q '^licenseprobe NOTICE marker$' "$out" || fail "notices: the Apache-2.0 NOTICE is missing"
	grep -A3 '^example.com/licenseprobe/mpl$' "$out" | grep -q 'source code is available' ||
		fail "notices: the MPL-2.0 entry does not say where its source is"
	grep -q 'Mozilla Public License' "$out" || fail "notices: the MPL-2.0 license text is missing"
	[ "$failures" -gt 0 ] || echo "ok      apache + mpl: accepted, NOTICE and source reproduced"
fi

if [ "$failures" -gt 0 ]; then
	echo "license-probe: $failures expectation(s) failed; the license check in $notices stopped doing its job." >&2
	exit 1
fi
echo "license-probe: all expectations met"
