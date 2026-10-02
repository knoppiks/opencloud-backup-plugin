# opencloud-backup-plugin

A self-service, encrypted backup extension for [OpenCloud](https://opencloud.eu)
(oCIS / Reva architecture).

Built for the **self-hosting parent running OpenCloud for the whole family** —
not the enterprise. A user clicks "back up my data" once; backups then run
automatically to an external S3 target. The admin can operate and recover
backups **without ever seeing the plaintext content** of a user's data.

> Status: **in development.** The backup pipeline works end to end — a seeded
> OpenCloud Space is snapshotted, encrypted and deduplicated onto an S3 target —
> and **both restore paths work**: an admin Take-Out that the user decrypts
> offline with their Recovery Key (verified with OpenCloud stopped), and a
> user-triggered restore back into their Space. Unattended scheduling, retention
> and key rotation have landed since. The user interface, **Backup Vault**, is an
> OpenCloud Web extension under [`web/`](web/): per-Space overview and status,
> the setup wizard with the Recovery Key ceremony (tested byte-for-byte against
> the Go implementation in both directions), restore, and Recovery Key check and
> replacement. Backup targets are managed through an admin HTTP API or seeded at
> first start; the admin view for them is the current phase. Nothing has yet
> been exercised end to end in a real browser. The design and phased roadmap
> live in [`.agents/plan/`](.agents/plan/).

## What it does

- **One-click, then forget.** The user enables backup for a Space and picks a
  schedule; scheduled runs happen unattended, server-side.
- **Client-side encryption.** Data is snapshotted and encrypted before it leaves
  for the S3 target. The target ("Buddy S3") only ever sees encrypted, obfuscated
  objects.
- **The user holds the key.** At setup the user gets a Recovery Key to store in
  their password manager. It is the only way *anyone outside the server* can
  decrypt their backup, and the only one that still works when the server is
  gone. The server keeps its own wrapped copy of the same key — that is what
  makes unattended backups possible, and it is a deliberate, documented
  trade-off: this is not a zero-knowledge design.
- **Admin-supported recovery, admin stays blind.** The admin can extract an
  encrypted "Take-Out" of a Space and hand it to the user, who decrypts it offline
  with their Recovery Key. The admin never sees plaintext and cannot restore into
  a user's Space.
- **Snapshots with history and deduplication.** Powered by
  [kopia](https://kopia.io); daily backups of mostly-static data stay cheap.

## Why

No existing self-hosted platform (Nextcloud, ownCloud, oCIS, Seafile, Pydio
Cells) ships a per-Space, client-encrypted, self-service backup. oCIS's native
answer is "stop the whole instance and snapshot the filesystem" — coarse and
admin-only. This plugin fills that gap with per-Space, scheduled, encrypted
backups that primarily defend against the most common real-world disaster:
**a family member's computer getting ransomware-encrypted.**

## How it works (high level)

```
OpenCloud Space  ──(CS3 read)──▶  backup worker  ──(encrypt + dedup, kopia)──▶  Garage S3
     ▲                              (server-side)                                  │
     │                                                                             │
  Path B restore ◀──────────────── user, via Web UI ◀── snapshot history ──────────┘
                                                     └── Path A: admin extracts
                                                         ciphertext, user decrypts
                                                         offline with Recovery Key
```

- **Backup target:** [Garage](https://garagehq.deuxfleurs.fr) (self-hosted,
  S3-compatible).
- **Two recovery paths:** admin Take-Out + offline decrypt (works even when
  OpenCloud is completely down), and user-driven restore back into OpenCloud.
- **Scheduling:** each Space runs on a schedule (nightly by default), unattended
  and server-side, with a per-Space lock so runs never overlap. Runs are staggered
  so they do not all start at once, at most a couple run at a time, and a service
  restart neither repeats a run nor loses one. If a Space stops backing up
  successfully, a notification is raised for its members — a backup that quietly
  died is the failure this is most worried about. Member notifications are
  recorded and served over the API; mailing them needs the user directory and is
  not wired yet, so today the operator's log is where a failure is noticed
  first.
- **Retention that actually happens.** Each Space keeps its snapshots for a time
  window it chooses. Applying that window — expiring what has aged out and
  reclaiming the storage it frees, including what a failed run left behind — is
  its own unattended job, daily by default. It never runs inside a backup run and
  never while one is in flight, and it never gives up a Space's newest complete
  snapshot, however old that snapshot is.
- **No database.** The service keeps its own state (schedules, run history,
  wrapped key envelopes, target records) in a dedicated OpenCloud Space, so a
  deployment needs no second storage system. That Space must not be one an end
  user belongs to — see the runbook below.

## Deployment preconditions

**Supported OpenCloud versions: 7.3.0 to 7.5.0.** The fixture the tests run
against is pinned to 7.5.0 by digest. The service talks to OpenCloud over CS3,
which OpenCloud does not treat as a public interface, so test a new OpenCloud
release against the fixture before upgrading a deployment that runs backups.

The image is published as `ghcr.io/knoppiks/opencloud-backupd` for every
release tag, or built from the `Dockerfile` in this repository (`make image`). It
contains the service
and the admin `takeout` tool, runs as uid 65532, and starts the service with no
arguments — an argument is an operator subcommand, which is how key rotation
runs from the same image. The offline `decrypt` CLI is deliberately not in it;
that one belongs on the user's machine.

Five things the service assumes. It checks the last three at startup and
refuses to run rather than working in a way that looks fine and is not. The
first two it cannot check, so they are on you.

- **TLS in front.** The listener speaks plain HTTP. A Space's Data Key is sent to
  the server once, at key setup — that is the price of unattended backups, and it
  is the only key material that ever crosses the wire (the Recovery Key never
  does). Put the service behind an ingress that terminates TLS on the same origin
  as OpenCloud, or set `TLS_CERT_FILE` and `TLS_KEY_FILE` and let it terminate
  TLS itself. **Nothing enforces this from inside the process** — it cannot see
  what is in front of it, so it logs which mode it started in and trusts you.
- **One origin, shared with OpenCloud, under a path prefix.** The web extension
  runs inside the OpenCloud SPA and calls this service with the user's own
  bearer token. There is no CORS middleware here and no `OPTIONS` route, on
  purpose: a second origin would be one more place the Data Key travels to. So
  the ingress must route a prefix on OpenCloud's origin to this service —
  `BACKUPD_BASE_PATH` (recommended `/backup`, giving `/backup/api/v1/…`) tells
  it which prefix to expect, and the extension's `apiPath` must agree. Without
  the prefix the API would sit at `/api/v1/`, a namespace OpenCloud also owns.
  `/healthz` and `/readyz` stay at the root regardless, because the kubelet
  reaches them on the pod rather than through the ingress.
- **Exactly one instance.** Two instances against one state Space can mark each
  other's runs failed and back up the same Space twice, so the manifest deploys
  with `strategy: Recreate` — a rolling update would run two by design. Each
  instance also announces itself in the state Space and refuses to start while
  another one is live.
- **A memory-backed work directory.** `BACKUP_WORK_DIR` holds kopia's per-run
  cache; the manifest mounts it as an `emptyDir` with `medium: Memory` so nothing
  about a run can outlive the pod. Set `BACKUP_WORK_DIR_ALLOW_DISK=true` to
  accept a disk-backed one. Target credentials are not written there under any
  circumstances — they exist only in process memory.
- **Durable state.** `STATE_SPACE_ID` is required (see below). A deployment that
  really wants throwaway state sets `STATE_BACKEND=memory` and accepts that a
  restart discards every schedule, every run record and every wrapped Data Key.

`OIDC_AUDIENCE` is required alongside `OIDC_ISSUER`. One OpenCloud issuer signs
tokens for several applications, so without it a token minted for something else
opens this API.

`OC_BASE_URL` is required alongside `OIDC_ISSUER` too. A token's subject is not
the caller's OpenCloud user id, and every permission is a statement about that
id, so the service asks OpenCloud's graph API who the caller is (once per
token). `ADMIN_SUBJECT_ALLOWLIST`, if you use it, lists OpenCloud user ids,
despite its name.

Set `TZ` to the household's timezone. Schedules are read in the container's zone,
because "nightly at 02:30" is about the family's night — an unset `TZ` means UTC,
which in Berlin is 03:30 in winter and 04:30 in summer. `SCHEDULE_TIMEZONE`
overrides it for schedules alone, and an unknown zone *there* is refused at
startup; a misspelt `TZ` is not caught by anything and silently means UTC, so
check the startup log says the zone you meant.

## Deployment: the state Space

The service needs one OpenCloud Space of its own, and it is picky about which,
because that Space holds the server-side copy of every wrapped Data Key.

**You cannot create it in the OpenCloud admin UI**, and it is worth saying why
before the command that does. A Space created through the UI or the graph API
leaves *its creator* — your admin account — holding a manager grant, and
OpenCloud refuses to remove the last one ("cannot remove the last share with
manager permissions on a space root"). That is a Space an end user can empty,
which is the one thing this Space must not be.

So the service creates it, as its own service account:

```sh
# Needs CS3_GATEWAY_ADDR, OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET —
# the same values the service runs with. STATE_SPACE_ID must be unset.
backupd provision-state-space
# prints the space id on stdout
```

Set `STATE_SPACE_ID` to the id it prints. The Space it creates is granted to the
service account alone, so no person is a member of it.

The command refuses to run while `STATE_SPACE_ID` is already set. Creating a
second state Space would leave the first one holding every wrapped Data Key with
nothing pointing at it — which looks like a working deployment until somebody
needs a restore.

The service **refuses to start** if the configured Space is a personal Space or
carries a member grant held by anyone other than its own service account. A
member could delete the folder without knowing what it was, and losing it would
mean every unattended backup for the affected Spaces stopping until each user
re-ran the key ceremony with their Recovery Key. (If OpenCloud is not reachable
at startup the check is deferred to first use with a warning — an IdP or gateway
that is a few seconds late must not crash-loop the backup service.)

What lives there is metadata and ciphertext only — wrapped key envelopes and
wrapped target credentials, never plaintext keys. Records whose loss cannot be
repaired are written append-only: a new version each time, the previous one left
untouched, so a crash mid-write cannot destroy one.

The service **refuses to start** without `STATE_SPACE_ID`. Setting
`STATE_BACKEND=memory` instead runs it with in-memory state — schedules, history
and key envelopes then die with the process, which is a smoke-test mode and
nothing else.

## Deployment: installing Backup Vault

The extension ships as an image, `ghcr.io/knoppiks/opencloud-backup-vault-web`,
with the same version as the service; run the two at the same version. It
carries the bundle at the path OpenCloud's own `web-extensions` images use, so
it drops into the same initContainer pattern:

```yaml
initContainers:
- name: init-backup-vault
  image: ghcr.io/knoppiks/opencloud-backup-vault-web:<version>
  # The image's default command copies the bundle into /extensions/backup-vault.
  volumeMounts:
  - { name: extensions, mountPath: /extensions }
# ...then copy /extensions/backup-vault/ into
# <OC_DATA_DIR>/web/assets/apps/ (default /var/lib/opencloud/web/assets/apps/).
```

OpenCloud scans the apps directory **at startup only**, which an initContainer
gets for free. Copying into a running pod does nothing until it restarts.

**OpenCloud's Content Security Policy has to allow WebAssembly.** The Recovery
Key's key derivation (Argon2id) runs in the browser as WebAssembly, and the
default policy of OpenCloud 7.3.0 to 7.5.0 (`script-src 'self' 'unsafe-inline'`) blocks
compiling it. Everything else still works, so the failure shows up late: setup,
"Check my Recovery Key" and the key replacement all fail in the browser. Add
`'wasm-unsafe-eval'` to `script-src` in the CSP file OpenCloud reads
(`PROXY_CSP_CONFIG_FILE_LOCATION`):

```yaml
directives:
  script-src:
  - '''self'''
  - '''unsafe-inline'''
  - '''wasm-unsafe-eval'''
```

OpenCloud uses that file **instead of** its default, so start from your current
policy and add the one line. [`test/fixtures/opencloud/csp.yaml`](test/fixtures/opencloud/csp.yaml)
is the 7.3.0 default plus that line. `'wasm-unsafe-eval'` allows WebAssembly
compilation only. It is not `'unsafe-eval'`: `eval()` and `new Function()` stay
blocked.

## Deployment: step by step

In order, for a Kubernetes deployment next to an existing OpenCloud. `deploy/`
holds the manifests; every `REPLACE_ME` in them is refused at startup.

1. **OpenCloud: expose the gateway and the data server.**
   - Set `OC_GATEWAY_GRPC_ADDR=0.0.0.0:9142` and add port 9142 to a Service.
     Point `CS3_GATEWAY_ADDR` at it.
   - From OpenCloud 7.5 on, also set `STORAGE_USERS_HTTP_ADDR=0.0.0.0:9158` and
     add port 9158 to the Service. Point `CS3_DATA_SERVER_URL` at it, scheme and
     host only (for example `http://opencloud.files.svc.cluster.local:9158`).
     7.5 hands CS3 clients the storage provider's own address, by default
     `http://localhost:9158/data`, instead of the public data gateway. The
     service keeps that path and replaces the scheme and host. OpenCloud's own
     routing is unchanged.
   - Both are plaintext and check the service account's token on every
     request. Keep them on the cluster network.
2. **OpenCloud: the CSP override** (above).
3. **Secrets.** Two fresh 32-byte keys (`SRW_KEY`, `TW_KEY`) and OpenCloud's
   service account id and secret (`service_account_id` and
   `service_account_secret` in `opencloud.yaml`, or `OC_SERVICE_ACCOUNT_ID` and
   `OC_SERVICE_ACCOUNT_SECRET` if you set them). See
   `deploy/secret-wrap-keys.yaml`.
4. **The state Space.** Run `backupd provision-state-space` once, from the
   service image with the service account and `CS3_GATEWAY_ADDR` set, and put
   the id it prints into `STATE_SPACE_ID` (see "the state Space" above).
5. **The service.** `deploy/deployment-backupd.yaml` and
   `deploy/service-backupd.yaml`. With OpenCloud's built-in IdP,
   `OIDC_ISSUER` and `OC_BASE_URL` are OpenCloud's URL and `OIDC_AUDIENCE` is
   `web`. Set `TZ` to the household's zone. The pod has to reach OpenCloud's
   public URL (issuer, graph, and the `/data` gateway reva hands out) and trust
   its certificate.
6. **The route.** `deploy/ingress-backupd.yaml`: path `/backup/api` on
   OpenCloud's own host, to the service, prefix not stripped. It must not be
   `/backup`, because the extension's own pages live under `/backup-vault/`.
7. **The extension** (above), then restart OpenCloud.
8. **A target.** Seed one with `BOOTSTRAP_*` (granted to all users), or add one
   as an OpenCloud admin through `/backup/api/v1/admin/targets` or Backup
   Vault's admin view. The bucket key needs read and write.
   - "Check connection" tells the admin only a coarse verdict, such as
     "Endpoint not reachable". The service log has the cause, in a
     `target connection check failed` line: the name that did not resolve,
     the refused connection, the untrusted certificate, or what the endpoint
     answered. Credentials are never in it.
   - **A destination whose certificate comes from a private CA** is "not
     reachable" until the service trusts that CA. Mount the CA certificate
     into the pod (for example from a ConfigMap) and point `SSL_CERT_DIR` at
     the directory. That adds to the image's public CA bundle. Do not use
     `SSL_CERT_FILE` for this: it replaces the bundle, and the service then
     cannot verify a publicly certified OpenCloud issuer.
9. **Check it.** `/readyz` answers 200 once the gateway is reachable. In
   OpenCloud, Backup Vault appears in the app menu. Set up one Space, run
   "Back up now", and restore from it before relying on it.

## Protecting backups from the credential that writes them

This is the part where the honest answer is longer than the reassuring one.

**What already works, and is what the threat model is actually about.** A family
member's laptop gets ransomware-encrypted, the OpenCloud client dutifully syncs
the encrypted files up, and the next run backs them up. The backup store is
untouched by this, because the credential that reaches it lives in the cluster
and has never been on anybody's laptop. The bad backup is simply one more
snapshot; yesterday's is still there and still restores. That defence is
retention **depth**, it is the reason retention cannot be set below a week, and
it is tested end to end (`TestIntegration_RansomwareDoesNotEvictGoodHistory`).

**What does not work, and cannot be made to work on Garage.** If somebody gets
the target's S3 credentials out of the cluster, nothing in S3 stops them
destroying every backup in the bucket. Garage has exactly three grants, and the
pinned version's behaviour is pinned by test (`TestGarageGrantMatrix`):

| grant | put | get | list | **delete** | administer the bucket |
|---|---|---|---|---|---|
| `--read` | — | yes | yes | — | — |
| `--write` | yes | — | — | **yes** | — |
| `--owner` | — | — | — | — | yes |

Two things follow, and both contradict what an earlier version of this project's
plan assumed. `--write` includes deleting, so there is no "append-only" key.
`--owner` grants no object access at all — it is bucket administration — so it
is not a "more powerful" key that could be reserved for maintenance. And since a
key without `--read` cannot open an encrypted repository, every role this
service has needs `--read --write`, which is the same capability.

**What the credential split gives you.** A target can hold two credentials: one
that backup and restore runs use, and one that only the retention/reclamation
job uses. That separation is real inside this service — a backup run never holds
the maintenance key and vice versa — and it means the two can be revoked and
rotated independently, and that the storage logs tell you which actor did what.
On Garage it is **not** a limit on what either key can do. Do not write it down
anywhere as a blast-radius bound. If you ever point this service at a backend
with real IAM policies, the separation is what lets you make it one.

Set it up by putting both key pairs in the Secret
(`bootstrap-s3-maintenance-access-key-id` and its secret, alongside the ordinary
pair) — or leave it out, in which case one key does both jobs, which is a
perfectly reasonable deployment and the default.

The service asks each target, on every retention run, whether it supports S3
Object Lock. Garage answers no, and that is logged at debug. If a target ever
answers yes, it is logged at info — that line is the signal that this section
can be revisited.

### The real answer today: snapshot the storage host

Out-of-band filesystem snapshots are the only thing that makes a backup
undeletable by the credentials that wrote it. They live on the machine that runs
Garage, not here, and this project does not ship them — it tells you to set them
up.

Put Garage's `metadata_dir` and `data_dir` on ZFS or Btrfs and snapshot them on
a schedule:

```sh
# ZFS. -r over a parent dataset holding both directories, so metadata and data
# are captured at the same instant — Garage's metadata is a live SQLite
# database, and a torn pair is worse than no snapshot.
zfs snapshot -r tank/garage@$(date -u +%Y%m%dT%H%M%SZ)

# Btrfs has no cross-subvolume atomicity. Either keep both directories in one
# subvolume, or stop Garage for the second it takes.
btrfs subvolume snapshot -r /srv/garage /srv/.snapshots/garage-$(date -u +%Y%m%dT%H%M%SZ)
```

Four things decide whether this is protection or theatre:

1. **Keep them at least as long as the deepest Space's retention window.** A
   snapshot layer shallower than the thing it protects has not protected it.
2. **The snapshots must not be destroyable with the credentials that run
   Garage.** That is the entire point. The schedule runs as root on the storage
   host; the backup service has an S3 key and no shell there. If Garage runs as
   root on that host, this buys much less than it looks like.
3. **Send them somewhere else.** `zfs send | ssh` to a second machine, or
   `sanoid`/`syncoid`. A root compromise on the storage host destroys local
   snapshots, and a fire destroys the host.
4. **Test a restore.** Stop Garage, roll back or clone the dataset, start it,
   and run an actual restore of an actual Space through this service. A backup
   path nobody has walked is a guess; that rule applies to this layer too.

What this defends against: leaked or abused S3 credentials, a compromise of this
service, Garage bugs that lose data. What it does not: losing the storage host
itself — which is what item 3 is for — and it is not a substitute for the
household's Recovery Keys, which are the only thing that makes any of this
readable.

## Who may do what

Access follows the Space's own OpenCloud roles — the service never invents its
own permission model, and never trusts what the browser claims:

| | viewer | editor | manager / owner |
|---|---|---|---|
| See status, schedule, history, snapshots | yes | yes | yes |
| Fetch the recovery envelope, restore a backup | yes | yes | yes |
| Back up now, change schedule / retention / target | — | yes | yes |
| Set up the Space's keys, replace the Recovery Key | — | — | yes |

An expired share is not a share: once a grant's expiry passes, that person is
refused like any stranger. Shares given to a **group** work too, but the service
has to ask OpenCloud who is in which group — set `OC_BASE_URL` for that. Without
it, a Space shared with a group tells those members it cannot verify them,
rather than quietly letting them in or shutting them out.

An OpenCloud administrator gets none of this by virtue of being an
administrator; they see a Space's backups only if they are a member of it.

## Recovery runbook

Two ways back. Pick the first one that applies.

### The worst case: OpenCloud is gone (Path A)

Server dead, disks gone, whole deployment unrecoverable — as long as the backup
store still exists, the data is recoverable. This path needs **no OpenCloud, no
database, and no server**: just the S3 store and the user's Recovery Key.

**Step 1 — the administrator extracts the backup.** This moves *encrypted* data
only. The `takeout` tool cannot decrypt anything: there is no option to give it a
key, and the code that could unwrap one is not built into it. An administrator
can never read a user's files.

It ships in the service image (`--entrypoint /takeout`), so this runs from the
cluster the deployment already has, without OpenCloud being up. Credentials come
from the environment, never from flags: a command line is readable by every
process on the machine.

```sh
export S3_ACCESS_KEY_ID=...        # credentials for the backup store
export S3_SECRET_ACCESS_KEY=...

# -prefix is the deployment prefix configured on the target.
# -insecure is needed for a plain-HTTP endpoint, which a self-hosted Garage
# usually is; drop it if the store is behind TLS.
takeout \
  -endpoint buddy.example:3900 \
  -bucket   backups \
  -prefix   oc/ \
  -space    <space-id> \
  -out      ./takeout-alice \
  -insecure
```

The result is a self-contained folder: the encrypted repository, the user's
encrypted key envelope, and a manifest with checksums. Hand it to the user — a
USB stick is fine, it is useless without their Recovery Key.

**Step 2 — the user decrypts it, on their own machine.** `decrypt` uses **no
network at all**. Builds for Linux, macOS and Windows are produced by
`make decrypt-release`.

```sh
decrypt -in ./takeout-alice -list             # which backups are in here?
decrypt -in ./takeout-alice -out ./my-files   # restore the newest one
```

It asks for the Recovery Key (the `ocbk1-…` string from the password manager);
the key is never echoed, never stored, and never sent anywhere. If the key is
wrong it says so and writes nothing.

`decrypt -in ./takeout-alice -verify` checks a Take-Out against its manifest and
needs no key at all.

A Take-Out carries the key envelope the target held when it was made. After a
Recovery Key replacement the target's copy is refreshed by the next backup run,
so a Take-Out made in between still wants the *old* key. Any member can
download the Space's current envelope as `recovery.ocbke` from the Space's
"Recovery Key" page in Backup Vault and hand it to `decrypt`:

```sh
decrypt -in ./takeout-alice -envelope ./recovery.ocbke -out ./my-files
```

The same works for a Take-Out that was forced without an envelope. The file is
ciphertext, as useless without the Recovery Key as the Take-Out itself.

### The normal case: OpenCloud is running (Path B)

The user picks a backup and confirms; the files appear in a new
`Restore/<timestamp>/` folder inside their Space — existing files are never
overwritten or deleted. Only members of a Space can do this; an administrator
cannot restore into someone else's Space, by design.

In Backup Vault this is "Restore files from a backup" on the Space's page. Any
member can use it, viewers included. The page follows the restore to its end
and then links to the folder. The folder is also listed under the Space's
recent activity, including after a failed restore that left part of the files
behind.

The same flow over the API, with the user's own session:
`GET /api/v1/spaces/{id}/snapshots` lists the backups,
`POST /api/v1/spaces/{id}/restore` with `{"snapshot_id":"…"}` starts the restore
as a background job, and `GET /api/v1/spaces/{id}/backup/runs/{job_id}` follows
that job until it is done.

### Replacing a key

Nothing here re-encrypts a backup. A key is replaced by re-wrapping what it
protects, so no data is re-uploaded and no existing backup stops working.

**A user's Recovery Key** (lost paper, a key that was photographed or shared):
the current key unwraps the envelope on the user's own device, a new key is
generated there, and only the re-wrapped envelope is sent back
(`POST /api/v1/spaces/{id}/backup/recovery-key/rotate`). The plaintext of
neither key ever reaches the server, and no backup is re-uploaded. A user who has
*lost* their Recovery Key cannot do this — there is no escrow, by design.

In Backup Vault this is "Replace the Recovery Key", reached from the Space's
"Recovery Key" page, for managers and owners. The new key is shown once and two
of its groups must be typed back before anything is sent. The old key stops
working once the next backup has run, because that run refreshes the envelope
copy on the target; the page offers "Back up now" for that reason. After that,
destroy the old key. Other members of a shared Space need the new one.

The request names the envelope it replaces (`replaces_sha256`, a hash of the
ciphertext). If someone else replaced the key in the meantime, the service
answers 409 instead of silently discarding one of the two new keys.

The same page offers every member "Check my Recovery Key". It tries a key
against the Space's envelope in the browser and says whether it opens the
backups; nothing is sent. It is the way to find out that a key is lost before
it is needed.

Setting up a Space again is **not** a way to fix a lost key. The service refuses
it, on purpose: a second setup would install a new Data Key and every existing
backup for that Space would become unreadable.

**The server's own keys** (`SRW_KEY`, `TW_KEY` — a leaked secret, a departing
admin, a cluster restored from a snapshot). Stop the service first, then run the
command from a pod with the service's **full environment** — it reads and
rewrites records in the state Space, so it needs `CS3_GATEWAY_ADDR`,
`STATE_SPACE_ID` and the service account exactly as the service does. Only the
key variables change:

```sh
# Data-key custody. Both variables must be set; the old one is retired.
SRW_KEY_OLD=<current> SRW_KEY=<new> backupd rotate-srw -service-stopped

# Target-credential custody.
TW_KEY_OLD=<current> TW_KEY=<new>  backupd rotate-tw  -service-stopped
```

The command refuses to run while any backup still holds a lease. It is safe to
re-run: an interrupted rotation is finished by running it again. When it
succeeds, remove the old key from the deployment — it opens nothing any more.
Users are unaffected and need do nothing.

### Keep this in mind

- **The Recovery Key is the whole story.** The server cannot reconstruct it. Lost
  key plus lost server means lost data — store it in a password manager now.
- **History has a floor.** Retention can be shortened but not switched off: at
  least a week is always kept. Depth is what makes ransomware survivable, and it
  is not something a browser session should be able to give away.
- **Nothing in S3 stops a stolen target credential deleting backups.** Garage
  cannot express a key that writes without deleting. Snapshot the storage host —
  see the section above; it is the only real answer available today.
- Every backup run republishes the encrypted key envelopes to the target: the
  user's recovery envelope, so a Take-Out is always self-contained, and the
  service's own envelope, so losing the state Space costs a re-configuration
  rather than the ability to run unattended backups at all.
- Keep a copy of the `decrypt` binary somewhere that is not the server you are
  trying to recover.

## Developing the web extension

The UI is an OpenCloud Web extension in [`web/`](web/). It runs *inside* the
OpenCloud SPA and calls this service with the signed-in user's own bearer token,
which is why it has to share OpenCloud's origin — see the preconditions above.

```sh
make web-install                 # dependencies (pnpm via Corepack)
make web-lint web-typecheck web-test web-build
```

To see it in a browser, against the pinned OpenCloud:

```sh
make dev-up                      # Garage + OpenCloud 7.5.0 + the fixture proxy, seeded
make web-install-fixture         # build, install into the fixture, verify it registered
```

`web-install-fixture` restarts OpenCloud, because the apps directory is scanned
at startup only — a bundle dropped in while it is running is invisible, with no
error anywhere. It then checks that the app appears in `config.json`'s
`external_apps` carrying its `config.apiPath`, and that the entry chunk serves
200. `make web-verify-fixture` repeats the check without rebuilding.

The fixture's `:9200` is a Caddy proxy providing the single origin: OpenCloud
everywhere, and `/backup/*` to a `backupd` on the host at port 8080. OpenCloud
itself is on `:9201` if you need to bypass the proxy. To run the service behind
it:

```sh
source test/fixtures/opencloud/fixture.env   # written by up.sh
backupd provision-state-space                # once; then add the id to fixture.env
source test/fixtures/opencloud/fixture.env
go run ./cmd/backupd
```

`fixture.env` carries `OIDC_AUDIENCE=web` (the SPA's client id — anything else
401s every request), `BACKUPD_BASE_PATH=/backup` to match the proxy, and
`SSL_CERT_FILE` pointing at the proxy's extracted CA root so the service can
verify the OIDC issuer without turning verification off.

`backupd` runs on the host rather than in the compose stack on purpose: tokens
carry `iss: https://localhost:9200`, and inside a container `localhost` is that
container.

### End-to-end tests in a real browser

```sh
make dev-up && make web-install-fixture      # preconditions, as above
(cd web && pnpm exec playwright install chromium)   # once
make e2e
```

`make e2e` starts its own `backupd` on `:8080`, so stop one started by hand
first. That instance keeps its state in a CS3 state Space, as a real deployment
does, under a fresh prefix per run. It generates wrap keys per run and uses a
fresh repository prefix in the dev Garage. If `fixture.env` has no
`STATE_SPACE_ID`, the first run provisions a state Space and appends its id
there. It builds `takeout` and `decrypt` alongside.

The run goes through the flow in order:

1. An admin creates the destination through Backup Vault and grants it to
   `testuser`.
2. `testuser` protects their personal Space. First a wrongly retyped Recovery
   Key must stop the setup, then the real setup completes.
3. `testuser` backs up and restores.
4. The Recovery Key from the browser opens a take-out with `decrypt`, both
   before and after replacing the key.

Every request the browser sends is checked for any form of any Recovery Key
shown. Set `E2E_CHROME=/usr/bin/google-chrome` to use a system Chrome instead of
Playwright's.

## Documentation

- Design decisions, trust model, and threat model:
  [`.agents/plan/decisions.md`](.agents/plan/decisions.md)
- Phased implementation roadmap: [`.agents/plan/`](.agents/plan/)

## License

TBD.
