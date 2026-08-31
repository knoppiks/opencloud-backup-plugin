# opencloud-backup-plugin

A self-service, encrypted backup extension for [OpenCloud](https://opencloud.eu)
(oCIS / Reva architecture).

Built for the **self-hosting parent running OpenCloud for the whole family** —
not the enterprise. A user clicks "back up my data" once; backups then run
automatically to an external S3 target. The admin can operate and recover
backups **without ever seeing the plaintext content** of a user's data.

> Status: **in development.** The backup pipeline works end to end — a seeded
> OpenCloud Space is snapshotted, encrypted and deduplicated onto an S3 target
> and restores byte-identically. Restore CLIs, scheduling and the Web UI are
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

## Documentation

- Design decisions, trust model, and threat model:
  [`.agents/plan/decisions.md`](.agents/plan/decisions.md)
- Phased implementation roadmap: [`.agents/plan/`](.agents/plan/)

## License

TBD.
