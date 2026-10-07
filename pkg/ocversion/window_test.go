package ocversion

import "testing"

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWindowContains(t *testing.T) {
	w := validPins().Window() // 7.3.0 to 8.2.0, plus every 8.2.x patch
	for v, want := range map[string]bool{
		"7.2.4":  false, // below the oldest leg
		"7.3.0":  true,  // oldest leg
		"7.4.0":  true,  // between the legs, never run itself
		"7.5.0":  true,  // newest minor of the previous major
		"8.0.1":  true,  // channel does not matter inside the range
		"8.2.0":  true,  // newest Rolling leg
		"8.2.3":  true,  // Production leg
		"8.2.99": true,  // a later patch of the Production line
		"8.3.0":  false, // newer than anything tested
		"9.0.0":  false,
	} {
		if got := w.Contains(mustVersion(t, v)); got != want {
			t.Errorf("Contains(%s) = %v, want %v", v, got, want)
		}
	}
}

func TestWindowWithoutProduction(t *testing.T) {
	p := validPins()
	p.Legs = p.Legs[1:]
	w := p.Window()
	if w.Contains(mustVersion(t, "8.2.1")) {
		t.Error("8.2.1 is newer than every leg but inside a window with no Production leg")
	}
	if !w.Contains(mustVersion(t, "8.2.0")) {
		t.Error("8.2.0, the newest leg, is outside")
	}
}

// The current Production line stays supported while it is current, even once
// two newer majors have pushed the Rolling range past it; the releases between
// it and the range do not come with it.
func TestWindowKeepsAnOlderProductionLine(t *testing.T) {
	p := validPins()
	p.Legs[0].Tag = "6.2.1"
	w := p.Window()
	for v, want := range map[string]bool{"6.2.0": true, "6.2.7": true, "6.3.0": false, "7.2.0": false} {
		if got := w.Contains(mustVersion(t, v)); got != want {
			t.Errorf("Contains(%s) = %v, want %v", v, got, want)
		}
	}
	if got, want := w.String(), "7.3.0 to 8.2.0, plus every 6.2.x patch release"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWindowDeduplicatesProductionLines(t *testing.T) {
	p := validPins()
	p.Legs = append(p.Legs, Leg{Name: "production-old", Channel: ChannelProduction,
		Image: "example/oc", Tag: "8.2.1", Digest: digestOf("d")})
	if got, want := p.Window().String(), "7.3.0 to 8.2.0, plus every 8.2.x patch release"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
