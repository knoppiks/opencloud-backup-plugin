# Backup Vault for opencloud-compose

An add-on for [opencloud-compose](https://github.com/opencloud-eu/opencloud-compose),
laid out like its own add-ons: copy it into your opencloud-compose directory
and add its files to `COMPOSE_FILE`.

```
backup/backup.yml           the backup service and the web extension's installer
backup/.env.example         the variables it reads, to append to .env
backup/ca/                  your own CA's certificate, if you use one
traefik/backup.yml          the route, with opencloud-compose's Traefik
external-proxy/backup.yml   the port, with your own proxy instead
```

Use the add-on from a release: its files name the images of that release, by
digest. The copy in the repository names no version and refuses to start
until `BACKUP_VERSION` is set.

## What is supported

- **Traefik** (`traefik/opencloud.yml`), with Let's Encrypt or with local
  certificates.
- **Your own proxy** (`external-proxy/opencloud.yml`). It has to route
  `/backup/api` on OpenCloud's host to the backup service.
- **OpenCloud's built-in identity provider.** Keycloak and other external
  identity providers (`idm/*`) are **not supported yet**.
- **The OpenCloud versions in the README's supported range.** opencloud-compose
  may default to a newer OpenCloud than Backup Vault was tested with. Set
  `OC_DOCKER_TAG` in `.env` to a supported version.

## Installing

Run everything from your opencloud-compose directory, with OpenCloud set up
and running **without** the add-on: its first start generates the service
account the backup service needs.

1. **Copy the add-on** into the opencloud-compose directory, and append
   `backup/.env.example` to `.env`.

2. **Allow WebAssembly in OpenCloud's Content Security Policy.** The Recovery
   Key ceremony runs Argon2id in the browser as WebAssembly, which the default
   policy blocks. In `config/opencloud/csp.yaml`, add one line under
   `script-src`:

   ```yaml
     script-src:
       - '''self'''
       - '''unsafe-inline'''
       - '''wasm-unsafe-eval'''
   ```

   It allows compiling WebAssembly only; `eval()` stays blocked.

3. **Secrets.** In `.env`, single-quoted:
   - `BACKUP_OC_SERVICE_ACCOUNT_ID` and `BACKUP_OC_SERVICE_ACCOUNT_SECRET`:
     OpenCloud's service account, as its first start generated it:

     ```sh
     docker compose exec opencloud sh -c \
       'grep -m1 service_account_id: /etc/opencloud/opencloud.yaml; grep -m1 service_account_secret: /etc/opencloud/opencloud.yaml'
     ```

     The secret reaches every Space's files. Keep `.env` readable by you
     only.
   - `BACKUP_SRW_KEY` and `BACKUP_TW_KEY`: two different keys, each from
     `openssl rand -base64 32`. Keep a copy outside the server.

4. **Your own CA**, with local certificates: copy the CA's certificate into
   `backup/ca/`. The backup service trusts it in addition to the public CAs.
   OpenCloud itself still needs `INSECURE=true` for local certificates; the
   backup service never turns certificate checks off.

5. **Add the add-on to `COMPOSE_FILE`**, after the files it extends:

   ```
   COMPOSE_FILE=docker-compose.yml:...:traefik/opencloud.yml:backup/backup.yml:traefik/backup.yml
   ```

   With your own proxy, `external-proxy/opencloud.yml:backup/backup.yml:external-proxy/backup.yml`
   instead, and route `https://<OC_DOMAIN>/backup/api` to
   `127.0.0.1:${BACKUP_PORT:-9280}`, prefix not stripped. Not `/backup`:
   the extension's own pages live under `/backup-vault/`.

6. **Create the state Space.** The service keeps its schedules, history and
   wrapped keys in a Space of its own, and refuses to start without one.
   OpenCloud first, with the add-on's settings, then the Space:

   ```sh
   docker compose up -d opencloud
   docker compose run --rm -T backupd provision-state-space
   ```

   If it reports OpenCloud as unavailable, OpenCloud is still starting: run
   it again a little later. Put the id it prints into `.env` as
   `BACKUP_STATE_SPACE_ID`,
   **single-quoted** (it contains a `$`), then start everything with
   `docker compose up -d`. Run `provision-state-space` once: a second run creates a second,
   empty Space.

7. **Check it.** `docker compose ps` shows `backupd` as healthy, and Backup
   Vault is in OpenCloud's app menu. Add a backup destination as an
   OpenCloud admin in Backup Vault's admin view, set up one Space, run
   "Back up now", and restore from it before relying on it.

## Running it

- **After a reboot** the backup service waits for OpenCloud and is unhealthy
  until OpenCloud answers. It logs `waiting for OpenCloud` meanwhile.
- **Upgrading:** replace the add-on's files with the new release's and run
  `docker compose up -d`. A new web extension restarts OpenCloud once, because
  OpenCloud reads its extensions at startup only.
- **One instance only.** Do not scale `backupd`: a second instance against the
  same state Space is refused.
- **Memory:** 512 MB by default, of which `/tmp` (kopia's cache during a run)
  takes up to 256 MB. Raise `BACKUP_MEM_LIMIT` and `BACKUP_TMP_SIZE` together
  for large Spaces.
- Every setting of the backup service is in the
  [environment reference](../../docs/reference/environment-backupd.md); the
  add-on passes those it needs, from the `BACKUP_*` variables in `.env`.
