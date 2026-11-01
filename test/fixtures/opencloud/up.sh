#!/usr/bin/env bash
# Bring up the OpenCloud fixture: opencloud-compose at a pinned commit, with
# the shipped compose add-on and a test-only overlay (#158, decision #27).
# Idempotent: re-running keeps the stack, its .env and OpenCloud's data. Use
# ./down.sh to stop it, ./down.sh --purge to start over.
#
#   OC_LEG                 a leg of internal/ocversion/versions.yaml, or
#                          "canary"; unset means the file's default. That
#                          file is the only place OpenCloud is pinned.
#   FIXTURE_BACKUPD        where backupd runs:
#                            host       (default) on the host, `go run` or the
#                                       E2E global-setup; /backup/api is
#                                       routed to the host's port 8080
#                            container  the add-on's containers, set up by
#                                       backup/setup.sh as an operator would
#   FIXTURE_IMAGE_TAG      container mode: the tag of the locally built
#                          images (`make image web-image IMAGE_TAG=…`);
#                          default dev
#   OPENCLOUD_COMPOSE_REF  the opencloud-compose commit; default the one in
#                          opencloud-compose.env. "main" for the canary.
#
# A stack stays on the leg and the opencloud-compose commit it was created
# with: OpenCloud does not promise to open a newer release's data, and the
# checkout carries the fixture's edits. Switching needs ./down.sh --purge.
#
# Writes ./fixture.env (source it) and ./ca.crt, the test CA the stack's
# certificate is issued by.
set -euo pipefail
# shellcheck source=/dev/null # lib.sh: CI runs shellcheck without -x
. "$(dirname "$0")/lib.sh"
cd "${FIXTURE_DIR}"

PINS="${REPO_ROOT}/internal/ocversion/versions.yaml"
UPSTREAM=https://github.com/opencloud-eu/opencloud-compose
MODE="${FIXTURE_BACKUPD:-host}"
IMAGE_TAG="${FIXTURE_IMAGE_TAG:-dev}"
# shellcheck source=/dev/null # sets OPENCLOUD_COMPOSE_COMMIT
. ./opencloud-compose.env
REF="${OPENCLOUD_COMPOSE_REF:-${OPENCLOUD_COMPOSE_COMMIT}}"
# uid:gid the OpenCloud image runs as, and the add-on's installer with it.
OC_UID_GID=1000:1000
HELPER_IMAGE="alpine:3"

die() {
  echo "up.sh: $*" >&2
  exit 1
}

case "${MODE}" in
host | container) ;;
*) die "FIXTURE_BACKUPD must be host or container, not '${MODE}'" ;;
esac

# --- which OpenCloud --------------------------------------------------------

command -v yq >/dev/null || die "needs yq (mikefarah/yq v4) to read ${PINS}"
LEG="${OC_LEG:-$(yq -r '.default' "${PINS}")}"
# One expression over legs and canary: the canary is selectable like any leg,
# and has no digest because it follows its tag.
pin() {
  LEG="${LEG}" yq -r "[.legs[], .canary] | map(select(.name == strenv(LEG))) | .[0].$1 // \"\"" "${PINS}"
}
oc_image=$(pin image)
oc_tag=$(pin tag)
oc_digest=$(pin digest)
[ -n "${oc_image}" ] && [ -n "${oc_tag}" ] ||
  die "no leg '${LEG}' in ${PINS}; legs: $(yq -r '[.legs[].name, .canary.name] | join(", ")' "${PINS}")"
# opencloud-compose builds the reference as IMAGE:TAG, so the digest rides on
# the tag.
oc_tag="${oc_tag}${oc_digest:+@${oc_digest}}"
echo "OpenCloud leg: ${LEG} (${oc_image}:${oc_tag})"

# --- opencloud-compose ------------------------------------------------------

# fetch_upstream checks out REF into the stack directory. Fetched by commit,
# git verifies the content is that commit's.
fetch_upstream() {
  echo "fetching opencloud-compose ${REF}..."
  mkdir -p "${STACK_DIR}"
  git -C "${STACK_DIR}" init -q
  git -C "${STACK_DIR}" fetch -q --depth 1 "${UPSTREAM}" "${REF}"
  git -C "${STACK_DIR}" -c advice.detachedHead=false checkout -q FETCH_HEAD
}

if [ ! -d "${STACK_DIR}/.git" ]; then
  fetch_upstream
fi
commit=$(git -C "${STACK_DIR}" rev-parse HEAD)
if [[ ${REF} =~ ^[0-9a-f]{40}$ ]] && [ "${commit}" != "${REF}" ]; then
  die "the stack is opencloud-compose ${commit}, not ${REF}; run ./down.sh --purge first"
fi
echo "opencloud-compose: ${commit}"

# --- the add-on, as an operator installs it ---------------------------------

cp -r "${REPO_ROOT}/deploy/compose/." "${STACK_DIR}/"
mkdir -p "${STACK_DIR}/fixture"
cp fixture.yml host-backupd.yml "${STACK_DIR}/fixture/"

if [ ! -f "${STACK_DIR}/.env" ]; then
  cp "${STACK_DIR}/.env.example" "${STACK_DIR}/.env"
  cat "${STACK_DIR}/backup/.env.example" >> "${STACK_DIR}/.env"
  chmod 600 "${STACK_DIR}/.env"
fi

previous="$(env_get OC_DOCKER_IMAGE):$(env_get OC_DOCKER_TAG)"
if [ "${previous}" != ":" ] && [ "${previous}" != "${oc_image}:${oc_tag}" ] &&
  docker volume inspect "${PROJECT}_opencloud-data" >/dev/null 2>&1; then
  die "this stack runs ${previous}, not ${oc_image}:${oc_tag}; run ./down.sh --purge first to switch OpenCloud versions"
fi

compose_file=docker-compose.yml:traefik/opencloud.yml:backup/backup.yml:traefik/backup.yml:fixture/fixture.yml
if [ "${MODE}" = host ]; then
  compose_file="${compose_file}:fixture/host-backupd.yml"
fi

env_set COMPOSE_PROJECT_NAME "${PROJECT}"
env_set COMPOSE_FILE "${compose_file}"
env_set OC_DOCKER_IMAGE "${oc_image}"
env_set OC_DOCKER_TAG "${oc_tag}"
env_set OC_DOMAIN "${DOMAIN}"
env_set TRAEFIK_PORT_HTTPS "${HTTPS_PORT}"
env_set TRAEFIK_PORT_HTTP 9080
env_set TRAEFIK_SERVICES_TLS_CONFIG "tls=true"
# The proxy's certificate is the test CA's, which OpenCloud does not trust.
env_set INSECURE true
# The tests seed users and files over basic auth, without OIDC.
env_set PROXY_ENABLE_BASIC_AUTH true
env_set INITIAL_ADMIN_PASSWORD admin
env_set LOG_LEVEL error
if [ "${MODE}" = container ]; then
  for image in opencloud-backupd opencloud-backup-vault-web; do
    docker image inspect "${image}:${IMAGE_TAG}" >/dev/null 2>&1 ||
      die "no image ${image}:${IMAGE_TAG}: run 'make image web-image IMAGE_TAG=${IMAGE_TAG}' first"
  done
  env_set BACKUP_VERSION "${IMAGE_TAG}"
  env_set BACKUP_DOCKER_IMAGE opencloud-backupd
  env_set BACKUP_WEB_DOCKER_IMAGE opencloud-backup-vault-web
fi

# --- the test CA ------------------------------------------------------------

# One CA per stack, for the proxy's certificate. The host's tests trust it
# through SSL_CERT_FILE, the backup service through the add-on's backup/ca/.
# Nothing turns certificate checks off.
issue_certificate() {
  local dir="${STACK_DIR}/fixture/ca"
  mkdir -p "${dir}"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -subj "/CN=OpenCloud fixture test CA" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -keyout "${dir}/ca.key" -out "${dir}/ca.crt" 2>/dev/null
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj "/CN=${DOMAIN}" \
    -keyout "${STACK_DIR}/certs/fixture.key" -out "${dir}/fixture.csr" 2>/dev/null
  printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n' "${DOMAIN}" > "${dir}/ext"
  openssl x509 -req -days 825 -in "${dir}/fixture.csr" -CA "${dir}/ca.crt" -CAkey "${dir}/ca.key" \
    -CAcreateserial -extfile "${dir}/ext" -out "${STACK_DIR}/certs/fixture.crt" 2>/dev/null
}

if [ ! -f "${STACK_DIR}/certs/fixture.crt" ]; then
  echo "issuing the proxy's certificate from a new test CA..."
  issue_certificate
fi
cp "${STACK_DIR}/fixture/ca/ca.crt" ca.crt
cp ca.crt "${STACK_DIR}/backup/ca/fixture-ca.crt"
printf 'tls:\n  certificates:\n    - certFile: /certs/fixture.crt\n      keyFile: /certs/fixture.key\n' \
  > "${STACK_DIR}/config/traefik/dynamic/fixture-certs.yml"

if [ "${MODE}" = host ]; then
  cp traefik-host-backupd.yml "${STACK_DIR}/config/traefik/dynamic/backup-host.yml"
else
  rm -f "${STACK_DIR}/config/traefik/dynamic/backup-host.yml"
fi

# --- OpenCloud's browser policy ---------------------------------------------

# The add-on's README, step 2: one line under script-src. Done here as an
# operator does it, so a change in opencloud-compose's csp.yaml that breaks
# the step fails here.
csp="${STACK_DIR}/config/opencloud/csp.yaml"
if ! grep -q "wasm-unsafe-eval" "${csp}"; then
  awk '
    { print }
    /^[[:space:]]*script-src:[[:space:]]*$/ {
      match($0, /^[[:space:]]*/)
      printf "%s  - '\'''\'''\''wasm-unsafe-eval'\'''\'''\''\n", substr($0, 1, RLENGTH)
      found = 1
    }
    END { if (!found) exit 1 }
  ' "${csp}" > "${csp}.new" || die "${csp} has no script-src: the add-on's README, step 2, no longer fits"
  mv "${csp}.new" "${csp}"
fi

# The add-on's installer writes the extension as OpenCloud's user, and so
# must install-webapp.sh. A runner's uid is not 1000; a root helper hands
# the directory over, for any host uid.
docker run --rm -v "${STACK_DIR}/config/opencloud/apps:/apps" "${HELPER_IMAGE}" \
  chown -R "${OC_UID_GID}" /apps

# --- start ------------------------------------------------------------------

if [ "${MODE}" = container ]; then
  echo "setting up the add-on (backup/setup.sh)..."
  (cd "${STACK_DIR}" && backup/setup.sh)
fi
echo "starting the stack (${MODE} backupd)..."
fixture_compose up -d --quiet-pull

echo "waiting for OpenCloud (basic-auth graph/me)..."
for i in $(seq 1 90); do
  if curl -fsS --cacert ca.crt -u admin:admin "${ORIGIN}/graph/v1.0/me" -o /dev/null 2>/dev/null; then
    break
  fi
  [ "${i}" != 90 ] || die "timed out waiting for OpenCloud at ${ORIGIN}"
  sleep 2
done

if [ "${MODE}" = container ]; then
  echo "waiting for backupd to be healthy..."
  for i in $(seq 1 90); do
    [ "$(fixture_compose ps --format '{{.Health}}' backupd)" != healthy ] || break
    [ "${i}" != 90 ] || {
      fixture_compose logs --tail 50 backupd >&2
      die "backupd did not become healthy"
    }
    sleep 2
  done
fi

# --- fixture.env ------------------------------------------------------------

config_yaml=$(fixture_compose exec -T opencloud cat /etc/opencloud/opencloud.yaml)

# yaml_val prints a scalar from OpenCloud's generated configuration, without
# the quotes its writer adds around values with special characters.
yaml_val() {
  { printf '%s\n' "${config_yaml}" | grep -m1 -E "^[[:space:]]*$1:" || true; } |
    sed "s/^[^:]*: *//" |
    sed "s/^'\(.*\)'$/\1/; s/^\"\(.*\)\"$/\1/"
}

# emit prints `export KEY='value'`, any single quote in value escaped.
emit() {
  local key="$1" val="$2"
  val=${val//\'/\'\\\'\'}
  printf "export %s='%s'\n" "$key" "$val"
}

sa_id=$(yaml_val service_account_id)
sa_secret=$(yaml_val service_account_secret)
jwt=$(yaml_val jwt_secret)
machine=$(yaml_val machine_auth_api_key)
for required in sa_id sa_secret jwt machine; do
  [ -n "${!required}" ] || die "could not read ${required} from OpenCloud's configuration"
done

{
  echo "export CS3_GATEWAY_ADDR=127.0.0.1:9142"
  # Where the data server is reachable from the host (fixture.yml).
  echo "export CS3_DATA_SERVER_URL=http://127.0.0.1:9158"
  emit CS3_SERVICE_ACCOUNT_ID "${sa_id}"
  emit CS3_SERVICE_ACCOUNT_SECRET "${sa_secret}"
  emit OC_JWT_SECRET "${jwt}"
  emit OC_MACHINE_AUTH_API_KEY "${machine}"
  echo "export OC_URL=${ORIGIN}"
  # The service account under the names backupd itself reads.
  emit OC_SERVICE_ACCOUNT_ID "${sa_id}"
  emit OC_SERVICE_ACCOUNT_SECRET "${sa_secret}"
  # What a host-run backupd needs to sit behind the fixture's origin.
  echo "export OC_BASE_URL=${ORIGIN}"
  echo "export OIDC_ISSUER=${ORIGIN}"
  # Must equal the SPA's client id or every extension request 401s
  # (decisions.md #21). config.json says "web".
  echo "export OIDC_AUDIENCE=web"
  echo "export BACKUPD_BASE_PATH=/backup"
  emit SSL_CERT_FILE "${FIXTURE_DIR}/ca.crt"
  # Which OpenCloud and opencloud-compose the tests run against, for their
  # logs and for tests that assert on the version itself.
  emit OC_FIXTURE_LEG "${LEG}"
  emit OC_FIXTURE_IMAGE "${oc_image}:${oc_tag}"
  emit OC_FIXTURE_COMPOSE_COMMIT "${commit}"
  # Where backupd runs; the E2E global-setup starts one only in host mode.
  emit FIXTURE_BACKUPD "${MODE}"
} > fixture.env

echo "OpenCloud fixture is up."
echo "  web:          ${ORIGIN}  (admin / admin)"
if [ "${MODE}" = host ]; then
  echo "  backup API:   ${ORIGIN}/backup/api/ -> host port 8080"
else
  echo "  backup API:   ${ORIGIN}/backup/api/ -> the add-on's backupd"
fi
echo "  CS3 gateway:  127.0.0.1:9142"
echo "  compose:      test/fixtures/opencloud/compose.sh ps"
echo "  credentials:  written to test/fixtures/opencloud/fixture.env (source it)"
