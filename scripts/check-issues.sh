#!/usr/bin/env bash
# Checks issues are ready to be shipped and described (#112): each carries
# exactly one kind/ label, which the release notes group by.
#
# Usage: check-issues.sh [-m] [-e] [-w] FILE
#   FILE  a JSON array of issues as scripts/release-issues.sh prints it
#         ({"number", "title", "labels": [names], "milestone"})
#   -m    each issue also needs a milestone (its phase)
#   -e    the array must not be empty (a pull request closes an issue)
#   -w    report problems as warnings and succeed (release candidates)
#
# Warnings and errors are printed as GitHub workflow commands, so they show
# up on the run's summary page.
set -euo pipefail

milestone=0 nonempty=0 warn=0
while getopts "mew" opt; do
	case $opt in
	m) milestone=1 ;;
	e) nonempty=1 ;;
	w) warn=1 ;;
	*)
		echo "usage: $0 [-m] [-e] [-w] FILE" >&2
		exit 2
		;;
	esac
done
shift $((OPTIND - 1))
[ $# -eq 1 ] || {
	echo "usage: $0 [-m] [-e] [-w] FILE" >&2
	exit 2
}
file=$1

level=error
[ "$warn" = 1 ] && level=warning

problems=$(jq -r --argjson milestone "$milestone" --argjson nonempty "$nonempty" '
	(if $nonempty == 1 and length == 0
	 then ["closes no issue: add \"Closes #<issue>\" to the pull request description"]
	 else [] end)
	+ [ .[] |
		([.labels[] | select(startswith("kind/"))] | length) as $kinds |
		(if $kinds != 1 then "#\(.number) \(.title): has \($kinds) kind/ labels, needs exactly one" else empty end),
		(if $milestone == 1 and .milestone == null then "#\(.number) \(.title): has no milestone (its phase)" else empty end)
	  ]
	| .[]' "$file")

if [ -z "$problems" ]; then
	echo "check-issues: $(jq length "$file") issue(s) ready" >&2
	exit 0
fi
while IFS= read -r p; do
	echo "::$level::$p"
done <<<"$problems"
[ "$warn" = 1 ]
