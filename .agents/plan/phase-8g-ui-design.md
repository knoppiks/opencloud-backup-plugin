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

- [ ] PR A — issue #59
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
  - [ ] Finding for `PageLayout`: `main` does not fill the app container when
    its content is narrow (admin destinations page); needs `ext:w-full`.
  - [ ] Remove "Back to …" / "Manage backup destinations" links (e2e
    `admin-link` and spec selectors with them), breadcrumbs instead.
  - [ ] Shared parts, overview table, space detail.
- [ ] PR B — issue #60
