#!/usr/bin/env bash
# Fails unless the draft release of TAG carries the notes it was given
# (#112). GoReleaser passed the notes nowhere when its changelog step was
# disabled, and the drafts of v0.1.0-rc.4 and rc.5 got an empty body without
# any job failing.
#
# Compares whitespace-normalised text, because GitHub may adjust line
# endings and trailing whitespace.
#
# Usage: check-release-notes.sh TAG NOTES_FILE
# GH names the GitHub CLI (tests set it); GH_REPO selects the repository.
set -euo pipefail

GH=${GH:-gh}

[ $# -eq 2 ] || {
	echo "usage: $0 TAG NOTES_FILE" >&2
	exit 2
}
tag=$1 notes=$2

normalise() { tr -s '[:space:]' ' ' | sed 's/^ //; s/ $//'; }

want=$(normalise <"$notes")
[ -n "$want" ] || {
	echo "check-release-notes: $notes is empty" >&2
	exit 1
}

# A draft is not found by tag name (`gh release view` cannot see it), so the
# list is searched. A tag has at most one release from the workflow.
body=$("$GH" api "repos/{owner}/{repo}/releases" --paginate \
	--jq ".[] | select(.tag_name == \"$tag\" and .draft) | .body")
got=$(normalise <<<"$body")

if [ "$got" != "$want" ]; then
	echo "::error::the draft release $tag does not carry the release notes ($(wc -c <<<"$body") bytes, want $(wc -c <"$notes"))"
	exit 1
fi
echo "check-release-notes: the draft $tag carries the notes ($(wc -c <"$notes") bytes)" >&2
