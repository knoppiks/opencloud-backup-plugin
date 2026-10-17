#!/usr/bin/env bash
# Tests the release's shell scripts (phase-11 §11.3, #104; §11.5, #112):
#
#   scripts/image-tags.sh            which tags a published release gets
#   scripts/release-notes.sh         what changed by kind, OpenCloud legs; refuses
#                                    an image list not by digest, a bad issue list
#   scripts/check-issues.sh          one kind/ per issue, milestones, warn mode
#   scripts/check-release-notes.sh   the draft carries the notes it was given
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
versions=$root/internal/ocversion/versions.yaml
cat >"$work/issues.json" <<'JSON'
[
  {"number": 7, "title": "Fix A Thing", "labels": ["kind/Bug", "aspect/Core"], "milestone": "Phase 2"},
  {"number": 3, "title": "Add A Feature", "labels": ["kind/Feature"], "milestone": "Phase 2"},
  {"number": 9, "title": "Rename A Setting", "labels": ["kind/Enhancement", "hint/Breaking", "hint/Operator Action"], "milestone": "Phase 2"},
  {"number": 5, "title": "Unkinded", "labels": ["aspect/Core"], "milestone": null},
  {"number": 2, "title": "A Bigger Piece", "labels": ["kind/Epic"], "milestone": "Phase 2"}
]
JSON
echo '[]' >"$work/none.json"
# release_notes TAG [IMAGES] [ISSUES]; an empty argument takes the default.
release_notes() {
	local images=${2:-$work/images.txt} issues=${3:-$work/issues.json}
	"$scripts/release-notes.sh" "$1" "$images" "$issues" "$versions"
}

notes=$(release_notes v1.2.3)
grep -qxF "ghcr.io/knoppiks/opencloud-backupd@$digest" <<<"$notes" || fail "notes lack the backupd digest"
grep -qxF "ghcr.io/knoppiks/opencloud-backup-vault-web@$digest" <<<"$notes" || fail "notes lack the web digest"
grep -qF "release.yml@refs/tags/v1.2.3" <<<"$notes" || fail "notes lack the signing identity"
grep -q "Pre-release" <<<"$notes" && fail "notes of v1.2.3 call it a pre-release"
# Images are signed with cosign v3; an older cosign finds no signature (#108).
grep -qF "v2.4 or newer" <<<"$notes" || fail "notes do not name the cosign version for files"
grep -qF "cosign v3 or newer" <<<"$notes" || fail "notes do not name the cosign version for images"
grep -qF "no signatures found" <<<"$notes" || fail "notes do not explain an older cosign's answer"

# What changed (#112): sections in this order, only the non-empty ones, each
# issue under its kind, hinted ones also on top.
sections=$(grep '^### ' <<<"$notes" | tr '\n' '|')
want='### Breaking changes|### Operators must act when upgrading|### Highlights|### Features|### Enhancements|### Bug fixes|### Other|'
[ "$sections" = "$want" ] || fail "notes sections: got '$sections', want '$want'"
section_of() { awk -v item="- $1" '/^### /{s=$0} $0==item{print s}' <<<"$notes" | tr '\n' '|'; }
[ "$(section_of "Fix A Thing #7")" = "### Bug fixes|" ] || fail "#7 is under '$(section_of "Fix A Thing #7")'"
[ "$(section_of "Rename A Setting #9")" = "### Breaking changes|### Operators must act when upgrading|### Enhancements|" ] ||
	fail "#9 is under '$(section_of "Rename A Setting #9")'"
[ "$(section_of "Unkinded #5")" = "### Other|" ] || fail "#5 is under '$(section_of "Unkinded #5")'"
[ "$(section_of "A Bigger Piece #2")" = "### Highlights|" ] || fail "#2 is under '$(section_of "A Bigger Piece #2")'"

# Tested OpenCloud versions: every leg of versions.yaml, by digest.
legs=0
while IFS=$'\t' read -r octag image legdigest; do
	legs=$((legs + 1))
	grep -qF "| $octag | \`$image@$legdigest\` |" <<<"$notes" || fail "notes lack OpenCloud $octag ($image@$legdigest)"
done < <(yq -o=tsv '.legs[] | [.tag, .image, .digest]' <"$versions")
[ "$legs" -gt 0 ] || fail "read no OpenCloud legs from versions.yaml"

notes=$(release_notes v1.2.3 "" "$work/none.json")
grep -qF "No issues closed since the previous release." <<<"$notes" || fail "notes without issues do not say so"
grep -q '^### ' <<<"$notes" && fail "notes without issues have sections"

notes=$(release_notes v1.2.3-rc.1)
grep -q "Pre-release" <<<"$notes" || fail "notes of v1.2.3-rc.1 do not call it a pre-release"

echo "ghcr.io/knoppiks/opencloud-backupd:1.2.3" >"$work/by-tag.txt"
expect_refused "release-notes with an image by tag" release_notes v1.2.3 "$work/by-tag.txt"
: >"$work/empty.txt"
expect_refused "release-notes with no images" release_notes v1.2.3 "$work/empty.txt"
cat "$work/images.txt" "$work/by-tag.txt" >"$work/mixed.txt"
expect_refused "release-notes with one image by tag" release_notes v1.2.3 "$work/mixed.txt"
echo '{"not": "a list"}' >"$work/bad.json"
expect_refused "release-notes with a malformed issue list" release_notes v1.2.3 "" "$work/bad.json"

# --- check-release-notes --------------------------------------------------------
# A fake GitHub CLI whose releases API returns $BODY as the draft's body.
cat >"$work/gh-notes" <<'SH'
#!/usr/bin/env bash
jq -n --arg body "$BODY" '[{tag_name: "v1.2.3-rc.1", draft: true, body: $body},
	{tag_name: "v1.2.2", draft: false, body: "older"}] | .[] | select(.tag_name == "v1.2.3-rc.1" and .draft) | .body' -r
SH
chmod +x "$work/gh-notes"
printf '## What changed\n\n- Fix A Thing #7\n' >"$work/notes.md"
check_notes() { GH=$work/gh-notes BODY=$1 "$scripts/check-release-notes.sh" v1.2.3-rc.1 "$work/notes.md" >/dev/null 2>&1; }
check_notes $'## What changed\r\n\r\n- Fix A Thing #7\r\n' || fail "check-release-notes refused the notes with CRLF line endings"
expect_refused "check-release-notes: empty body (GoReleaser dropped the notes)" check_notes $'\n'
expect_refused "check-release-notes: other body" check_notes "## Changelog"
: >"$work/empty-notes.md"
expect_refused "check-release-notes: empty notes file" \
	env GH="$work/gh-notes" BODY="" "$scripts/check-release-notes.sh" v1.2.3-rc.1 "$work/empty-notes.md"

# --- check-issues ---------------------------------------------------------------
check_issues() { "$scripts/check-issues.sh" "$@" >/dev/null 2>&1; }
echo '[{"number": 1, "title": "Ready", "labels": ["kind/Bug", "aspect/Core"], "milestone": "Phase 11"}]' >"$work/ready.json"
echo '[{"number": 1, "title": "Two", "labels": ["kind/Bug", "kind/Feature"], "milestone": "Phase 11"}]' >"$work/two-kinds.json"
echo '[{"number": 1, "title": "Loose", "labels": ["kind/Bug"], "milestone": null}]' >"$work/no-milestone.json"
check_issues -m -e "$work/ready.json" || fail "check-issues refused a ready issue"
check_issues "$work/none.json" || fail "check-issues refused a release without issues"
check_issues "$work/no-milestone.json" || fail "check-issues wants a milestone without -m"
expect_refused "check-issues: no kind/" check_issues "$work/issues.json"
expect_refused "check-issues: two kind/" check_issues "$work/two-kinds.json"
expect_refused "check-issues -m: no milestone" check_issues -m "$work/no-milestone.json"
expect_refused "check-issues -e: closes nothing" check_issues -e "$work/none.json"
check_issues -w "$work/two-kinds.json" || fail "check-issues -w failed instead of warning"
out=$("$scripts/check-issues.sh" -w "$work/two-kinds.json" 2>&1)
grep -qF '::warning::#1 Two: has 2 kind/ labels' <<<"$out" || fail "check-issues -w does not warn: $out"
out=$("$scripts/check-issues.sh" "$work/two-kinds.json" 2>&1 || true)
grep -qF '::error::#1 Two' <<<"$out" || fail "check-issues does not report an error: $out"

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
