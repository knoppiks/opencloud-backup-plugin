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
CONFIG="opencloud.yaml"

mkdir -p config data apps

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

# Extract a scalar value from the generated YAML, stripping optional single- or
# double-quotes that the config writer adds around values with special chars.
yaml_val() {
  # Match the key at any indentation; tolerate "no match" without tripping -e.
  { grep -m1 -E "^[[:space:]]*$1:" "config/${CONFIG}" || true; } \
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

{
  echo "export CS3_GATEWAY_ADDR=127.0.0.1:9142"
  emit CS3_SERVICE_ACCOUNT_ID "${sa_id}"
  emit CS3_SERVICE_ACCOUNT_SECRET "${sa_secret}"
  emit OC_JWT_SECRET "${jwt}"
  emit OC_MACHINE_AUTH_API_KEY "${machine}"
  echo "export OC_URL=https://localhost:9200"
} > fixture.env

echo "OpenCloud fixture is up."
echo "  web:          https://localhost:9200  (admin / admin)"
echo "  CS3 gateway:  127.0.0.1:9142"
echo "  credentials:  written to test/fixtures/opencloud/fixture.env (source it)"
