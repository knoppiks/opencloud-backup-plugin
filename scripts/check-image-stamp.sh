#!/usr/bin/env bash
# Checks that every artifact in the two images reports the same version and
# commit (#102): the programs' -version lines, the images' OCI labels, and the
# web bundle's own version constant. Run after `make image web-image`.
#
# Usage: check-image-stamp.sh SERVICE_IMAGE WEB_IMAGE VERSION COMMIT
set -euo pipefail

[ $# -eq 4 ] || {
	echo "usage: $0 SERVICE_IMAGE WEB_IMAGE VERSION COMMIT" >&2
	exit 2
}
service=$1 web=$2 version=$3 commit=$4
short=${commit:0:12}

failures=0
expect() {
	local what=$1 got=$2 want=$3
	if [ "$got" = "$want" ]; then
		echo "ok      $what: $got"
	else
		echo "FAILED  $what: got '$got', want '$want'" >&2
		failures=$((failures + 1))
	fi
}

# "<program> <version> (<commit>, <go>, <platform>)" -> "<version> <commit>"
version_line() {
	sed -E 's/^[^ ]+ ([^ ]+) \(([^,]+),.*$/\1 \2/'
}

label() {
	docker inspect --format "{{ index .Config.Labels \"org.opencontainers.image.$2\" }}" "$1"
}

expect "backupd version" "$(docker run --rm "$service" version | version_line)" "$version $short"
expect "takeout -version" \
	"$(docker run --rm --entrypoint /takeout "$service" -version | version_line)" "$version $short"
expect "install-bundle -version" "$(docker run --rm "$web" -version | version_line)" "$version $short"

for image in "$service" "$web"; do
	expect "$image label version" "$(label "$image" version)" "$version"
	expect "$image label revision" "$(label "$image" revision)" "$commit"
done

# The bundle carries its version as a string constant, in the chunk that
# compares it with the service's. Installed as the caller, so the copy can
# be removed again.
extensions=$(mktemp -d)
trap 'rm -rf "$extensions"' EXIT
docker run --rm --network none --user "$(id -u):$(id -g)" \
	-v "$extensions:/extensions" "$web" >/dev/null
if grep -rqF -- "$version" "$extensions/backup-vault/js"; then
	echo "ok      bundle version: $version"
else
	echo "FAILED  bundle version: '$version' not in the bundle" >&2
	failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
	echo "check-image-stamp: $failures artifact(s) do not report $version ($short)" >&2
	exit 1
fi
echo "check-image-stamp: every artifact reports $version ($short)"
