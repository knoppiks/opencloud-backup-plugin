#!/usr/bin/env bash
# Seed a known file into the admin personal space and print its space id +
# sha256, for the CS3 read-path test's checksum assertion. Also seeds a normal
# (non-admin) user for the Phase-2 admin-detection tests.
#
# Also seeds a nested file under a non-ASCII folder so the Phase-4 pipeline test
# exercises directory traversal and structure preservation, not just a flat read.
#
# Prints (appended to fixture.env):
#   CS3_TARGET_USER_ID, CS3_EXPECT_FILE, CS3_EXPECT_SHA256
#   CS3_EXPECT_NESTED_FILE, CS3_EXPECT_NESTED_SHA256
#   OC_ADMIN_USER_ID, OC_NORMAL_USER_ID, OC_ADMIN_APP_ROLE_ID
set -euo pipefail
cd "$(dirname "$0")"

# --- normal (non-admin) user for admin-detection tests ---------------------
# Idempotent: creating an existing user returns 409, which we tolerate. The user
# gets the default "User" app role (appRoleId d7beeea8-…), i.e. isAdmin=false.
NORMAL_USER="testuser"
NORMAL_PASS="Test-User-1!"
curl -sk -u admin:admin -X POST "https://localhost:9200/graph/v1.0/users" \
  -H "Content-Type: application/json" \
  -d "{\"onPremisesSamAccountName\":\"${NORMAL_USER}\",\"displayName\":\"Test User\",\"mail\":\"${NORMAL_USER}@example.org\",\"passwordProfile\":{\"password\":\"${NORMAL_PASS}\"}}" \
  -o /dev/null -w "seed user '${NORMAL_USER}' http=%{http_code}\n" || true

normal_uid=$(curl -sk -u "${NORMAL_USER}:${NORMAL_PASS}" \
  "https://localhost:9200/graph/v1.0/me" \
  | grep -o '"id":"[^"]*"' | head -1 | sed 's/"id":"//; s/"//')

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

# --- nested, non-ASCII path (Phase-4 walk + structure preservation) --------
# MKCOL is idempotent enough for a fixture: an existing collection returns 405,
# which is fine.
NESTED_DIR="ordner-фото"
NESTED_NAME="café.bin"
curl -sk -u admin:admin -X MKCOL \
  "https://localhost:9200/dav/spaces/${space}/${NESTED_DIR}" \
  -o /dev/null -w "seed mkcol http=%{http_code}\n" || true

NESTED_LOCAL="$(mktemp)"
printf 'phase-4 nested payload\n%s\n' "$(head -c 4096 /dev/urandom | base64)" > "${NESTED_LOCAL}"
NESTED_SHA=$(sha256sum "${NESTED_LOCAL}" | cut -d' ' -f1)
curl -sk -u admin:admin -T "${NESTED_LOCAL}" \
  "https://localhost:9200/dav/spaces/${space}/${NESTED_DIR}/${NESTED_NAME}" \
  -o /dev/null -w "seed nested upload http=%{http_code}\n"
rm -f "${NESTED_LOCAL}"

cat >> fixture.env <<EOF
export CS3_TARGET_USER_ID='${uid}'
export CS3_EXPECT_FILE='/${FILE}'
export CS3_EXPECT_SHA256='${SHA}'
export CS3_EXPECT_NESTED_FILE='/${NESTED_DIR}/${NESTED_NAME}'
export CS3_EXPECT_NESTED_SHA256='${NESTED_SHA}'
export OC_ADMIN_USER_ID='${uid}'
export OC_NORMAL_USER_ID='${normal_uid}'
export OC_ADMIN_APP_ROLE_ID='71881883-1768-46bd-a24d-a356a2afdf7f'
EOF

echo "seeded /${FILE} into personal space ${space}"
echo "  owner:  ${uid}"
echo "  sha256: ${SHA}"
echo "seeded /${NESTED_DIR}/${NESTED_NAME}"
echo "  sha256: ${NESTED_SHA}"
echo "  normal user: ${NORMAL_USER} (${normal_uid})"
echo "  admin appRoleId: 71881883-1768-46bd-a24d-a356a2afdf7f"
