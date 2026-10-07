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
	"%2E%2E", "%2F", "%", "%4", "%zz", "%2e",
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
		// Accepted means well formed: every escape is "%" and two hex digits.
		// Anything looser lets two different keys name the same record.
		for i := 0; i < len(s); i++ {
			if s[i] != '%' {
				continue
			}
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				t.Fatalf("UnescapeSegment accepted the malformed escape at %d in %q", i, s)
			}
			i += 2
		}
		again, err := state.UnescapeSegment(state.EscapeSegment(out))
		if err != nil || again != out {
			t.Fatalf("re-escaping %q does not round-trip: %q, %v", out, again, err)
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

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
