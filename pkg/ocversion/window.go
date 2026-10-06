package ocversion

import (
	"fmt"
	"strings"
)

// Window is the set of OpenCloud versions a build supports
// (compatibility-policy.md §3): every patch release of the Production lines
// it was tested on, plus the Rolling releases between its oldest and newest
// tested Rolling leg.
//
// A Rolling release newer than the newest leg is outside, even a patch
// release: supported means tested, and it was not.
type Window struct {
	// ProductionLines are MAJOR.MINOR pairs; Patch is ignored.
	ProductionLines []Version
	OldestRolling   Version
	NewestRolling   Version
}

// Window derives the support window from the pinned legs. Parse guarantees at
// least one Rolling leg.
func (p Pins) Window() Window {
	var w Window
	for _, v := range p.versionsOf(ChannelProduction) {
		line := Version{Major: v.Major, Minor: v.Minor}
		// Sorted, so two legs on one line are adjacent.
		if n := len(w.ProductionLines); n == 0 || w.ProductionLines[n-1] != line {
			w.ProductionLines = append(w.ProductionLines, line)
		}
	}
	rolling := p.versionsOf(ChannelRolling)
	w.OldestRolling, w.NewestRolling = rolling[0], rolling[len(rolling)-1]
	return w
}

// Contains reports whether v is inside the window.
func (w Window) Contains(v Version) bool {
	for _, line := range w.ProductionLines {
		if v.Major == line.Major && v.Minor == line.Minor {
			return true
		}
	}
	return v.Compare(w.OldestRolling) >= 0 && v.Compare(w.NewestRolling) <= 0
}

// String is the window in words, e.g. "Production 7.2.x and Rolling 7.3.0 to
// 8.1.0". It is what the README states and what the admin view repeats.
func (w Window) String() string {
	var parts []string
	for _, line := range w.ProductionLines {
		parts = append(parts, fmt.Sprintf("Production %d.%d.x", line.Major, line.Minor))
	}
	if w.OldestRolling == w.NewestRolling {
		parts = append(parts, "Rolling "+w.OldestRolling.String())
	} else {
		parts = append(parts, fmt.Sprintf("Rolling %s to %s", w.OldestRolling, w.NewestRolling))
	}
	return strings.Join(parts, " and ")
}
