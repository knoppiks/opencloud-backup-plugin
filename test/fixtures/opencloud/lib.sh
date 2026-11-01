# shellcheck shell=bash disable=SC2034 # the variables are for the scripts that source this
# What the fixture's scripts share (#158): where the stack is, its origin,
# and how to run docker compose on it. Sourced, not run.

FIXTURE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${FIXTURE_DIR}/../../.." && pwd)"
# The opencloud-compose checkout with the add-on copied in, as an operator
# has it. Gitignored: it holds the generated keys in its .env.
STACK_DIR="${FIXTURE_DIR}/stack"
PROJECT=opencloud-fixture
DOMAIN=cloud.opencloud.localhost
HTTPS_PORT=9200
ORIGIN="https://${DOMAIN}:${HTTPS_PORT}"

# env_get NAME prints NAME's value in the stack's .env (the last assignment),
# without quotes; nothing when unset or empty.
env_get() {
  local line
  line=$(grep -E "^$1=" "${STACK_DIR}/.env" 2>/dev/null | tail -n 1) || true
  line=${line#*=}
  line=${line%\"}
  line=${line#\"}
  line=${line%\'}
  line=${line#\'}
  printf '%s' "${line}"
}

# env_set NAME VALUE sets NAME in the stack's .env, in place when it has a
# line, appended otherwise.
env_set() {
  local tmp
  tmp=$(mktemp "${STACK_DIR}/.env.XXXXXX")
  NAME=$1 VALUE=$2 awk '
    BEGIN { name = ENVIRON["NAME"]; line = name "=" ENVIRON["VALUE"] }
    !done && index($0, name "=") == 1 { print line; done = 1; next }
    { print }
    END { if (!done) print line }
  ' "${STACK_DIR}/.env" > "${tmp}"
  cat "${tmp}" > "${STACK_DIR}/.env"
  rm -f "${tmp}"
}

# fixture_compose runs docker compose on the stack. In host mode the add-on's
# required variables stay empty (setup.sh never ran), and Compose refuses the
# whole file without them; stand-ins fill them for the command only. They
# reach no container: the services that read them are not started.
fixture_compose() {
  local name env=()
  for name in BACKUP_VERSION BACKUP_SRW_KEY BACKUP_TW_KEY \
    BACKUP_OC_SERVICE_ACCOUNT_ID BACKUP_OC_SERVICE_ACCOUNT_SECRET; do
    [ -n "$(env_get "${name}")" ] || env+=("${name}=unused-in-host-mode")
  done
  (cd "${STACK_DIR}" && env "${env[@]}" docker compose "$@")
}
