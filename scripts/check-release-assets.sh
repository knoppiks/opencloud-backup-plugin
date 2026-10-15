#!/usr/bin/env bash
# Checks what a release (or `make release-snapshot`) built before anyone
# relies on it (phase-11 §11.3, #104):
#
#   - every expected archive is there, named <program>_<version>_<os>_<arch>,
#     with nothing more or less inside than the program, LICENSE and the
#     THIRD_PARTY_NOTICES generated for that archive's platform;
#   - every archive has an SPDX SBOM;
#   - the web bundle archive holds the bundle, its notices and LICENSE;
#   - SHA256SUMS lists exactly the release's assets, and they match it;
#   - the programs this machine can run report the stamp, and are static.
#
# Usage: check-release-assets.sh RELEASE_DIST ASSETS_DIR VERSION COMMIT
#   RELEASE_DIST  GoReleaser's dist directory (archives, SBOMs, SHA256SUMS)
#   ASSETS_DIR    assets added by earlier release jobs (web bundle, image
#                 SBOMs, image list)
#   VERSION       the stamp, with its leading v (v1.2.3, v1.2.3-4-gabc…)
#   COMMIT        the full commit the stamp names
# DECRYPT_PLATFORMS / TAKEOUT_PLATFORMS: space-separated os/arch pairs the
# archives must cover (the Makefile passes its own list; defaults below).
set -euo pipefail

[ $# -eq 4 ] || {
	echo "usage: $0 RELEASE_DIST ASSETS_DIR VERSION COMMIT" >&2
	exit 2
}
dist=$(realpath "$1") assets=$(realpath -m "$2") version=$3 commit=$4
ver=${version#v}
decrypt_platforms=${DECRYPT_PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64}
takeout_platforms=${TAKEOUT_PLATFORMS:-linux/amd64 linux/arm64}
host="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "check-release-assets: $*" >&2
	failures=$((failures + 1))
}

# list_archive FILE: the archive's entries, one per line, files only.
list_archive() {
	case $1 in
	*.zip) unzip -Z1 "$1" | grep -v '/$' ;;
	*) tar -tzf "$1" | grep -v '/$' ;;
	esac
}

extract() {
	case $1 in
	*.zip) unzip -q -o "$1" -d "$2" ;;
	*) tar -xzf "$1" -C "$2" ;;
	esac
}

# A program the host can run must report the stamp and be static.
check_program() {
	local bin=$1 program=$2 want line
	want="$program $version ("
	line=$("$bin" -version) || {
		fail "$program -version failed"
		return
	}
	case $line in
	"$want"*) ;;
	*) fail "$program -version printed '$line', want it to start with '$want'" ;;
	esac
	case $line in
	*"(${commit:0:12}"*) ;;
	*) fail "$program -version printed '$line', want commit ${commit:0:12}" ;;
	esac
	if ! file "$bin" | grep -q 'statically linked'; then
		fail "$program is not statically linked: $(file -b "$bin")"
	fi
}

expected_assets=()

# check_archives PROGRAM PLATFORMS...
check_archives() {
	local program=$1 platform os arch ext name archive exe entries want notices
	shift
	for platform in "$@"; do
		os=${platform%/*}
		arch=${platform#*/}
		ext=tar.gz exe=$program
		if [ "$os" = windows ]; then
			ext=zip exe=$program.exe
		fi
		name=${program}_${ver}_${os}_${arch}
		archive=$dist/$name.$ext
		expected_assets+=("$name.$ext" "$name.$ext.spdx.json")
		if [ ! -f "$archive" ]; then
			fail "missing $name.$ext"
			continue
		fi

		entries=$(list_archive "$archive" | LC_ALL=C sort)
		want=$(printf '%s\n' "$name/LICENSE" "$name/THIRD_PARTY_NOTICES" "$name/$exe" | LC_ALL=C sort)
		[ "$entries" = "$want" ] || fail "$name.$ext holds"$'\n'"$entries"$'\n'"want"$'\n'"$want"

		mkdir -p "$work/$name"
		extract "$archive" "$work/$name"
		notices=$work/$name/$name/THIRD_PARTY_NOTICES
		if [ -f "$notices" ] && ! grep -qx "Platforms: $os/$arch" "$notices"; then
			fail "$name.$ext: THIRD_PARTY_NOTICES is not the one for $os/$arch ($(grep -m1 '^Platforms:' "$notices"))"
		fi
		[ "$platform" = "$host" ] && check_program "$work/$name/$name/$exe" "$program"

		local sbom=$dist/$name.$ext.spdx.json
		if [ ! -f "$sbom" ]; then
			fail "missing SBOM $name.$ext.spdx.json"
		elif ! jq -e '.spdxVersion | startswith("SPDX-")' "$sbom" >/dev/null; then
			fail "$name.$ext.spdx.json is not an SPDX JSON document"
		fi
	done
}

# shellcheck disable=SC2086 # the platform lists are word lists
check_archives decrypt $decrypt_platforms
# shellcheck disable=SC2086
check_archives takeout $takeout_platforms

# The web bundle, as an install without the initContainer image unpacks it.
web=backup-vault_$ver.tar.gz
if [ ! -f "$assets/$web" ]; then
	fail "missing $web"
else
	entries=$(list_archive "$assets/$web")
	for f in manifest.json LICENSE THIRD_PARTY_NOTICES; do
		grep -qx "backup-vault/$f" <<<"$entries" || fail "$web holds no backup-vault/$f"
	done
	grep -qv '^backup-vault/' <<<"$entries" && fail "$web holds files outside backup-vault/"
fi

# Whatever else the release jobs added (image SBOMs, the image list).
if [ -d "$assets" ]; then
	while IFS= read -r f; do
		expected_assets+=("$f")
	done < <(cd "$assets" && find . -maxdepth 1 -type f -printf '%f\n')
fi

# SHA256SUMS: exactly the assets, and every one matches.
sums=$dist/SHA256SUMS
if [ ! -f "$sums" ]; then
	fail "missing SHA256SUMS"
else
	listed=$(awk '{ sub(/^\*/, "", $2); print $2 }' "$sums" | LC_ALL=C sort)
	want=$(printf '%s\n' "${expected_assets[@]}" | LC_ALL=C sort -u)
	[ "$listed" = "$want" ] || fail "SHA256SUMS lists"$'\n'"$listed"$'\n'"want"$'\n'"$want"
	mkdir -p "$work/sums"
	for f in "${expected_assets[@]}"; do
		for dir in "$dist" "$assets"; do
			if [ -f "$dir/$f" ]; then
				ln -sf "$dir/$f" "$work/sums/$f"
			fi
		done
	done
	(cd "$work/sums" && sha256sum --quiet -c "$sums") || fail "SHA256SUMS does not match the assets"
fi

if [ "$failures" -gt 0 ]; then
	echo "check-release-assets: $failures problem(s)" >&2
	exit 1
fi
echo "check-release-assets: ${#expected_assets[@]} assets for $version look right" >&2
