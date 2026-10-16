#!/usr/bin/env bash
# Writes a release's notes (markdown, to stdout): the images by digest and
# how to verify everything (phase-11 §11.3, #104). Categorised change notes
# are phase-11 §11.5.
#
# Usage: release-notes.sh TAG IMAGES_FILE
#   TAG          the release's tag (vX.Y.Z or vX.Y.Z-rc.N)
#   IMAGES_FILE  the release's image list: one image@sha256:digest per line
set -euo pipefail

REPO=knoppiks/opencloud-backup-plugin

[ $# -eq 2 ] || {
	echo "usage: $0 TAG IMAGES_FILE" >&2
	exit 2
}
tag=$1 images=$2
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

cat <<EOF
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
