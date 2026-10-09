package state_test

// Fuzz targets for the key-segment escaping. State-Space record names are
// built from OpenCloud ids and read back into ids from a listing, so the
// escaping is part of the record layout the compatibility policy promises to
// keep reading (compatibility-policy.md §2).

import (
	"strings"
	"testing"

	"opencloud-backup-plugin/pkg/state"
)

// segmentSeeds are the shapes segments really take, plus the escapes a
// hand-edited or foreign key could hold.
var segmentSeeds = []string{
	"simple",
	"1284d238-aa92-42ce-bdc4-0b0000009157$4c510ada!4c510ada",
	"with/slash",
	".", "..", "",
	"ümlaut",
	"%2E%2E", "%2F", "%", "%4", "%zz", "%2e", "%61", "a$b",
}

func FuzzEscapeSegment(f *testing.F) {
	for _, s := range segmentSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		esc := state.EscapeSegment(s)
		if esc == "." || esc == ".." {
			t.Fatalf("%q escapes to the path segment %q", s, esc)
		}
		if i := strings.IndexFunc(esc, unsafeInSegment); i >= 0 {
			t.Fatalf("%q escapes to %q, which holds %q", s, esc, esc[i])
		}
		back, err := state.UnescapeSegment(esc)
		if err != nil {
			t.Fatalf("UnescapeSegment(%q): %v", esc, err)
		}
		if back != s {
			t.Fatalf("round trip of %q gave %q", s, back)
		}
	})
}

func FuzzUnescapeSegment(f *testing.F) {
	for _, s := range segmentSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := state.UnescapeSegment(s)
		if err != nil {
			return
		}
		// Accepted means canonical: s is the one key EscapeSegment writes for
		// out. Anything looser lets two different keys name the same record.
		if esc := state.EscapeSegment(out); esc != s {
			t.Fatalf("UnescapeSegment accepted %q for %q, whose key is %q", s, out, esc)
		}
	})
}

// unsafeInSegment reports a rune EscapeSegment must never leave in its output.
func unsafeInSegment(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case r == '.' || r == '_' || r == '-' || r == '%':
		return false
	default:
		return true
	}
}
