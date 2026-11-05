#!/usr/bin/env bash
# Builds the deployment add-ons as release assets (phase-12, #160): the
# repository's copies, which name no release, stamped with the release's
# images by digest.
#
#   backup-vault-compose_<ver>.tar.gz    deploy/compose/, unpacked straight
#                                        into an opencloud-compose directory
#                                        (backup/, traefik/backup.yml,
#                                        external-proxy/backup.yml), with
#                                        LICENSE under backup/
#   backup-vault-kustomize_<ver>.tar.gz  deploy/kustomize/ and LICENSE, in
#                                        backup-vault-kustomize_<ver>/
#
# The stamp, per image:
#
#   compose    ${BACKUP_VERSION:?…}  ->  ${BACKUP_VERSION:-<ver>@sha256:…}
#   Kustomize  newTag: unreleased    ->  newTag: <ver>, digest: sha256:…
#              app.kubernetes.io/version: unreleased  ->  <ver>
#
# Only the files git tracks go in, with git's modes (setup.sh stays
# executable); owner, order and times are fixed (DATE), so the same commit
# and images give the same archives. scripts/check-deploy-archives.sh checks
# the result.
#
# Usage: deploy-archives.sh IMAGES_FILE VERSION OUT_DIR DATE
#   IMAGES_FILE  the release's image list (images.txt): image@sha256:digest
#                per line, this project's two images
#   VERSION      the release's version, with its leading v
#   OUT_DIR      where the archives go (the release assets)
#   DATE         the archives' file times (the commit's)
set -euo pipefail

[ $# -eq 4 ] || {
	echo "usage: $0 IMAGES_FILE VERSION OUT_DIR DATE" >&2
	exit 2
}
images=$1 version=$2 out=$3 date=$4
tag=${version#v}
root=$(git rev-parse --show-toplevel)

BACKUPD=ghcr.io/knoppiks/opencloud-backupd
WEB=ghcr.io/knoppiks/opencloud-backup-vault-web

die() {
	echo "deploy-archives: $*" >&2
	exit 1
}

# digest_of REPO: the digest images.txt lists for REPO, exactly once.
digest_of() {
	local refs
	refs=$(grep -E "^${1//./\\.}@sha256:[0-9a-f]{64}$" "$images" || true)
	[ "$(grep -c . <<<"$refs")" -eq 1 ] || die "$images must list $1@sha256:<digest> once"
	echo "${refs#*@}"
}

[ -f "$images" ] || die "$images not found"
[[ $tag =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || die "$version is not a version"
backupd_digest=$(digest_of "$BACKUPD")
web_digest=$(digest_of "$WEB")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# copy_tracked SRC DEST: the files git tracks under SRC, with their mode.
copy_tracked() {
	local mode path rel
	mkdir -p "$2"
	while IFS=$'\t' read -r mode path; do
		mode=${mode%% *}
		rel=${path#"$1"/}
		mkdir -p "$2/$(dirname "$rel")"
		cp "$root/$path" "$2/$rel"
		case $mode in
		100755) chmod 0755 "$2/$rel" ;;
		*) chmod 0644 "$2/$rel" ;;
		esac
	done < <(git -C "$root" ls-files -s -- "$1")
	[ -n "$(find "$2" -type f)" ] || die "git tracks nothing under $1"
}

# stamp_compose FILE REPO DIGEST: the `${BACKUP_VERSION:?…}` of REPO's
# image becomes the release, with its digest.
stamp_compose() {
	sed -E -i "\\|:-${2//./\\.}\\}:| s|\\\$\\{BACKUP_VERSION:\\?[^}]*\\}|\${BACKUP_VERSION:-$tag@$3}|" "$1"
}

# stamp_kustomization FILE REPO DIGEST: the `newTag: unreleased` of REPO's
# `images:` entry becomes the release, with its digest.
stamp_kustomization() {
	awk -v repo="$2" -v tag="$tag" -v digest="$3" '
		{ line = $0 }
		$0 ~ "^- name: " repo "$" { want = 1; print; next }
		want && /^[[:space:]]+newTag: unreleased$/ {
			indent = line; sub(/newTag:.*/, "", indent)
			print indent "newTag: " tag
			print indent "digest: " digest
			want = 0; next
		}
		/^[^[:space:]]/ || /^- / { want = 0 }
		{ print }
	' "$1" >"$work/stamped"
	cat "$work/stamped" >"$1" # written over: the file keeps its mode
}

# unstamped DIR: what is left of the repository's placeholders in its YAML,
# outside comments. The READMEs describe them.
unstamped() {
	grep -rnE --include='*.yml' --include='*.yaml' 'BACKUP_VERSION:\?|unreleased' "$1" |
		grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true
}

# archive DIR TARBALL: the files under DIR, in a fixed order, owner and time.
archive() {
	(cd "$1" && find . -type f -printf '%P\n' | LC_ALL=C sort) >"$work/files"
	tar -C "$1" --no-recursion --mtime="$date" --owner=0 --group=0 --numeric-owner \
		-T "$work/files" -cf - | gzip -n >"$2"
}

mkdir -p "$out"

# --- compose ------------------------------------------------------------------
compose=$work/compose
copy_tracked deploy/compose "$compose"
stamp_compose "$compose/backup/backup.yml" "$BACKUPD" "$backupd_digest"
stamp_compose "$compose/backup/backup.yml" "$WEB" "$web_digest"
install -m 0644 "$root/LICENSE" "$compose/backup/LICENSE"
left=$(unstamped "$compose")
[ -z "$left" ] || die "the compose add-on is not stamped everywhere:"$'\n'"$left"
archive "$compose" "$out/backup-vault-compose_$tag.tar.gz"

# --- Kustomize ----------------------------------------------------------------
name=backup-vault-kustomize_$tag
kustomize=$work/kustomize/$name
copy_tracked deploy/kustomize "$kustomize"
stamp_kustomization "$kustomize/base/kustomization.yaml" "$BACKUPD" "$backupd_digest"
stamp_kustomization "$kustomize/components/web-extension/kustomization.yaml" "$WEB" "$web_digest"
sed -i -E "s/^([[:space:]]+app\\.kubernetes\\.io\\/version:) unreleased$/\\1 \"$tag\"/" \
	"$kustomize/base/kustomization.yaml"
install -m 0644 "$root/LICENSE" "$kustomize/LICENSE"
left=$(unstamped "$kustomize")
[ -z "$left" ] || die "the Kustomize add-on is not stamped everywhere:"$'\n'"$left"
archive "$work/kustomize" "$out/$name.tar.gz"

echo "deploy-archives: backup-vault-compose_$tag.tar.gz, $name.tar.gz" >&2
