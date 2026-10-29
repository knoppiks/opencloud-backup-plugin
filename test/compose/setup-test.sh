#!/usr/bin/env bash
# Tests deploy/compose/backup/setup.sh (#157) against a fake docker: what it
# writes into .env, what it refuses, that it never replaces a value or
# creates a second state Space, and that no secret reaches its output.
# No Docker needed; the run against opencloud-compose itself is #158's.
#
# Usage: setup-test.sh
set -euo pipefail

root=$(git rev-parse --show-toplevel)
addon=$root/deploy/compose
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

# Values the fake OpenCloud hands out, generated so none is a literal here.
sa_id=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')
sa_secret=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
space_id="storage-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')\$space!node"

# The fake docker: records every call, answers what setup.sh asks.
# FAKE_PROVISION_FAILURES: how many provision calls fail as "not up yet".
# FAKE_CREATED_UNUSABLE: provisioning reports a created but unusable Space.
# FAKE_CONFIG_ERROR: what `compose config` refuses with.
mkdir "$work/bin"
cat >"$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_LOG"
[ "$1" = compose ] || exit 1
shift
case "$1" in
version) echo "Docker Compose version v5" ;;
config)
	if [ -n "${FAKE_CONFIG_ERROR:-}" ]; then echo "$FAKE_CONFIG_ERROR" >&2; exit 1; fi ;;
ps) [ -f "$FAKE_STATE/running" ] && echo abc123 ;;
up) touch "$FAKE_STATE/running" ;;
exec)
	case "$*" in
	*service_account_id*) echo "    service_account_id: $FAKE_SA_ID" ;;
	*service_account_secret*) echo "    service_account_secret: \"$FAKE_SA_SECRET\"" ;;
	esac ;;
run)
	n=$(cat "$FAKE_STATE/provisions" 2>/dev/null || echo 0)
	echo $((n + 1)) >"$FAKE_STATE/provisions"
	if [ -n "${FAKE_CREATED_UNUSABLE:-}" ]; then
		echo '{"level":"ERROR","err":"the Space was created (id x) but is not usable as service state"}' >&2
		exit 1
	fi
	if [ "$n" -lt "${FAKE_PROVISION_FAILURES:-0}" ]; then
		echo '{"level":"ERROR","err":"rpc error: code = Unavailable"}' >&2
		exit 1
	fi
	echo '{"level":"INFO","msg":"state space created"}' >&2
	echo "$FAKE_SPACE_ID" ;;
esac
EOF
chmod +x "$work/bin/docker"

# csp DIR DIRECTIVE: OpenCloud's policy, with 'wasm-unsafe-eval' under
# DIRECTIVE only.
csp() {
	local q="'''"
	{
		echo "directives:"
		echo "  script-src:"
		echo "    - ${q}self${q}"
		[ "$2" != script-src ] || echo "    - ${q}wasm-unsafe-eval${q}"
		echo "    - ${q}unsafe-inline${q}"
		echo "  style-src:"
		echo "    - ${q}self${q}"
		[ "$2" != style-src ] || echo "    - ${q}wasm-unsafe-eval${q}"
		echo "  worker-src:"
		echo "    - ${q}self${q}"
	} >"$1/config/opencloud/csp.yaml"
}

# stack NAME COMPOSE_FILE: a fresh opencloud-compose directory with the
# add-on copied in and its .env.example appended. Prints its path.
stack() {
	local dir=$work/$1
	mkdir -p "$dir/config/opencloud" "$dir/traefik" "$dir/external-proxy" "$dir/idm" "$dir/state"
	cp -r "$addon/." "$dir/"
	# shellcheck disable=SC2016 # a Compose variable, for setup.sh to read, not the shell
	printf 'services:\n  opencloud:\n    image: x:${OC_DOCKER_TAG:-8.1.0}\n' >"$dir/docker-compose.yml"
	touch "$dir/traefik/opencloud.yml" "$dir/external-proxy/opencloud.yml" "$dir/idm/ldap-keycloak.yml"
	csp "$dir" script-src
	{
		echo "OC_DOMAIN=cloud.example"
		echo "COMPOSE_FILE=$2"
		cat "$addon/backup/.env.example"
	} >"$dir/.env"
	chmod 600 "$dir/.env"
	echo "$dir"
}

traefik_stack=docker-compose.yml:traefik/opencloud.yml:backup/backup.yml:traefik/backup.yml

# setup DIR [VAR=value…]: runs setup.sh in DIR; output in DIR/out, status
# in DIR/status.
setup() {
	local dir=$1
	shift
	(
		cd "$dir"
		env PATH="$work/bin:$PATH" FAKE_LOG="$dir/docker.log" FAKE_STATE="$dir/state" \
			FAKE_SA_ID="$sa_id" FAKE_SA_SECRET="$sa_secret" FAKE_SPACE_ID="$space_id" \
			PROVISION_PAUSE=0 PROVISION_ATTEMPTS=3 "$@" \
			backup/setup.sh >"$dir/out" 2>&1
	) && echo 0 >"$dir/status" || echo $? >"$dir/status"
}

value() { grep -E "^$2=" "$1/.env" | tail -n 1 | cut -d= -f2-; }

expect_status() {
	[ "$(cat "$1/status")" = "$2" ] || fail "$3: exit $(cat "$1/status"), want $2: $(cat "$1/out")"
}

no_secret_in_output() {
	local v
	for v in "$sa_secret" "$(value "$1" BACKUP_SRW_KEY | tr -d "'")" "$(value "$1" BACKUP_TW_KEY | tr -d "'")"; do
		[ -z "$v" ] && continue
		if grep -qF -- "$v" "$1/out"; then fail "$2: a secret is in the output"; fi
	done
}

# --- a first install --------------------------------------------------------

d=$(stack fresh "$traefik_stack")
setup "$d"
expect_status "$d" 0 "first install"
for name in BACKUP_SRW_KEY BACKUP_TW_KEY; do
	[[ $(value "$d" $name) =~ ^\'[A-Za-z0-9+/]{43}=\'$ ]] || fail "first install: $name is not a quoted base64 key"
done
[ "$(value "$d" BACKUP_SRW_KEY)" != "$(value "$d" BACKUP_TW_KEY)" ] || fail "first install: the two keys are the same"
[ "$(value "$d" BACKUP_OC_SERVICE_ACCOUNT_ID)" = "'$sa_id'" ] || fail "first install: service account id"
[ "$(value "$d" BACKUP_OC_SERVICE_ACCOUNT_SECRET)" = "'$sa_secret'" ] || fail "first install: service account secret (quotes stripped)"
[ "$(value "$d" BACKUP_STATE_SPACE_ID)" = "'$space_id'" ] || fail "first install: state Space id, single-quoted"
[ "$(grep -c '^BACKUP_SRW_KEY=' "$d/.env")" = 1 ] || fail "first install: the key's line is written once, in place"
[ "$(stat -c %a "$d/.env")" = 600 ] || fail "first install: .env lost its mode"
grep -qE '^compose up -d .*opencloud$' "$d/docker.log" || fail "first install: OpenCloud was not started"
grep -q 'Start everything' "$d/out" || fail "first install: no next steps"
grep -q 'Keep a copy' "$d/out" || fail "first install: no advice to keep the keys"
grep -q 'wasm-unsafe-eval' "$d/out" && fail "first install: CSP reported missing although it is there"
no_secret_in_output "$d" "first install"

# --- running it again changes nothing --------------------------------------

cp "$d/.env" "$work/before"
: >"$d/docker.log"
setup "$d"
expect_status "$d" 0 "second run"
cmp -s "$d/.env" "$work/before" || fail "second run: .env changed"
grep -q 'provision-state-space' "$d/docker.log" && fail "second run: a second state Space was created"
grep -q 'Keep a copy' "$d/out" && fail "second run: claims to have generated keys"
no_secret_in_output "$d" "second run"

# --- what is set is kept ----------------------------------------------------

d=$(stack keep "$traefik_stack")
own_key=$(head -c 32 /dev/urandom | base64)
sed -i "s|^BACKUP_SRW_KEY=.*|BACKUP_SRW_KEY='$own_key'|" "$d/.env"
setup "$d"
expect_status "$d" 0 "own key"
[ "$(value "$d" BACKUP_SRW_KEY)" = "'$own_key'" ] || fail "own key: replaced"
[ -n "$(value "$d" BACKUP_TW_KEY | tr -d "'")" ] || fail "own key: the other key was not generated"

d=$(stack configured-space "$traefik_stack")
sed -i "s|^BACKUP_STATE_SPACE_ID=.*|BACKUP_STATE_SPACE_ID='kept\$one!x'|" "$d/.env"
setup "$d"
expect_status "$d" 0 "configured state Space"
grep -q 'provision-state-space' "$d/docker.log" && fail "configured state Space: created another"
[ "$(value "$d" BACKUP_STATE_SPACE_ID)" = "'kept\$one!x'" ] || fail "configured state Space: replaced"

d=$(stack other-account "$traefik_stack")
sed -i "s|^BACKUP_OC_SERVICE_ACCOUNT_ID=.*|BACKUP_OC_SERVICE_ACCOUNT_ID='someone-else'|" "$d/.env"
setup "$d"
expect_status "$d" 1 "a service account that is not OpenCloud's"
[ "$(value "$d" BACKUP_OC_SERVICE_ACCOUNT_ID)" = "'someone-else'" ] || fail "other account: replaced"

# --- OpenCloud still starting -----------------------------------------------

d=$(stack slow "$traefik_stack")
setup "$d" FAKE_PROVISION_FAILURES=2
expect_status "$d" 0 "OpenCloud not ready at first"
[ "$(value "$d" BACKUP_STATE_SPACE_ID)" = "'$space_id'" ] || fail "slow OpenCloud: no state Space id"

d=$(stack down "$traefik_stack")
setup "$d" FAKE_PROVISION_FAILURES=9
expect_status "$d" 1 "OpenCloud never ready"
[ -z "$(value "$d" BACKUP_STATE_SPACE_ID)" ] || fail "OpenCloud never ready: an id was written"

# A Space that was created but is unusable is not created again.
d=$(stack unusable "$traefik_stack")
setup "$d" FAKE_CREATED_UNUSABLE=1
expect_status "$d" 1 "created but unusable"
[ "$(cat "$d/state/provisions")" = 1 ] || fail "created but unusable: tried $(cat "$d/state/provisions") times, want once"

# --- refused before anything is changed ------------------------------------

refused() {
	local name=$1 compose_file=$2 want=$3
	local d
	d=$(stack "$name" "$compose_file")
	cp "$d/.env" "$work/before"
	setup "$d"
	expect_status "$d" 1 "$name"
	grep -q -- "$want" "$d/out" || fail "$name: output lacks \"$want\": $(cat "$d/out")"
	cmp -s "$d/.env" "$work/before" || fail "$name: .env changed"
}

refused no-addon docker-compose.yml:traefik/opencloud.yml "lacks backup/backup.yml"
refused no-route docker-compose.yml:traefik/opencloud.yml:backup/backup.yml "lacks traefik/backup.yml"
refused wrong-route docker-compose.yml:external-proxy/opencloud.yml:backup/backup.yml:traefik/backup.yml "lacks external-proxy/backup.yml"
refused before-base backup/backup.yml:docker-compose.yml:traefik/opencloud.yml:traefik/backup.yml "after docker-compose.yml"
refused missing-file "$traefik_stack:nope.yml" "nope.yml, which does not exist"

d=$(stack unreadable "$traefik_stack")
setup "$d" FAKE_CONFIG_ERROR='required variable BACKUP_VERSION is missing a value'
expect_status "$d" 1 "compose cannot read the configuration"
grep -q BACKUP_VERSION "$d/out" || fail "unreadable configuration: compose's error not shown"

# --- what is reported -------------------------------------------------------

d=$(stack no-csp "$traefik_stack")
csp "$d" none
setup "$d"
expect_status "$d" 0 "CSP without WebAssembly"
grep -q "under script-src" "$d/out" || fail "CSP: the missing line is not reported"

d=$(stack wasm-elsewhere "$traefik_stack")
csp "$d" style-src
setup "$d"
grep -q "under script-src" "$d/out" || fail "CSP: wasm-unsafe-eval under style-src counted for script-src"

d=$(stack external docker-compose.yml:external-proxy/opencloud.yml:backup/backup.yml:external-proxy/backup.yml)
setup "$d"
expect_status "$d" 0 "own proxy"
grep -q '127.0.0.1:9280' "$d/out" || fail "own proxy: the route to add is not named"

d=$(stack keycloak "$traefik_stack:idm/ldap-keycloak.yml")
setup "$d"
grep -q '#162' "$d/out" || fail "keycloak: not warned"

d=$(stack readable "$traefik_stack")
chmod 644 "$d/.env"
setup "$d"
grep -q 'chmod 600' "$d/out" || fail "world-readable .env: not warned"
[ "$(stat -c %a "$d/.env")" = 644 ] || fail "world-readable .env: mode changed behind the operator's back"

d=$(stack new-opencloud "$traefik_stack")
echo "OC_DOCKER_TAG=99.0.0" >>"$d/.env"
setup "$d"
expect_status "$d" 0 "untested OpenCloud"
grep -q 'outside the versions' "$d/out" || fail "untested OpenCloud: not warned"

if [ $failures -gt 0 ]; then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo "setup.sh: all cases pass"
