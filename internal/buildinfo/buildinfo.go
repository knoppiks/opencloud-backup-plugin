// Package buildinfo says which build of the plugin is running.
//
// Every artifact of a release carries the same version
// (compatibility-policy.md §1). The release stamps it at link time:
//
//	go build -ldflags "-X opencloud-backup-plugin/internal/buildinfo.Version=v1.2.3 \
//	  -X opencloud-backup-plugin/internal/buildinfo.Commit=<sha> \
//	  -X opencloud-backup-plugin/internal/buildinfo.Date=<RFC 3339>"
//
// Phase 11 wires that into the Makefile, the Dockerfile and the release. Until
// then, and for any build nobody stamped, the version is "dev" and the commit
// and date come from what the Go toolchain recorded (`go install …@v1.2.3`
// records the module version too).
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
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
