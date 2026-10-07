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
	w := validPins().Window() // Production 7.2.x, Rolling 7.3.0 to 8.1.0
	for v, want := range map[string]bool{
		"7.1.9":  false, // before the Production line
		"7.2.0":  true,  // any patch of the Production line
		"7.2.4":  true,
		"7.2.99": true,
		"7.3.0":  true, // oldest Rolling
		"7.4.0":  true, // between the legs, never run itself
		"8.0.1":  true,
		"8.1.0":  true,  // newest Rolling
		"8.1.1":  false, // newer than anything tested
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
	if p.Window().Contains(mustVersion(t, "7.2.4")) {
		t.Error("7.2.4 is inside a window with no Production leg")
	}
}

func TestWindowDeduplicatesProductionLines(t *testing.T) {
	p := validPins()
	p.Legs = append(p.Legs, Leg{Name: "production-old", Channel: ChannelProduction,
		Image: "example/oc", Tag: "7.2.1", Digest: digestOf("d")})
	if got, want := p.Window().String(), "Production 7.2.x and Rolling 7.3.0 to 8.1.0"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
