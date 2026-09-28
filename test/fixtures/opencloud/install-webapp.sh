#!/usr/bin/env bash
# Install the built web extension into the OpenCloud fixture and prove it loaded.
#
# Two things here are not optional and are the reason this is a script rather
# than a line in a runbook:
#
#   1. OpenCloud scans the apps directory *at startup only*. A bundle dropped in
#      while it is running is invisible, with no error anywhere — so the restart
#      is part of installing, not a step someone remembers.
#   2. "It loads" is checkable without a browser. OpenCloud renders config.json
#      dynamically from the apps it found, so the app appearing in external_apps
#      with its config, plus a 200 on the entry chunk, is exactly what Spike 4
#      verified by hand. Sub-phase 8f adds the in-browser half.
#
# Usage: ./install-webapp.sh [--no-build]
set -euo pipefail
cd "$(dirname "$0")"

APP_ID="backup-vault"
WEB_DIR="../../../web"
BASE="https://localhost:9200"

if [ "${1:-}" != "--no-build" ]; then
  echo "building the extension..."
  (cd "${WEB_DIR}" && corepack pnpm build >/dev/null)
fi

if [ ! -f "${WEB_DIR}/dist/manifest.json" ]; then
  echo "no ${WEB_DIR}/dist/manifest.json — run 'make web-build' first" >&2
  exit 1
fi

echo "installing into apps/${APP_ID}/..."
rm -rf "apps/${APP_ID}"
mkdir -p "apps/${APP_ID}"
cp -r "${WEB_DIR}/dist/." "apps/${APP_ID}/"

# The container runs as uid/gid 1000 and has to be able to read what we wrote.
docker run --rm -v "$(pwd)/apps:/apps" alpine:3 chown -R 1000:1000 /apps >/dev/null 2>&1 || true

echo "restarting OpenCloud (the apps directory is scanned at startup only)..."
docker compose restart opencloud >/dev/null

echo -n "waiting for readiness"
for _ in $(seq 1 60); do
  if curl -fsSk "${BASE}/config.json" -o /dev/null 2>/dev/null; then break; fi
  echo -n "."
  sleep 2
done
echo

echo "verifying the app is registered and served..."
CONFIG=$(curl -fsSk "${BASE}/config.json")

ENTRY=$(printf '%s' "${CONFIG}" | python3 -c '
import json, sys
config = json.load(sys.stdin)
apps = {a.get("id"): a for a in config.get("external_apps", [])}
app = apps.get("'"${APP_ID}"'")
if app is None:
    sys.exit("FAIL: \"'"${APP_ID}"'\" is not in config.json external_apps (found: %s)"
             % (sorted(apps) or "none"))
# The config key is how the extension learns its API path. It travels
# src/manifest.json -> dist/manifest.json -> here -> applicationConfig.
if not app.get("config", {}).get("apiPath"):
    sys.exit("FAIL: the app is registered but carries no config.apiPath: %r" % (app,))
print(app["path"])
')

echo "  registered: ${APP_ID}"
echo "  apiPath:    $(printf '%s' "${CONFIG}" | python3 -c 'import json,sys; print([a for a in json.load(sys.stdin)["external_apps"] if a["id"]=="'"${APP_ID}"'"][0]["config"]["apiPath"])')"

STATUS=$(curl -sk -o /dev/null -w '%{http_code}' "${BASE}${ENTRY}")
if [ "${STATUS}" != "200" ]; then
  echo "FAIL: the entry chunk ${ENTRY} answered ${STATUS}, want 200" >&2
  exit 1
fi
echo "  entrypoint: ${ENTRY} (200)"

echo
echo "Backup Vault is installed. Open ${BASE} (admin / admin) and pick it from the app menu."
echo "The API it calls needs backupd behind the same origin — see the Phase 8 runbook."
