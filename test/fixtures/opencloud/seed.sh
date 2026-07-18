#!/usr/bin/env bash
# Seed a known file into the admin personal space and print its space id +
# sha256, for the CS3 read-path test's checksum assertion.
#
# Prints:
#   CS3_TARGET_USER_ID, CS3_EXPECT_FILE, CS3_EXPECT_SHA256  (appended to fixture.env)
set -euo pipefail
cd "$(dirname "$0")"

FILE="${1:-spike3.txt}"
LOCAL="$(mktemp)"
printf 'spike-3 cs3 read path payload\n%s\n' "$(head -c 2048 /dev/urandom | base64)" > "${LOCAL}"
SHA=$(sha256sum "${LOCAL}" | cut -d' ' -f1)

# Resolve admin's user id + personal space id via the graph API.
me=$(curl -sk -u admin:admin "https://localhost:9200/graph/v1.0/me")
uid=$(printf '%s' "${me}" | grep -o '"id":"[^"]*"' | head -1 | sed 's/"id":"//; s/"//')

drives=$(curl -sk -u admin:admin "https://localhost:9200/graph/v1.0/me/drives")
space=$(printf '%s' "${drives}" | grep -o '"driveType":"personal","id":"[^"]*"' | sed 's/.*"id":"//; s/"//')

curl -sk -u admin:admin -T "${LOCAL}" \
  "https://localhost:9200/dav/spaces/${space}/${FILE}" -o /dev/null -w "seed upload http=%{http_code}\n"
rm -f "${LOCAL}"

cat >> fixture.env <<EOF
export CS3_TARGET_USER_ID='${uid}'
export CS3_EXPECT_FILE='/${FILE}'
export CS3_EXPECT_SHA256='${SHA}'
EOF

echo "seeded /${FILE} into personal space ${space}"
echo "  owner:  ${uid}"
echo "  sha256: ${SHA}"
