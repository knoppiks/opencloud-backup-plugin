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

- **Language/tooling:** Go (module `opencloud-backup-plugin`, toolchain pinned in
  `go.mod`). Frontend (Phase 8) is Vue 3 + TypeScript under `/web/`.
- **Layout:** thin `/cmd/*` binaries; all logic in `/pkg/*` behind interfaces.
  See `phase-1-scaffolding.md` for the canonical package layout — follow it.
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

## Git

- **Never commit.** The user commits. You may `git add` to stage intended files
  and suggest a concise, imperative commit message (<= 72-char subject).
- Do not modify git config, skip hooks, force-push, or amend.

## When unsure

Ask. Do not make silent assumptions on ambiguous requirements, security
trade-offs, or anything touching the key/trust model.
