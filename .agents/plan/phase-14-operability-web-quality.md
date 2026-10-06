# Phase 14 — Operability & Web Quality

**Goal:** an operator can see that backups are healthy without reading logs,
the state Space stops growing without bound, and Backup Vault works for
everyone in the household — keyboard and screen-reader users, German
speakers, slow phones, and browsers other than Chromium.

**Depends on:** Phase 10 (logging, config), Phase 11 (versioned releases).
Runs in parallel with Phase 13. **Findings:** review-2026-10.md M1–M4,
W1–W8.

Two independent tracks; each can ship as its own PRs.

---

## Track A — Operability

### A.1 Metrics

`/metrics` (Prometheus text format) on a **separate listener**
(`METRICS_ADDR`, default off or `:9090`), never on the API listener — the
ingress routes `/backup/api` and nothing under it should expose internals,
and the compose add-on keeps the port on the internal network.

Metrics, all cheap to compute from what the service already records:

- `backupd_last_success_timestamp_seconds{kind}` and
  `backupd_runs_total{kind,outcome}` — `kind` ∈ backup/restore/prune;
- `backupd_run_duration_seconds{kind}` histogram; bytes and files processed;
- `backupd_spaces_configured`, `backupd_spaces_stale` (same
  `notify.StaleRule` as the board — one definition, decisions 8d.1);
- scheduler tick errors, lease recoveries, unreadable state records;
- build info (`version`, `commit`) and observed OpenCloud version.

**DECISION NEEDED — per-Space labels.** R8 settled that the *service log*
names Spaces because its reader is the operator. Metrics have the same
reader, so `space` labels would be consistent with R8 and make "which Space
is stale" answerable from Grafana. Against: metrics are more often shipped to
third-party systems than logs, and #15's spirit is that the admin role does
not learn about Spaces. Proposal: aggregated by default, per-Space behind
`METRICS_PER_SPACE=true`, with the docs stating the trade-off exactly as R8
does.

Example Grafana dashboard + alert rules (stale backup, failure rate) in
`deploy/monitoring/`, matching opencloud-compose's monitoring overlay.

### A.2 Logs

- `LOG_LEVEL` (debug/info/warn/error) and `LOG_FORMAT` (json/text).
- Wire kopia's logger (`repo/logging`) into slog at debug, filtered so no
  repository password or blob content can appear (test with the `noleak`
  helpers).
- A documented list of log lines worth alerting on (Phase 13 links it).

### A.3 R10 — state-Space revision growth

Execute `remediation-plans.md` R10 as written: spike first (can a revision
be reclaimed at all on 7.2–8.x; `PurgeRecycle` reachability with the service
account), measure, then pick the option. Provisional recommendation stays
A + D (slow the heartbeats, document the residual), C for the instance record
only. Must keep the lease-expiry < recovery invariant (R6).

### A.4 Resource behaviour

- Measure a backup of a large Space (≥ 50 k files, ≥ 50 GB) in the compose
  add-on's limits: peak RSS, work-dir tmpfs use, duration. Record the numbers
  in the operator docs as sizing guidance. Closes deployment-readiness
  "first containerised run".
- Opt-in `pprof` on the metrics listener (`PPROF=true`).

### A.5 Gateway transport

The gRPC connection to OpenCloud's gateway is plaintext-only and carries the
service-account token (M4). **Spike:** does OpenCloud's gateway accept TLS
(`OC_GATEWAY_GRPC_TLS…`) on 7.2–8.x? If yes, add `CS3_GATEWAY_TLS` + CA
option; if no, the docs keep saying "internal network only" and the item
goes to the backlog.

---

## Track B — Web quality

### B.1 Accessibility (W1)

- Focus management: on every step change in wizard, restore, rotation,
  retention editor and target form, move focus to the new step's heading
  (`tabindex="-1"`); on validation failure, to the first invalid field.
- Live regions mounted empty and filled afterwards; one summary alert on the
  overview instead of one per failed row; "Copied" announced.
- User picker: announce search state and result count; per-person accessible
  names on Add/Remove.
- Heading order (`LostKeyNotice`).
- `eslint-plugin-vuejs-accessibility`; `@axe-core/playwright` on every page
  of the screenshot tour, failing on serious/critical.

### B.2 Language (W2)

- The server already sends stable error codes; the bundle maps **codes** to
  translated text and stops displaying server `message`s to users (kept in
  a "details" disclosure for support). `last_error` becomes a code + safe
  parameters on the API side (API level bump, Phase 11's `/version`).
- Plurals via `$ngettext`; the translation spec learns to extract
  `$ngettext`/`$pgettext`.
- `admin/wording.ts` stops parsing an English sentence: the server returns
  the count as a field.

### B.3 The CSP failure, diagnosed (W3)

Detect a WebAssembly compile failure caused by CSP
(`CompileError`/`securitypolicyviolation` event naming `script-src`) and show
a specific message: to members "Backups can't be set up on this server yet —
tell your administrator", to admins the exact CSP line and a docs link.
Covered by an E2E leg that serves the default CSP.

### B.4 Argon2id off the main thread (W4)

Run the derivation in a module Web Worker. **Measure first:** OpenCloud's
CSP `worker-src`/`script-src` must allow it, and the worker must load
`hash-wasm` from the bundle's own origin. If the CSP forbids workers, record
it and keep the main-thread path with a progress indicator. Time the
derivation on a low-end Android phone and a 2016-era laptop; record numbers
next to the Argon2id defaults (decision #19).

### B.5 Tests and lint (W5, W6)

- Vitest coverage on, thresholds set at the measured value (ratchet, never
  lowered).
- Type-aware ESLint (`recommendedTypeChecked`: `no-floating-promises`,
  `no-misused-promises`).
- E2E: Firefox and WebKit projects; a mobile-viewport project; a German
  locale run of the journey; clipboard and real downloads asserted; the app
  menu entry clicked; a shared Space with a viewer and an editor member.
- Go coverage gate (Phase 10 started reporting it), same ratchet rule.

### B.6 Secret-hygiene nits (W8)

`autocomplete="off"` + `spellcheck="false"` on gate inputs; wipe the Data Key
on self-verification failure; `recovery.ocbke` download named
`recovery-<space-name>.ocbke`.

---

## Exit criteria

- [ ] `/metrics` on its own listener; dashboard + alerts shipped; per-Space
      label decision recorded.
- [ ] R10 spike recorded and chosen option implemented and documented.
- [ ] Sizing numbers from a large-Space run published.
- [ ] axe clean (serious/critical) on every page; focus test per flow.
- [ ] No server English shown in the German UI (E2E in `de`).
- [ ] CSP failure diagnosed in-app (E2E).
- [ ] Argon2id in a worker, or the measured reason why not.
- [ ] E2E green on Chromium, Firefox, WebKit and mobile viewport; coverage
      ratchets in place.
