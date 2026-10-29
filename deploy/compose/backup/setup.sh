#!/usr/bin/env bash
# First-install helper for the Backup Vault add-on. Run it once from the
# opencloud-compose directory, after copying the add-on in and appending
# backup/.env.example to .env:
#
#   backup/setup.sh
#
# It writes into .env only what is still empty, and never replaces a value:
#
#   - BACKUP_SRW_KEY and BACKUP_TW_KEY, generated;
#   - BACKUP_OC_SERVICE_ACCOUNT_ID and _SECRET, from OpenCloud's generated
#     configuration;
#   - BACKUP_STATE_SPACE_ID, by creating the state Space, unless one is
#     already configured.
#
# It checks COMPOSE_FILE and OpenCloud's browser policy, and says what is
# left to do. It never prints a key or a secret. Running it again changes
# nothing that is already set.
set -euo pipefail

# The OpenCloud releases this version of Backup Vault was tested with. A test
# holds them to the project's pin file.
OPENCLOUD_OLDEST=7.3.0
OPENCLOUD_NEWEST=8.1.0

ENV_FILE=${ENV_FILE:-.env}
CSP_FILE=${CSP_FILE:-config/opencloud/csp.yaml}
# How long the state Space's creation is retried while OpenCloud starts.
PROVISION_ATTEMPTS=${PROVISION_ATTEMPTS:-24}
PROVISION_PAUSE=${PROVISION_PAUSE:-5}

problems=0
todo=()
keys_generated=0

say() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
problem() {
	printf 'problem: %s\n' "$*" >&2
	problems=$((problems + 1))
}
die() {
	printf 'setup: %s\n' "$*" >&2
	exit 1
}

# --- .env -------------------------------------------------------------------

# env_get NAME prints NAME's value in .env (the last assignment), without
# its quotes; nothing when unset or empty.
env_get() {
	local line
	line=$(grep -E "^$1=" "$ENV_FILE" | tail -n 1) || true
	line=${line#*=}
	line=${line%\"}
	line=${line#\"}
	line=${line%\'}
	line=${line#\'}
	printf '%s' "$line"
}

# env_set NAME VALUE sets NAME single-quoted in .env: on its line when there
# is one (empty, as .env.example leaves it), appended otherwise. The value is
# passed through the environment, never on a command line.
env_set() {
	case $2 in
	*"'"* | *$'\n'*) die "$1: the value cannot be written single-quoted" ;;
	esac
	local tmp
	tmp=$(mktemp "$ENV_FILE.XXXXXX")
	NAME=$1 VALUE=$2 awk '
		BEGIN { name = ENVIRON["NAME"]; line = name "='\''" ENVIRON["VALUE"] "'\''" }
		!done && index($0, name "=") == 1 { print line; done = 1; next }
		{ print }
		END { if (!done) print line }
	' "$ENV_FILE" >"$tmp"
	# Written over, not moved: .env keeps its owner and mode.
	cat "$tmp" >"$ENV_FILE"
	rm -f "$tmp"
}

# compose_before_secrets runs docker compose while .env may still lack what
# the add-on requires (the keys, the service account). Compose refuses to
# read the add-on's file without them, also for a command about another
# service. The commands run this way never create the backup service, so
# stand-ins for what is missing reach no container.
compose_before_secrets() {
	local name env=()
	for name in BACKUP_SRW_KEY BACKUP_TW_KEY BACKUP_OC_SERVICE_ACCOUNT_ID BACKUP_OC_SERVICE_ACCOUNT_SECRET; do
		[ -n "$(env_get $name)" ] || env+=("$name=not-yet-set")
	done
	env "${env[@]}" docker compose "$@"
}

# --- checks -----------------------------------------------------------------

check_directory() {
	[ -f docker-compose.yml ] && [ -f backup/backup.yml ] ||
		die "run this from the opencloud-compose directory, with the add-on copied in (backup/backup.yml)"
	[ -f "$ENV_FILE" ] || die "$ENV_FILE not found: copy .env.example to .env and append backup/.env.example"
	grep -qE '^#?BACKUP_SRW_KEY=' "$ENV_FILE" ||
		die "$ENV_FILE has no Backup Vault settings: append backup/.env.example to it"
	command -v docker >/dev/null || die "docker not found"
	docker compose version >/dev/null 2>&1 || die "docker compose (v2) not found"
	if [ -n "$(find "$ENV_FILE" -perm -004)" ]; then
		warn "$ENV_FILE is readable by every user on this host, and will hold keys: chmod 600 $ENV_FILE"
	fi
}

# check_compose_file: the add-on is in COMPOSE_FILE, with the route that
# matches the proxy setup.
check_compose_file() {
	local value sep files=() f has_backup=0 base_first=0 traefik=0 external=0
	value=${COMPOSE_FILE:-$(env_get COMPOSE_FILE)}
	sep=${COMPOSE_PATH_SEPARATOR:-$(env_get COMPOSE_PATH_SEPARATOR)}
	sep=${sep:-:}
	if [ -z "$value" ]; then
		problem "COMPOSE_FILE is not set in $ENV_FILE; add backup/backup.yml and the route (see backup/.env.example)"
		return
	fi
	IFS=$sep read -r -a files <<<"$value"
	for f in "${files[@]}"; do
		case $f in
		docker-compose.yml) [ $has_backup = 1 ] || base_first=1 ;;
		backup/backup.yml) has_backup=1 ;;
		traefik/opencloud.yml) traefik=1 ;;
		external-proxy/opencloud.yml) external=1 ;;
		idm/*) warn "COMPOSE_FILE has $f: Backup Vault supports OpenCloud's built-in identity provider only, for now (#162)" ;;
		esac
		[ -f "$f" ] || problem "COMPOSE_FILE names $f, which does not exist"
	done
	if [ $has_backup = 0 ]; then
		problem "COMPOSE_FILE lacks backup/backup.yml"
	elif [ $base_first = 0 ]; then
		problem "COMPOSE_FILE: backup/backup.yml must come after docker-compose.yml"
	fi
	if [ $traefik = 1 ]; then
		has_entry "traefik/backup.yml" "${files[@]}" || problem "COMPOSE_FILE has traefik/opencloud.yml but lacks traefik/backup.yml, the route"
	elif [ $external = 1 ]; then
		has_entry "external-proxy/backup.yml" "${files[@]}" || problem "COMPOSE_FILE has external-proxy/opencloud.yml but lacks external-proxy/backup.yml"
		todo+=("Route https://<OC_DOMAIN>/backup/api in your proxy to 127.0.0.1:$(env_get BACKUP_PORT | grep . || echo 9280), prefix not stripped.")
	else
		problem "COMPOSE_FILE has neither traefik/opencloud.yml nor external-proxy/opencloud.yml: nothing routes /backup/api"
	fi
}

has_entry() {
	local want=$1 f
	shift
	for f in "$@"; do [ "$f" = "$want" ] && return 0; done
	return 1
}

# check_csp: script-src allows WebAssembly, which the Recovery Key ceremony
# needs. The edit is the operator's: this only checks it.
check_csp() {
	if [ ! -f "$CSP_FILE" ]; then
		warn "$CSP_FILE not found; cannot check that it allows WebAssembly"
		return
	fi
	if awk '
		/^[[:space:]]*script-src:/ { inside = 1; match($0, /^[[:space:]]*/); indent = RLENGTH; next }
		inside {
			match($0, /^[[:space:]]*/)
			if ($0 ~ /^[[:space:]]*[a-z-]+:/ && RLENGTH <= indent) exit
			if (index($0, "wasm-unsafe-eval")) { found = 1; exit }
		}
		END { exit found ? 0 : 1 }
	' "$CSP_FILE"; then
		say "ok: $CSP_FILE allows WebAssembly"
	else
		todo+=("Add '''wasm-unsafe-eval''' under script-src in $CSP_FILE (see backup/README.md, step 2), then restart OpenCloud. Without it the Recovery Key setup fails in the browser.")
	fi
}

# check_opencloud_version: OC_DOCKER_TAG, or the base file's default, is in
# the tested range. A warning: OpenCloud decides, not this script.
check_opencloud_version() {
	local tag
	tag=$(env_get OC_DOCKER_TAG)
	if [ -z "$tag" ]; then
		tag=$(grep -oE 'OC_DOCKER_TAG:-[^}]+' docker-compose.yml | head -n 1) || true
		tag=${tag#OC_DOCKER_TAG:-}
	fi
	if ! [[ $tag =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
		warn "OpenCloud's version (${tag:-unknown}) is not a release number; Backup Vault was tested with $OPENCLOUD_OLDEST to $OPENCLOUD_NEWEST"
		return
	fi
	if [ "$(printf '%s\n' "$OPENCLOUD_OLDEST" "$tag" | sort -V | head -n 1)" != "$OPENCLOUD_OLDEST" ] ||
		[ "$(printf '%s\n' "$tag" "$OPENCLOUD_NEWEST" | sort -V | tail -n 1)" != "$OPENCLOUD_NEWEST" ]; then
		warn "OpenCloud $tag is outside the versions this Backup Vault was tested with ($OPENCLOUD_OLDEST to $OPENCLOUD_NEWEST): set OC_DOCKER_TAG in $ENV_FILE"
	else
		say "ok: OpenCloud $tag is a tested version"
	fi
}

# --- secrets ----------------------------------------------------------------

generate_key() {
	if command -v openssl >/dev/null; then
		openssl rand -base64 32
	else
		head -c 32 /dev/urandom | base64
	fi
}

ensure_keys() {
	local name
	for name in BACKUP_SRW_KEY BACKUP_TW_KEY; do
		if [ -n "$(env_get $name)" ]; then
			say "ok: $name is set; left as it is"
		else
			env_set $name "$(generate_key)"
			say "generated $name"
			keys_generated=1
		fi
	done
	if [ $keys_generated = 1 ]; then
		todo+=("Keep a copy of BACKUP_SRW_KEY and BACKUP_TW_KEY from $ENV_FILE somewhere other than this server.")
	fi
	if [ "$(env_get BACKUP_SRW_KEY)" = "$(env_get BACKUP_TW_KEY)" ]; then
		die "BACKUP_SRW_KEY and BACKUP_TW_KEY are the same key; they must differ (the service refuses to start)"
	fi
}

# ensure_opencloud_running: OpenCloud's container is up. Started when it is not:
# its first start generates the service account.
ensure_opencloud_running() {
	if [ -n "$(compose_before_secrets ps --status running -q opencloud 2>/dev/null)" ]; then
		return
	fi
	say "starting OpenCloud"
	local out
	if ! out=$(compose_before_secrets up -d --quiet-pull opencloud 2>&1); then
		printf '%s\n' "$out" >&2
		die "could not start OpenCloud; see above"
	fi
}

# read_service_account FIELD prints FIELD from OpenCloud's generated
# configuration, retrying while its first start writes it.
read_service_account() {
	local value attempt
	for attempt in $(seq "$PROVISION_ATTEMPTS"); do
		value=$(compose_before_secrets exec -T opencloud \
			sh -c "grep -m1 '$1:' /etc/opencloud/opencloud.yaml" 2>/dev/null) || true
		value=${value#*:}
		value=${value//[[:space:]\"\']/}
		if [ -n "$value" ]; then
			printf '%s' "$value"
			return
		fi
		[ "$attempt" = "$PROVISION_ATTEMPTS" ] || sleep "$PROVISION_PAUSE"
	done
	die "could not read $1 from OpenCloud's configuration (/etc/opencloud/opencloud.yaml in the opencloud container)"
}

ensure_service_account() {
	local have_id have_secret id secret
	have_id=$(env_get BACKUP_OC_SERVICE_ACCOUNT_ID)
	have_secret=$(env_get BACKUP_OC_SERVICE_ACCOUNT_SECRET)
	ensure_opencloud_running
	id=$(read_service_account service_account_id)
	secret=$(read_service_account service_account_secret)
	if [ -z "$have_id" ] && [ -z "$have_secret" ]; then
		env_set BACKUP_OC_SERVICE_ACCOUNT_ID "$id"
		env_set BACKUP_OC_SERVICE_ACCOUNT_SECRET "$secret"
		say "took OpenCloud's service account from its configuration"
	elif [ "$have_id" = "$id" ] && [ "$have_secret" = "$secret" ]; then
		say "ok: the service account in $ENV_FILE is OpenCloud's; left as it is"
	else
		die "BACKUP_OC_SERVICE_ACCOUNT_ID/_SECRET in $ENV_FILE are not the ones in OpenCloud's configuration; correct or empty both, then run this again"
	fi
}

# ensure_state_space creates the state Space unless one is configured.
# Creating a second one would leave the first holding every wrapped Data Key
# with nothing pointing at it.
ensure_state_space() {
	if [ -n "$(env_get BACKUP_STATE_SPACE_ID)" ]; then
		say "ok: BACKUP_STATE_SPACE_ID is set; no new state Space is created"
		return
	fi
	local out="" err attempt
	err=$(mktemp)
	for attempt in $(seq "$PROVISION_ATTEMPTS"); do
		if out=$(docker compose run --rm -T backupd provision-state-space 2>"$err"); then
			break
		fi
		out=""
		# Created but not usable: never try again, that would create a second.
		if grep -q 'was created' "$err"; then
			cat "$err" >&2
			rm -f "$err"
			die "the state Space was created but is not usable; see above"
		fi
		if [ "$attempt" = "$PROVISION_ATTEMPTS" ]; then
			tail -n 5 "$err" >&2
			rm -f "$err"
			die "could not create the state Space; see above"
		fi
		say "OpenCloud is not ready yet; trying again in ${PROVISION_PAUSE}s"
		sleep "$PROVISION_PAUSE"
	done
	rm -f "$err"
	out=$(printf '%s\n' "$out" | tail -n 1)
	[[ $out =~ ^[^[:space:]\']+\$[^[:space:]\']+$ ]] || die "provision-state-space printed no Space id"
	env_set BACKUP_STATE_SPACE_ID "$out"
	say "created the state Space and stored its id"
}

# --- main -------------------------------------------------------------------

main() {
	check_directory
	check_compose_file
	if [ $problems -gt 0 ]; then
		die "fix the problems above, then run this again; nothing was changed"
	fi
	local err
	if ! err=$(compose_before_secrets config -q 2>&1); then
		printf '%s\n' "$err" >&2
		die "docker compose cannot read the configuration (above); nothing was changed"
	fi
	check_opencloud_version
	check_csp
	ensure_keys
	ensure_service_account
	ensure_state_space

	say ""
	say "Done. Left to do:"
	local step n=1
	for step in "${todo[@]}" \
		"Start everything: docker compose up -d" \
		"Add a backup destination as an OpenCloud admin in Backup Vault's admin view, set up one Space, back it up and restore from it."; do
		say "  $n. $step"
		n=$((n + 1))
	done
}

main "$@"
