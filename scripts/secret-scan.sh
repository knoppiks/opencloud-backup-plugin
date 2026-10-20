#!/usr/bin/env bash
# Scans for committed and about-to-be-committed secrets with gitleaks
# (AGENTS.md, "Secret scanning"; #125):
#
#   1. HEAD's whole history, which is what CI fails on: a secret once
#      committed stays in history, whatever a later commit does.
#   2. The working tree, so a secret is found before it is committed. Only
#      the files git would commit (tracked, or untracked and not ignored) are
#      scanned, from a copy: the checkout also holds node_modules/, dist/ and
#      the OpenCloud fixture's runtime data, which are not ours to judge and
#      would take a long time.
#
# The copy keeps the repository's relative paths, .gitleaks.toml and
# .gitleaksignore, so allowlists and fingerprints apply unchanged.
#
# -v names file, line, rule and fingerprint, so a failure is actionable from
# the log; --redact keeps the secret itself out of it.
#
# Usage: secret-scan.sh GITLEAKS
set -euo pipefail

[ $# -eq 1 ] || {
	echo "usage: $0 GITLEAKS" >&2
	exit 2
}
gitleaks=$(realpath "$1")
root=$(git rev-parse --show-toplevel)
tree="$root/dist/secret-scan"

cd "$root"
echo "== history (HEAD)"
# Scoped to HEAD's ancestry: gitleaks defaults to `git log --all`, and a CI
# checkout with every branch would judge this tree by other branches.
"$gitleaks" git . --log-opts="HEAD" --redact --no-banner -v

echo "== working tree"
rm -rf "$tree"
mkdir -p "$tree"
trap 'rm -rf "$tree"' EXIT
git ls-files -z --cached --others --exclude-standard |
	while IFS= read -r -d '' f; do
		# A tracked file deleted in the working tree has nothing to scan.
		[ -f "$f" ] || continue
		mkdir -p "$tree/$(dirname "$f")"
		cp "$f" "$tree/$f"
	done
(cd "$tree" && "$gitleaks" dir . --redact --no-banner -v)
