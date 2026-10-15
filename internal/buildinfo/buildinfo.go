// Package buildinfo says which build of the plugin is running.
//
// Every artifact of a release carries the same version
// (compatibility-policy.md §1). The release stamps it at link time:
//
//	go build -ldflags "-X github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo.Version=v1.2.3 \
//	  -X github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo.Commit=<sha> \
//	  -X github.com/knoppiks/opencloud-backup-plugin/internal/buildinfo.Date=<RFC 3339>"
//
// The Makefile, both Dockerfiles, CI and the release stamp all three: the
// version from `git describe`, the date as the commit's time, so the same
// commit always builds the same binary. For a build nobody stamped the
// version, commit and date come from what the Go toolchain recorded: a build
// inside a checkout records the VCS state; `go install …@<version>` records
// the module version only, from which a pseudo-version's commit and time are
// read. With nothing recorded, the version is "dev".
package buildinfo

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Set at link time with -ldflags -X. They are variables, not constants,
// because -X can only set variables.
var (
	// Version is the plugin version, e.g. "v1.2.3".
	Version = ""
	// Commit is the source revision the build was made from.
	Commit = ""
	// Date is when the build was made, RFC 3339.
	Date = ""
)

// DevVersion is the version of a build nobody stamped.
const DevVersion = "dev"

// Info describes the running build.
type Info struct {
	// Version is the plugin version, or DevVersion.
	Version string
	// Commit is the source revision, or empty when unknown.
	Commit string
	// Date is the build or commit time, or empty when unknown.
	Date string
	// Modified is true when the toolchain recorded uncommitted changes.
	Modified bool
	// GoVersion is the toolchain that built the binary.
	GoVersion string
	// Platform is GOOS/GOARCH.
	Platform string
}

// Get reports the running build: what was stamped at link time, falling
// back to what the toolchain recorded.
func Get() Info {
	recorded, _ := debug.ReadBuildInfo()
	return resolve(stamp{Version: Version, Commit: Commit, Date: Date}, recorded)
}

// stamp is what -ldflags -X set.
type stamp struct{ Version, Commit, Date string }

// resolve combines the link-time stamp with the toolchain's record. A stamped
// value always wins: the release knows better than the build cache.
func resolve(s stamp, recorded *debug.BuildInfo) Info {
	info := Info{
		Version:   s.Version,
		Commit:    s.Commit,
		Date:      s.Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if recorded != nil {
		fillFromToolchain(&info, recorded)
	}
	if info.Version == "" {
		info.Version = DevVersion
	}
	return info
}

// fillFromToolchain fills what the stamp left empty.
func fillFromToolchain(info *Info, recorded *debug.BuildInfo) {
	// "(devel)" is what a build inside the module's own checkout records;
	// it says nothing a reader can use.
	if v := recorded.Main.Version; info.Version == "" && v != "" && v != "(devel)" {
		info.Version = v
	}
	if recorded.GoVersion != "" {
		info.GoVersion = recorded.GoVersion
	}
	stamped := info.Commit != ""
	if !stamped {
		if rev, at, ok := pseudoVersionRevision(recorded.Main.Version); ok {
			info.Commit = rev
			if info.Date == "" {
				info.Date = at
			}
		}
	}
	for _, setting := range recorded.Settings {
		switch setting.Key {
		case "vcs.revision":
			if !stamped {
				info.Commit = setting.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = setting.Value
			}
		case "vcs.modified":
			if !stamped {
				info.Modified = setting.Value == "true"
			}
		}
	}
}

// pseudoVersion matches the tail every Go pseudo-version ends in, whichever
// of its three forms (v0.0.0-T-R, vX.Y.Z-pre.0.T-R, vX.Y.Z-0.T-R): a 14-digit
// UTC time and a 12-hex-digit revision prefix, optionally followed by
// "+incompatible" or the "+dirty" a build in a modified checkout records.
var pseudoVersion = regexp.MustCompile(`[-.](\d{14})-([0-9a-f]{12})(\+incompatible)?(\+dirty)?$`)

// pseudoVersionRevision returns the revision prefix and the commit time
// (RFC 3339) of a pseudo-version. `go install …@<commit>` records nothing
// else about the source: no vcs.* settings, because the module came from
// the proxy, not a checkout.
func pseudoVersionRevision(version string) (rev, at string, ok bool) {
	m := pseudoVersion.FindStringSubmatch(version)
	if m == nil || !strings.HasPrefix(version, "v") {
		return "", "", false
	}
	t, err := time.Parse("20060102150405", m[1])
	if err != nil {
		return "", "", false
	}
	return m[2], t.UTC().Format(time.RFC3339), true
}

// String is the one-line answer to "which version is this?", as printed by
// `-version`: "<program> <version> (<commit>, <go>, <platform>)".
func (i Info) String() string {
	return i.Line("")
}

// Line is String with the program's name in front.
func (i Info) Line(program string) string {
	commit := "commit unknown"
	if i.Commit != "" {
		commit = shortCommit(i.Commit)
		if i.Modified {
			commit += "+modified"
		}
	}
	line := fmt.Sprintf("%s (%s, %s, %s)", i.Version, commit, i.GoVersion, i.Platform)
	if program == "" {
		return line
	}
	return program + " " + line
}

// shortCommit abbreviates a revision the way git does by default.
func shortCommit(rev string) string {
	const short = 12
	if len(rev) > short {
		return rev[:short]
	}
	return rev
}
