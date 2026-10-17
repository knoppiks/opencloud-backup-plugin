#!/usr/bin/env bash
# Tests scripts/release-issues.sh (#112) in a throwaway repository with a
# fake GitHub CLI: which issues a release ships.
#
#   v1.0.0 ── PR #10 (closes #1) ── v1.1.0-rc.1 ── PR #11 (branch 2-…)
#          ── squash "… (#12)" (closes #3, and #4 not planned)
#          ── commit "Fixes #5" ── PR #13 (closes #6, status/Invalid)
#          ── commit "Refs #7" ── v1.1.0
#
# v1.1.0 ships #1, #2, #3, #5 (since v1.0.0, across its rc); v1.1.0-rc.1
# ships #1 only; v1.0.0, the first release, ships #9 (before it).
set -euo pipefail

root=$(git rev-parse --show-toplevel)
script=$root/scripts/release-issues.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

cat >"$work/gh" <<'EOF'
#!/usr/bin/env bash
url=https://github.com/o/r
issue() { # number state reason labels...
	local n=$1 state=$2 reason=$3
	shift 3
	jq -n --argjson n "$n" --arg s "$state" --arg r "$reason" --arg url "$url" '$ARGS.positional as $l |
		{number: $n, title: "Issue \($n)", url: "\($url)/issues/\($n)", state: $s, stateReason: $r,
		 labels: [$l[] | {name: .}], milestone: {title: "Phase 1"}}' --args "$@"
}
closes() { jq -n --arg url "$url" '{closingIssuesReferences: [$ARGS.positional[] | {number: (. | tonumber), url: "\($url)/issues/\(.)"}]}' --args "$@"; }
case "$1 $2 $3" in
"repo view --json") echo "{\"url\": \"$url\"}" ;;
"pr view 10") closes 1 ;;
"pr view 11") closes ;;
"pr view 12") jq -n --arg url "$url" '{closingIssuesReferences: [
	{number: 3, url: "\($url)/issues/3"}, {number: 4, url: "\($url)/issues/4"},
	{number: 8, url: "https://github.com/other/repo/issues/8"}]}' ;;
"pr view 13") closes 6 ;;
"pr view 20") closes 9 ;;
"issue view 1") issue 1 CLOSED COMPLETED kind/Bug ;;
"issue view 2") issue 2 CLOSED COMPLETED kind/Feature ;;
"issue view 3") issue 3 CLOSED COMPLETED kind/Enhancement ;;
"issue view 4") issue 4 CLOSED NOT_PLANNED kind/Bug ;;
"issue view 5") issue 5 CLOSED COMPLETED kind/Bug ;;
"issue view 6") issue 6 CLOSED COMPLETED status/Invalid ;;
"issue view 7") issue 7 OPEN "" kind/Bug ;;
"issue view 9") issue 9 CLOSED COMPLETED kind/Epic ;;
*) echo "fake gh: unexpected: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$work/gh"
export GH=$work/gh

repo=$work/repo
git init -q -b main "$repo"
g() { git -C "$repo" -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
commit() { g commit -q --allow-empty -m "$1"; }
# merge BRANCH PR: a merge commit as GitHub writes it.
merge() {
	g switch -q -c "$1"
	commit "Work on $1"
	g switch -q main
	g merge -q --no-ff "$1" -m "Merge pull request #$2 from o/$1"
}

commit "Initial"
merge 9-first-thing 20
g tag v1.0.0
merge 1-fix 10
g tag v1.1.0-rc.1
merge 2-feature 11
commit "Make It Better (#12)"
commit $'Fix It\n\nFixes #5'
merge 6-dry-run 13
commit $'Start Something\n\nRefs #7'
g tag v1.1.0

# numbers TAG: the issue numbers the script reports, space-separated.
numbers() {
	(cd "$repo" && "$script" "$1" 2>/dev/null) | jq -r '[.[].number] | map(tostring) | join(" ")'
}

expect() {
	local got
	got=$(numbers "$1") || {
		fail "$1: script failed"
		return
	}
	[ "$got" = "$2" ] || fail "$1: got '$got', want '$2'"
}

expect v1.1.0 "1 2 3 5"
expect v1.1.0-rc.1 "1"
expect v1.0.0 "9"

# The fields the notes and checks read.
got=$(cd "$repo" && "$script" v1.1.0-rc.1 2>/dev/null | jq -c '.[0]')
want='{"number":1,"title":"Issue 1","url":"https://github.com/o/r/issues/1","labels":["kind/Bug"],"milestone":"Phase 1"}'
[ "$got" = "$want" ] || fail "issue fields: got $got, want $want"

if (cd "$repo" && "$script" v9.9.9 >/dev/null 2>&1); then
	fail "accepted a tag that does not exist"
fi

if [ "$failures" -gt 0 ]; then
	echo "release-issues-test: $failures failure(s)" >&2
	exit 1
fi
echo "release-issues-test: ok" >&2
