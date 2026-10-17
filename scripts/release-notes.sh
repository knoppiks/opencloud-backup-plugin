#!/usr/bin/env bash
# Writes a release's notes (markdown, to stdout), the draft the owner edits
# before publishing (#104, #112):
#
#   - what changed: the issues the release ships, breaking changes and
#     required operator actions first, then grouped by kind/ label;
#   - the OpenCloud versions it was tested against, by digest;
#   - the images by digest, the downloads, and how to verify them.
#
# The changelog is these notes; there is no CHANGELOG file.
#
# Usage: release-notes.sh TAG IMAGES_FILE ISSUES_FILE VERSIONS_FILE
#   TAG            the release's tag (vX.Y.Z or vX.Y.Z-rc.N)
#   IMAGES_FILE    the release's image list: one image@sha256:digest per line
#   ISSUES_FILE    the issues it ships, as scripts/release-issues.sh prints them
#   VERSIONS_FILE  internal/ocversion/versions.yaml at the tag
# Needs jq and yq (mikefarah v4).
set -euo pipefail

REPO=knoppiks/opencloud-backup-plugin

[ $# -eq 4 ] || {
	echo "usage: $0 TAG IMAGES_FILE ISSUES_FILE VERSIONS_FILE" >&2
	exit 2
}
tag=$1 images=$2 issues=$3 versions=$4
version=${tag#v}

refs=$(grep -E '^[a-z0-9./-]+@sha256:[0-9a-f]{64}$' "$images" || true)
[ "$(wc -l <<<"$refs")" -eq "$(grep -c . "$images")" ] && [ -n "$refs" ] || {
	echo "release-notes: $images must list image@sha256:<digest>, one per line" >&2
	exit 1
}

if [[ $tag == *-rc.* ]]; then
	cat <<EOF
> **Pre-release.** For testing; not a supported version.

EOF
fi

jq -e 'type == "array" and all(.[]; (.number | type) == "number" and (.labels | type) == "array")' \
	"$issues" >/dev/null 2>&1 || {
	echo "release-notes: $issues is not a list of issues (scripts/release-issues.sh)" >&2
	exit 1
}

# --- what changed -------------------------------------------------------------
# section TITLE JQ_FILTER: the issues the filter selects, as "- Title #N", or
# nothing if it selects none. GitHub links "#N" by itself.
section() {
	local items
	items=$(jq -r "[.[] | select($2)] | sort_by(.number) | .[] | \"- \\(.title) #\\(.number)\"" "$issues")
	[ -n "$items" ] || return 0
	printf '### %s\n\n%s\n\n' "$1" "$items"
}

kind() { printf '(.labels | index("kind/%s")) != null' "$1"; }
hint() { printf '(.labels | index("hint/%s")) != null' "$1"; }

echo "## What changed"
echo
if [ "$(jq length "$issues")" -eq 0 ]; then
	printf 'No issues closed since the previous release.\n\n'
fi
section "Breaking changes" "$(hint Breaking)"
section "Operators must act when upgrading" "$(hint "Operator Action")"
section "Highlights" "$(kind Epic)"
section "Features" "$(kind Feature)"
section "Enhancements" "$(kind Enhancement)"
section "Bug fixes" "$(kind Bug)"
section "Refactoring" "$(kind Refactoring)"
section "Other" "([.labels[] | select(. == \"kind/Epic\" or . == \"kind/Feature\" or . == \"kind/Enhancement\" or . == \"kind/Bug\" or . == \"kind/Refactoring\")] | length) == 0"

# --- tested OpenCloud versions ---------------------------------------------------
# yq reads stdin: a confined yq (snap) cannot open every path.
legs=$(yq -o=json '.legs' <"$versions" |
	jq -r '.[] | "| \(.name) | \(.channel) | \(.tag) | `\(.image)@\(.digest)` |"')
[ -n "$legs" ] || {
	echo "release-notes: no OpenCloud legs in $versions" >&2
	exit 1
}

cat <<EOF
## Tested with OpenCloud

CI ran the full suite (integration and browser end-to-end) against each of
these, pinned by digest, before this release was built. The range of
OpenCloud versions they make supported:
[supported versions](https://github.com/$REPO/blob/$tag/README.md#deployment-preconditions).

| Leg | Channel | Version | Image |
|---|---|---|---|
$legs

## Images

Deploy by digest. The tag \`$version\` points at the same digest once this
release is published.

\`\`\`
$refs
\`\`\`

## Downloads

| Asset | What it is |
|---|---|
| \`decrypt_${version}_<os>_<arch>\` | Opens a Take-Out with your Recovery Key, offline. Linux, macOS, Windows. |
| \`takeout_${version}_linux_<arch>\` | The administrator's Take-Out tool, outside the image. |
| \`backup-vault_${version}.tar.gz\` | The web extension, for installs without the initContainer image. |
| \`*.spdx.json\` | SBOMs: one per archive, one per image and platform. |
| \`images.txt\` | The images above, covered by the signature. |
| \`SHA256SUMS\`, \`SHA256SUMS.sigstore.json\` | Checksums of every asset, and their signature. |

Each archive holds the program, \`LICENSE\` and the third-party notices for
its platform.

## Verify

With [cosign](https://docs.sigstore.dev/cosign/system_config/installation/)
v2.4 or newer, in the directory holding the downloaded assets, \`SHA256SUMS\` and
\`SHA256SUMS.sigstore.json\`:

\`\`\`sh
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \\
  --certificate-identity https://github.com/$REPO/.github/workflows/release.yml@refs/tags/$tag \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c SHA256SUMS   # macOS: shasum -a 256 --ignore-missing -c SHA256SUMS
\`\`\`

or run [\`scripts/verify-release.sh -t $tag\`](https://github.com/$REPO/blob/$tag/scripts/verify-release.sh),
which does both. Build provenance: \`gh attestation verify <archive> --repo $REPO\`.
Images: \`cosign verify <image>@<digest> --certificate-identity …\` (same
identity and issuer) with **cosign v3 or newer**, or
\`gh attestation verify oci://<image>@<digest> --repo $REPO\`. An older cosign
reports \`no signatures found\` for these images: upgrade, do not conclude
they were tampered with.

**macOS:** the binaries are not notarised. After verifying, allow \`decrypt\`
to run with \`xattr -d com.apple.quarantine ./decrypt\`.

Details: [Verifying a release](https://github.com/$REPO/blob/$tag/README.md#verifying-a-release).
EOF
