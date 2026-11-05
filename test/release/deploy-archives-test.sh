#!/usr/bin/env bash
# Tests scripts/deploy-archives.sh and scripts/check-deploy-archives.sh
# (#160): the add-ons from this checkout, stamped with stand-in digests, are
# accepted and rebuild bit-identically; the stamp refuses an image list it
# cannot use; the check refuses each way an archive can be wrong. No network.
#
# Usage: deploy-archives-test.sh
# KUSTOMIZE names the kustomize binary (the Makefile passes the pinned one).
set -euo pipefail

root=$(git rev-parse --show-toplevel)
scripts=$root/scripts
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

version=v1.2.3-rc.1 tag=1.2.3-rc.1 date=2026-01-02T03:04:05Z
compose=backup-vault-compose_$tag.tar.gz
kustomize=backup-vault-kustomize_$tag

failures=0
fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

digest() { echo "sha256:$(printf '%s' "$1" | sha256sum | cut -d' ' -f1)"; }
d_backupd=$(digest backupd) d_web=$(digest web) d_other=$(digest other)

printf '%s\n' "ghcr.io/knoppiks/opencloud-backup-vault-web@$d_web" \
	"ghcr.io/knoppiks/opencloud-backupd@$d_backupd" >"$work/images.txt"

build() {
	"$scripts/deploy-archives.sh" "$1" "$version" "$2" "$date" 2>"$work/build.err"
}

# --- the stamp ----------------------------------------------------------------
build "$work/images.txt" "$work/assets" || fail "deploy-archives refused: $(cat "$work/build.err")"
cp "$work/images.txt" "$work/assets/"

"$scripts/check-deploy-archives.sh" "$work/assets" "$version" 2>"$work/check.err" ||
	fail "check-deploy-archives refused what deploy-archives built:"$'\n'"$(cat "$work/check.err")"

build "$work/images.txt" "$work/again" || fail "second build refused"
for f in "$compose" "$kustomize.tar.gz"; do
	cmp -s "$work/assets/$f" "$work/again/$f" || fail "$f does not rebuild bit-identically"
done

# The compose add-on keeps BACKUP_VERSION as an override of the release.
line=$(tar -xzOf "$work/assets/$compose" backup/backup.yml | grep -F ':-ghcr.io/knoppiks/opencloud-backupd}')
want="image: \${BACKUP_DOCKER_IMAGE:-ghcr.io/knoppiks/opencloud-backupd}:\${BACKUP_VERSION:-$tag@$d_backupd}"
[ "${line#"${line%%[![:space:]]*}"}" = "$want" ] || fail "compose image line is '$line', want '$want'"

# The image list it is given must name each image once, by digest.
refused_build() {
	local what=$1
	shift
	printf '%s\n' "$@" >"$work/bad-images.txt"
	if build "$work/bad-images.txt" "$work/bad-out"; then
		fail "deploy-archives with $what: accepted, want refused"
	fi
}
refused_build "the web image missing" "ghcr.io/knoppiks/opencloud-backupd@$d_backupd"
refused_build "an image twice" "ghcr.io/knoppiks/opencloud-backupd@$d_backupd" \
	"ghcr.io/knoppiks/opencloud-backupd@$d_other" "ghcr.io/knoppiks/opencloud-backup-vault-web@$d_web"
refused_build "an image by tag" "ghcr.io/knoppiks/opencloud-backupd:1.2.3" \
	"ghcr.io/knoppiks/opencloud-backup-vault-web@$d_web"

# --- the check refuses ----------------------------------------------------------
# repack DIR ARCHIVE: DIR's files into ARCHIVE, the way deploy-archives does.
repack() {
	(cd "$1" && find . -type f -printf '%P\n' | LC_ALL=C sort) >"$work/files"
	tar -C "$1" --no-recursion --mtime="$date" --owner=0 --group=0 --numeric-owner \
		-T "$work/files" -cf - | gzip -n >"$2"
}

# damaged WHICH DAMAGE: a copy of the assets, with DAMAGE run in the
# unpacked compose or Kustomize archive (repacked afterwards), or in the
# assets directory itself (assets). Prints the copy's assets directory.
damaged() {
	local which=$1 damage=$2 p=$work/probe
	rm -rf "$p"
	mkdir -p "$p/unpacked"
	cp -R "$work/assets" "$p/assets"
	case $which in
	compose)
		tar -xzf "$p/assets/$compose" -C "$p/unpacked"
		(cd "$p/unpacked" && eval "$damage")
		repack "$p/unpacked" "$p/assets/$compose"
		;;
	kustomize)
		tar -xzf "$p/assets/$kustomize.tar.gz" -C "$p/unpacked"
		(cd "$p/unpacked/$kustomize" && eval "$damage")
		repack "$p/unpacked" "$p/assets/$kustomize.tar.gz"
		;;
	assets) (cd "$p/assets" && eval "$damage") ;;
	esac
	echo "$p/assets"
}

# probe WHAT WHICH DAMAGE: the check refuses the damaged copy.
probe() {
	local assets
	assets=$(damaged "$2" "$3")
	if "$scripts/check-deploy-archives.sh" "$assets" "$version" 2>/dev/null; then
		fail "check-deploy-archives with $1: accepted, want refused"
	fi
}

# A repack alone is accepted, or the probes below prove nothing.
for which in compose kustomize; do
	assets=$(damaged "$which" true)
	"$scripts/check-deploy-archives.sh" "$assets" "$version" 2>"$work/check.err" ||
		fail "check-deploy-archives refused the $which archive merely repacked:"$'\n'"$(cat "$work/check.err")"
done

# shellcheck disable=SC2016 # expanded by the probe, inside the copy
{
	probe "no compose archive" assets "rm $compose"
	probe "no Kustomize archive" assets "rm $kustomize.tar.gz"
	probe "no images.txt" assets 'rm images.txt'
	probe "another digest in images.txt" assets "sed -i 's/$d_backupd/$d_other/' images.txt"
	probe "an archive of another version" assets "mv $compose backup-vault-compose_9.9.9.tar.gz"

	probe "the compose image unstamped" compose \
		"sed -i -E 's/BACKUP_VERSION:-[^}]+/BACKUP_VERSION:?unset/' backup/backup.yml"
	probe "the compose image at another digest" compose "sed -i 's/$d_web/$d_other/' backup/backup.yml"
	probe "setup.sh not executable" compose 'chmod 0644 backup/setup.sh'
	probe "a file git does not track" compose 'echo x >backup/.env'
	probe "a file outside the add-on's directories" compose 'echo x >docker-compose.yml'
	probe "no LICENSE" compose 'rm backup/LICENSE'
	probe "no traefik route" compose 'rm traefik/backup.yml'

	probe "the base unstamped" kustomize "cp '$root/deploy/kustomize/base/kustomization.yaml' base/"
	probe "the Component unstamped" kustomize \
		"cp '$root/deploy/kustomize/components/web-extension/kustomization.yaml' components/web-extension/"
	probe "the base without its digest" kustomize "sed -i '/^  digest:/d' base/kustomization.yaml"
	probe "the base's version label unstamped" kustomize \
		"sed -i -E 's/(app.kubernetes.io\\/version:).*/\\1 unreleased/' base/kustomization.yaml"
	probe "the base's version label another" kustomize \
		"sed -i -E 's/(app.kubernetes.io\\/version:).*/\\1 \"9.9.9\"/' base/kustomization.yaml"
	probe "a Deployment naming an image images: does not set" kustomize \
		"sed -i 's|image: ghcr.io/knoppiks/opencloud-backupd\$|image: ghcr.io/knoppiks/opencloud-backup-old:1.2.3|' base/deployment-backupd.yaml"
	probe "a base that does not render" kustomize 'echo "resources: [missing.yaml]" >>base/kustomization.yaml'
	probe "an executable manifest" kustomize 'chmod 0755 base/service-backupd.yaml'
}

if [ "$failures" -gt 0 ]; then
	echo "deploy-archives-test: $failures failure(s)" >&2
	exit 1
fi
echo "deploy-archives-test: ok" >&2
