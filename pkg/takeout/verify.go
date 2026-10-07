package takeout

import (
	"context"
	"fmt"
	"os"

	"opencloud-backup-plugin/pkg/snapshot"
)

// Verify re-reads a Take-Out and checks it against its own manifest. It needs no
// key: the integrity of the ciphertext is verifiable by whoever holds the copy.
func Verify(ctx context.Context, dir string) error {
	m, err := ReadManifest(dir)
	if err != nil {
		return err
	}

	if err := snapshot.VerifyRepoDir(ctx, m.RepoPath(dir), m.Blobs); err != nil {
		return fmt.Errorf("%w: %s", ErrCorrupt, err.Error())
	}
	if m.EnvelopeRef != "" {
		if _, err := os.Stat(m.EnvelopePath(dir)); err != nil {
			return fmt.Errorf("%w: key envelope is missing", ErrCorrupt)
		}
	}
	return nil
}
