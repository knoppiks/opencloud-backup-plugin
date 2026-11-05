#!/usr/bin/env bash
# Lists the images of this project that deployment files name (#110, #160),
# one per line as FILE<TAB>LINE<TAB>REF, in the form a deployment pulls:
#
#   image: ghcr.io/…:TAG[@sha256:…]            a manifest, or rendered output
#   image: ${VAR:-ghcr.io/…}:${BACKUP_VERSION:-TAG[@sha256:…]}
#                                              the compose add-on: REF is the
#                                              defaults, ghcr.io/…:TAG[@…]
#   images: [{name, newTag, digest}]           a kustomization.yaml:
#                                              name:newTag[@digest]
#
# What the release has not stamped stays visible as such: the compose add-on
# from the repository gives ghcr.io/…:${BACKUP_VERSION:?…}, the Kustomize
# base ghcr.io/…:unreleased. Images of others are not listed.
#
# Usage: deploy-images.sh FILE...
# Needs yq (mikefarah v4) for kustomization files.
set -euo pipefail

[ $# -gt 0 ] || {
	echo "usage: $0 FILE..." >&2
	exit 2
}

ours='ghcr\.io/knoppiks/opencloud-backup[a-z-]*'

# image_lines FILE: every `image:` of ours, as LINE<TAB>VALUE.
image_lines() {
	grep -nE "^[[:space:]]*-?[[:space:]]*image:[[:space:]]*[\"']?(\\$\\{[A-Z_]+:-)?$ours" "$1" |
		sed -E 's/^([0-9]+):[^:]*:[[:space:]]*["'\'']?/\1\t/; s/["'\'']?[[:space:]]*(#.*)?$//'
}

# compose_default VALUE: a compose image's defaults, or VALUE as it is.
compose_default() {
	sed -E 's/^\$\{[A-Z_]+:-([^}]+)\}:\$\{BACKUP_VERSION:-([^}]+)\}$/\1:\2/; s/^\$\{[A-Z_]+:-([^}]+)\}:/\1:/' <<<"$1"
}

# kustomization_images FILE: the `images:` entries of ours, as LINE<TAB>REF.
# yq reads stdin: a confined yq (snap) cannot open every path. Its `line`
# does not count a leading comment, so the line is the entry's `name:`.
kustomization_images() {
	local entries name ref line
	entries=$(yq -r '.images[]? | select(.name | test("^ghcr\.io/knoppiks/opencloud-backup")) |
		.name + "\t" + .name + ":" + (.newTag // "") + "@" + (.digest // "")' <"$1") || {
		echo "deploy-images: cannot read $1" >&2
		return 1
	}
	[ -n "$entries" ] || return 0
	while IFS=$'\t' read -r name ref; do
		line=$(grep -nE -m1 "^[[:space:]]*-?[[:space:]]*name:[[:space:]]*[\"']?${name//./\\.}[\"']?[[:space:]]*$" "$1" | cut -d: -f1)
		printf '%s\t%s\n' "${line:-0}" "${ref%@}"
	done <<<"$entries"
}

for file in "$@"; do
	[ -f "$file" ] && [ -r "$file" ] || {
		echo "deploy-images: cannot read $file" >&2
		exit 1
	}
	case ${file##*/} in
	kustomization.yaml | kustomization.yml | Kustomization)
		kustomization_images "$file" | sed "s|^|$file\t|"
		;;
	*)
		while IFS=$'\t' read -r line value; do
			printf '%s\t%s\t%s\n' "$file" "$line" "$(compose_default "$value")"
		done < <(image_lines "$file" || true)
		;;
	esac
done
