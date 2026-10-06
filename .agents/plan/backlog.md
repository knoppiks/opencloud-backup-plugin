# Backlog

Work that is wanted, not scheduled into a phase. Each item says why it is not
in a phase yet. Moving an item into a phase is a decision taken with the
owner; items here are not promises.

| Item | Source | Why not now |
|---|---|---|
| **Phase 5b — re-attach after an OpenCloud rebuild.** User supplies the Recovery Key; the browser fetches `recovery.ocbke` of an old Space id from a granted target, unwraps the DK and sets up the *new* Space pointing at the old repository; server checks the DK opens it. Turns "OpenCloud rebuilt ⇒ Path A only" into "Path B after one ceremony". | remediation-plans.md, cross-cutting | **DECISION NEEDED** (open since September): recommendation there was v1. If it is v1, it belongs before 1.0.0 (compatibility-policy §2) — slot it after Phase 12. |
| **Single-file / partial restore** and overwrite-in-place | decisions #3 | v1 is full restore by decision; kopia supports per-file restore, so it is UI + API work. |
| **Object Lock path** against a capable backend | decisions #5, issue #33 | No backend in the test environment implements Object Lock; untested code is not shipped. |
| **Live progress** during a run | Phase 6 amendment, phase-8 | Needs kopia's uploader progress wired through the engine; status board shows "running since". |
| **Member notifications by mail** | Phase 6 amendment | Needs the user directory (graph) for addresses and a decision on what a mail may say. |
| **OpenCloud notification service as a sink** | Phase 6 amendment | No verified API for an external plugin; spike first. OpenCloud 8.0's announcement banner is another candidate surface for operator notices. |
| **Batched overview** (one request instead of one per Space) | phase-8 | Fine at family scale; revisit if a household has dozens of Spaces. |
| **Notification list on the status board** | phase-8 | Notifications are served by the API but not shown. |
| **Editing Space grants** from Backup Vault | phase-8 | Shown read-only; OpenCloud's own UI does it. Probably never. |
| **Restore folder named after the snapshot time**, not the restore start | phase-8 | Small; needs a filename-safe rule both sides agree on. |
| **macOS notarisation / Windows signing of `decrypt`** | Phase 11 | Needs paid developer accounts; checksums + cosign cover integrity. |
| **Recovering from `server.ocbke`** as a flow instead of a manual operator step | R1 amendment | Rare; documented as manual. Would pair well with 5b. |
| **TLS to the CS3 gateway** | Phase 14 A.5 | Only if the spike shows OpenCloud supports it. |
| **Helm chart** of our own | Phase 12 | Compose is primary, Kustomize secondary; a chart only if Kubernetes users ask, and then as values for OpenCloud's chart rather than a parallel one. |
| **Contribution upstream**: `'wasm-unsafe-eval'` in opencloud-compose's default CSP | Phase 12 option C | Tracked there once decided. |
| **A third UI language** | translations.ts | Revisit the `.po` pipeline decision when it happens. |
