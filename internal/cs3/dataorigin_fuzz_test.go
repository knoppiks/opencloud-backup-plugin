package cs3

// Fuzz target for the data-server origin override (CS3_DATA_SERVER_URL). The
// value is operator input, and what it accepts decides where file bytes and
// the access token travel, so "accepted" has to mean exactly a scheme and a
// host, and applying it must change nothing but those.

import (
	"net/url"
	"testing"
)

func FuzzParseDataServerOrigin(f *testing.F) {
	for _, s := range []string{
		"http://opencloud:9158",
		"https://opencloud.example.org/",
		"http://[::1]:9158",
		"http://user:pw@host",
		"http://host/data",
		"http://host?x=1",
		"http://host#frag",
		"ftp://host",
		"http:host",
		"//host",
		"",
	} {
		f.Add(s)
	}

	const gatewayEndpoint = "http://localhost:9158/data/spaces/some%2Fid?x=1"
	gateway, err := url.Parse(gatewayEndpoint)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		origin, err := ParseDataServerOrigin(raw)
		if err != nil {
			return
		}
		if origin.Scheme != "http" && origin.Scheme != "https" {
			t.Fatalf("accepted scheme %q", origin.Scheme)
		}
		if origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" ||
			origin.Fragment != "" || origin.Opaque != "" {
			t.Fatalf("accepted more than a scheme and a host: %#v", origin)
		}

		// What the client does with it: the gateway's path and query stay,
		// only scheme and host change, and the result means the same when
		// parsed again.
		c := &Client{dataOrigin: origin}
		out, err := c.dataEndpoint(gatewayEndpoint, "")
		if err != nil {
			t.Fatalf("dataEndpoint: %v", err)
		}
		got, err := url.Parse(out)
		if err != nil {
			t.Fatalf("rewritten endpoint %q does not parse: %v", out, err)
		}
		if got.Scheme != origin.Scheme || got.Host != origin.Host {
			t.Fatalf("rewritten endpoint %q goes to %s://%s, want %s://%s",
				out, got.Scheme, got.Host, origin.Scheme, origin.Host)
		}
		if got.EscapedPath() != gateway.EscapedPath() || got.RawQuery != gateway.RawQuery || got.User != nil {
			t.Fatalf("rewritten endpoint %q changed more than the origin", out)
		}
	})
}
