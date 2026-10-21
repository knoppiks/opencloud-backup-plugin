# Contributing

Thank you for helping. This project keeps families' backups and the keys to
them, so a few rules are stricter than usual; they are below, with the reason
for each. What the project is and how it works is in the [README](README.md);
the design decisions behind it are in
[`.agents/plan/decisions.md`](.agents/plan/decisions.md).

A vulnerability is reported privately, not as an issue or pull request: see
[SECURITY.md](SECURITY.md).

## Before you start

Open an issue first, or comment on an existing one, for anything larger than
a typo. Every pull request closes an issue (see [Pull requests](#pull-requests)),
and the issue is where the change is agreed on.

## Development setup

You need:

- **Go**, the version in `go.mod`'s `toolchain` line (with `GOTOOLCHAIN=auto`,
  the default, any recent Go fetches it);
- **Docker** with Compose, for the S3 store (Garage) and the OpenCloud fixture
  the integration and browser tests run against;
- **Node.js** (the version CI uses is `NODE_VERSION` in
  `.github/workflows/ci.yml`) with Corepack, which provides the pinned pnpm, for
  the web extension under `web/`;
- [`yq`](https://github.com/mikefarah/yq) v4, which the OpenCloud fixture uses
  to read its pin file.

`make help` lists every target. The ones you need most:

```sh
make build                      # every binary into dist/ (DEBUG=1 keeps symbols)
make test                       # Go unit tests, with the race detector
make dev-up                     # Garage + OpenCloud + the fixture proxy, seeded
make test-integration           # Go tests against Garage (needs Docker)
make test-opencloud             # Go tests against the OpenCloud fixture (after dev-up)
make web-install                # the web extension's dependencies
make web-install-fixture        # build the extension into the fixture (after dev-up)
make e2e                        # browser end-to-end tests (after web-install-fixture)
make dev-down                   # stop Garage and the fixture
```

Linters and checks (`make lint`, `make pipeline-lint`, `make editorconfig`,
`make secret-scan`, …) build or pull their pinned versions themselves; you do
not install them. The README's
[Developing the web extension](README.md#developing-the-web-extension)
section explains the fixture in more detail.

## Tests that must pass

CI runs every check on every pull request, and all of them but one are
required to merge. Locally, run at least what your change touches:

| You changed | Run |
|---|---|
| Go code | `make test lint`, and `make test-integration` if it touches storage, kopia or keys |
| Anything OpenCloud or the browser sees (CS3 calls, the API, the web bundle, the fixture) | `make dev-up test-opencloud web-install-fixture e2e`; unit and Garage tests do not catch these |
| The web extension | `make web-lint web-typecheck web-test web-build` |
| Workflows, shell scripts, Dockerfiles | `make pipeline-lint` |
| Anything | `make editorconfig secret-scan` |

New code comes with tests: untested code is not finished. A change to how
data is backed up is not finished until the matching restore is tested too.

## Rules that are not negotiable

These protect users' data. A pull request that breaks one is not merged,
however useful it is otherwise.

- **No home-made cryptography or deduplication.** kopia encrypts and
  deduplicates the data; key envelopes use maintained libraries. Do not
  write a cipher, a key derivation, a MAC or a chunker yourself.
- **Key material is never logged or shown.** No Data Key, Recovery Key, wrap
  key, wrapped key blob or credential in a log line, an error message, an API
  response or the admin view.
- **The Recovery Key stays on the user's device.** It is created and used in
  the browser or in the offline `decrypt` tool, and it never travels over the
  network in plaintext.
- **Admins see ciphertext only.** Nothing may let an OpenCloud admin decrypt a
  user's backup or restore into a user's Space. Admins can hand out an
  encrypted Take-Out; only the user can open it.
- **Retention is by age, never by count.** Snapshots are kept for a time
  window. Keeping "the last N" lets a burst of bad snapshots (ransomware)
  push out every good one.
- **External versions are pinned** (images by digest, tools by version, actions
  by commit), so a build tomorrow uses what was tested today.
- **Errors say what failed, not how the inside looks.** Wrap errors with
  context; never pass CS3, S3 or credential details through the public API.

When a change would touch the key or trust model, ask in the issue first.

## No secrets in the repository, not even fake ones

CI scans the whole history with gitleaks and fails on any finding in any
commit. A secret that was committed once stays in the history, so a later
commit cannot fix it.

- Do not write anything that looks like a key in source or tests: Recovery
  Keys (`ocbk1-…`), base64 or hex keys, tokens, "example" passwords. The scanner
  cannot tell a test value from a real one. Generate test keys at run time,
  or assemble a fixed one from parts.
- Run `make secret-scan` before you push. It scans the history and the files
  you are about to commit, and nothing else (scanning the checkout directly
  walks `node_modules/` for minutes).
- Real fixture material (golden test vectors) is allow-listed by path in
  `.gitleaks.toml`, with a reason. Never add an entry to silence a finding in
  hand-written code.

## Issues

Use the issue forms: bug report, feature request, or question. Each sets the
issue's kind for you. The maintainer adds the rest when triaging, and every
issue needs it before a pull request closing it can merge:

- **exactly one `kind/`** label: `Feature`, `Enhancement`, `Bug`,
  `Refactoring`, `Epic` or `Question`;
- **any `aspect/`** labels for the areas it touches (`Core`, `Security`,
  `Web UI`, …);
- **`hint/Breaking`** if an upgrade breaks something that worked, and
  **`hint/Operator Action`** if whoever runs it must do something on upgrade;
- **a milestone**: the phase it belongs to, or `Roadmap`.

The release notes are built from the issues a release closes, grouped by
kind, and print each issue's title. So a title says what changes for a user
or operator ("Show the Last Backup's Size"), not what changes in the code.

## Pull requests

- **Branch** from `main`, named after the issue: `<number>-<title-slug>`
  (`137-explain-how-to-report-vulnerabilities-contribute-and-ask-questions`).
  The release tooling finds a release's issues by these names too.
- **One commit per pull request.** Fold review fixes into it (amend and
  force-push your branch).
- **Commit message:**
  - subject in Title Case and the imperative mood ("Add Weekly Schedule",
    "Fix Restore of Empty Folders"), at most 50 characters;
  - body lines at most 72 characters, saying why rather than what;
  - `Closes #<issue>` as the last line, in the commit and in the pull
    request description. Use `Refs #<issue>` only if the pull request
    deliberately leaves part of the issue open, and say what remains.
- The `closes a ready issue` check fails until the description closes an
  issue with a kind and a milestone. After the issue is fixed, re-run the
  check or edit the description.

## License

By contributing you agree that your contribution is licensed under the
[Apache License 2.0](LICENSE), like the rest of the project.
