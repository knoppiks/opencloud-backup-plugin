package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"opencloud-backup-plugin/internal/config"
	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/snapshot"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The work directory holds kopia's per-run cache. A disk-backed one is allowed,
// but only when the operator has said so — silence must not mean "yes".
func TestResolveWorkDir_RefusesDiskUnlessAllowed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the filesystem check only answers on Linux")
	}
	dir := diskBackedDir(t)

	if _, err := resolveWorkDir(workEnv{WorkDir: dir}, quietLogger()); err == nil {
		t.Fatal("a disk-backed work directory must be refused by default")
	} else if !strings.Contains(err.Error(), workDirAllowDiskVar) {
		t.Fatalf("err = %v, want it to name the override", err)
	}

	got, err := resolveWorkDir(workEnv{WorkDir: dir, WorkDirAllowDisk: true}, quietLogger())
	if err != nil {
		t.Fatalf("resolveWorkDir with the override set: %v", err)
	}
	if got != dir {
		t.Fatalf("work dir = %q, want %q", got, dir)
	}
}

// diskBackedDir returns a directory that is definitely not memory-backed. The
// OS temp directory often is (systemd mounts /tmp on tmpfs), so the fallback is
// a directory beside the source tree, which is on a real filesystem by
// definition — it is where this test's source file lives.
func diskBackedDir(t *testing.T) string {
	t.Helper()
	candidates := []string{t.TempDir()}
	if local, err := os.MkdirTemp(".", "workdir-test-*"); err == nil {
		t.Cleanup(func() { _ = os.RemoveAll(local) })
		candidates = append(candidates, local)
	}
	for _, dir := range candidates {
		memory, err := snapshot.MemoryBacked(dir)
		if err == nil && !memory {
			return dir
		}
	}
	t.Skip("no disk-backed directory available to test the refusal with")
	return ""
}

func TestResolveWorkDir_AcceptsAMemoryBackedDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the filesystem check only answers on Linux")
	}
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("no tmpfs available to point at")
	}
	dir, err := os.MkdirTemp("/dev/shm", "backupd-workdir-*")
	if err != nil {
		t.Fatalf("create tmpfs work dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if _, err := resolveWorkDir(workEnv{WorkDir: dir}, quietLogger()); err != nil {
		t.Fatalf("a memory-backed work directory must be accepted: %v", err)
	}
}

// A crashed process leaves its run directory behind; the next start clears it.
func TestResolveWorkDir_SweepsLeftovers(t *testing.T) {
	dir := t.TempDir()
	leftover := filepath.Join(dir, "kopia-run-abandoned")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}

	if _, err := resolveWorkDir(workEnv{WorkDir: dir, WorkDirAllowDisk: true}, quietLogger()); err != nil {
		t.Fatalf("resolveWorkDir: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatal("the leftover run directory survived startup")
	}
}

func TestResolveWorkDir_RejectsAMissingDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the filesystem check only answers on Linux")
	}
	cfg := workEnv{WorkDir: filepath.Join(t.TempDir(), "not-created"), WorkDirAllowDisk: true}
	if _, err := resolveWorkDir(cfg, quietLogger()); err == nil {
		t.Fatal("a work directory that does not exist must be refused")
	}
}

// The prefix must reach the routes as if it were not there, and must not move
// the health probes: the kubelet hits those directly on the pod and knows
// nothing about the ingress that adds the prefix.
func TestMountBasePath(t *testing.T) {
	inner := http.NewServeMux()
	inner.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "healthz")
	})
	inner.HandleFunc("/api/v1/spaces", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "spaces")
	})

	cases := map[string]struct {
		basePath string
		request  string
		wantCode int
		wantBody string
	}{
		"no prefix serves routes at the root": {
			basePath: "", request: "/api/v1/spaces", wantCode: 200, wantBody: "spaces",
		},
		"no prefix serves the probe": {
			basePath: "", request: "/healthz", wantCode: 200, wantBody: "healthz",
		},
		"prefix serves routes beneath it": {
			basePath: "/backup", request: "/backup/api/v1/spaces", wantCode: 200, wantBody: "spaces",
		},
		"prefix keeps the probe at the root": {
			basePath: "/backup", request: "/healthz", wantCode: 200, wantBody: "healthz",
		},
		// The whole point of the prefix: the unprefixed path must not be a
		// second way in, or a collision with OpenCloud's own /api/v1 would
		// still be reachable.
		"prefix does not also serve the bare path": {
			basePath: "/backup", request: "/api/v1/spaces", wantCode: 404,
		},
		// The probe is reachable at both places, and that is deliberate: the
		// whole handler mounts under the prefix, and the root entry exists to
		// add the kubelet's path rather than to take the other one away.
		"prefix also serves the probe beneath it": {
			basePath: "/backup", request: "/backup/healthz", wantCode: 200, wantBody: "healthz",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mountBasePath(inner, tc.basePath).ServeHTTP(
				rec, httptest.NewRequest(http.MethodGet, tc.request, nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("GET %q with basePath %q = %d, want %d",
					tc.request, tc.basePath, rec.Code, tc.wantCode)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestChainRunsEveryCleanupInOrder(t *testing.T) {
	var order []string
	chain(
		func() { order = append(order, "first") },
		nil,
		func() { order = append(order, "second") },
	)()
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("cleanup order = %v", order)
	}
}

// The shipped manifests must be caught by the check that exists for them.
func TestShippedManifestPlaceholdersAreRefused(t *testing.T) {
	for _, path := range []string{"../../deploy/deployment-backupd.yaml", "../../deploy/secret-wrap-keys.yaml"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(body), config.PlaceholderMarker) {
			t.Errorf("%s no longer marks the values a deployer must fill in with %s; "+
				"the startup check has nothing to catch", path, config.PlaceholderMarker)
		}
	}

	manifest, err := os.ReadFile("../../deploy/deployment-backupd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	placeholders := regexp.MustCompile(`- name: ([A-Z0-9_]+)\s+value: "([^"]*`+config.PlaceholderMarker+`[^"]*)"`).
		FindAllStringSubmatch(string(manifest), -1)
	if len(placeholders) == 0 {
		t.Fatal("the manifest has no placeholder in a plain env value any more; this test checks nothing")
	}
	for _, m := range placeholders {
		if _, err := loadServiceEnv([]string{m[1] + "=" + m[2]}); !errors.Is(err, config.ErrPlaceholder) {
			t.Errorf("%s=%s from the manifest is not refused: %v", m[1], m[2], err)
		}
	}
}

// Every variable the shipped manifests set, or offer in a comment, is one the
// service reads: a manifest that sets a misspelt or retired name configures
// nothing, silently.
func TestShippedManifestsNameOnlyKnownVariables(t *testing.T) {
	known := map[string]bool{
		// Read by the Go runtime, not by the service's configuration.
		"TZ": true, "SSL_CERT_DIR": true, "SSL_CERT_FILE": true,
	}
	env := newServiceEnv()
	for _, name := range config.Names(&env) {
		known[name] = true
	}
	for path, pattern := range map[string]string{
		"../../deploy/deployment-backupd.yaml":         `(?m)- name: ([A-Z][A-Z0-9_]+)\s*$`,
		"../../deploy/configmap-bootstrap-target.yaml": `(?m)^  ([A-Z][A-Z0-9_]+):`,
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		matches := regexp.MustCompile(pattern).FindAllStringSubmatch(string(body), -1)
		if len(matches) == 0 {
			t.Fatalf("%s: no variables found; this test checks nothing", path)
		}
		for _, m := range matches {
			if !known[m[1]] {
				t.Errorf("%s names %s, which the service does not read", path, m[1])
			}
		}
	}
}

// The image runs this binary with no arguments, because the first argument is
// an operator subcommand. The manifest and the Dockerfile have to agree on that
// or the pod starts, reads "/backupd" as a command it does not know, and exits.
func TestImageEntrypointTakesNoArguments(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if !strings.Contains(string(dockerfile), `ENTRYPOINT ["/backupd"]`) {
		t.Error(`the image must start the service as ENTRYPOINT ["/backupd"], with no arguments`)
	}
	if strings.Contains(string(dockerfile), "\nCMD ") {
		t.Error("a CMD would be appended to the entrypoint and read as an operator subcommand")
	}
	// The user's offline recovery tool has no business on the server.
	if strings.Contains(string(dockerfile), "cmd/decrypt") {
		t.Error("the service image must not ship the decrypt CLI (decisions.md #2, #15)")
	}

	manifest, err := os.ReadFile("../../deploy/deployment-backupd.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(manifest), "args:") {
		t.Error("the deployment must set no args: anything there is read as an operator subcommand")
	}
}

func TestCS3ClientOptions_DataServerURL(t *testing.T) {
	opts, err := cs3ClientOptions(cs3.Env{})
	if err != nil || len(opts) != 1 {
		t.Fatalf("unset: %d options, %v; want the HTTP client only", len(opts), err)
	}

	opts, err = cs3ClientOptions(cs3.Env{DataServerURL: "http://opencloud.files.svc.cluster.local:9158"})
	if err != nil || len(opts) != 2 {
		t.Fatalf("set: %d options, %v; want the HTTP client and the origin", len(opts), err)
	}
}

// Loading refuses a bad URL; dialing refuses it too, for an Env built any
// other way.
func TestDialCS3_RefusesABadDataServerURL(t *testing.T) {
	client, closeFn, err := dialCS3(cs3.Env{GatewayAddr: "127.0.0.1:1", DataServerURL: "not a url"})
	defer closeFn()
	if err == nil || client != nil {
		t.Fatalf("dialCS3 = %v, %v; want a refusal", client, err)
	}
}
