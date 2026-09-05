package snapshot

// Repository copy, used by the admin Take-Out (Path A).
//
// A Take-Out cannot be a byte-for-byte object copy: kopia's S3 driver stores
// flat blob ids, while its filesystem driver sharded them across directories and
// suffixes them, so objects synced verbatim out of a bucket would not reopen
// locally. The copy therefore runs at kopia's *blob* level — read blob id, write
// blob id — and lets each driver own its own layout.
//
// The pleasant consequence is that a Take-Out is a plain, standard kopia
// filesystem repository: our own decrypt CLI opens it, and so would a stock
// kopia release. For a last-resort recovery artefact that independence is worth
// more than saving a few lines here.
//
// Everything moved here is ciphertext. No Data Key is involved: opening a
// repository is not required to copy its blobs, which is exactly why the admin
// tool can do this without ever holding a key.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/kopia/kopia/repo/blob"
)

// BlobRef records one copied repository blob. The digest is over ciphertext and
// reveals nothing about the backed-up data; it exists so a Take-Out can be
// verified offline before anyone depends on it.
type BlobRef struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// CopyRepo copies every blob from src into a kopia filesystem repository at
// destDir, returning a record of what was copied.
//
// progress, when non-nil, is called after each blob with the running totals.
func CopyRepo(ctx context.Context, src blob.Storage, destDir string, progress func(blobs int, bytes int64)) ([]BlobRef, error) {
	if src == nil {
		return nil, fmt.Errorf("snapshot: source storage required")
	}
	if destDir == "" {
		return nil, fmt.Errorf("snapshot: destination directory required")
	}

	ids, err := blob.ListAllBlobs(ctx, src, "")
	if err != nil {
		return nil, fmt.Errorf("snapshot: list repository blobs: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("snapshot: repository is empty")
	}

	dest, err := DirOpener{Dir: destDir}.Open(ctx, Repo{}, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dest.Close(context.WithoutCancel(ctx)) }()

	refs := make([]BlobRef, 0, len(ids))
	var total int64

	for _, meta := range ids {
		var buf blobBuffer
		if err := src.GetBlob(ctx, meta.BlobID, 0, -1, &buf); err != nil {
			return nil, fmt.Errorf("snapshot: read blob %s: %w", meta.BlobID, err)
		}
		if err := dest.PutBlob(ctx, meta.BlobID, blobBytes(buf.data), blob.PutOptions{
			// Preserve the source timestamp: kopia uses blob times for
			// retention and maintenance reasoning.
			SetModTime: meta.Timestamp,
		}); err != nil {
			// Some drivers reject SetModTime; a Take-Out is still valid without
			// it, so fall back rather than fail the recovery path.
			if err := dest.PutBlob(ctx, meta.BlobID, blobBytes(buf.data), blob.PutOptions{}); err != nil {
				return nil, fmt.Errorf("snapshot: write blob %s: %w", meta.BlobID, err)
			}
		}

		sum := sha256.Sum256(buf.data)
		refs = append(refs, BlobRef{
			ID:     string(meta.BlobID),
			Size:   int64(len(buf.data)),
			SHA256: hex.EncodeToString(sum[:]),
		})
		total += int64(len(buf.data))

		if progress != nil {
			progress(len(refs), total)
		}
	}
	return refs, nil
}

// VerifyRepoDir re-reads a copied repository and checks it against the records
// CopyRepo produced. It needs no key: integrity of the ciphertext is verifiable
// by whoever holds the copy.
func VerifyRepoDir(ctx context.Context, dir string, refs []BlobRef) error {
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("snapshot: repository directory is missing")
	}

	st, err := DirOpener{Dir: dir}.Open(ctx, Repo{}, false)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close(context.WithoutCancel(ctx)) }()

	for _, ref := range refs {
		var buf blobBuffer
		if err := st.GetBlob(ctx, blob.ID(ref.ID), 0, -1, &buf); err != nil {
			return fmt.Errorf("snapshot: blob %s is missing or unreadable", ref.ID)
		}
		sum := sha256.Sum256(buf.data)
		if int64(len(buf.data)) != ref.Size || hex.EncodeToString(sum[:]) != ref.SHA256 {
			return fmt.Errorf("snapshot: blob %s does not match its recorded digest", ref.ID)
		}
	}
	return nil
}

// blobBuffer implements blob.OutputBuffer over a plain byte slice. kopia's own
// gather buffers live in an internal package, and a repository blob is bounded
// by kopia's pack size, so a contiguous buffer is adequate here.
type blobBuffer struct{ data []byte }

func (b *blobBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *blobBuffer) Reset() { b.data = b.data[:0] }

func (b *blobBuffer) Length() int { return len(b.data) }

// blobBytes implements blob.Bytes over a byte slice.
type blobBytes []byte

func (b blobBytes) Length() int { return len(b) }

func (b blobBytes) Reader() io.ReadSeekCloser { return blobReader{bytes.NewReader(b)} }

func (b blobBytes) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(b)
	if err != nil {
		return int64(n), fmt.Errorf("snapshot: write blob bytes: %w", err)
	}
	return int64(n), nil
}

// blobReader adds the no-op Close that blob.Bytes requires.
type blobReader struct{ *bytes.Reader }

func (blobReader) Close() error { return nil }
