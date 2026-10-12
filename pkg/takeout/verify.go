package takeout

import (
	"context"
	"fmt"
	"os"

	"github.com/knoppiks/opencloud-backup-plugin/internal/snapshot"
)

// Verify re-reads a Take-Out and checks it against its own manifest. It needs no
// key: the integrity of the ciphertext is verifiable by whoever holds the copy.
func Verify(ctx context.Context, dir string) error {
	m, err := ReadManifest(dir)
	if err != nil {
		return err
	}

	if err := snapshot.VerifyRepoDir(ctx, m.RepoPath(dir), copiedBlobs(m.Blobs)); err != nil {
		return fmt.Errorf("%w: %s", ErrCorrupt, err.Error())
	}
	if m.EnvelopeRef != "" {
		if _, err := os.Stat(m.EnvelopePath(dir)); err != nil {
			return fmt.Errorf("%w: key envelope is missing", ErrCorrupt)
		}
	}
	return nil
}

// copiedBlobs converts the manifest's blob records into the copier's.
func copiedBlobs(refs []BlobRef) []snapshot.BlobRef {
	out := make([]snapshot.BlobRef, len(refs))
	for i, r := range refs {
		out[i] = snapshot.BlobRef{ID: r.ID, Size: r.Size, SHA256: r.SHA256}
	}
	return out
}
