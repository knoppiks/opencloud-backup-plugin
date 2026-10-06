package cs3

// Where file bytes are fetched from.
//
// The gateway answers InitiateFileDownload/InitiateFileUpload with the URL the
// bytes move through. Up to OpenCloud 7.4 that was the public data gateway
// (OC_URL + /data) plus a transfer token. From 7.5 on, the storage provider's
// data server is always "exposed": the gateway hands out its own address
// (STORAGE_USERS_DATA_SERVER_URL, by default http://localhost:9158/data) and no
// transfer token. That address is meant for OpenCloud's own services; from any
// other host, "localhost" is the wrong machine.
//
// The data-server origin override fixes that without changing how OpenCloud
// routes its own traffic: the path the gateway returns is kept, and only the
// scheme and host are replaced by an address this service can reach, such as
// the Service in front of OpenCloud's pod. The data server still checks the
// access token on every request.
//
// The override applies only to an endpoint handed out WITHOUT a transfer
// token, which is the 7.5+ shape. An endpoint with a token is the public data
// gateway, which this service reaches already; sending it to the data server
// instead fails every transfer ("invalid upload path", measured on 7.3.0 in
// Phase 9). Keying on the response rather than on a version number lets one
// configuration serve the whole support window, and an upgrade across 7.5.

import (
	"errors"
	"fmt"
	"net/url"
)

// WithDataServerOrigin sends every file transfer the gateway routes to its
// data server directly (no transfer token) to origin (scheme and host)
// instead, keeping the gateway's path. Nil leaves the gateway's URL untouched.
func WithDataServerOrigin(origin *url.URL) ClientOption {
	return func(c *Client) { c.dataOrigin = origin }
}

// ParseDataServerOrigin validates an origin for WithDataServerOrigin. Only a
// scheme and host (with an optional port) are accepted: a path would be
// ambiguous, because the gateway's path is kept as it is.
func ParseDataServerOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("data server origin: %w", err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("data server origin: scheme must be http or https")
	case u.Host == "":
		return nil, errors.New("data server origin: host is missing")
	case u.User != nil:
		return nil, errors.New("data server origin: credentials are not allowed")
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return nil, errors.New("data server origin: give scheme and host only, no path " +
			"(the path OpenCloud returns is kept)")
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// dataEndpoint applies the origin override to an endpoint the gateway
// returned, unless it came with a transfer token (see the file comment).
func (c *Client) dataEndpoint(endpoint, transferToken string) (string, error) {
	if c.dataOrigin == nil || transferToken != "" {
		return endpoint, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		// Not echoed: the endpoint names internal addresses.
		return "", errors.New("cs3: gateway returned an unparseable data endpoint")
	}
	u.Scheme, u.Host = c.dataOrigin.Scheme, c.dataOrigin.Host
	return u.String(), nil
}
