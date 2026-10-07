package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func TestUnstampedBuildIsDev(t *testing.T) {
	got := resolve(stamp{}, nil)
	if got.Version != DevVersion {
		t.Fatalf("Version = %q, want %q", got.Version, DevVersion)
	}
	if got.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("Platform = %q", got.Platform)
	}
	if got.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q", got.GoVersion)
	}
}

// A build inside the module's own checkout records "(devel)", which is not a
// version anybody can look up.
func TestDevelIsNotAVersion(t *testing.T) {
	got := resolve(stamp{}, &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}})
	if got.Version != DevVersion {
		t.Fatalf("Version = %q, want %q", got.Version, DevVersion)
	}
}

// `go install …@v1.2.3` records the module version; that is a real one.
func TestGoInstallVersionIsUsed(t *testing.T) {
	got := resolve(stamp{}, &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}})
	if got.Version != "v1.2.3" {
		t.Fatalf("Version = %q", got.Version)
	}
}

func TestToolchainRecordFillsTheGaps(t *testing.T) {
	recorded := &debug.BuildInfo{
		GoVersion: "go1.99.0",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123"},
			{Key: "vcs.time", Value: "2026-10-07T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := resolve(stamp{}, recorded)
	if got.Commit != "0123456789abcdef0123" || got.Date != "2026-10-07T10:00:00Z" || !got.Modified {
		t.Fatalf("info = %+v", got)
	}
	if got.GoVersion != "go1.99.0" {
		t.Fatalf("GoVersion = %q", got.GoVersion)
	}
}

// What the release stamps wins over what the build cache recorded.
func TestStampWins(t *testing.T) {
	recorded := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.1"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "recorded"},
			{Key: "vcs.time", Value: "recorded-time"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := resolve(stamp{Version: "v1.0.0", Commit: "stamped", Date: "stamped-date"}, recorded)
	if got.Version != "v1.0.0" || got.Commit != "stamped" || got.Date != "stamped-date" || got.Modified {
		t.Fatalf("info = %+v", got)
	}
}

func TestLine(t *testing.T) {
	info := Info{Version: "v1.2.3", Commit: "0123456789abcdef", GoVersion: "go1.26.0", Platform: "linux/amd64"}
	if got, want := info.Line("decrypt"), "decrypt v1.2.3 (0123456789ab, go1.26.0, linux/amd64)"; got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}

	info.Modified = true
	if got := info.String(); !strings.Contains(got, "0123456789ab+modified") {
		t.Fatalf("String = %q, want the modified marker", got)
	}

	info.Commit = "abc"
	if got := info.String(); !strings.HasPrefix(got, "v1.2.3 (abc+modified,") {
		t.Fatalf("String = %q", got)
	}

	info.Commit = ""
	if got := info.String(); !strings.Contains(got, "commit unknown") {
		t.Fatalf("String = %q, want it to admit the commit is unknown", got)
	}
}

func TestGetReadsTheRunningBinary(t *testing.T) {
	got := Get()
	if got.Version == "" || got.GoVersion == "" || got.Platform == "" {
		t.Fatalf("Get = %+v", got)
	}
}
