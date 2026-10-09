package main

// The service's configuration.
//
// Each component declares the variables it needs on a struct of its own
// (api.Env, cs3.Env, scheduler.Env, notify.SMTPEnv, targets.BootstrapEnv);
// serviceEnv composes them with the sections only this binary uses, and
// internal/config reads, defaults and validates the lot once at startup. Every
// part of the wiring is handed its own section, never the whole.
//
// The sections declared here are this binary's own: the listener, the choice
// of state store, the custody keys and the run work directory. The custody
// keys stay here rather than in pkg/keys because that package is public
// (decisions.md, 10.7), and a public package does not carry this
// deployment's variable names.

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/kopia/kopia/repo/blob/throttling"

	"opencloud-backup-plugin/internal/config"
	"opencloud-backup-plugin/pkg/api"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/cs3state"
	"opencloud-backup-plugin/pkg/keys"
	"opencloud-backup-plugin/pkg/notify"
	"opencloud-backup-plugin/pkg/scheduler"
	"opencloud-backup-plugin/pkg/targets"
)

// serviceEnv is the configuration of the service and its operator commands:
// they ship in the same binary because they need the same configuration.
type serviceEnv struct {
	HTTP      listenerEnv          `section:"HTTP listener"`
	API       api.Env              // its own sections: Authentication, OpenCloud
	CS3       cs3.Env              `section:"CS3 (reading and writing Spaces)"`
	State     stateEnv             `section:"Service state"`
	Keys      custodyEnv           `section:"Custody keys"`
	Backup    workEnv              `section:"Backup pipeline"`
	Scheduler scheduler.Env        `section:"Scheduling"`
	SMTP      notify.SMTPEnv       `section:"Operator notifications"`
	Bootstrap targets.BootstrapEnv `section:"Default backup target (optional first-start seeding)"`
}

// newServiceEnv is the configuration of an unset environment: every
// component's own defaults.
func newServiceEnv() serviceEnv {
	return serviceEnv{
		HTTP:      listenerEnv{Addr: ":8080"},
		API:       api.DefaultEnv(),
		State:     stateEnv{Prefix: cs3state.DefaultPrefix},
		Scheduler: scheduler.DefaultEnv(),
	}
}

// loadServiceEnv reads and validates the configuration from environ, reporting
// every problem in the one error.
func loadServiceEnv(environ []string) (serviceEnv, error) {
	env := newServiceEnv()
	if err := config.Load(environ, &env); err != nil {
		return serviceEnv{}, err
	}
	return env, nil
}

// Validate checks what spans components.
func (e *serviceEnv) Validate() error {
	// Seeding seals the target's credentials with the TW key. Without it the
	// seeding used to be skipped without a word.
	if e.Bootstrap.Enable && !e.Keys.TW.IsSet() {
		return errors.New("BOOTSTRAP_ENABLE is true but TW_KEY is unset: the seeded target's credentials are sealed with it")
	}
	return nil
}

// LogValue is the effective configuration, one attribute per variable, with
// every secret shown only as set or unset (config.Secret redacts itself).
func (e serviceEnv) LogValue() slog.Value { return config.Effective(&e) }

// listenerEnv is the HTTP listener.
type listenerEnv struct {
	Addr string `env:"BACKUPD_ADDR" doc:"Address the API listens on."`
	// BasePath is normalised by Validate: no trailing slash, "" for none.
	BasePath    string `env:"BACKUPD_BASE_PATH" doc:"Path prefix the ingress routes to this service, such as /backup. A path, never a URL. The health probes stay at /healthz and /readyz."`
	TLSCertFile string `env:"TLS_CERT_FILE" doc:"Certificate file, for a service that terminates TLS itself. Set together with TLS_KEY_FILE. Unset, the listener is plain HTTP and a TLS-terminating ingress in front of it is required: a Data Key crosses it once per Space, at key setup."`
	TLSKeyFile  string `env:"TLS_KEY_FILE" doc:"Private key file for TLS_CERT_FILE."`
}

// Validate normalises the base path and refuses half a TLS configuration: a
// deployment that thinks it is encrypted.
func (l *listenerEnv) Validate() error {
	var errs []error
	basePath, err := cleanBasePath(l.BasePath)
	if err != nil {
		errs = append(errs, err)
	}
	l.BasePath = basePath
	if (l.TLSCertFile == "") != (l.TLSKeyFile == "") {
		errs = append(errs, errors.New("TLS_CERT_FILE and TLS_KEY_FILE must be set together"))
	}
	return errors.Join(errs...)
}

// probePaths are reached directly on the pod and therefore never carry the
// ingress's path prefix.
var probePaths = []string{"/healthz", "/readyz"}

// cleanBasePath returns the validated path prefix the routes are mounted
// under, or "" for none.
//
// The service shares an origin with OpenCloud (decisions.md, R5), so `/api/v1/`
// is a namespace it shares with OpenCloud's own; a prefix retires the whole
// collision class. This is a *path*, never an origin — the same constraint the
// browser client enforces on its side. Accepting an absolute URL here would let
// a deployment place the API somewhere the extension cannot reach it without
// CORS, which the service deliberately does not implement.
func cleanBasePath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf(
			"BACKUPD_BASE_PATH must be a path such as /backup, not a URL or a query: got %q", raw)
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("BACKUPD_BASE_PATH must start with a slash: got %q", raw)
	}
	trimmed := strings.TrimRight(raw, "/")
	if trimmed == "" {
		// "/" and "//" mean "no prefix". Saying so beats refusing a value that
		// expresses the default.
		return "", nil
	}
	if path.Clean(trimmed) != trimmed {
		return "", fmt.Errorf(
			"BACKUPD_BASE_PATH must be a clean path without empty or relative segments: got %q", raw)
	}
	for _, probe := range probePaths {
		if trimmed == probe {
			return "", fmt.Errorf("BACKUPD_BASE_PATH must not be %s: the health probes live there", probe)
		}
	}
	return trimmed, nil
}

// stateBackendMemory is the STATE_BACKEND value that opts into throwaway
// state.
const stateBackendMemory = "memory"

// stateEnv is where the service keeps its own memory.
type stateEnv struct {
	SpaceID string `env:"STATE_SPACE_ID" doc:"Id of the OpenCloud Space holding schedules, run history, wrapped key envelopes and target records. Create it with backupd provision-state-space. Required unless STATE_BACKEND is memory."`
	Prefix  string `env:"STATE_PREFIX" doc:"Folder inside the state Space the records live under."`
	Backend string `env:"STATE_BACKEND" values:"memory" doc:"Set to memory for throwaway state that does not survive a restart (smoke tests only)."`
}

// memory reports whether the operator explicitly asked for throwaway state.
func (s stateEnv) memory() bool { return s.Backend == stateBackendMemory }

// custodyEnv holds the two custody keys and, during a rotation, the ones
// retiring. Each is base64 of 32 random bytes.
type custodyEnv struct {
	SRW    config.Secret `env:"SRW_KEY" doc:"Server Runtime Wrap key: wraps every Space's Data Key so unattended runs are possible. Unset, key setup and backups are unavailable."`
	SRWOld config.Secret `env:"SRW_KEY_OLD" doc:"The retiring SRW key, for backupd rotate-srw only."`
	TW     config.Secret `env:"TW_KEY" doc:"Target Wrap key: seals the backup targets' S3 credentials. Must differ from SRW_KEY. Unset, backups are unavailable."`
	TWOld  config.Secret `env:"TW_KEY_OLD" doc:"The retiring TW key, for backupd rotate-tw only."`
}

// Validate checks each key's shape and refuses the one combination that looks
// like a working configuration but is not: the same key in both roles.
//
// SRW guards Data Keys, TW guards target credentials, and decisions.md keeps
// them distinct so the two can rotate independently — retiring a leaked target
// credential must not mean re-wrapping every Space's Data Key. Setting them to
// one value silently collapses that into a single blast radius, and the failure
// is invisible: everything works. It is a misconfiguration to catch at boot.
func (k *custodyEnv) Validate() error {
	var errs []error
	decoded := map[string][]byte{}
	defer func() {
		for _, key := range decoded {
			keys.Zeroize(key)
		}
	}()
	for _, named := range []struct {
		env string
		key config.Secret
	}{
		{"SRW_KEY", k.SRW}, {"SRW_KEY_OLD", k.SRWOld}, {"TW_KEY", k.TW}, {"TW_KEY_OLD", k.TWOld},
	} {
		key, err := decodeWrapKey(named.env, named.key)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		decoded[named.env] = key
	}

	srw, tw := decoded["SRW_KEY"], decoded["TW_KEY"]
	// Constant-time because both operands are secrets, and cheap either way.
	if srw != nil && tw != nil && subtle.ConstantTimeCompare(srw, tw) == 1 {
		errs = append(errs, errors.New(
			"SRW_KEY and TW_KEY must be different keys: data-key custody and "+
				"target-credential custody are separate on purpose (decisions.md #14)"))
	}
	return errors.Join(errs...)
}

// decodeWrapKey decodes a base64-encoded 256-bit wrapping key (SRW or TW). It
// returns (nil, nil) when the key is unset, so the caller decides whether the
// feature is optional. The caller owns the returned bytes and zeroizes them.
//
// Errors describe only the shape of the problem, never the value (AGENTS.md:
// never log key material).
func decodeWrapKey(env string, s config.Secret) ([]byte, error) {
	if !s.IsSet() {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s.Reveal()))
	if err != nil {
		return nil, fmt.Errorf("%s must be base64-encoded", env)
	}
	if len(key) != keys.SRWKeySize {
		keys.Zeroize(key)
		return nil, fmt.Errorf("%s must decode to %d bytes", env, keys.SRWKeySize)
	}
	return key, nil
}

// workEnv tunes the backup pipeline and says where a run keeps its working
// files.
type workEnv struct {
	WorkDir                string `env:"BACKUP_WORK_DIR" doc:"Parent directory of each run's kopia config and cache. Must be memory-backed (an emptyDir with medium Memory). Unset, the OS temp directory."`
	WorkDirAllowDisk       bool   `env:"BACKUP_WORK_DIR_ALLOW_DISK" doc:"Accept a disk-backed BACKUP_WORK_DIR, where kopia's cache can outlive a crash."`
	Parallelism            int    `env:"BACKUP_PARALLELISM" doc:"Files hashed and uploaded concurrently. 0 is one per CPU."`
	UploadBytesPerSecond   int    `env:"BACKUP_UPLOAD_BYTES_PER_SECOND" doc:"Bandwidth cap towards the target. 0 is unlimited."`
	DownloadBytesPerSecond int    `env:"BACKUP_DOWNLOAD_BYTES_PER_SECOND" doc:"Bandwidth cap from the target. 0 is unlimited."`
}

// limits are the optional upload/download caps applied to the S3 target, in
// bytes per second. Zero means unlimited.
func (w workEnv) limits() throttling.Limits {
	return throttling.Limits{
		UploadBytesPerSecond:   float64(w.UploadBytesPerSecond),
		DownloadBytesPerSecond: float64(w.DownloadBytesPerSecond),
	}
}
