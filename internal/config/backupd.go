package config

//go:generate go run ./gen -o ../../docs/reference/environment.md

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"reflect"
	"strings"
	"time"

	"opencloud-backup-plugin/pkg/keys"
)

// Backupd is the backup service's whole configuration, every operator
// command's included: they ship in the same binary because they need the same
// configuration (cmd/backupd).
//
// Load it with LoadBackupd. A Backupd that did not come from LoadBackupd has
// not been validated.
type Backupd struct {
	HTTP      HTTP      `section:"HTTP listener"`
	OIDC      OIDC      `section:"Authentication"`
	OpenCloud OpenCloud `section:"OpenCloud"`
	CS3       CS3       `section:"CS3 (reading and writing Spaces)"`
	State     State     `section:"Service state"`
	Keys      Keys      `section:"Custody keys"`
	Backup    Backup    `section:"Backup pipeline"`
	Scheduler Scheduler `section:"Scheduling"`
	SMTP      SMTP      `section:"Operator notifications"`
	Bootstrap Bootstrap `section:"Default backup target (optional first-start seeding)"`
}

// HTTP is the listener.
type HTTP struct {
	Addr string `env:"BACKUPD_ADDR" default:":8080" doc:"Address the API listens on."`
	// BasePath is normalised by LoadBackupd: no trailing slash, "" for none.
	BasePath    string `env:"BACKUPD_BASE_PATH" doc:"Path prefix the ingress routes to this service, such as /backup. A path, never a URL. The health probes stay at /healthz and /readyz."`
	TLSCertFile string `env:"TLS_CERT_FILE" doc:"Certificate file, for a service that terminates TLS itself. Set together with TLS_KEY_FILE. Unset, the listener is plain HTTP and a TLS-terminating ingress in front of it is required: a Data Key crosses it once per Space, at key setup."`
	TLSKeyFile  string `env:"TLS_KEY_FILE" doc:"Private key file for TLS_CERT_FILE."`
}

// OIDC validates the bearer tokens the web extension sends.
type OIDC struct {
	Issuer   string `env:"OIDC_ISSUER" doc:"OpenID Connect issuer whose tokens are accepted. Unset, every authenticated route refuses. Requires OIDC_AUDIENCE and OC_BASE_URL."`
	Audience string `env:"OIDC_AUDIENCE" doc:"Client id the tokens must be issued for. Without it any token the issuer minted for any application would be accepted."`
}

// OpenCloud is the Graph API side: who a caller is, their groups, their role.
type OpenCloud struct {
	BaseURL        string   `env:"OC_BASE_URL" doc:"OpenCloud's public URL. A caller's user id, groups and admin role are read from its Graph API, and its version from its status endpoint."`
	AdminAppRoleID string   `env:"OC_ADMIN_APP_ROLE_ID" default:"71881883-1768-46bd-a24d-a356a2afdf7f" doc:"Id of OpenCloud's admin app role. The default is OpenCloud's built-in Admin role."`
	AdminAllowlist []string `env:"ADMIN_SUBJECT_ALLOWLIST" doc:"OpenCloud user ids that are backup admins, instead of asking Graph for the admin role. Despite the name these are user ids, not token subjects."`
}

// CS3 is the gateway the service reads and restores Spaces through.
type CS3 struct {
	GatewayAddr          string `env:"CS3_GATEWAY_ADDR" doc:"OpenCloud's CS3 gateway, host:port (plaintext gRPC, cluster network only). Unset, the service starts without Spaces, backups or durable state. Requires OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET."`
	DataServerURL        string `env:"CS3_DATA_SERVER_URL" doc:"Where OpenCloud's storage data server is reachable from this service, scheme and host only. Needed from OpenCloud 7.5 on; harmless before."`
	ServiceAccountID     Secret `env:"OC_SERVICE_ACCOUNT_ID" doc:"Id of the OpenCloud service account the service acts as."`
	ServiceAccountSecret Secret `env:"OC_SERVICE_ACCOUNT_SECRET" doc:"Secret of that service account. It reaches every Space's plaintext: the most valuable credential in the deployment."`
}

// State is where the service keeps its own memory.
type State struct {
	SpaceID string `env:"STATE_SPACE_ID" doc:"Id of the OpenCloud Space holding schedules, run history, wrapped key envelopes and target records. Create it with backupd provision-state-space. Required unless STATE_BACKEND is memory."`
	Prefix  string `env:"STATE_PREFIX" default:".backup-service-state" doc:"Folder inside the state Space the records live under."`
	Backend string `env:"STATE_BACKEND" values:"memory" doc:"Set to memory for throwaway state that does not survive a restart (smoke tests only)."`
}

// Keys are the two custody keys and, during a rotation, the ones retiring.
// Each is base64 of 32 random bytes.
type Keys struct {
	SRW    Secret `env:"SRW_KEY" doc:"Server Runtime Wrap key: wraps every Space's Data Key so unattended runs are possible. Unset, key setup and backups are unavailable."`
	SRWOld Secret `env:"SRW_KEY_OLD" doc:"The retiring SRW key, for backupd rotate-srw only."`
	TW     Secret `env:"TW_KEY" doc:"Target Wrap key: seals the backup targets' S3 credentials. Must differ from SRW_KEY. Unset, backups are unavailable."`
	TWOld  Secret `env:"TW_KEY_OLD" doc:"The retiring TW key, for backupd rotate-tw only."`
}

// Backup tunes the pipeline.
type Backup struct {
	WorkDir                string `env:"BACKUP_WORK_DIR" doc:"Parent directory of each run's kopia config and cache. Must be memory-backed (an emptyDir with medium Memory). Unset, the OS temp directory."`
	WorkDirAllowDisk       bool   `env:"BACKUP_WORK_DIR_ALLOW_DISK" default:"false" doc:"Accept a disk-backed BACKUP_WORK_DIR, where kopia's cache can outlive a crash."`
	Parallelism            int    `env:"BACKUP_PARALLELISM" default:"0" doc:"Files hashed and uploaded concurrently. 0 is one per CPU."`
	UploadBytesPerSecond   int    `env:"BACKUP_UPLOAD_BYTES_PER_SECOND" default:"0" doc:"Bandwidth cap towards the target. 0 is unlimited."`
	DownloadBytesPerSecond int    `env:"BACKUP_DOWNLOAD_BYTES_PER_SECOND" default:"0" doc:"Bandwidth cap from the target. 0 is unlimited."`
}

// Scheduler tunes unattended runs.
type Scheduler struct {
	MaxConcurrent      int    `env:"SCHEDULER_MAX_CONCURRENT" default:"2" doc:"Scheduled runs at the same time."`
	JobHistoryDays     int    `env:"JOB_HISTORY_DAYS" default:"365" doc:"Days finished runs and notifications are kept."`
	PruneIntervalHours int    `env:"PRUNE_INTERVAL_HOURS" default:"24" doc:"How often each Space's retention is applied. How much is kept is the Space's own setting."`
	Timezone           string `env:"SCHEDULE_TIMEZONE" doc:"IANA zone schedules are read in, such as Europe/Berlin. Unset, the container's zone (TZ)."`

	// location is Timezone, loaded by LoadBackupd.
	location *time.Location
}

// Location is the zone schedules are read in: SCHEDULE_TIMEZONE, or the
// container's own zone. "Nightly at half past two" means the family's night,
// and a deployment that sets TZ for its logs has already said which zone it
// thinks in.
func (s Scheduler) Location() *time.Location {
	if s.location != nil {
		return s.location
	}
	return time.Local
}

// SMTP mails operator notifications. All of host, port, sender and recipient
// are needed; otherwise notifications are recorded and logged only.
type SMTP struct {
	Host       string `env:"SMTP_HOST" doc:"Mail server for operator notifications."`
	Port       int    `env:"SMTP_PORT" default:"0" doc:"Mail server port, such as 587. 0 disables mail."`
	Username   string `env:"SMTP_USERNAME" doc:"Mail server login, if it requires one."`
	Password   Secret `env:"SMTP_PASSWORD" doc:"Mail server password."`
	From       string `env:"SMTP_FROM" doc:"Sender address."`
	OperatorTo string `env:"NOTIFY_OPERATOR_EMAIL" doc:"Where operator notifications go. They never name a Space."`
}

// Bootstrap seeds one backup target on first start, when no target exists
// yet. It never overrides what an admin configured.
type Bootstrap struct {
	Enable                     bool   `env:"BOOTSTRAP_ENABLE" default:"false" doc:"Seed the target below on start, if no target exists yet. Needs TW_KEY."`
	TargetID                   string `env:"BOOTSTRAP_TARGET_ID" doc:"Id of the seeded target. Unset, a fixed default id."`
	TargetName                 string `env:"BOOTSTRAP_TARGET_NAME" doc:"Display name of the seeded target."`
	Endpoint                   string `env:"BOOTSTRAP_S3_ENDPOINT" doc:"S3 endpoint, host:port or URL."`
	Region                     string `env:"BOOTSTRAP_S3_REGION" doc:"S3 region."`
	Bucket                     string `env:"BOOTSTRAP_S3_BUCKET" doc:"S3 bucket."`
	Prefix                     string `env:"BOOTSTRAP_S3_PREFIX" doc:"Prefix inside the bucket."`
	UsePathStyle               bool   `env:"BOOTSTRAP_S3_USE_PATH_STYLE" default:"false" doc:"Path-style addressing (Garage and most self-hosted S3 need it)."`
	DisableTLS                 bool   `env:"BOOTSTRAP_S3_DISABLE_TLS" default:"false" doc:"Plain HTTP to the endpoint."`
	AccessKeyID                Secret `env:"BOOTSTRAP_S3_ACCESS_KEY_ID" doc:"Access key id backups write with."`
	SecretAccessKey            Secret `env:"BOOTSTRAP_S3_SECRET_ACCESS_KEY" doc:"Secret for that access key id."`
	MaintenanceAccessKeyID     Secret `env:"BOOTSTRAP_S3_MAINTENANCE_ACCESS_KEY_ID" doc:"Optional second access key id, used by prune runs only. Set both halves or neither."`
	MaintenanceSecretAccessKey Secret `env:"BOOTSTRAP_S3_MAINTENANCE_SECRET_ACCESS_KEY" doc:"Secret for the maintenance access key id."`
}

// StateBackendMemory is the STATE_BACKEND value that opts into throwaway state.
const StateBackendMemory = "memory"

// MemoryState reports whether the operator explicitly asked for throwaway
// state.
func (s State) MemoryState() bool { return s.Backend == StateBackendMemory }

// probePaths are reached directly on the pod and therefore never carry the
// ingress's path prefix.
var probePaths = []string{"/healthz", "/readyz"}

// ProbePaths returns the health probe paths, which a base path must not hide.
func ProbePaths() []string { return append([]string(nil), probePaths...) }

// LoadBackupd parses and validates the service's configuration from environ
// ("NAME=value" entries, as os.Environ returns them). Every problem found is
// reported in the one error.
//
// What is checked here is the configuration itself. Whether the things it
// names exist (the work directory's filesystem, the state Space, the identity
// provider) is the startup's business.
func LoadBackupd(environ []string) (Backupd, error) {
	var c Backupd
	err := parse(environ, &c)
	if errors.Is(err, ErrPlaceholder) {
		// Every diagnosis after an unfinished manifest is of a symptom.
		return Backupd{}, err
	}
	// Validated even when a variable did not parse, so one restart shows
	// every problem; a variable that did not parse is at its zero value.
	if err := errors.Join(err, c.validate()); err != nil {
		return Backupd{}, err
	}
	return c, nil
}

// validate checks what depends on more than one variable, or on a variable's
// meaning rather than its type, and normalises what it checks.
func (c *Backupd) validate() error {
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	basePath, err := cleanBasePath(c.HTTP.BasePath)
	add(err)
	c.HTTP.BasePath = basePath

	if (c.HTTP.TLSCertFile == "") != (c.HTTP.TLSKeyFile == "") {
		add(errors.New("TLS_CERT_FILE and TLS_KEY_FILE must be set together"))
	}

	if c.OIDC.Issuer != "" {
		if c.OIDC.Audience == "" {
			add(errors.New(
				"OIDC_AUDIENCE is required when OIDC_ISSUER is set: without it any token " +
					"the same issuer minted for any application is accepted here"))
		}
		// The token's `sub` is not the OpenCloud user id, and every
		// authorization decision is taken on the latter (pkg/api/users.go).
		if c.OpenCloud.BaseURL == "" {
			add(errors.New(
				"OC_BASE_URL is required when OIDC_ISSUER is set: a caller's OpenCloud " +
					"user id is read from its graph API, and the token's subject is not that id"))
		}
	}

	// Every CS3 call is made as the service account. Without it the service
	// used to start, pass its readiness probe on nothing, and fail on the
	// first gateway call with an authentication error that named neither
	// variable.
	if c.CS3.GatewayAddr != "" && (!c.CS3.ServiceAccountID.IsSet() || !c.CS3.ServiceAccountSecret.IsSet()) {
		add(errors.New(
			"OC_SERVICE_ACCOUNT_ID and OC_SERVICE_ACCOUNT_SECRET are required when CS3_GATEWAY_ADDR " +
				"is set: every CS3 call is made as the service account"))
	}

	if c.Scheduler.Timezone != "" {
		loc, err := time.LoadLocation(c.Scheduler.Timezone)
		if err != nil {
			add(fmt.Errorf("SCHEDULE_TIMEZONE %q is not a known timezone", c.Scheduler.Timezone))
		}
		c.Scheduler.location = loc
	}

	add(c.Keys.validate())
	return errors.Join(errs...)
}

// validate checks each key's shape and refuses the one combination that looks
// like a working configuration but is not: the same key in both roles.
//
// SRW guards Data Keys, TW guards target credentials, and decisions.md keeps
// them distinct so the two can rotate independently — retiring a leaked target
// credential must not mean re-wrapping every Space's Data Key. Setting them to
// one value silently collapses that into a single blast radius, and the failure
// is invisible: everything works. It is a misconfiguration to catch at boot.
func (k Keys) validate() error {
	var errs []error
	decoded := map[string][]byte{}
	for _, named := range []struct {
		env string
		key Secret
	}{
		{"SRW_KEY", k.SRW}, {"SRW_KEY_OLD", k.SRWOld}, {"TW_KEY", k.TW}, {"TW_KEY_OLD", k.TWOld},
	} {
		key, err := DecodeWrapKey(named.env, named.key)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		decoded[named.env] = key
	}
	defer func() {
		for _, key := range decoded {
			keys.Zeroize(key)
		}
	}()

	srw, tw := decoded["SRW_KEY"], decoded["TW_KEY"]
	// Constant-time because both operands are secrets, and cheap either way.
	if srw != nil && tw != nil && subtle.ConstantTimeCompare(srw, tw) == 1 {
		errs = append(errs, errors.New(
			"SRW_KEY and TW_KEY must be different keys: data-key custody and "+
				"target-credential custody are separate on purpose (decisions.md #14)"))
	}
	return errors.Join(errs...)
}

// DecodeWrapKey decodes a base64-encoded 256-bit wrapping key (SRW or TW). It
// returns (nil, nil) when the key is unset, so the caller decides whether the
// feature is optional. The caller owns the returned bytes and zeroizes them.
//
// Errors describe only the shape of the problem, never the value (AGENTS.md:
// never log key material).
func DecodeWrapKey(env string, s Secret) ([]byte, error) {
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

// BackupdNames lists every variable the service reads, in declaration order.
func BackupdNames() []string {
	vars := variables(reflect.ValueOf(&Backupd{}).Elem())
	names := make([]string, len(vars))
	for i, v := range vars {
		names[i] = v.env
	}
	return names
}

// LogValue is the effective configuration, one attribute per variable, keyed
// by the variable's name in lower case. Secrets show only whether they are
// set: the Secret type redacts itself, so this needs no list of names.
func (c Backupd) LogValue() slog.Value {
	return effective(&c)
}

// effective renders every variable of cfg, a pointer to a struct.
func effective(cfg any) slog.Value {
	vars := variables(reflect.ValueOf(cfg).Elem())
	attrs := make([]slog.Attr, 0, len(vars))
	for _, v := range vars {
		attrs = append(attrs, slog.Any(strings.ToLower(v.env), v.value.Interface()))
	}
	return slog.GroupValue(attrs...)
}
