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
> user-triggered restore back into their Space. Scheduling and the Web UI are
> still to come. The design and phased roadmap live in
> [`.agents/plan/`](.agents/plan/).

## What it does

- **One-click, then forget.** The user enables backup for a Space and picks a
  schedule; scheduled runs happen unattended, server-side.
- **Client-side encryption.** Data is snapshotted and encrypted before it leaves
  for the S3 target. The target ("Buddy S3") only ever sees encrypted, obfuscated
  objects.
- **The user holds the key.** At setup the user gets a Recovery Key to store in
  their password manager. It is the only thing that can decrypt their backup.
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
  successfully, its members are told — a backup that quietly died is the failure
  this is most worried about.
- **No database.** The service keeps its own state (schedules, run history,
  wrapped key envelopes, target records) in a dedicated OpenCloud Space, so a
  deployment needs no second storage system. That Space must not be one an end
  user belongs to — see the runbook below.

## Deployment preconditions

Four things the service assumes. It checks each of them at startup and refuses to
run rather than working in a way that looks fine and is not.

- **TLS in front.** The listener speaks plain HTTP. A Space's Data Key is sent to
  the server once, at key setup — that is the price of unattended backups, and it
  is the only key material that ever crosses the wire (the Recovery Key never
  does). Put the service behind an ingress that terminates TLS on the same origin
  as OpenCloud, or set `TLS_CERT_FILE` and `TLS_KEY_FILE` and let it terminate
  TLS itself.
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

Set `TZ` to the household's timezone. Schedules are read in the container's zone,
because "nightly at 02:30" is about the family's night — an unset `TZ` means UTC,
which in Berlin is 03:30 in winter and 04:30 in summer. `SCHEDULE_TIMEZONE`
overrides it for schedules alone, and an unknown zone is refused at startup.

## Deployment: the state Space

The service needs one OpenCloud Space of its own, and it is picky about which,
because that Space holds the server-side copy of every wrapped Data Key.

1. Create a **project Space** for the service — for example "Backup service
   state" — and add **no members** to it. The service account reaches it with
   owner scope; nobody else needs to.
2. Set `STATE_SPACE_ID` to that Space's id.

The service **refuses to start** if the configured Space is a personal Space or
carries any member grant. A member could delete the folder without knowing what
it was, and losing it would mean every unattended backup for the affected Spaces
stopping until each user re-ran the key ceremony with their Recovery Key.

What lives there is metadata and ciphertext only — wrapped key envelopes and
wrapped target credentials, never plaintext keys. Records whose loss cannot be
repaired are written append-only: a new version each time, the previous one left
untouched, so a crash mid-write cannot destroy one.

The service **refuses to start** without `STATE_SPACE_ID`. Setting
`STATE_BACKEND=memory` instead runs it with in-memory state — schedules, history
and key envelopes then die with the process, which is a smoke-test mode and
nothing else.

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
only. The `takeout` tool cannot decrypt anything; there is no option to give it a
key, so an administrator can never read a user's files.

```sh
export S3_ACCESS_KEY_ID=...        # credentials for the backup store
export S3_SECRET_ACCESS_KEY=...

takeout \
  -endpoint buddy.example:3900 \   # the S3 endpoint
  -bucket   backups \
  -prefix   oc/ \                  # as configured on the target
  -space    <space-id> \
  -out      ./takeout-alice
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

### The normal case: OpenCloud is running (Path B)

The user restores from the web UI: pick a backup, confirm, done. The files
appear in a new `Restore/<timestamp>/` folder inside their Space — existing files
are never overwritten or deleted. Only members of a Space can do this; an
administrator cannot restore into someone else's Space, by design.

Under the hood: `GET /api/v1/spaces/{id}/snapshots` lists the backups,
`POST /api/v1/spaces/{id}/restore` with `{"snapshot_id":"…"}` starts the restore
as a background job.

### Replacing a key

Nothing here re-encrypts a backup. A key is replaced by re-wrapping what it
protects, so no data is re-uploaded and no existing backup stops working.

**A user's Recovery Key** (lost paper, a key that was photographed or shared):
replace it from the web UI. The browser asks for the current Recovery Key,
unwraps the envelope locally, and stores a new one. The old key stops working;
the plaintext of neither key ever reaches the server. A user who has *lost* their
Recovery Key cannot do this — there is no escrow, by design.

Setting up a Space again is **not** a way to fix a lost key. The service refuses
it, on purpose: a second setup would install a new Data Key and every existing
backup for that Space would become unreadable.

**The server's own keys** (`SRW_KEY`, `TW_KEY` — a leaked secret, a departing
admin, a cluster restored from a snapshot). Stop the service first; the same
image runs the command:

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
- Every backup run republishes the encrypted key envelopes to the target: the
  user's recovery envelope, so a Take-Out is always self-contained, and the
  service's own envelope, so losing the state Space costs a re-configuration
  rather than the ability to run unattended backups at all.
- Keep a copy of the `decrypt` binary somewhere that is not the server you are
  trying to recover.

## Documentation

- Design decisions, trust model, and threat model:
  [`.agents/plan/decisions.md`](.agents/plan/decisions.md)
- Phased implementation roadmap: [`.agents/plan/`](.agents/plan/)

## License

TBD.
