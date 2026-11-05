#!/usr/bin/env bash
# Tests scripts/check-deploy-images.sh against a fake registry (#110): what
# it accepts, and that it refuses each way a manifest can name an image that
# a deployment cannot pull, in each form scripts/deploy-images.sh reads
# (manifests, the compose add-on, kustomization `images:`; #160). No network.
#
# Usage: deploy-images-test.sh
set -euo pipefail

root=$(git rev-parse --show-toplevel)
check=$root/scripts/check-deploy-images.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

d1=sha256:$(printf 1 | sha256sum | cut -d' ' -f1)
d2=sha256:$(printf 2 | sha256sum | cut -d' ' -f1)

# The fake registry: two published tags.
cat >"$work/resolve" <<EOF
#!/usr/bin/env bash
case \$1 in
ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4) echo $d1 ;;
ghcr.io/knoppiks/opencloud-backup-vault-web:0.1.0-rc.4) echo $d2 ;;
*) echo "not found: \$1" >&2; exit 1 ;;
esac
EOF
chmod +x "$work/resolve"
export RESOLVE=$work/resolve

failures=0
fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

# manifest NAME CONTENT: writes a manifest and prints its path.
manifest() {
	printf '%s\n' "$2" >"$work/$1.yaml"
	echo "$work/$1.yaml"
}

accepts() {
	"$check" "$2" >/dev/null 2>&1 || fail "$1: refused, want accepted"
}

refuses() {
	if "$check" "$2" >/dev/null 2>&1; then
		fail "$1: accepted, want refused"
	fi
}

accepts "published tag" "$(manifest ok "        image: ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4")"
accepts "quoted, list item, comment" "$(manifest quoted "  - image: \"ghcr.io/knoppiks/opencloud-backup-vault-web:0.1.0-rc.4\"  # pinned")"
accepts "tag and its digest" "$(manifest digest "  image: ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4@$d1")"
accepts "other images are not ours to check" "$(manifest other "  image: ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4
  image: dxflrs/garage:v2.3.0")"

# The compose add-on, as a release stamps it and as the repository has it.
accepts "compose, stamped" "$(manifest compose "    image: \${BACKUP_DOCKER_IMAGE:-ghcr.io/knoppiks/opencloud-backupd}:\${BACKUP_VERSION:-0.1.0-rc.4@$d1}")"
refuses "compose, stamped with another digest" "$(manifest composewrong "    image: \${BACKUP_DOCKER_IMAGE:-ghcr.io/knoppiks/opencloud-backupd}:\${BACKUP_VERSION:-0.1.0-rc.4@$d2}")"
refuses "compose, unstamped" "$(manifest composeraw "    image: \${BACKUP_DOCKER_IMAGE:-ghcr.io/knoppiks/opencloud-backupd}:\${BACKUP_VERSION:?set BACKUP_VERSION}")"

# kustomization.yaml `images:`, likewise.
kustomization() {
	mkdir -p "$work/$1"
	printf '%s\n' "$2" >"$work/$1/kustomization.yaml"
	echo "$work/$1/kustomization.yaml"
}
accepts "kustomization, stamped" "$(kustomization kok "# a leading comment
images:
- name: ghcr.io/knoppiks/opencloud-backupd
  newTag: 0.1.0-rc.4
  digest: $d1
- name: dxflrs/garage
  newTag: v2.3.0")"
refuses "kustomization, unreleased" "$(kustomization kraw "images:
- name: ghcr.io/knoppiks/opencloud-backupd
  newTag: unreleased")"
refuses "kustomization, no tag" "$(kustomization knotag "images:
- name: ghcr.io/knoppiks/opencloud-backupd
  digest: $d1")"
refuses "kustomization, another digest" "$(kustomization kwrong "images:
- name: ghcr.io/knoppiks/opencloud-backup-vault-web
  newTag: 0.1.0-rc.4
  digest: $d1")"
refuses "kustomization, unreadable" "$(kustomization kbad "images: [")"

# deploy-images.sh names the line of each image, for the messages.
got=$("$root/scripts/deploy-images.sh" "$work/kok/kustomization.yaml" "$work/compose.yaml" | cut -f2,3)
want=$(printf '%s\t%s\n' 3 "ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4@$d1" 1 "ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4@$d1")
[ "$got" = "$want" ] || fail "deploy-images.sh printed"$'\n'"$got"$'\n'"want"$'\n'"$want"

refuses "a file that does not exist" "$work/nonexistent.yaml"
refuses "unpublished version" "$(manifest missing "  image: ghcr.io/knoppiks/opencloud-backupd:0.1.0")"
refuses "latest" "$(manifest latest "  image: ghcr.io/knoppiks/opencloud-backupd:latest")"
refuses "no tag" "$(manifest notag "  image: ghcr.io/knoppiks/opencloud-backupd")"
refuses "minor tag (moves)" "$(manifest minor "  image: ghcr.io/knoppiks/opencloud-backupd:0.1")"
refuses "digest of another image" "$(manifest wrongdigest "  image: ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4@$d2")"
refuses "no image of ours at all" "$(manifest none "  image: dxflrs/garage:v2.3.0")"
refuses "one good, one missing" "$(manifest mixed "  image: ghcr.io/knoppiks/opencloud-backupd:0.1.0-rc.4
  image: ghcr.io/knoppiks/opencloud-backup-vault-web:0.1.0")"

if [ "$failures" -gt 0 ]; then
	echo "deploy-images-test: $failures failure(s)" >&2
	exit 1
fi
echo "deploy-images-test: ok" >&2
