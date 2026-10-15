#!/usr/bin/env bash
# Prints the image tags a published release gets, one per line
# (release-publish.yml, phase-11 §11.3):
#
#   X.Y.Z   always (X.Y.Z-rc.N for a pre-release)
#   X.Y     for a release, if it is the newest published release of X.Y,
#           so publishing an older patch late cannot move X.Y backwards
#
# Never `latest`: a deployment pins a version.
#
# Usage: image-tags.sh TAG [PUBLISHED_TAG...]
#   TAG            the release being published (vX.Y.Z or vX.Y.Z-rc.N)
#   PUBLISHED_TAG  the tags of the repository's published releases
#                  (pre-releases and other tags are ignored)
set -euo pipefail

[ $# -ge 1 ] || {
	echo "usage: $0 TAG [PUBLISHED_TAG...]" >&2
	exit 2
}
tag=$1
shift

release='^v([0-9]+)\.([0-9]+)\.([0-9]+)$'
prerelease='^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'

if [[ $tag =~ $prerelease ]]; then
	echo "${tag#v}"
	exit 0
fi
[[ $tag =~ $release ]] || {
	echo "image-tags: $tag is not vX.Y.Z or vX.Y.Z-rc.N" >&2
	exit 1
}
echo "${tag#v}"

minor=v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}
newest=$(
	for t in "$tag" "$@"; do
		if [[ $t =~ $release ]] && [[ $t == "$minor".* ]]; then
			echo "$t"
		fi
	done | sort -V | tail -n 1
)
[ "$newest" = "$tag" ] && echo "${minor#v}"
exit 0
