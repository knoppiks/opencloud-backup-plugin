package ocversion

import (
	"fmt"
	"strings"
)

// Window is the set of OpenCloud versions a build supports
// (compatibility-policy.md §3): every release from the oldest to the newest
// Rolling leg, plus every patch release of the Production lines a leg pins.
//
// Inside the range the channel does not matter: a Production release is a
// Rolling release OpenCloud kept patching on a side branch. The Production
// line is added on its own because those patches can be newer than the newest
// Rolling leg, and because the line stays supported while it is current even
// once it is older than the two majors the range spans.
//
// A release newer than the newest leg is outside, even a patch release of
// it: supported means tested, and it was not.
type Window struct {
	Oldest Version
	Newest Version
	// ProductionLines are MAJOR.MINOR pairs; Patch is ignored.
	ProductionLines []Version
}

// Window derives the support window from the pinned legs. Parse guarantees at
// least one Rolling leg.
func (p Pins) Window() Window {
	rolling := p.versions(isRolling)
	w := Window{Oldest: rolling[0], Newest: rolling[len(rolling)-1]}
	for _, v := range p.versions(func(l Leg) bool { return l.Channel == ChannelProduction }) {
		line := Version{Major: v.Major, Minor: v.Minor}
		// Sorted, so two legs on one line are adjacent.
		if n := len(w.ProductionLines); n == 0 || w.ProductionLines[n-1] != line {
			w.ProductionLines = append(w.ProductionLines, line)
		}
	}
	return w
}

// Contains reports whether v is inside the window.
func (w Window) Contains(v Version) bool {
	if v.Compare(w.Oldest) >= 0 && v.Compare(w.Newest) <= 0 {
		return true
	}
	for _, line := range w.ProductionLines {
		if v.Major == line.Major && v.Minor == line.Minor {
			return true
		}
	}
	return false
}

// String is the window in words, e.g. "7.3.0 to 8.1.0" or "8.0.0 to 9.1.0,
// plus every 8.2.x patch release". It is what the README states and what the
// admin view repeats.
func (w Window) String() string {
	s := w.Oldest.String()
	if w.Oldest != w.Newest {
		s += " to " + w.Newest.String()
	}
	var lines []string
	for _, line := range w.ProductionLines {
		lines = append(lines, fmt.Sprintf("%d.%d.x", line.Major, line.Minor))
	}
	if len(lines) > 0 {
		s += ", plus every " + strings.Join(lines, " and ") + " patch release"
	}
	return s
}
