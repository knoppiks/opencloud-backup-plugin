#!/usr/bin/env bash
# Tests the release's shell scripts (phase-11 §11.3, #104):
#
#   scripts/image-tags.sh            which tags a published release gets
#   scripts/release-notes.sh         refuses an image list that is not by digest
#   scripts/verify-release.sh        the checksum half refuses a changed asset
#                                    and an empty download; the signature half
#                                    cannot be skipped by accident
#   scripts/check-release-assets.sh  still refuses (with RELEASE_DIST given):
#                                    a changed archive, a missing SBOM
#
# Usage: release-scripts-test.sh [RELEASE_DIST ASSETS_DIR VERSION COMMIT]
#   With arguments (after `make release-snapshot`), the asset check is
#   probed against a damaged copy of that snapshot.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
scripts=$root/scripts
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

# expect_tags WANT TAG [PUBLISHED...]: WANT is the expected output, with
# tags separated by spaces.
expect_tags() {
	local want=$1 got
	shift
	got=$("$scripts/image-tags.sh" "$@" | tr '\n' ' ' | sed 's/ $//') || {
		fail "image-tags $* failed"
		return
	}
	[ "$got" = "$want" ] || fail "image-tags $*: got '$got', want '$want'"
}

expect_refused() {
	local what=$1
	shift
	if "$@" >/dev/null 2>&1; then
		fail "$what: accepted, want refused"
	fi
}

# --- image-tags ---------------------------------------------------------------
expect_tags "1.2.3 1.2" v1.2.3
expect_tags "1.2.3 1.2" v1.2.3 v1.2.2 v1.1.9
expect_tags "1.2.3" v1.2.3 v1.2.4
expect_tags "1.2.10 1.2" v1.2.10 v1.2.9
expect_tags "1.2.3 1.2" v1.2.3 v1.3.0 v1.2.4-rc.1 v1.2.30x
expect_tags "1.2.3-rc.1" v1.2.3-rc.1
expect_tags "1.2.3-rc.1" v1.2.3-rc.1 v1.2.0
expect_refused "image-tags 1.2.3 (no v)" "$scripts/image-tags.sh" 1.2.3
expect_refused "image-tags v1.2" "$scripts/image-tags.sh" v1.2
expect_refused "image-tags v1.2.3-beta" "$scripts/image-tags.sh" v1.2.3-beta
expect_refused "image-tags without a tag" "$scripts/image-tags.sh"

# --- release-notes ------------------------------------------------------------
digest=sha256:$(printf 'x' | sha256sum | cut -d' ' -f1)
printf '%s\n' "ghcr.io/knoppiks/opencloud-backupd@$digest" \
	"ghcr.io/knoppiks/opencloud-backup-vault-web@$digest" >"$work/images.txt"
notes=$("$scripts/release-notes.sh" v1.2.3 "$work/images.txt")
grep -qxF "ghcr.io/knoppiks/opencloud-backupd@$digest" <<<"$notes" || fail "notes lack the backupd digest"
grep -qxF "ghcr.io/knoppiks/opencloud-backup-vault-web@$digest" <<<"$notes" || fail "notes lack the web digest"
grep -qF "release.yml@refs/tags/v1.2.3" <<<"$notes" || fail "notes lack the signing identity"
grep -q "Pre-release" <<<"$notes" && fail "notes of v1.2.3 call it a pre-release"
# Images are signed with cosign v3; an older cosign finds no signature (#108).
grep -qF "v2.4 or newer" <<<"$notes" || fail "notes do not name the cosign version for files"
grep -qF "cosign v3 or newer" <<<"$notes" || fail "notes do not name the cosign version for images"
grep -qF "no signatures found" <<<"$notes" || fail "notes do not explain an older cosign's answer"
notes=$("$scripts/release-notes.sh" v1.2.3-rc.1 "$work/images.txt")
grep -q "Pre-release" <<<"$notes" || fail "notes of v1.2.3-rc.1 do not call it a pre-release"

echo "ghcr.io/knoppiks/opencloud-backupd:1.2.3" >"$work/by-tag.txt"
expect_refused "release-notes with an image by tag" "$scripts/release-notes.sh" v1.2.3 "$work/by-tag.txt"
: >"$work/empty.txt"
expect_refused "release-notes with no images" "$scripts/release-notes.sh" v1.2.3 "$work/empty.txt"
cat "$work/images.txt" "$work/by-tag.txt" >"$work/mixed.txt"
expect_refused "release-notes with one image by tag" "$scripts/release-notes.sh" v1.2.3 "$work/mixed.txt"

# --- verify-release (checksums) -----------------------------------------------
dl=$work/download
mkdir -p "$dl"
head -c 1024 /dev/urandom >"$dl/decrypt_1.2.3_linux_amd64.tar.gz"
head -c 1024 /dev/urandom >"$dl/decrypt_1.2.3_linux_amd64.tar.gz.spdx.json"
head -c 1024 /dev/urandom >"$dl/takeout_1.2.3_linux_amd64.tar.gz"
(cd "$dl" && sha256sum ./*.gz ./*.json | sed 's| \./| |' >SHA256SUMS)
echo "0000000000000000000000000000000000000000000000000000000000000000  not-downloaded.zip" >>"$dl/SHA256SUMS"

unsigned() { VERIFY_RELEASE_UNSIGNED_SNAPSHOT=1 "$scripts/verify-release.sh" "$@"; }
unsigned "$dl" >/dev/null 2>&1 || fail "verify-release refused intact assets"
rm "$dl/takeout_1.2.3_linux_amd64.tar.gz"
unsigned "$dl" >/dev/null 2>&1 || fail "verify-release refused a partial download"
printf 'x' >>"$dl/decrypt_1.2.3_linux_amd64.tar.gz"
expect_refused "verify-release with a changed asset" unsigned "$dl"
mkdir -p "$work/none"
cp "$dl/SHA256SUMS" "$work/none/"
expect_refused "verify-release with nothing downloaded" unsigned "$work/none"
# The signature is checked unless the test switch is set: no bundle, refused.
head -c 1024 /dev/urandom >"$work/none/decrypt_1.2.3_linux_amd64.tar.gz"
expect_refused "verify-release without a signature" "$scripts/verify-release.sh" "$work/none"

# --- check-release-assets still refuses ----------------------------------------
if [ $# -eq 4 ]; then
	dist=$1 assets=$2 version=$3 commit=$4
	probe() {
		local what=$1 damage=$2
		rm -rf "$work/probe"
		mkdir -p "$work/probe"
		cp -R "$dist" "$work/probe/dist"
		cp -R "$assets" "$work/probe/assets"
		(cd "$work/probe" && eval "$damage")
		expect_refused "check-release-assets with $what" \
			"$scripts/check-release-assets.sh" "$work/probe/dist" "$work/probe/assets" "$version" "$commit"
	}
	# shellcheck disable=SC2016 # expanded by the probe, inside the copy
	probe "a changed archive" 'printf x >>"$(ls dist/takeout_*_linux_arm64.tar.gz)"'
	probe "a missing SBOM" 'rm dist/decrypt_*_darwin_arm64.tar.gz.spdx.json'
	probe "a missing web archive" 'rm assets/backup-vault_*.tar.gz'
	probe "an asset SHA256SUMS does not list" 'echo extra >assets/unlisted.txt'
elif [ $# -ne 0 ]; then
	echo "usage: $0 [RELEASE_DIST ASSETS_DIR VERSION COMMIT]" >&2
	exit 2
fi

if [ "$failures" -gt 0 ]; then
	echo "release-scripts-test: $failures failure(s)" >&2
	exit 1
fi
echo "release-scripts-test: ok" >&2
