#!/usr/bin/env bash
# Prints the issues a release ships, as a JSON array (#112):
#
#   [{"number", "title", "url", "labels": [names], "milestone"}, …]
#
# sorted by number. "Ships" means: done by work merged between the previous
# final release (vX.Y.Z, no -rc) and TAG. For the first release that is all
# history up to TAG. A release candidate's range starts at the previous final
# release too, so every candidate of a version lists everything that version
# will ship.
#
# An issue belongs to that work if any of these names it:
#
#   - a pull request merged in the range closes it (GitHub's closing
#     references: "Closes #N" in the description);
#   - a pull request merged in the range came from a branch named after it
#     (`N-slug`, the branch convention in AGENTS.md);
#   - a commit in the range closes it ("Closes/Fixes/Resolves #N").
#
# and it is closed as completed. Issues closed as not planned, or labelled
# status/Invalid, status/Duplicate or status/Wontfix, did not ship. Issues of
# other repositories are left out.
#
# Pull requests are found in the first-parent history: merge commits
# ("Merge pull request #N from owner/branch") and squash merges
# ("Subject (#N)").
#
# Usage: release-issues.sh TAG
# Needs git (with tags), jq, and the GitHub CLI (GH names it; tests set it;
# GH_REPO selects the repository).
set -euo pipefail

GH=${GH:-gh}

[ $# -eq 1 ] || {
	echo "usage: $0 TAG" >&2
	exit 2
}
tag=$1

git rev-parse -q --verify "$tag^{commit}" >/dev/null || {
	echo "release-issues: no such tag: $tag" >&2
	exit 1
}

# The newest final release before TAG, if any.
range=$tag
if prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' "$tag^" 2>/dev/null); then
	range=$prev..$tag
fi
echo "release-issues: $tag ships what was merged in $range" >&2

first_parent=$(git log --first-parent --format=%s "$range")
prs=$(sed -nE 's/^Merge pull request #([0-9]+) .*/\1/p; s/.*\(#([0-9]+)\)$/\1/p' <<<"$first_parent" | sort -un)

candidates=()
add() { candidates+=("$@"); }

# Branches named after their issue.
while IFS= read -r n; do
	[ -n "$n" ] && add "$n"
done < <(sed -nE 's|^Merge pull request #[0-9]+ from [^/ ]+/([0-9]+)-.*|\1|p' <<<"$first_parent")

# Closing keywords in any commit of the range.
while IFS= read -r n; do
	[ -n "$n" ] && add "$n"
done < <(git log --format=%B "$range" |
	grep -oiE '\b(close[sd]?|fix(e[sd])?|resolve[sd]?):? +#[0-9]+' | grep -oE '[0-9]+$' || true)

repo_url=$("$GH" repo view --json url | jq -r .url)

# Closing references of the merged pull requests.
for pr in $prs; do
	while IFS= read -r n; do
		[ -n "$n" ] && add "$n"
	done < <("$GH" pr view "$pr" --json closingIssuesReferences |
		jq -r --arg prefix "$repo_url/issues/" \
			'.closingIssuesReferences[] | select(.url | startswith($prefix)) | .number')
done

{
	for n in $(printf '%s\n' ${candidates[@]+"${candidates[@]}"} | sort -un); do
		# A number may name a pull request rather than an issue.
		"$GH" issue view "$n" --json number,title,url,labels,milestone,state,stateReason 2>/dev/null || continue
	done
} | jq -s '
	map(select(.state == "CLOSED" and .stateReason == "COMPLETED"
		and ([.labels[].name] | any(. == "status/Invalid" or . == "status/Duplicate" or . == "status/Wontfix") | not))
	  | {number, title, url, labels: [.labels[].name], milestone: (.milestone.title // null)})
	| sort_by(.number)'
