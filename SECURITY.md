# Security Policy

This project backs up a family's files and holds the keys to them. A
vulnerability in it can mean someone reads data that should have stayed
encrypted, or that a backup is gone when it is needed. Please report one
privately, as described below, and not in a public issue.

## Supported versions

While the version is 0.x, only the **newest published release** gets
security fixes. A fix ships as a new release; nothing is backported to older
releases, and release candidates (`-rc.N`) are never fixed: move to the
release that follows them.

| Version | Supported |
|---|---|
| Newest release on the [releases page](https://github.com/knoppiks/opencloud-backup-plugin/releases) | yes |
| Older releases | no |
| Release candidates (`vX.Y.Z-rc.N`) | no |

The plugin is supported on the OpenCloud versions its release names; the
window and how it moves are in the
[compatibility policy](.agents/plan/compatibility-policy.md) and the README's
[deployment preconditions](README.md#deployment-preconditions). A report
that needs an OpenCloud version outside that window is still welcome; say
which version you ran.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting:
[**Report a vulnerability**](https://github.com/knoppiks/opencloud-backup-plugin/security/advisories/new)
(the "Security" tab of the repository). Only the maintainer sees the report.

Please include:

- the plugin version (`backupd version`, `decrypt -version`, or the version
  Backup Vault's destinations page shows an admin) and the OpenCloud version;
- what an attacker needs (which role, which credential, which network
  position) and what they gain;
- steps to reproduce, or a proof of concept.

Do not put a real Recovery Key, key file, S3 credential or service-account
secret in the report. If one was exposed, say so and rotate it (the README's
[Replacing a key](README.md#replacing-a-key)); the maintainer never needs it.

## What to expect

This is maintained by one person in their own time, so responses are best
effort:

- an acknowledgement within **14 days**;
- an assessment (accepted or not, and why) once the report is understood;
- for an accepted report, a fix in a new release, a GitHub security advisory
  that credits you (unless you prefer not to be named), and a CVE where it
  applies.

Please keep the details private until the fixed release is out. If a fix takes
long, the maintainer agrees a disclosure date with you.

## Scope

In scope, and the most important to hear about:

- **Key handling.** The plaintext Recovery Key leaving the user's browser or
  the `decrypt` CLI; a Data Key, wrap key (SRW, TW), wrapped key blob, S3
  credential or service-account secret written to a log, an error message, the
  API or the admin view; key material not cleared after use; a flaw in how
  key envelopes or Take-Outs are parsed or verified.
- **The ciphertext-only admin boundary.** An OpenCloud admin, through Backup
  Vault or the API, obtaining the plaintext of a user's backup, restoring into
  a Space they are not a member of, or obtaining a key that would let them
  decrypt. The admin path is designed to hand out ciphertext only.
- **Access between users.** A member seeing, restoring or changing the
  backups of a Space they do not belong to; anyone acting without signing in.
- **Backups that silently fail.** Anything that makes a backup or retention run
  report success while the data is not recoverable.
- **The release.** A way to get a modified binary or image past the
  README's [verification steps](README.md#verifying-a-release).

Out of scope, because it is documented behaviour rather than a flaw:

- **Garage has no Object Lock.** Whoever holds a target's S3 credentials can
  delete every backup in its bucket; no grant on Garage writes without
  deleting. The README's
  [Protecting backups from the credential that writes them](README.md#protecting-backups-from-the-credential-that-writes-them)
  explains this and what to do instead (snapshots of the storage host).
- **The server can decrypt.** It keeps a wrapped copy of each Data Key so that
  backups run unattended; this is not a zero-knowledge design. Whoever
  controls the cluster (its Secrets and the running service) can therefore
  decrypt. The boundary is the OpenCloud admin *role*, not the cluster
  operator.
- **The OpenCloud service-account secret** gives read access to every Space
  in OpenCloud itself; leaking it exposes the data without this project being
  involved.
- **Vulnerabilities in OpenCloud, kopia, Garage or another dependency** belong
  to that project. Report them there; report here too if this plugin makes
  one reachable that otherwise would not be.
- Versions that are not supported (above), and reports from automated
  scanners without a way the shipped code is affected.
