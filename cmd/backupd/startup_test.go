package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

	t.Setenv(workDirVar, dir)
	t.Setenv(workDirAllowDiskVar, "")
	if _, err := resolveWorkDir(quietLogger()); err == nil {
		t.Fatal("a disk-backed work directory must be refused by default")
	} else if !strings.Contains(err.Error(), workDirAllowDiskVar) {
		t.Fatalf("err = %v, want it to name the override", err)
	}

	t.Setenv(workDirAllowDiskVar, "true")
	got, err := resolveWorkDir(quietLogger())
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

	t.Setenv(workDirVar, dir)
	t.Setenv(workDirAllowDiskVar, "")
	if _, err := resolveWorkDir(quietLogger()); err != nil {
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

	t.Setenv(workDirVar, dir)
	t.Setenv(workDirAllowDiskVar, "true")
	if _, err := resolveWorkDir(quietLogger()); err != nil {
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
	t.Setenv(workDirVar, filepath.Join(t.TempDir(), "not-created"))
	t.Setenv(workDirAllowDiskVar, "true")
	if _, err := resolveWorkDir(quietLogger()); err == nil {
		t.Fatal("a work directory that does not exist must be refused")
	}
}

func TestMemoryStateRequested(t *testing.T) {
	for value, want := range map[string]bool{
		"":         false,
		"memory":   true,
		"  Memory": true,
		"cs3":      false,
		"mem":      false,
	} {
		t.Setenv(stateBackendVar, value)
		if got := memoryStateRequested(); got != want {
			t.Fatalf("%s=%q -> %v, want %v", stateBackendVar, value, got, want)
		}
	}
}

// Half a TLS configuration is a deployment that thinks it is encrypted.
func TestTLSFiles(t *testing.T) {
	t.Setenv(tlsCertVar, "")
	t.Setenv(tlsKeyVar, "")
	cert, key, err := tlsFiles()
	if err != nil || cert != "" || key != "" {
		t.Fatalf("unset = (%q, %q, %v), want plain HTTP", cert, key, err)
	}

	t.Setenv(tlsCertVar, "/etc/tls/tls.crt")
	if _, _, err := tlsFiles(); err == nil {
		t.Fatal("a certificate without a key must be refused")
	}

	t.Setenv(tlsKeyVar, "/etc/tls/tls.key")
	cert, key, err = tlsFiles()
	if err != nil {
		t.Fatalf("tlsFiles: %v", err)
	}
	if cert != "/etc/tls/tls.crt" || key != "/etc/tls/tls.key" {
		t.Fatalf("tlsFiles = (%q, %q)", cert, key)
	}
}

// The base path is a path, not an origin. The browser client refuses an
// absolute URL on its side for the same reason: the extension and this service
// must share an origin, because nothing here implements CORS.
func TestResolveBasePath(t *testing.T) {
	cases := map[string]struct {
		set     string
		want    string
		wantErr string
	}{
		"unset":                {set: "", want: ""},
		"prefix":               {set: "/backup", want: "/backup"},
		"trailing slash":       {set: "/backup/", want: "/backup"},
		"nested":               {set: "/apps/backup", want: "/apps/backup"},
		"whitespace":           {set: "  /backup  ", want: "/backup"},
		"root means unset":     {set: "/", want: ""},
		"double root is unset": {set: "//", want: ""},

		"absolute URL":       {set: "https://backup.example.org/api", wantErr: "not a URL"},
		"scheme relative":    {set: "//backup.example.org/api", wantErr: "clean path"},
		"no leading slash":   {set: "backup", wantErr: "must start with a slash"},
		"query":              {set: "/backup?x=1", wantErr: "not a URL"},
		"fragment":           {set: "/backup#x", wantErr: "not a URL"},
		"empty segment":      {set: "/backup//api", wantErr: "clean path"},
		"relative segment":   {set: "/backup/../etc", wantErr: "clean path"},
		"shadows healthz":    {set: "/healthz", wantErr: "health probes"},
		"shadows readyz":     {set: "/readyz", wantErr: "health probes"},
		"shadows with slash": {set: "/readyz/", wantErr: "health probes"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(basePathVar, tc.set)
			got, err := resolveBasePath()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveBasePath(%q) = %q, want an error", tc.set, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBasePath(%q): %v", tc.set, err)
			}
			if got != tc.want {
				t.Fatalf("resolveBasePath(%q) = %q, want %q", tc.set, got, tc.want)
			}
		})
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

// A manifest deployed unedited must not come up and then reject every request.
func TestCheckPlaceholders(t *testing.T) {
	cases := map[string]struct {
		environ []string
		want    []string
	}{
		"edited": {
			environ: []string{"OIDC_AUDIENCE=web", "STATE_SPACE_ID=storage$space", "PATH=/usr/bin"},
		},
		"unedited audience and state space": {
			environ: []string{
				"STATE_SPACE_ID=REPLACE_ME_WITH_THE_STATE_SPACE_ID",
				"OIDC_AUDIENCE=REPLACE_ME_WITH_THE_OIDC_CLIENT_ID",
			},
			want: []string{"OIDC_AUDIENCE", "STATE_SPACE_ID"},
		},
		"unedited wrapping key": {
			environ: []string{"SRW_KEY=REPLACE_ME_GENERATE_OUT_OF_BAND"},
			want:    []string{"SRW_KEY"},
		},
		// Somebody else's placeholder is somebody else's problem: refusing to
		// start over a variable this service never reads would be a surprise
		// with no fix inside this deployment.
		"foreign placeholder": {
			environ: []string{"SOME_OTHER_CHART_TOKEN=REPLACE_ME"},
		},
	}

	for name, tc := range cases {
		got := unreplacedPlaceholders(tc.environ)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: unreplacedPlaceholders = %v, want %v", name, got, tc.want)
		}

		err := checkPlaceholders(tc.environ)
		if (err != nil) != (len(tc.want) > 0) {
			t.Errorf("%s: checkPlaceholders = %v", name, err)
			continue
		}
		for _, want := range tc.want {
			if err != nil && !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not name %s", name, err, want)
			}
		}
	}
}

// The shipped manifests must be caught by the check that exists for them.
func TestShippedManifestPlaceholdersAreRefused(t *testing.T) {
	for _, path := range []string{"../../deploy/deployment-backupd.yaml", "../../deploy/secret-wrap-keys.yaml"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(body), placeholderMarker) {
			t.Errorf("%s no longer marks the values a deployer must fill in with %s; "+
				"the startup check has nothing to catch", path, placeholderMarker)
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
