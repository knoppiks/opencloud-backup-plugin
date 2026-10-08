package ocversion

import (
	"strings"
	"testing"
)

// digestOf builds a well-formed digest at runtime rather than spelling out a
// 64-character hex literal (AGENTS.md, secret scanning).
func digestOf(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func validPins() Pins {
	return Pins{
		// The policy's shape at a future date: majors 7 and 8, a
		// Production line cut from 8.2 and patched past it.
		Default: "newest",
		Legs: []Leg{
			{Name: "production", Channel: ChannelProduction, Image: "example/oc", Tag: "8.2.3", Digest: digestOf("a")},
			{Name: "oldest", Channel: ChannelRolling, Image: "example/oc-rolling", Tag: "7.3.0", Digest: digestOf("b")},
			{Name: "previous-major", Channel: ChannelRolling, Image: "example/oc-rolling", Tag: "7.5.0", Digest: digestOf("e")},
			{Name: "newest", Channel: ChannelRolling, Image: "example/oc-rolling", Tag: "8.2.0", Digest: digestOf("c")},
		},
		Canary: Leg{Name: "canary", Channel: ChannelRolling, Image: "example/oc-rolling", Tag: "latest"},
	}
}

func TestEmbeddedPinsAreValid(t *testing.T) {
	p, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if p.Canary.Digest != "" {
		t.Errorf("canary is pinned to %s; it must follow its tag", p.Canary.Digest)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Pins)
		want   string
	}{
		{"valid", func(*Pins) {}, ""},
		{"no legs", func(p *Pins) { p.Legs = nil }, "no legs"},
		{"unknown default", func(p *Pins) { p.Default = "nope" }, `default "nope"`},
		{"duplicate name", func(p *Pins) { p.Legs[1].Name = "production" }, "duplicate"},
		{"unnamed leg", func(p *Pins) { p.Legs[0].Name = "" }, "no name"},
		{"lts channel", func(p *Pins) { p.Legs[0].Channel = "lts" }, "channel"},
		{"no image", func(p *Pins) { p.Legs[0].Image = "" }, "no image"},
		{"unpinned leg", func(p *Pins) { p.Legs[0].Digest = "" }, "digest"},
		{"short digest", func(p *Pins) { p.Legs[0].Digest = "sha256:abc" }, "digest"},
		{"floating tag", func(p *Pins) { p.Legs[0].Tag = "latest" }, "tag"},
		{"production only", func(p *Pins) { p.Legs = p.Legs[:1]; p.Default = "production" }, "no rolling"},
		{"three majors", func(p *Pins) { p.Legs[1].Tag = "6.2.0" }, "span majors 6 to 8"},
		{"three majors, newest last", func(p *Pins) { p.Legs[3].Tag = "9.0.0" }, "span majors 7 to 9"},
		{"production older than both majors", func(p *Pins) {
			p.Legs[0].Tag = "6.2.1"
		}, ""},
		{"canary without tag", func(p *Pins) { p.Canary.Tag = "" }, "canary"},
		{"canary named like a leg", func(p *Pins) { p.Canary.Name = "production" }, "collides"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPins()
			tt.mutate(&p)
			err := p.validate()
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && err == nil:
				t.Fatalf("expected an error containing %q", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("legs: [")); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestRef(t *testing.T) {
	p := validPins()
	if got, want := p.Legs[0].Ref(), "example/oc:8.2.3@"+digestOf("a"); got != want {
		t.Errorf("pinned Ref() = %q, want %q", got, want)
	}
	if got, want := p.Canary.Ref(), "example/oc-rolling:latest"; got != want {
		t.Errorf("canary Ref() = %q, want %q", got, want)
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]Version{
		"8.1.0":   {8, 1, 0},
		"v7.2.4":  {7, 2, 4},
		"10.0.12": {10, 0, 12},
	} {
		got, err := ParseVersion(in)
		if err != nil || got != want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "8", "8.1", "8.1.0.1", "8.1.x", "8.1.0-rc.1", "-1.0.0", "latest"} {
		if _, err := ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) accepted", in)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	v := func(s string) Version {
		t.Helper()
		x, err := ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	tests := []struct {
		a, b string
		want int
	}{
		{"7.3.0", "7.3.0", 0},
		{"7.3.0", "7.5.0", -1},
		{"8.0.0", "7.5.0", 1},
		{"7.2.4", "7.2.10", -1},
		{"7.10.0", "7.9.9", 1},
	}
	for _, tt := range tests {
		if got := v(tt.a).Compare(v(tt.b)); got != tt.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
	if s := v("8.1.0").String(); s != "8.1.0" {
		t.Errorf("String() = %q", s)
	}
}

func TestSupportedSentence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Pins)
		want   string
	}{
		{
			"range plus a production line",
			func(*Pins) {},
			"**Supported OpenCloud versions: 7.3.0 to 8.2.0, plus every 8.2.x patch release.**",
		},
		{
			"legs out of order",
			func(p *Pins) { p.Legs[1], p.Legs[3] = p.Legs[3], p.Legs[1] },
			"**Supported OpenCloud versions: 7.3.0 to 8.2.0, plus every 8.2.x patch release.**",
		},
		{
			"no production leg",
			func(p *Pins) { p.Legs = p.Legs[1:] },
			"**Supported OpenCloud versions: 7.3.0 to 8.2.0.**",
		},
		{
			"one leg",
			func(p *Pins) { p.Legs = p.Legs[3:] },
			"**Supported OpenCloud versions: 8.2.0.**",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPins()
			tt.mutate(&p)
			if got := p.SupportedSentence(); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}
