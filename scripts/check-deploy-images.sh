#!/usr/bin/env bash
# Fails when a manifest names one of this project's images at a tag that is
# not published (phase-11 §11.4, #110). deploy/ is the copy-paste starting
# point of a deployment; an image that does not exist is its first failure.
#
# For every `image:` of ghcr.io/knoppiks/opencloud-backup* in the given files:
#
#   - the reference has a version tag (X.Y.Z or X.Y.Z-rc.N), never `latest`,
#     no other moving tag and no tag at all: published tags exist only for
#     versions (release-publish.yml);
#   - the tag exists in the registry; with `tag@sha256:…`, it also names
#     that digest.
#
# Usage: check-deploy-images.sh FILE...
# RESOLVE names the command that prints a reference's digest, or fails if it
# does not exist (default: docker buildx imagetools inspect). Tests set it.
set -euo pipefail

[ $# -gt 0 ] || {
	echo "usage: $0 FILE..." >&2
	exit 2
}

resolve_digest() {
	if [ -n "${RESOLVE:-}" ]; then
		"$RESOLVE" "$1"
	else
		docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' | jq -r .digest
	fi
}

ours='ghcr\.io/knoppiks/opencloud-backup[a-z-]*'
version='[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?'

failures=0 checked=0
fail() {
	echo "check-deploy-images: $*" >&2
	failures=$((failures + 1))
}

while IFS= read -r line; do
	file=${line%%:*}
	rest=${line#*:}
	lineno=${rest%%:*}
	ref=$(sed -E 's/^[^:]*:[[:space:]]*["'\'']?//; s/["'\'']?[[:space:]]*(#.*)?$//' <<<"${rest#*:}")
	where="$file:$lineno"
	checked=$((checked + 1))

	if ! [[ $ref =~ ^($ours):($version)(@(sha256:[0-9a-f]{64}))?$ ]]; then
		fail "$where: $ref: name a version tag (X.Y.Z or X.Y.Z-rc.N), optionally @sha256:<digest>"
		continue
	fi
	repo=${BASH_REMATCH[1]} tag=${BASH_REMATCH[2]} digest=${BASH_REMATCH[5]}

	if ! got=$(resolve_digest "$repo:$tag" 2>/dev/null) || [ -z "$got" ]; then
		fail "$where: $repo:$tag is not published"
		continue
	fi
	if [ -n "$digest" ] && [ "$got" != "$digest" ]; then
		fail "$where: $repo:$tag is $got, the manifest pins $digest"
		continue
	fi
	echo "check-deploy-images: $where: $repo:$tag${digest:+@$digest} published" >&2
done < <(grep -nE "^[[:space:]]*-?[[:space:]]*image:[[:space:]]*[\"']?$ours" "$@" /dev/null || true)

if [ "$checked" -eq 0 ]; then
	fail "no image of this project found in: $*"
fi
if [ "$failures" -gt 0 ]; then
	echo "check-deploy-images: $failures problem(s)" >&2
	exit 1
fi
