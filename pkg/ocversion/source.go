package ocversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Info is what an OpenCloud instance says about itself.
type Info struct {
	// Version is the release as OpenCloud reports it ("8.1.0"). Kept as a
	// string: a development build need not be MAJOR.MINOR.PATCH, and it
	// should still be logged as it is.
	Version string
	// Edition is "stable" on the Production channel and "rolling" on Rolling.
	Edition string
}

// Source tells which OpenCloud the service runs against.
type Source interface {
	Fetch(ctx context.Context) (Info, error)
}

// StatusSource reads GET <BaseURL>/status.php. Measured in the Phase 9 spike
// (phase-0-findings.md) on 7.2.4, 7.3.0, 7.5.0 and 8.1.0: it needs no
// credential, and `productversion` / `edition` carry the release on all of
// them. `version` and `versionstring` are a frozen ownCloud-compat "0.1.0"
// and are ignored.
type StatusSource struct {
	BaseURL string
	Client  *http.Client
}

// statusBodyLimit bounds what is read from status.php, which is ~300 bytes.
const statusBodyLimit = 64 << 10

// Fetch implements Source.
func (s StatusSource) Fetch(ctx context.Context) (Info, error) {
	url := strings.TrimRight(s.BaseURL, "/") + "/status.php"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Info{}, fmt.Errorf("opencloud status: %w", err)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("opencloud status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("opencloud status: HTTP %d", resp.StatusCode)
	}

	var body struct {
		ProductVersion string `json:"productversion"`
		Edition        string `json:"edition"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, statusBodyLimit)).Decode(&body); err != nil {
		return Info{}, fmt.Errorf("opencloud status: decode: %w", err)
	}
	if body.ProductVersion == "" {
		return Info{}, errors.New("opencloud status: no productversion")
	}
	return Info{Version: body.ProductVersion, Edition: body.Edition}, nil
}
