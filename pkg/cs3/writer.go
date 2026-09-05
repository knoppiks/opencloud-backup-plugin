package cs3

// The write path into a Space (Phase 5, restore Path B) — the inverse of the
// Phase-4 read path.
//
// Shape of an upload, mirroring the download flow validated in Spike 3:
//
//  1. CreateContainer for each directory (space-relative reference: space root
//     resource id + "./rel"; a bare resource id resolves to "/" and fails).
//  2. InitiateFileUpload, which returns a data-gateway endpoint plus a transfer
//     token, with the content length announced up front.
//  3. PUT the bytes to that endpoint carrying the access token and the transfer
//     token.
//
// Restores never overwrite live data: the caller writes below a fresh
// "Restore/<timestamp>/" folder (decisions.md #3). This client does not enforce
// that — it is a transport — but it also never deletes or truncates anything.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
)

const (
	// uploadLengthHeader announces the body size to reva's upload initiation.
	uploadLengthHeader = "Upload-Length"
	// mtimeHeader asks OpenCloud to keep the file's original modification time.
	// Support is best-effort: mtime is in backup scope (decisions.md #4), but a
	// server that ignores this still yields a correct restore.
	mtimeHeader = "X-OC-Mtime"
	// preferredUploadProtocol is the simple one-shot PUT protocol. TUS is
	// advertised too, but resumability buys nothing for a server-side restore
	// that retries whole files.
	preferredUploadProtocol = "simple"
)

// ErrAlreadyExists is returned when a restore target path is already taken.
// A restore must never overwrite live data, so callers surface this rather than
// clobbering (decisions.md #3).
var ErrAlreadyExists = errors.New("cs3: resource already exists")

// SpaceWriter is the write boundary onto OpenCloud, used by restores.
type SpaceWriter interface {
	// MakeDir creates one space-relative directory. It is idempotent: an
	// existing directory is not an error.
	MakeDir(ctx context.Context, space Space, relDir string) error
	// Upload writes size bytes from r to a space-relative path, creating the
	// file. modTime is preserved where the server supports it.
	Upload(ctx context.Context, space Space, relPath string, size int64, modTime time.Time, r io.Reader) error
}

var _ SpaceWriter = (*Client)(nil)

// MakeDir creates a directory inside a Space.
func (c *Client) MakeDir(ctx context.Context, space Space, relDir string) error {
	rel := cleanRel(relDir)
	if rel == "" {
		// The space root always exists.
		return nil
	}

	authCtx, _, err := c.authContext(ctx)
	if err != nil {
		return err
	}

	res, err := c.gw.CreateContainer(authCtx, &provider.CreateContainerRequest{
		Ref: reference(space, rel),
	})
	if err != nil {
		return fmt.Errorf("cs3 create container: %w", err)
	}
	switch res.GetStatus().GetCode() {
	case rpc.Code_CODE_OK, rpc.Code_CODE_ALREADY_EXISTS:
		return nil
	default:
		return fmt.Errorf("cs3 CreateContainer: code=%s", res.GetStatus().GetCode())
	}
}

// Upload writes one file into a Space.
//
// The size must be known up front: reva announces the content length to the
// storage driver before any byte moves, and a restore always knows it from the
// snapshot metadata.
func (c *Client) Upload(ctx context.Context, space Space, relPath string, size int64, modTime time.Time, r io.Reader) error {
	rel := cleanRel(relPath)
	if rel == "" {
		return fmt.Errorf("cs3 upload: empty path")
	}
	if size < 0 {
		return fmt.Errorf("cs3 upload: negative size")
	}

	authCtx, token, err := c.authContext(ctx)
	if err != nil {
		return err
	}

	req := &provider.InitiateFileUploadRequest{
		Ref:    reference(space, rel),
		Opaque: uploadOpaque(size, modTime),
	}
	res, err := c.gw.InitiateFileUpload(authCtx, req)
	if err != nil {
		return fmt.Errorf("cs3 initiate upload: %w", err)
	}
	if code := res.GetStatus().GetCode(); code == rpc.Code_CODE_ALREADY_EXISTS {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, rel)
	}
	if err := statusErr(res.GetStatus(), "InitiateFileUpload"); err != nil {
		return err
	}

	endpoint, transfer := pickUploadProtocol(res.GetProtocols())
	if endpoint == "" {
		return fmt.Errorf("cs3 initiate upload: no upload endpoint returned")
	}
	return c.put(ctx, endpoint, token, transfer, size, modTime, r)
}

// put streams the body to the data gateway.
func (c *Client) put(ctx context.Context, endpoint, accessToken, transferToken string, size int64, modTime time.Time, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, body)
	if err != nil {
		return fmt.Errorf("cs3 upload request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set(TokenHeader, accessToken)
	if transferToken != "" {
		req.Header.Set(TransferHeader, transferToken)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if !modTime.IsZero() {
		req.Header.Set(mtimeHeader, strconv.FormatInt(modTime.Unix(), 10))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cs3 upload: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Status only — the response body may echo internal detail.
		return fmt.Errorf("cs3 upload: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// uploadOpaque carries the content length (required by reva to pick an upload
// strategy) and the desired modification time.
func uploadOpaque(size int64, modTime time.Time) *typespb.Opaque {
	m := map[string]*typespb.OpaqueEntry{
		uploadLengthHeader: {
			Decoder: "plain",
			Value:   []byte(strconv.FormatInt(size, 10)),
		},
	}
	if !modTime.IsZero() {
		m[mtimeHeader] = &typespb.OpaqueEntry{
			Decoder: "plain",
			Value:   []byte(strconv.FormatInt(modTime.Unix(), 10)),
		}
	}
	return &typespb.Opaque{Map: m}
}

// pickUploadProtocol prefers the simple PUT protocol, falling back to whatever
// endpoint the gateway offers.
func pickUploadProtocol(protocols []*gateway.FileUploadProtocol) (endpoint, transferToken string) {
	for _, p := range protocols {
		if p.GetUploadEndpoint() == "" {
			continue
		}
		if p.GetProtocol() == preferredUploadProtocol {
			return p.GetUploadEndpoint(), p.GetToken()
		}
		if endpoint == "" {
			endpoint, transferToken = p.GetUploadEndpoint(), p.GetToken()
		}
	}
	return endpoint, transferToken
}
