# AGENTS.md

Operating rules for AI agents working in this repository. For *what the project
is*, see `README.md`. For *design decisions and rationale*, see
`.agents/plan/decisions.md`. Do not duplicate that content here.

## Read before acting

1. `.agents/plan/decisions.md` — locked decisions, trust/key model, threat model.
   **These are binding.** If a task conflicts with a locked decision, stop and
   flag it; do not silently deviate.
2. `.agents/plan/README.md` — phase index and per-phase docs. Work is organized
   by phase; find the relevant phase doc before writing code.
3. `.agents/plan/phase-N-*.md` — deliverables and exit criteria for that phase.

## Hard constraints (non-negotiable)

- **Never hand-roll cryptography or deduplication.** kopia owns data crypto/dedup;
  the key-envelope layer uses maintained libraries only.
- **Never log key material** (DK, RK, SRW keys, wrapped blobs) and never expose it
  in the admin UI or error messages.
- **Plaintext Recovery Key must never cross the network** — it is generated and
  used client-side / in the standalone CLI only.
- **Retention is time-based (`keep-within`), never count-based.**
- **The admin path handles ciphertext only.** No code may give the admin a
  plaintext-decrypt or restore-into-user-Space capability.
- **A backup path is not complete until its restore path is tested.**

## Engineering conventions

- **Language/tooling:** Go (module `github.com/knoppiks/opencloud-backup-plugin`,
  toolchain pinned in `go.mod`, the same patch in CI and the Dockerfile).
  Frontend (Phase 8) is Vue 3 + TypeScript under `/web/`.
- **Layout:** thin `/cmd/*` binaries; all logic in `/internal/*` behind
  interfaces, with `/pkg` reserved for the format reference implementations
  (`pkg/keys`, `pkg/takeout`, `pkg/takeout/decrypt`; decision #26). See
  `phase-1-scaffolding.md` for the canonical package layout — follow it.
- **Testability first:** dependency injection, interfaces at every external
  boundary (CS3 client, S3 target, key store, clock). No hidden globals.
- **Tests are mandatory.** Untested code is incomplete. Unit tests alongside
  implementation; integration tests behind `-tags integration` against the Garage
  fixture (and, where relevant, a test OpenCloud instance).
- **Pin external versions** (Garage image tag, kopia, oCIS test image). Record the
  supported range.
- **Errors:** wrap with context; never leak internal/CS3/S3 details or credentials
  through the public API.

## Workflow

- For any multi-step task, maintain a todo list and keep exactly one item in
  progress; mark items done only when their exit criteria (including tests) pass.
- Respect phase dependencies (see `.agents/plan/README.md`). Do not start a phase
  whose prerequisites are unmet without saying so.
- When a Phase 0 spike outcome contradicts a planning assumption, **update
  `.agents/plan/decisions.md`** (with rationale) rather than diverging quietly.
- `.agents/` is always writable; keep planning docs in sync with reality.

## Issues, labels, milestones

The release notes are built from the issues a release ships, grouped by
label (`scripts/release-issues.sh`, `scripts/release-notes.sh`); there is no
CHANGELOG file. CI refuses a pull request whose closing issue is not ready
(`pr-issue.yml`), and a final release that ships an issue without a kind.

- **Every issue gets exactly one `kind/`**: `Feature` (new functionality),
  `Enhancement` (improves existing), `Bug`, `Refactoring` (no behaviour
  change), `Epic` (a phase-sized issue split into others), `Question`.
- **Any number of `aspect/`** for the areas it touches (`Core`, `Security`,
  `Recovery`, `Web UI`, `API`, `Configuration`, `Observability`, `Tests`,
  `Pipeline`, `Release`, `Repository`, `Documentation`, `Dependencies`,
  `OpenCloud Compatibility`, `Performance`, `Accessibility`). The labels live
  on GitHub only; `gh label list` is the reference.
- **`hint/Breaking`** when an upgrade breaks something that worked, and
  **`hint/Operator Action`** when an operator must do something on upgrade.
  Both put the issue at the top of the release notes.
- **Milestone = the phase** the issue belongs to (`Phase 11: Release & Supply
  Chain`, …); `Roadmap` for work not yet planned into a phase.
- **The title is what the release notes print.** Write it for an operator or
  user reading them: the outcome, not the change.
- An issue closed without shipping is closed as *not planned*, or labelled
  `status/Invalid`, `status/Duplicate` or `status/Wontfix`, so the notes
  leave it out.

## Secret scanning (CI runs gitleaks on the full history)

CI fails on any gitleaks finding in **any commit**, so a leak cannot be fixed by
a follow-up edit alone: once committed, it is in history.

- **Never write key-shaped literals in source or tests.** That includes
  Recovery Keys (`ocbk1-XXXXX-…`), base64/hex keys, tokens, and "fake" secrets
  in specs. gitleaks' `generic-api-key` rule cannot tell a test value from a
  real one. Generate test keys at runtime (`generateRecoveryKey()`,
  `randomBytes`), or build fixed ones from parts (e.g. `groups.join('-')`).
- **Scan before staging is done.** Run `make secret-scan`, or without a local
  gitleaks, the CI-pinned version:
  `go run github.com/zricethezav/gitleaks/v8@v<GITLEAKS_VERSION from ci.yml> git . --log-opts="HEAD" --redact --no-banner`
  plus `… dir . --redact --no-banner` for uncommitted changes.
- **Genuine test material** (golden vectors, fixture credentials) is
  allowlisted by path in `.gitleaks.toml`, with a reason. Add to it only for
  generated or fixture files, never to silence a finding in hand-written code.
- **A finding already in history** is fixed in the source *and* its
  fingerprint added to `.gitleaksignore` with a reason. Rewriting published
  history is the user's call, not the agent's.

## Git

- **Never commit.** The user commits. You may `git add` to stage intended files
  and suggest a concise, imperative commit message (<= 72-char subject).
- Do not modify git config, skip hooks, force-push, or amend.
- **Unattended sessions work in a worktree, never in the main checkout.** The
  main checkout belongs to the user and may hold their uncommitted work. The
  unattended driver gives each session its own worktree under
  `../opencloud-backup-plugin.worktrees/`; work and test only there, and never
  `checkout`, `stash`, `reset` or `clean` in the main checkout. Interactive
  sessions work in the main checkout as usual.
- **Unattended sessions do not sign commits.** Where an unattended run is
  explicitly permitted to commit, pass `--no-gpg-sign` to every `git commit`
  (including `--amend`): nobody is there to enter the key passphrase, and a
  signing prompt would hang the session. Do not change `commit.gpgsign` in git
  config to achieve this.
- **A branch is named after its issue**: `<number>-<title-slug>`
  (`112-write-release-notes-from-the-milestones-issues`). The release notes
  also find a release's issues by these names.
- **A new branch tracks its own name on `origin`, never `main`.** Branch off
  `origin/main` without tracking it, then point the upstream at the
  same-named remote branch (it need not exist yet), so the user's plain
  `git push` creates it:

  ```sh
  git switch --no-track -c <branch> origin/main
  git config branch.<branch>.remote origin
  git config branch.<branch>.merge refs/heads/<branch>
  ```

  These two per-branch settings are the one exception to "do not modify git
  config". `git switch -c <branch> origin/main` alone sets the upstream to
  `main`, so a `git push` would target `main`.
- **One commit per issue/PR.** Follow-up work on an open PR is folded into
  its commit by the user (amend). When suggesting a message for such a
  change, give the full combined message for the whole PR, not a delta.
- **Link the issue with a closing keyword**: `Closes #<ID>` as the last line
  of the commit message and of the PR description, so merging closes the
  issue. Use `Refs #<ID>` only when the PR deliberately leaves part of the
  issue open, and say in the PR what remains.

## When unsure

Ask. Do not make silent assumptions on ambiguous requirements, security
trade-offs, or anything touching the key/trust model.
