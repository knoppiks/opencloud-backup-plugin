# 10.3 rework: configuration declared per component (#74, PR #75)

Owner decision (2026-10-09): replace the single `config.Backupd` struct with
configuration declared by each component. Keep the goals of #74 (one list,
strict parsing, every error at startup, effective-config line, generated
reference, placeholder check driven by declarations).

## Shape

- **`internal/config` = machinery only.** No domain imports. Provides:
  - `Secret` (unchanged).
  - `Load(environ, root any) error`: walks a struct pointer; nested structs
    are sections; parses `env`-tagged fields; refuses placeholders alone;
    then calls `Validate() error` on every nested struct that implements
    `Validator` (pointer receiver, may normalise), then on the root. All
    errors joined.
  - **Defaults are the values already in the struct** when `Load` is called.
    Components provide `DefaultXxxEnv()` built from their own constants, so
    there is no second copy in a tag and no defaults-sync test.
  - `Names(root)`, `Effective(root) slog.Value`, `Reference(programs) []byte`.
- **Components declare dedicated env structs** (`XxxEnv`) with `env`/`doc`
  tags, a `Validate`, and a mapping to the package's own option type.
  - `pkg/api.Env` (sections OIDC, OpenCloud; issuer => audience + base URL).
  - `pkg/cs3.Env` (gateway, data server URL now validated at load, service
    account required with a gateway).
  - `pkg/scheduler.Env` (+ timezone validation, `Options()`).
  - `pkg/notify.SMTPEnv` (`Config()`).
  - `pkg/targets.BootstrapEnv` (`Config()`).
- **Binary-local sections** (in `cmd/backupd`): HTTP listener (base path,
  TLS pair, probe paths), state selection, custody keys (shape, SRW != TW),
  backup work dir/tuning. Reasons: `pkg/keys` is going public (10.7, no tags
  on public packages); `pkg/snapshot` is linked by `decrypt` (#80/#81);
  the rest is wiring that only `cmd/backupd` does.
- **`cmd/takeout`** declares its own two S3 credentials (`pkg/takeout` is
  public).
- **Composition root** per binary in `cmd/<bin>/env.go`: a struct of the
  component structs, `newEnv()` with defaults, cross-component checks.
  Consumers get only their section (`dialCS3(cfg.CS3)`,
  `provision(cfg.CS3, cfg.State)`, ...). No function takes the whole root
  except `run`/`serve` (dispatch, logging).
- **Reference**: one file per binary, `docs/reference/environment-backupd.md`
  and `environment-takeout.md`, each written by a golden test in its `cmd`
  package (`go test ./cmd/... -run TestEnvironmentReference -update`, wrapped
  by `make generate`), since `package main` cannot be imported by a
  generator. (Owner, 2026-10-09.)
- **Bootstrap completeness** is validated at load (`BootstrapEnv.Validate`)
  as well as at seeding time (defence in depth). The F1 lifecycle test builds
  its broken configuration directly instead of through `Load`. (Owner,
  2026-10-09.)

## Unchanged behaviour

Variable names, strictness rules, error texts, placeholder semantics, the
configuration log line keys.
