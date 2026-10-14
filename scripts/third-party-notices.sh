#!/usr/bin/env bash
# Writes the third-party notices for Go programs, and refuses a dependency
# whose license is not on the allow-list (phase-11 §11.1, #99).
#
# For every platform given, the packages' dependency graph is resolved as a
# build for that platform would resolve it (decrypt on Windows links modules
# that Linux does not), the licenses are checked against ALLOWED_LICENSES, and
# each dependency's license file and NOTICE file are collected. The output is
# their union, preceded by the Go standard library's license, which every Go
# binary embeds and go-licenses does not report.
#
# The file is generated where it ships (the image builds, the release), never
# committed, so it cannot go stale.
#
# Usage: third-party-notices.sh -o FILE [-p "os/arch ..."] PACKAGE...
#   -o FILE      where to write the notices
#   -p PLATFORMS space-separated GOOS/GOARCH pairs (default: this machine's)
# GO_LICENSES names a go-licenses binary to use instead of building the
# pinned one.
set -euo pipefail
# A program without third-party dependencies (install-bundle) saves nothing.
shopt -s nullglob

GO_LICENSES_VERSION=v2.0.1
MODULE=github.com/knoppiks/opencloud-backup-plugin

# Permissive licenses, plus MPL-2.0 (file-level copyleft), admitted by the
# owner for one dependency: github.com/mxk/go-vss, which kopia links into the
# Windows decrypt. Its entry names where its source is published. MPL-2.0
# leaves this list again once decrypt stops linking kopia's upload path
# (decisions.md, phase 11.1 amendments).
ALLOWED_LICENSES=Apache-2.0,BSD-2-Clause,BSD-3-Clause,MIT,ISC,CC0-1.0,MPL-2.0

usage() {
	echo "usage: $0 -o FILE [-p \"os/arch ...\"] PACKAGE..." >&2
	exit 2
}

out=""
platforms="$(go env GOOS)/$(go env GOARCH)"
while getopts "o:p:" opt; do
	case $opt in
	o) out=$OPTARG ;;
	p) platforms=$OPTARG ;;
	*) usage ;;
	esac
done
shift $((OPTIND - 1))
[ -n "$out" ] && [ $# -gt 0 ] || usage

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# go-licenses resolves the standard library through GOROOT. Under a toolchain
# that `go` switched to (go.mod's toolchain line), its compiled-in GOROOT is
# not the one in use, and every standard package is reported as a module
# without module information.
GOROOT=$(go env GOROOT)
export GOROOT

if [ -z "${GO_LICENSES:-}" ]; then
	# Built for this machine, whatever GOOS/GOARCH the caller has set.
	GOOS=$(go env GOHOSTOS) GOARCH=$(go env GOHOSTARCH) GOBIN="$work/bin" \
		go install "github.com/google/go-licenses/v2@$GO_LICENSES_VERSION"
	GO_LICENSES="$work/bin/go-licenses"
fi

# run LOG ARGS...: go-licenses is chatty on stderr (assembly files it cannot
# inspect); its output is shown only when it fails.
run() {
	local log=$1
	shift
	if ! "$GO_LICENSES" "$@" >"$log" 2>&1; then
		cat "$log" >&2
		return 1
	fi
}

for platform in $platforms; do
	os=${platform%/*}
	arch=${platform#*/}
	echo "third-party-notices: $os/$arch: $*" >&2
	# Our own packages are ignored by name; their dependencies are still
	# checked. The project's license ships beside this file, not inside it.
	GOOS=$os GOARCH=$arch run "$work/check-$os-$arch.log" check \
		--allowed_licenses="$ALLOWED_LICENSES" --ignore "$MODULE" "$@" || {
		echo "third-party-notices: a dependency's license is not allowed for $os/$arch (allowed: $ALLOWED_LICENSES)" >&2
		exit 1
	}
	GOOS=$os GOARCH=$arch run "$work/save-$os-$arch.log" save \
		--save_path "$work/save/$os-$arch" --ignore "$MODULE" "$@"
	GOOS=$os GOARCH=$arch go list -deps -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' "$@" \
		>>"$work/modules"
done

# The files a notice reproduces. go-licenses saves a license file (and a
# NOTICE beside it) per library; for a license that requires the source to be
# offered (MPL-2.0) it copies the library's whole source instead. Only the
# license and notice files are kept from such a copy, and the library is
# recorded so its entry can say where the source is published.
is_notice_file() {
	local name
	name=$(basename "$1" | tr '[:upper:]' '[:lower:]')
	case $name in
	licen[sc]e* | unlicen[sc]e* | copying* | notice*) return 0 ;;
	*) return 1 ;;
	esac
}

# The union over platforms: the same module at the same version has the same
# files, so a later platform's copy of a file is skipped, not compared.
mkdir -p "$work/merged"
: >"$work/sourced"
for dir in "$work"/save/*/; do
	while IFS= read -r -d '' f; do
		f=${f#./}
		lib=$(dirname "$f")
		case $f in
		*.go) echo "$lib" >>"$work/sourced" ;;
		esac
		is_notice_file "$f" || continue
		[ -e "$work/merged/$f" ] && continue
		mkdir -p "$work/merged/$lib"
		cp "$dir/$f" "$work/merged/$f"
	done < <(cd "$dir" && find . -type f -print0)
done

# A library whose source was copied is a directory holding Go files whose
# ancestors do not: the copy includes its subpackages. Their own license
# files, if any, stay with the library's entry rather than forming entries.
LC_ALL=C sort -u -o "$work/sourced" "$work/sourced"
source_roots=()
while IFS= read -r lib; do
	for root in "${source_roots[@]}"; do
		case $lib in "$root"/*) continue 2 ;; esac
	done
	source_roots+=("$lib")
done <"$work/sourced"

is_source_root() {
	local root
	for root in "${source_roots[@]}"; do
		[ "$root" = "$1" ] && return 0
	done
	return 1
}

is_inside_source_root() {
	local root
	for root in "${source_roots[@]}"; do
		case $1 in "$root"/*) return 0 ;; esac
	done
	return 1
}

# module_of LIB: "path version" of the module that contains package LIB.
module_of() {
	awk -v lib="$1" '
		(lib == $1 || index(lib, $1 "/") == 1) && length($1) > length(best) { best = $1; ver = $2 }
		END { if (best != "") print best, ver }' "$work/modules"
}

# proxy_url MODULE VERSION: the module's source archive on the Go module
# proxy, which serves every published version immutably. Upper-case letters
# are escaped as the proxy protocol requires.
proxy_url() {
	local escaped
	escaped=$(printf '%s' "$1" | sed -E 's/([A-Z])/!\L\1/g')
	printf 'https://proxy.golang.org/%s/@v/%s.zip' "$escaped" "$2"
}

rule=$(printf '%.0s-' {1..79})

section() {
	printf '\n%s\n%s\n%s\n\n' "$rule" "$1" "$rule"
}

# entry LIB: the library's heading, where its source is published if its
# license asks for that, and each file kept for it.
entry() {
	local lib=$1 first=1 f mod
	section "$lib"
	if is_source_root "$lib"; then
		read -r -a mod <<<"$(module_of "$lib")"
		if [ "${#mod[@]}" -ne 2 ]; then
			echo "third-party-notices: no module version for $lib" >&2
			return 1
		fi
		printf 'Included unmodified from %s %s. Its source code is available at\n%s\n\n' \
			"${mod[0]}" "${mod[1]}" "$(proxy_url "${mod[0]}" "${mod[1]}")"
	fi
	while IFS= read -r f; do
		[ "$first" -eq 1 ] || printf '\n'
		first=0
		printf -- '-- %s --\n\n' "$f"
		cat "$work/merged/$lib/$f"
	done < <(cd "$work/merged/$lib" && find . -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort)
}

notices() {
	cat <<HEADER
THIRD-PARTY NOTICES

This program includes the software listed below. Each entry reproduces that
software's license and, where it has one, its NOTICE file. The program itself
is licensed under the Apache License, Version 2.0 (see LICENSE).

Generated for: $*
Platforms: $platforms
HEADER

	section "Go standard library and runtime ($(go env GOVERSION))"
	cat "$GOROOT/LICENSE"

	local lib
	while IFS= read -r lib; do
		is_inside_source_root "$lib" && continue
		entry "$lib" || return 1
	done < <(cd "$work/merged" && find . -type f -printf '%h\n' | sed 's|^\./||' | LC_ALL=C sort -u)
}

notices "$@" >"$work/notices"
mv "$work/notices" "$out"
echo "third-party-notices: wrote $out" >&2
