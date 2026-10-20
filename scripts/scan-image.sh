#!/usr/bin/env bash
# Scans container images for known vulnerabilities with Trivy (phase-11
# §11.6, #116). For each image it prints every HIGH and CRITICAL finding,
# then fails if any CRITICAL one has a fixed version available: that one a
# rebuild on a current base image, or a dependency bump, would remove.
# Unfixed findings are reported, not blocking: nothing the project can do
# removes them.
#
# An image the local Docker daemon has (a `make image` build) is scanned
# from `docker save`, anything else is pulled from its registry with the
# credentials of `docker login`.
#
# Trivy runs as the image TRIVY_IMAGE (pinned by digest, Renovate bumps it).
# Its vulnerability database is cached in TRIVY_CACHE. A finding the owner
# has judged harmless goes into .trivyignore.yaml at the repository root,
# with a statement (why) and an expired_at.
#
# Usage: scan-image.sh IMAGE...
set -euo pipefail

[ $# -gt 0 ] || {
	echo "usage: $0 IMAGE..." >&2
	exit 2
}
: "${TRIVY_IMAGE:?TRIVY_IMAGE is not set (the Makefile sets it)}"

root=$(git rev-parse --show-toplevel)
cache=${TRIVY_CACHE:-$root/dist/trivy-cache}
work=$(mktemp -d "${TMPDIR:-/tmp}/scan-image.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$cache"

# The registry credentials, read-only, for images that are not local.
docker_config=()
if [ -f "${DOCKER_CONFIG:-$HOME/.docker}/config.json" ]; then
	docker_config=(-v "${DOCKER_CONFIG:-$HOME/.docker}/config.json:/docker/config.json:ro" -e DOCKER_CONFIG=/docker)
fi

ignorefile=()
[ -f "$root/.trivyignore.yaml" ] && ignorefile=(--ignorefile /repo/.trivyignore.yaml)

# trivy ARGS...: Trivy as the invoking user, the repository read-only at
# /repo, the scratch directory at /work.
trivy() {
	docker run --rm --user "$(id -u):$(id -g)" \
		-v "$root:/repo:ro" -v "$work:/work" -v "$cache:/cache" "${docker_config[@]}" \
		"$TRIVY_IMAGE" --cache-dir /cache --quiet "$@"
}

failed=()
for image in "$@"; do
	echo "== $image"
	if docker image inspect "$image" >/dev/null 2>&1; then
		docker save -o "$work/image.tar" "$image"
		target=(--input /work/image.tar)
	else
		target=("$image")
	fi
	# The report, then the verdict: a second scan against the database the
	# first one fetched, filtered to what blocks.
	trivy image --scanners vuln "${ignorefile[@]}" --severity HIGH,CRITICAL "${target[@]}"
	if ! trivy image --scanners vuln "${ignorefile[@]}" --skip-db-update \
		--severity CRITICAL --ignore-unfixed --exit-code 1 "${target[@]}" >/dev/null; then
		echo "FAILED  $image: a fixable CRITICAL vulnerability (listed above)" >&2
		failed+=("$image")
	fi
	rm -f "$work/image.tar"
done

if [ ${#failed[@]} -gt 0 ]; then
	echo "fixable critical vulnerabilities in: ${failed[*]}" >&2
	exit 1
fi
echo "no fixable critical vulnerabilities in $# image(s)"
