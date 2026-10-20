#!/usr/bin/env bash
# Proves the image scan in scripts/scan-image.sh still refuses what it must
# (#116). A scan that silently stops refusing looks exactly like an image
# without a problem, so this builds two stand-in images and scans them:
#
#   - one claiming Alpine 3.16's zlib 1.2.12-r1, which has a critical
#     vulnerability with a fix (CVE-2022-37434, fixed in 1.2.12-r2): the scan
#     must fail;
#   - the same with 1.2.12-r2: the scan must pass.
#
# The images hold nothing but the files Trivy reads to know what is
# installed; there is no zlib in them.
#
# Usage: scan-probe.sh (TRIVY_IMAGE set, as the Makefile does)
set -euo pipefail

root=$(git rev-parse --show-toplevel)
base="$root/dist/scan-probe"
rm -rf "$base"
trap 'rm -rf "$base"; docker image rm -f scan-probe:vulnerable scan-probe:patched >/dev/null 2>&1 || true' EXIT

# stand_in TAG ZLIB_VERSION: an image whose apk database lists that zlib.
stand_in() {
	local dir="$base/$1"
	mkdir -p "$dir/rootfs/etc" "$dir/rootfs/lib/apk/db"
	echo 3.16.0 >"$dir/rootfs/etc/alpine-release"
	printf 'P:zlib\nV:%s\nA:x86_64\no:zlib\n\n' "$2" >"$dir/rootfs/lib/apk/db/installed"
	printf 'FROM scratch\nCOPY rootfs/ /\n' >"$dir/Dockerfile"
	docker build -q -t "scan-probe:$1" "$dir" >/dev/null
}

stand_in vulnerable 1.2.12-r1
stand_in patched 1.2.12-r2

failures=0
if "$root/scripts/scan-image.sh" scan-probe:vulnerable >"$base/vulnerable.log" 2>&1; then
	cat "$base/vulnerable.log"
	echo "FAILED  the scan passed an image with a fixable critical vulnerability" >&2
	failures=$((failures + 1))
elif ! grep -q 'CVE-2022-37434' "$base/vulnerable.log"; then
	cat "$base/vulnerable.log"
	echo "FAILED  the scan failed, but not on CVE-2022-37434" >&2
	failures=$((failures + 1))
else
	echo "ok      fixable critical vulnerability refused"
fi

if ! "$root/scripts/scan-image.sh" scan-probe:patched >"$base/patched.log" 2>&1; then
	cat "$base/patched.log"
	echo "FAILED  the scan refused an image without a known vulnerability" >&2
	failures=$((failures + 1))
else
	echo "ok      patched image passed"
fi

[ "$failures" -eq 0 ]
