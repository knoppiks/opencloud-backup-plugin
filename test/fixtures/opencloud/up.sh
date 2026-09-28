#!/usr/bin/env bash
# Bring up the test OpenCloud fixture and print the worker credentials the CS3
# spike / integration tests need. Idempotent: re-running reuses the generated
# config. Use ./down.sh to tear down (optionally purging config+data).
#
# Outputs (also written to ./fixture.env for `source`-ing):
#   CS3_GATEWAY_ADDR, CS3_SERVICE_ACCOUNT_ID, CS3_SERVICE_ACCOUNT_SECRET,
#   OC_JWT_SECRET, OC_MACHINE_AUTH_API_KEY, OC_URL
set -euo pipefail

cd "$(dirname "$0")"
IMAGE="opencloudeu/opencloud-rolling:7.3.0"
# Root helper for the file-ownership chores the host user may not be allowed to
# do itself (same image down.sh --purge uses).
HELPER_IMAGE="alpine:3"
CONFIG="opencloud.yaml"
# uid:gid the OpenCloud image runs as (`opencloud-user`).
OC_UID=1000
OC_GID=1000

mkdir -p config data apps

# The image runs as uid 1000 and never elevates, so the bind mounts have to be
# writable by *that* uid — not by whoever runs this script. On a dev box with
# uid 1000 those happen to coincide; on a GitHub runner (uid 1001) they do not,
# and init dies with "open /etc/opencloud/opencloud.yaml: permission denied".
# Hand the directories over from a root helper, which works for any host uid.
docker run --rm -v "$(pwd):/work" "${HELPER_IMAGE}" \
  chown -R "${OC_UID}:${OC_GID}" /work/config /work/data /work/apps

# One-time init: generates config/opencloud.yaml with all secrets. --insecure
# true avoids the interactive prompt; admin password fixed to `admin` for tests.
if [ ! -f "config/${CONFIG}" ]; then
  echo "init: generating OpenCloud config (admin password: admin)..."
  docker run --rm \
    -v "$(pwd)/config:/etc/opencloud" \
    -v "$(pwd)/data:/var/lib/opencloud" \
    "${IMAGE}" init --insecure true --admin-password admin -f >/dev/null
fi

echo "starting OpenCloud fixture..."
docker compose up -d

echo "waiting for readiness (basic-auth graph/me)..."
for i in $(seq 1 60); do
  if curl -fsSk -u admin:admin "https://localhost:9200/graph/v1.0/me" -o /dev/null 2>/dev/null; then
    break
  fi
  sleep 2
  if [ "$i" = "60" ]; then echo "timed out waiting for OpenCloud" >&2; exit 1; fi
done

# init writes opencloud.yaml mode 0600 owned by the container's uid, so the host
# user cannot necessarily read it. Slurp it through the root helper instead of
# loosening the mode on a file full of secrets.
config_yaml=$(docker run --rm -v "$(pwd)/config:/config:ro" "${HELPER_IMAGE}" \
  cat "/config/${CONFIG}")

# Extract a scalar value from the generated YAML, stripping optional single- or
# double-quotes that the config writer adds around values with special chars.
yaml_val() {
  # Match the key at any indentation; tolerate "no match" without tripping -e.
  { printf '%s\n' "${config_yaml}" | grep -m1 -E "^[[:space:]]*$1:" || true; } \
    | sed "s/^[^:]*: *//" \
    | sed "s/^'\(.*\)'$/\1/; s/^\"\(.*\)\"$/\1/"
}

# Emit `export KEY='value'` with any single quotes in value safely escaped.
emit() {
  local key="$1" val="$2"
  val=${val//\'/\'\\\'\'} # ' -> '\''
  printf "export %s='%s'\n" "$key" "$val"
}

sa_id=$(yaml_val service_account_id)
sa_secret=$(yaml_val service_account_secret)
jwt=$(yaml_val jwt_secret)
machine=$(yaml_val machine_auth_api_key)

# An unreadable or unexpectedly-shaped config would otherwise yield an empty
# fixture.env and a pile of confusing auth failures several steps later.
for required in sa_id sa_secret jwt machine; do
  if [ -z "${!required}" ]; then
    echo "could not read ${required} from config/${CONFIG}" >&2
    exit 1
  fi
done

# Caddy terminates TLS on :9200 with its own local CA (see ./Caddyfile). A
# backupd running on the host has to verify that certificate to reach the OIDC
# issuer and the data gateway, and Go reads a CA bundle from SSL_CERT_FILE — so
# the root is extracted here rather than by disabling verification, which the
# service offers no switch for and should not.
echo "extracting the proxy's CA certificate..."
for i in $(seq 1 30); do
  if docker compose exec -T proxy \
    cat /data/caddy/pki/authorities/local/root.crt > ca.crt 2>/dev/null && [ -s ca.crt ]; then
    break
  fi
  sleep 2
  if [ "$i" = "30" ]; then echo "timed out waiting for the proxy's CA" >&2; exit 1; fi
done

{
  echo "export CS3_GATEWAY_ADDR=127.0.0.1:9142"
  emit CS3_SERVICE_ACCOUNT_ID "${sa_id}"
  emit CS3_SERVICE_ACCOUNT_SECRET "${sa_secret}"
  emit OC_JWT_SECRET "${jwt}"
  emit OC_MACHINE_AUTH_API_KEY "${machine}"
  echo "export OC_URL=https://localhost:9200"
  # The service account under the names backupd itself reads.
  emit OC_SERVICE_ACCOUNT_ID "${sa_id}"
  emit OC_SERVICE_ACCOUNT_SECRET "${sa_secret}"
  # Phase 8: what a host-run backupd needs to sit behind the fixture's origin.
  echo "export OC_BASE_URL=https://localhost:9200"
  echo "export OIDC_ISSUER=https://localhost:9200"
  # Must equal the SPA's client id or every extension request 401s
  # (decisions.md #21). config.json says "web".
  echo "export OIDC_AUDIENCE=web"
  echo "export BACKUPD_BASE_PATH=/backup"
  emit SSL_CERT_FILE "$(pwd)/ca.crt"
} > fixture.env

echo "OpenCloud fixture is up."
echo "  web:          https://localhost:9200  (admin / admin)  [via the proxy]"
echo "  opencloud:    https://localhost:9201  (direct, bypasses the proxy)"
echo "  backup API:   https://localhost:9200/backup/  -> host port 8080"
echo "  CS3 gateway:  127.0.0.1:9142"
echo "  credentials:  written to test/fixtures/opencloud/fixture.env (source it)"
