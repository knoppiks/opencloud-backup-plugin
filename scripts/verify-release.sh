#!/usr/bin/env bash
# Verifies downloaded release assets of opencloud-backup-plugin
# (phase-11 §11.3, #104). The same steps README "Verifying a release" lists.
#
#   1. SHA256SUMS was signed by this repository's release workflow, at the
#      given tag (cosign, keyless; needs cosign v2.4 or newer).
#   2. Every asset in the directory that SHA256SUMS lists matches it.
#   3. With -a, each archive also has GitHub build provenance from this
#      repository (needs the GitHub CLI, signed in).
#
# Usage: verify-release.sh [-t TAG] [-a] [DIR]
#   -t TAG  the release's tag (v1.2.3). Without it, a signature from any
#           v* tag of this repository is accepted.
#   -a      also check build provenance (gh attestation verify)
#   DIR     where SHA256SUMS, SHA256SUMS.sigstore.json and the downloaded
#           assets are (default: the current directory)
#
# Exits non-zero, naming the failed step, if anything does not verify.
#
# Runs under the bash 3.2 macOS ships: no empty-array expansions under -u.
set -euo pipefail

REPO=knoppiks/opencloud-backup-plugin
WORKFLOW=https://github.com/$REPO/.github/workflows/release.yml
ISSUER=https://token.actions.githubusercontent.com

usage() {
	echo "usage: $0 [-t TAG] [-a] [DIR]" >&2
	exit 2
}

tag="" attestations=0
while getopts "t:a" opt; do
	case $opt in
	t) tag=$OPTARG ;;
	a) attestations=1 ;;
	*) usage ;;
	esac
done
shift $((OPTIND - 1))
[ $# -le 1 ] || usage
cd "${1:-.}"

die() {
	echo "verify-release: FAILED: $*" >&2
	exit 1
}

[ -f SHA256SUMS ] || die "no SHA256SUMS in $(pwd)"

# 1. The signature. For tests of this script only, against a snapshot
# nobody signed; a real verification never sets it.
if [ "${VERIFY_RELEASE_UNSIGNED_SNAPSHOT:-}" = 1 ]; then
	echo "verify-release: WARNING: signature NOT checked (VERIFY_RELEASE_UNSIGNED_SNAPSHOT=1)" >&2
else
	command -v cosign >/dev/null || die "cosign is not installed (https://docs.sigstore.dev/cosign/system_config/installation/)"
	[ -f SHA256SUMS.sigstore.json ] || die "no SHA256SUMS.sigstore.json in $(pwd)"
	if [ -n "$tag" ]; then
		identity=(--certificate-identity "$WORKFLOW@refs/tags/$tag")
	else
		identity=(--certificate-identity-regexp "^${WORKFLOW//./\\.}@refs/tags/v[0-9]")
	fi
	cosign verify-blob --bundle SHA256SUMS.sigstore.json \
		"${identity[@]}" --certificate-oidc-issuer "$ISSUER" SHA256SUMS >/dev/null ||
		die "SHA256SUMS is not signed by $WORKFLOW${tag:+ at $tag}"
	echo "verify-release: SHA256SUMS signed by the release workflow${tag:+ at $tag}" >&2
fi

# 2. The checksums of whatever was downloaded.
if command -v sha256sum >/dev/null; then
	sha256=(sha256sum)
else
	sha256=(shasum -a 256) # macOS
fi
present=()
while read -r _ name; do
	name=${name#\*}
	[ -f "$name" ] && present+=("$name")
done <SHA256SUMS
[ "${#present[@]}" -gt 0 ] || die "none of the assets SHA256SUMS lists is in $(pwd)"
awk 'NR == FNR { want[$0] = 1; next } { n = $2; sub(/^\*/, "", n) } n in want' \
	<(printf '%s\n' "${present[@]}") SHA256SUMS | "${sha256[@]}" -c - ||
	die "an asset does not match SHA256SUMS"

# 3. Build provenance, per archive.
if [ "$attestations" = 1 ]; then
	command -v gh >/dev/null || die "-a needs the GitHub CLI (gh)"
	source_ref=()
	[ -n "$tag" ] && source_ref=(--source-ref "refs/tags/$tag")
	for name in "${present[@]}"; do
		case $name in *.tar.gz | *.zip) ;; *) continue ;; esac
		gh attestation verify "$name" --repo "$REPO" \
			--signer-workflow "$REPO/.github/workflows/release.yml" \
			${source_ref[@]+"${source_ref[@]}"} >/dev/null ||
			die "$name has no build provenance from $REPO${tag:+ at $tag}"
		echo "verify-release: $name: build provenance from $REPO" >&2
	done
fi

echo "verify-release: OK, ${#present[@]} asset(s) verified" >&2
