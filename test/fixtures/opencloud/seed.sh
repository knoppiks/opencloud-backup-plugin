#!/usr/bin/env bash
# Seed a known file into the admin personal space and print its space id +
# sha256, for the CS3 read-path test's checksum assertion. Also seeds a normal
# (non-admin) user for the Phase-2 admin-detection tests.
#
# Also seeds a nested file under a non-ASCII folder so the Phase-4 pipeline test
# exercises directory traversal and structure preservation, not just a flat read.
#
# Finally, seeds a shared project space carrying one grant of every kind the
# authorization model reads (viewer, editor, manager, a group grant and an
# expiring grant), so the grants opaque map is pinned against real reva output
# rather than an assumed shape.
#
# Prints (appended to fixture.env):
#   CS3_TARGET_USER_ID, CS3_EXPECT_FILE, CS3_EXPECT_SHA256
#   CS3_EXPECT_NESTED_FILE, CS3_EXPECT_NESTED_SHA256
#   OC_ADMIN_USER_ID, OC_NORMAL_USER_ID, OC_ADMIN_APP_ROLE_ID
#   OC_SHARED_SPACE_ID, OC_SHARED_SPACE_NAME, OC_SHARED_GROUP_ID,
#   OC_SHARED_VIEWER_ID, OC_SHARED_EDITOR_ID, OC_SHARED_MANAGER_ID,
#   OC_SHARED_GROUPED_ID, OC_SHARED_GRANT_EXPIRY
set -euo pipefail
cd "$(dirname "$0")"

OC="https://localhost:9200"
ADMIN="admin:admin"

# json_field extracts the first "<key>":"<value>" scalar from a JSON blob. The
# fixture scripts deliberately avoid a jq dependency.
json_field() {
  grep -o "\"$1\":\"[^\"]*\"" | head -1 | sed "s/\"$1\":\"//; s/\"$//"
}

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

# --- shared project space with one grant of every kind ---------------------
# The authorization model reads a space's grants / groups / grants_expirations
# opaque maps. Nothing else in the fixture produces them, so the shape they are
# parsed from would otherwise be an assumption. This seeds a project space with
# a viewer, an editor, a manager, a group grant and an expiring grant, and the
# CS3 integration test asserts the parsed roles match.
#
# Space role ids are OpenCloud's static unifiedRoleDefinitions
# (GET /graph/v1beta1/roleManagement/permissions/roleDefinitions).
ROLE_SPACE_VIEWER="a8d5fe5e-96e3-418d-825b-534dbdf22b99"
ROLE_SPACE_EDITOR="58c63c02-1d89-4572-916a-870abc5a1b7d"
ROLE_SPACE_MANAGER="312c0871-5ef7-4b3a-85b6-0e4074c64049"

SHARED_SPACE_NAME="Backup Roles"
SHARED_SPACE_ALIAS="project/backup-roles"
SHARED_GROUP_NAME="ocbp-family"
# Far enough out that the grant is live but its expiry is still recorded, so the
# test sees a populated grants_expirations map.
SHARED_GRANT_EXPIRY="2099-01-01T00:00:00Z"

# seed_user creates a user if absent and echoes their id.
seed_user() {
  local name="$1"
  curl -sk -u "${ADMIN}" -X POST "${OC}/graph/v1.0/users" \
    -H "Content-Type: application/json" \
    -d "{\"onPremisesSamAccountName\":\"${name}\",\"displayName\":\"${name}\",\"mail\":\"${name}@example.org\",\"passwordProfile\":{\"password\":\"Test-User-1!\"}}" \
    -o /dev/null || true
  curl -sk -u "${ADMIN}" "${OC}/graph/v1.0/users/${name}" | json_field id
}

# invite grants a role on the shared space to one principal. The third argument
# is "user" or "group"; the optional fourth is an RFC3339 expiry.
invite() {
  local principal="$1" role="$2" kind="$3" expiry="${4:-}"
  local body="{\"recipients\":[{\"objectId\":\"${principal}\",\"@libre.graph.recipient.type\":\"${kind}\"}],\"roles\":[\"${role}\"]"
  if [ -n "${expiry}" ]; then
    body="${body},\"expirationDateTime\":\"${expiry}\""
  fi
  body="${body}}"
  curl -sk -u "${ADMIN}" -X POST "${OC}/graph/v1beta1/drives/${shared_space}/root/invite" \
    -H "Content-Type: application/json" -d "${body}" \
    -o /dev/null -w "  grant ${kind} ${principal} http=%{http_code}\n"
}

viewer_uid=$(seed_user "ocbp-viewer")
editor_uid=$(seed_user "ocbp-editor")
manager_uid=$(seed_user "ocbp-manager")
grouped_uid=$(seed_user "ocbp-grouped")

curl -sk -u "${ADMIN}" -X POST "${OC}/graph/v1.0/groups" \
  -H "Content-Type: application/json" -d "{\"displayName\":\"${SHARED_GROUP_NAME}\"}" \
  -o /dev/null || true
group_id=$(curl -sk -u "${ADMIN}" "${OC}/graph/v1.0/groups" \
  | tr '{' '\n' | grep "\"displayName\":\"${SHARED_GROUP_NAME}\"" | json_field id || true)
if [ -z "${group_id}" ]; then
  echo "seed: could not resolve group '${SHARED_GROUP_NAME}'" >&2
  exit 1
fi

curl -sk -u "${ADMIN}" -X POST "${OC}/graph/v1.0/groups/${group_id}/members/\$ref" \
  -H "Content-Type: application/json" \
  -d "{\"@odata.id\":\"${OC}/graph/v1.0/users/${grouped_uid}\"}" \
  -o /dev/null || true

# Reuse the space if a previous seed already created it; creating a second one
# with the same name would succeed and leave the test picking arbitrarily.
shared_space=$(curl -sk -u "${ADMIN}" "${OC}/graph/v1.0/drives" \
  | tr '{' '\n' | grep "\"driveAlias\":\"${SHARED_SPACE_ALIAS}\"" | json_field id || true)
if [ -z "${shared_space}" ]; then
  shared_space=$(curl -sk -u "${ADMIN}" -X POST "${OC}/graph/v1.0/drives" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"${SHARED_SPACE_NAME}\",\"description\":\"grant-shape fixture\",\"quota\":{\"total\":10485760}}" \
    | json_field id || true)
fi
if [ -z "${shared_space}" ]; then
  echo "seed: could not create or resolve the shared space" >&2
  exit 1
fi

invite "${viewer_uid}"  "${ROLE_SPACE_VIEWER}"  user
invite "${editor_uid}"  "${ROLE_SPACE_EDITOR}"  user "${SHARED_GRANT_EXPIRY}"
invite "${manager_uid}" "${ROLE_SPACE_MANAGER}" user
invite "${group_id}"    "${ROLE_SPACE_VIEWER}"  group

cat >> fixture.env <<EOF
export CS3_TARGET_USER_ID='${uid}'
export CS3_EXPECT_FILE='/${FILE}'
export CS3_EXPECT_SHA256='${SHA}'
export CS3_EXPECT_NESTED_FILE='/${NESTED_DIR}/${NESTED_NAME}'
export CS3_EXPECT_NESTED_SHA256='${NESTED_SHA}'
export OC_ADMIN_USER_ID='${uid}'
export OC_NORMAL_USER_ID='${normal_uid}'
export OC_ADMIN_APP_ROLE_ID='71881883-1768-46bd-a24d-a356a2afdf7f'
export OC_SHARED_SPACE_ID='${shared_space}'
export OC_SHARED_SPACE_NAME='${SHARED_SPACE_NAME}'
export OC_SHARED_GROUP_ID='${group_id}'
export OC_SHARED_VIEWER_ID='${viewer_uid}'
export OC_SHARED_EDITOR_ID='${editor_uid}'
export OC_SHARED_MANAGER_ID='${manager_uid}'
export OC_SHARED_GROUPED_ID='${grouped_uid}'
export OC_SHARED_GRANT_EXPIRY='${SHARED_GRANT_EXPIRY}'
EOF

echo "seeded /${FILE} into personal space ${space}"
echo "  owner:  ${uid}"
echo "  sha256: ${SHA}"
echo "seeded /${NESTED_DIR}/${NESTED_NAME}"
echo "  sha256: ${NESTED_SHA}"
echo "  normal user: ${NORMAL_USER} (${normal_uid})"
echo "  admin appRoleId: 71881883-1768-46bd-a24d-a356a2afdf7f"
echo "seeded shared space '${SHARED_SPACE_NAME}' (${shared_space})"
echo "  viewer:  ${viewer_uid}"
echo "  editor:  ${editor_uid} (expires ${SHARED_GRANT_EXPIRY})"
echo "  manager: ${manager_uid}"
echo "  group:   ${group_id} containing ${grouped_uid}"
