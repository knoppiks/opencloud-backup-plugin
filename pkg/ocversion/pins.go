// Package ocversion knows which OpenCloud versions this plugin is tested
// against. The pins live in versions.yaml next to this file — the one place in
// the repository that names an OpenCloud image (compatibility-policy.md §3).
// The test fixture, CI and the README all derive from it, and the file is
// embedded so the build carries the window it was tested in.
package ocversion

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Channels OpenCloud publishes on that a leg may belong to. LTS is out of
// scope (compatibility-policy.md §3).
const (
	ChannelProduction = "production"
	ChannelRolling    = "rolling"
)

//go:embed versions.yaml
var embedded []byte

// Leg is one OpenCloud version the plugin is tested against.
type Leg struct {
	// Name is the leg's role ("newest-rolling"), stable across version bumps
	// so CI job names can be required by a ruleset.
	Name    string `yaml:"name"`
	Channel string `yaml:"channel"`
	Image   string `yaml:"image"`
	Tag     string `yaml:"tag"`
	// Digest pins the image; empty only for the canary, which follows its tag.
	Digest string `yaml:"digest"`
}

// Ref is the reference a container runtime pulls: image:tag, plus @digest
// when the leg is pinned.
func (l Leg) Ref() string {
	ref := l.Image + ":" + l.Tag
	if l.Digest != "" {
		ref += "@" + l.Digest
	}
	return ref
}

// Pins is the parsed versions.yaml.
type Pins struct {
	// Default is the leg the fixture starts when none is chosen.
	Default string `yaml:"default"`
	// Legs are the supported versions; CI runs every one of them, blocking.
	Legs []Leg `yaml:"legs"`
	// Canary follows the newest release by tag and is not supported.
	Canary Leg `yaml:"canary"`
}

// Embedded returns the pins compiled into this build. The file is validated
// by the package's tests, so an error here means a broken build.
func Embedded() (Pins, error) {
	return Parse(embedded)
}

// Parse decodes and validates a versions.yaml document.
func Parse(data []byte) (Pins, error) {
	var p Pins
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Pins{}, fmt.Errorf("parse OpenCloud pins: %w", err)
	}
	if err := p.validate(); err != nil {
		return Pins{}, fmt.Errorf("invalid OpenCloud pins: %w", err)
	}
	return p, nil
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (p Pins) validate() error {
	if len(p.Legs) == 0 {
		return errors.New("no legs")
	}
	seen := map[string]bool{}
	for _, l := range p.Legs {
		if err := l.validatePinned(); err != nil {
			return fmt.Errorf("leg %q: %w", l.Name, err)
		}
		if seen[l.Name] {
			return fmt.Errorf("leg %q: duplicate name", l.Name)
		}
		seen[l.Name] = true
	}
	if !seen[p.Default] {
		return fmt.Errorf("default %q is not a leg", p.Default)
	}
	if !slices.ContainsFunc(p.Legs, func(l Leg) bool { return l.Channel == ChannelRolling }) {
		return errors.New("no rolling leg")
	}
	if p.Canary.Name == "" || p.Canary.Image == "" || p.Canary.Tag == "" {
		return errors.New("canary needs name, image and tag")
	}
	if seen[p.Canary.Name] {
		return fmt.Errorf("canary %q: name collides with a leg", p.Canary.Name)
	}
	return nil
}

func (l Leg) validatePinned() error {
	switch {
	case l.Name == "":
		return errors.New("no name")
	case l.Channel != ChannelProduction && l.Channel != ChannelRolling:
		return fmt.Errorf("channel %q is neither %q nor %q", l.Channel, ChannelProduction, ChannelRolling)
	case l.Image == "":
		return errors.New("no image")
	case !digestPattern.MatchString(l.Digest):
		return fmt.Errorf("digest %q is not a sha256 digest", l.Digest)
	}
	if _, err := ParseVersion(l.Tag); err != nil {
		return fmt.Errorf("tag: %w", err)
	}
	return nil
}

// Version is an OpenCloud release number, MAJOR.MINOR.PATCH.
type Version struct{ Major, Minor, Patch int }

// ParseVersion reads "8.1.0" (a leading "v" is accepted). Pre-release and
// build suffixes are refused: no leg pins one, and a canary on one is not a
// supported version anyway.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	var n [3]int
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 {
			return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
		}
		n[i] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2]}, nil
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare returns -1, 0 or +1 as v is older than, equal to or newer than w.
func (v Version) Compare(w Version) int {
	for _, d := range [3]int{v.Major - w.Major, v.Minor - w.Minor, v.Patch - w.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionsOf returns the parsed tags of the legs on channel, oldest first.
// Tags were validated by Parse.
func (p Pins) versionsOf(channel string) []Version {
	var vs []Version
	for _, l := range p.Legs {
		if l.Channel == channel {
			v, _ := ParseVersion(l.Tag)
			vs = append(vs, v)
		}
	}
	slices.SortFunc(vs, Version.Compare)
	return vs
}

// SupportedSentence is the README's claim, generated so it cannot drift from
// what CI runs. A Production leg stands for its whole line (all patch
// releases, policy §3); Rolling is the range between the outer legs.
func (p Pins) SupportedSentence() string {
	var parts []string
	if prod := p.versionsOf(ChannelProduction); len(prod) > 0 {
		newest := prod[len(prod)-1]
		parts = append(parts, fmt.Sprintf("Production %d.%d.x", newest.Major, newest.Minor))
	}
	rolling := p.versionsOf(ChannelRolling)
	oldest, newest := rolling[0], rolling[len(rolling)-1]
	if oldest == newest {
		parts = append(parts, "Rolling "+oldest.String())
	} else {
		parts = append(parts, fmt.Sprintf("Rolling %s to %s", oldest, newest))
	}
	return "**Supported OpenCloud versions: " + strings.Join(parts, " and ") + ".**"
}
