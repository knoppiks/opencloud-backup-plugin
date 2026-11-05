#!/usr/bin/env bash
# Checks the deployment add-on archives a release (or `make
# release-snapshot`) built (#160), before anything is signed:
#
#   - both are there, holding exactly the files git tracks under
#     deploy/compose and deploy/kustomize plus LICENSE, owned by root, with
#     setup.sh the only executable; the compose add-on unpacks into backup/,
#     traefik/ and external-proxy/ only;
#   - every image of this project they name is the release's, by the digest
#     the release's images.txt lists: the compose add-on's defaults, the
#     kustomizations' `images:`, and what the base and the web-extension
#     Component render to (with app.kubernetes.io/version);
#   - nothing of the repository's placeholders is left.
#
# Offline: the registry has no tags for the release yet. release-publish.yml
# checks the archives against the registry once it has
# (scripts/check-deploy-images.sh).
#
# Usage: check-deploy-archives.sh ASSETS_DIR VERSION
#   ASSETS_DIR  the release assets: the two archives and images.txt
#   VERSION     the stamp, with its leading v
# KUSTOMIZE names the kustomize binary (default: kustomize).
set -euo pipefail

[ $# -eq 2 ] || {
	echo "usage: $0 ASSETS_DIR VERSION" >&2
	exit 2
}
assets=$(realpath "$1") version=$2
tag=${version#v}
root=$(git rev-parse --show-toplevel)
scripts=$root/scripts
kustomize=${KUSTOMIZE:-kustomize}

BACKUPD=ghcr.io/knoppiks/opencloud-backupd
WEB=ghcr.io/knoppiks/opencloud-backup-vault-web

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "check-deploy-archives: $*" >&2
	failures=$((failures + 1))
}

# want_ref REPO: REPO at the release, by the digest images.txt lists.
want_ref() {
	local digest
	digest=$(grep -E "^${1//./\\.}@sha256:[0-9a-f]{64}$" "$assets/images.txt" 2>/dev/null | head -1 | cut -d@ -f2)
	echo "$1:$tag@${digest:-<not in images.txt>}"
}

# listing ARCHIVE: MODE OWNER NAME per file, sorted.
listing() {
	tar --numeric-owner -tvzf "$1" | awk '$1 !~ /^d/ { print $1, $2, $NF }' | LC_ALL=C sort -k3
}

# want_listing SRC PREFIX LICENSE_AT: what the archive of SRC must hold.
want_listing() {
	local mode path rel perm
	{
		while IFS=$'\t' read -r mode path; do
			rel=${path#"$1"/}
			perm=-rw-r--r--
			[ "${mode%% *}" = 100755 ] && perm=-rwxr-xr-x
			echo "$perm 0/0 $2$rel"
		done < <(git -C "$root" ls-files -s -- "$1")
		echo "-rw-r--r-- 0/0 $2$3"
	} | LC_ALL=C sort -k3
}

# check_listing ARCHIVE SRC PREFIX LICENSE_AT
check_listing() {
	local got want
	got=$(listing "$1")
	want=$(want_listing "$2" "$3" "$4")
	[ "$got" = "$want" ] ||
		fail "${1##*/} holds"$'\n'"$got"$'\n'"want"$'\n'"$want"$'\n'"(diff: $(diff <(echo "$want") <(echo "$got") | grep '^[<>]' | tr '\n' ' '))"
}

# check_refs WHAT REPOS FILE...: FILE... name the images of ours in REPOS
# (space-separated), each of them, and each at the release by its digest.
check_refs() {
	local what=$1 repos=$2 file line ref refs found want
	shift 2
	refs=$("$scripts/deploy-images.sh" "$@") || {
		fail "$what: cannot read $*"
		return
	}
	while IFS=$'\t' read -r file line ref; do
		[ -n "$file" ] || continue
		want=$(want_ref "${ref%%:*}")
		[ "$ref" = "$want" ] || fail "$what: ${file#"$work"/}:$line names $ref, want $want"
	done <<<"$refs"
	found=$(cut -f3 <<<"$refs" | sed 's/:.*//' | LC_ALL=C sort -u | paste -sd' ')
	want=$(tr ' ' '\n' <<<"$repos" | LC_ALL=C sort -u | paste -sd' ')
	[ "$found" = "$want" ] || fail "$what names images of '$found', want '$want'"
}

# placeholders DIR: the repository's placeholders left in DIR's YAML,
# outside comments.
placeholders() {
	local left
	left=$(grep -rnE --include='*.yml' --include='*.yaml' 'BACKUP_VERSION:\?|unreleased' "$1" |
		grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true)
	[ -z "$left" ] || fail "placeholders left:"$'\n'"${left//"$work"\//}"
}

[ -f "$assets/images.txt" ] || fail "missing images.txt"

# --- compose ------------------------------------------------------------------
compose=backup-vault-compose_$tag.tar.gz
if [ ! -f "$assets/$compose" ]; then
	fail "missing $compose"
else
	check_listing "$assets/$compose" deploy/compose "" backup/LICENSE
	outside=$(tar -tzf "$assets/$compose" | grep -vE '^(backup|traefik|external-proxy)/[^/]' || true)
	[ -z "$outside" ] || fail "$compose unpacks outside backup/, traefik/, external-proxy/: $outside"
	mkdir -p "$work/compose"
	tar -xzf "$assets/$compose" -C "$work/compose"
	if [ -f "$work/compose/backup/backup.yml" ]; then
		check_refs "$compose" "$BACKUPD $WEB" "$work/compose/backup/backup.yml"
	fi
	placeholders "$work/compose"
fi

# --- Kustomize ----------------------------------------------------------------
name=backup-vault-kustomize_$tag
if [ ! -f "$assets/$name.tar.gz" ]; then
	fail "missing $name.tar.gz"
else
	check_listing "$assets/$name.tar.gz" deploy/kustomize "$name/" LICENSE
	mkdir -p "$work/kustomize"
	tar -xzf "$assets/$name.tar.gz" -C "$work/kustomize"
	k=$work/kustomize/$name
	placeholders "$k"
	if [ -d "$k/base" ] && [ -d "$k/components/web-extension" ]; then
		check_refs "$name, base" "$BACKUPD" "$k/base/kustomization.yaml"
		check_refs "$name, web extension" "$WEB" "$k/components/web-extension/kustomization.yaml"

		# What a cluster gets: the base, and the Component on a stand-in
		# for OpenCloud's Deployment.
		if "$kustomize" build "$k/base" >"$work/base.yaml" 2>"$work/err"; then
			check_refs "$name, base rendered" "$BACKUPD" "$work/base.yaml"
			versions=$(yq -N -r '.metadata.labels["app.kubernetes.io/version"]' <"$work/base.yaml" | sort -u)
			[ "$versions" = "$tag" ] || fail "$name, base rendered: app.kubernetes.io/version is $(echo "$versions" | paste -sd,), want $tag"
		else
			fail "$name: kustomize build base failed: $(cat "$work/err")"
		fi
		mkdir -p "$work/web"
		cp "$root/test/kustomize/web-extension/opencloud.yaml" "$work/web/"
		printf 'resources:\n- opencloud.yaml\ncomponents:\n- ../kustomize/%s/components/web-extension\n' "$name" >"$work/web/kustomization.yaml"
		if "$kustomize" build "$work/web" >"$work/web.yaml" 2>"$work/err"; then
			check_refs "$name, web extension rendered" "$WEB" "$work/web.yaml"
		else
			fail "$name: kustomize build of the web-extension Component failed: $(cat "$work/err")"
		fi
	fi
fi

if [ "$failures" -gt 0 ]; then
	echo "check-deploy-archives: $failures problem(s)" >&2
	exit 1
fi
echo "check-deploy-archives: both add-ons for $version name the release's images" >&2
