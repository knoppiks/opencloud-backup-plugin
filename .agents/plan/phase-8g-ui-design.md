# Phase 8g — OpenCloud-native UI

Closes the last open Phase 8 item: "UI reviewed against OpenCloud design system
(native look — success metric 7)" (`phase-8-web-ui.md`, exit criteria;
`decisions.md` success criterion 7).

Depends on: 8a–8f, #56 (the SDK's Tailwind CSS is imported; before it no `ext:`
class applied at all).

## Starting point (October 2026)

Compared with the Files app (left nav, header bar, padded content, tables):

- **No app shell.** Pages start flush left, no sidebar; navigation is ad-hoc
  links ("Back to Backup Vault", "All spaces", "Manage backup destinations").
- **Four design-system components in use** (`oc-button`, `oc-text-input`,
  `oc-spinner`, `oc-progress`). Radios, checkboxes, the weekday select and the
  time input are native; alerts are hand-made `role=alert` blocks; delete is an
  inline confirm.
- **Status is plain text** ("Protected", "Not backed up").
- **Lists are `<ul>`s** where OpenCloud uses tables (spaces, runs, destinations).
- DS 7.4 dropped the legacy `oc-*` utility classes and `--oc-space-*` tokens.
  The supported way is the SDK's `ext:`-prefixed Tailwind with `--oc-role-*`
  colour roles, which the views already use.

## Decisions (owner, 2026-10-05)

1. **Left nav via `navItems`:** "Spaces" (overview, active for `/space/*`) and
   "Backup destinations" (admins only, same ability rule as `useIsAdmin`; the
   server's 403 still decides). Ad-hoc back/manage links go; nested pages get a
   breadcrumb.
2. **Overview is a table** (like Files), not a card grid.
3. **Full swap to design-system controls**, accepting spec/e2e churn.
4. **Space detail is single column, sectioned** — no Files-style right sidebar.
5. **Space actions as buttons:** "Back up now" filled; "Restore files" and
   "Recovery Key" outline. No kebab menu.
6. **Screenshot tour kept in CI** as an e2e artifact (no pixel assertions).
7. **Admins without a granted destination** get their own notice ("No backup
   destination is available to you yet.") with an action to the admin view,
   instead of "ask your administrator".
8. **Two PRs, one issue each** (A: shell + overview + detail; B: forms/flows).

## Rules for every change

- Only host globals (`oc-*`) and web-pkg exports (shared singleton). Never
  import `@opencloud-eu/design-system` (would bundle a second copy; see
  `composables/useBackupApi.ts`).
- Layout with `ext:` Tailwind and role colours only. No hex values, no
  `<style>` blocks.
- Every page: header (breadcrumb/title, optional status, actions on the right),
  then sections with `h2`.
- Status is never colour alone: tag + icon + text.
- Selectors are `data-testid`; every new `oc-*` stub in `src/test/host.ts`
  passes attributes through and keeps a native element underneath where specs
  need `setValue`/`checked`.
- No key material in any new component; credentials stay write-only (#14);
  Recovery Key wording and gate behaviour unchanged (8d.4, #55).

## PR A — app shell, shared parts, overview, space detail

1. **Screenshot tour** (`web/e2e/screens.e2e.ts`): every route as user and as
   admin, PNGs into the e2e artifact. First run is the post-#56 baseline.
2. **Left nav** (`navItems` in `src/index.ts`). First verify the host draws the
   sidebar for a federated extension; fallback is a `sidebarNav` extension. If
   neither works, stop and re-plan.
3. **Shared parts:** `PageLayout.vue` (header, status slot, actions slot),
   `StatusTag.vue` (`oc-tag` + `oc-icon` per `spaceState`), notices as
   `oc-notification-message`, empty states via `NoContentMessage`. Stubs for
   `oc-tag`, `oc-icon`, `oc-table`, `oc-notification-message`,
   `oc-breadcrumb`.
4. **Overview:** `oc-table` — Space, Type, Status, Last backup, Next backup,
   Action — with a summary footer. A row's failed status shows in that row
   only. No-destination notice per decision 7.
5. **Space detail:** header (name, `StatusTag`, actions per decision 5),
   notifications (running with `oc-progress`, stale, last error), summary
   panel (last, next, retention with inline edit), "Recent activity" as
   `oc-table` (Type + trigger, Status, When, Details).

## PR B — forms and flows

6. **Setup wizard:** step indicator; `oc-radio` (destination, schedule),
   `oc-select` (weekday), time input (`oc-text-input type=time` if supported).
7. **Restore:** snapshot picker as table with row radio (date, files, size);
   confirm step as summary panel; outcome as notification.
8. **Recovery Key / Replace:** layout only.
9. **Admin:** destinations as `oc-table`; edit page sections Connection / Keys /
   Audience / Danger zone; delete via `oc-modal`; `oc-checkbox`, `oc-radio`;
   user-picker results with `oc-avatar`.
10. Stubs for `oc-radio`, `oc-checkbox`, `oc-select`, `oc-modal`.

## Exit criteria

- `make web-test web-lint web-typecheck`, e2e and `make secret-scan` green.
- Screenshot tour reviewed side by side with Files, light and dark theme.
- New pure helpers and components unit-tested (`StatusTag`, `PageLayout`,
  nav visibility, table cell mapping).
- PR B: tick the design-system exit criterion in `phase-8-web-ui.md`; update
  the "pinned against 7.3.0" comment in `web/vite.config.ts`.

## Progress

- [x] PR A — issue #59
  - [x] Screenshot tour (`web/e2e/screens.e2e.ts`, project `screens` after
    `journey`; CI artifact `ui-screens`). Waits for no visible `.oc-spinner`,
    not `networkidle` (OpenCloud holds an SSE stream open). Viewport
    1440×1400: the host scrolls inside its own container, so `fullPage`
    cannot capture more.
  - [x] Left nav. **Verified on the 7.5.0 fixture:** the host draws it; admin
    sees both items, the family member only "Spaces"; "Spaces" is active on
    Space pages. Implemented as `sidebarNav` extensions plus the
    `app.backup-vault.navItems` extension point returned from `setup()`
    (`src/navigation.ts`), not `navItems`: the host builds an item's
    extension id from its `name`, which must be a function to follow a
    language change.
  - [x] `PageLayout` on every view (full width; `narrow` caps forms at
    `max-w-2xl`). The breadcrumb trail is the title, as in Files; the `h1`
    repeats its last crumb, visually hidden. Trails built in
    `src/layout/breadcrumbs.ts`. Findings: the host's breadcrumb folds
    trails of 3+ crumbs into "…" unless `truncation-offset` is raised, and
    bolds only a *linked* current crumb, so `PageLayout` bolds
    `[aria-current=page]` itself. The class-based flows' `spaceName` is not
    reactive and slot content renders in the child, so those views compute
    their trail from `state` explicitly.
  - [x] "Back to …" / "Manage backup destinations" links removed; e2e reaches
    the admin view through the nav item.
  - [x] Shared parts. **Deviations from the plan, on purpose:**
    `oc-tag` only knows primary/secondary/tertiary and
    `oc-notification-message` is a dismissable toast (close button, timeout,
    always the "information" icon), so `ToneTag`/`StatusTag` and
    `NoticeBanner` draw the same shapes from theme roles
    (`src/layout/tone.ts`; the fixture theme defines `errorContainer`).
    `EmptyState` mirrors web-pkg's `NoContentMessage` instead of importing
    it, so component tests do not load web-pkg. Warning uses the tertiary
    container (purple in the default theme): the theme has no warning role;
    icon and words carry the meaning.
  - [x] Overview as `oc-table` (`status/overviewrow.ts`), admin variant of the
    no-destination notice. `SpaceCard` retired.
  - [x] Space detail: state and actions in the header, notices, summary
    cards, recent activity as `oc-table` (`status/runtext.ts`).
  - [x] Full e2e green (13 tests) on the fixture, screenshots reviewed in
    light and dark.
- [x] PR B — issue #60
  - [x] Host stubs for `oc-radio`, `oc-checkbox`, `oc-select`, `oc-avatar`,
    measured against design system 7.4: radio and checkbox put attributes
    on a wrapping `<span>` (the native input sits inside, so selectors are
    `[data-testid=…] input`); `oc-select` is vue-select, its model is the
    option object, and `data-testid` lands on its outer element *and* on
    vue-select's root (`.first()` in e2e). `src/test/modals.ts` fakes
    web-pkg's modal store.
  - [x] Setup wizard: `StepIndicator` (always three steps; an automatically
    chosen destination shows as done; none outside the sequence —
    `wizard/progress.ts`), `oc-radio` for destination and how often,
    `oc-select` for weekday and time. Admins without a destination get the
    overview's admin notice and action (decision 7) instead of "ask your
    administrator".
  - [x] Restore: picker as `oc-table` with a radio column (hidden label "Backup
    from …"; `restore/snapshotrow.ts`), confirm step as a summary panel,
    outcomes as notices.
  - [x] Recovery Key / Replace / gate: layout only. Every `$gettext` msgid in
    those files is unchanged against `main` (checked by script); the hand-made
    `role=alert` paragraphs became `NoticeBanner`s that keep their role and
    test ids.
  - [x] Admin: destinations as `oc-table` (`admin/targetrow.ts`) with "Add a
    backup destination" in the header actions; edit page sections
    Connection / Access keys / Who can back up here / Danger zone;
    `oc-checkbox`, `oc-radio`, `oc-avatar` for people.
  - [x] Also (owner, 2026-10-05): `RetentionEditor`'s save error is an
    `ActionError`, `ConnectionCheck`'s results a notice (success only when
    every pair works, `checkTone`).
  - [x] e2e: selectors moved; the journey picks weekly/Wednesday/03:00 with the
    host's controls and asserts the PUT; a new admin test deletes a
    throwaway destination through the dialog (cancel first); the tour adds
    the restore confirm step and the delete dialog. Viewport raised to
    1440×1900 so the destination page fits.

  **Deviations from the plan, on purpose:**
  - **No time field in the design system** (`OcTextInput.type` has no
    `time`, `OcDatepicker` is dates only). The time is an `oc-select` of half
    hours (`wizard/timeoptions.ts`). A stored time off that grid is added to
    the list, not rounded, so opening the wizard never changes it.
  - **Delete via web-pkg `useModals().dispatchModal`**, not an `<oc-modal>` in
    the template: it is what Files uses (focus trap, Esc, stacking). The
    delete runs in `onConfirm` and never throws into it, so the dialog
    closes whatever the answer and the outcome (in use, error) shows on the
    page. On 7.5.0 the host's dialog has no Cancel button, only the close
    "×" (accessible name "Cancel").
  - **`NoticeBanner.title` is optional**: one-sentence notices are said as
    the message, not in bold.
  - Connection and keys stay one form with one save: the server takes them
    in one request.
