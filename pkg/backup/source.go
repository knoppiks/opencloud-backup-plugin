package backup

// CS3-backed snapshot.Source: Phase 4 "Option B". kopia's walker pulls entries
// and bytes on demand straight from OpenCloud, so nothing is staged on disk and
// the largest Space no longer sets a disk-sizing requirement.
//
// Consistency stance (phase-4 plan): OpenCloud Spaces are live and there is no
// global point-in-time fence. Files are read one by one, so a file changed
// mid-run is captured either before or after the change — the same exposure any
// file-level backup has. Structure, size and mtime are recorded at walk time.
// This is documented rather than hidden.

import (
	"context"
	"io"
	"path"
	"time"

	"opencloud-backup-plugin/pkg/cs3"
	"opencloud-backup-plugin/pkg/snapshot"
)

// spaceSource adapts one CS3 Space to snapshot.Source.
type spaceSource struct {
	reader cs3.SpaceReader
	space  cs3.Space
}

var _ snapshot.Source = (*spaceSource)(nil)

// NewSpaceSource returns a snapshot.Source streaming directly from a Space.
func NewSpaceSource(reader cs3.SpaceReader, space cs3.Space) snapshot.Source {
	return &spaceSource{reader: reader, space: space}
}

// Name is the snapshot root directory name. The Space id is used rather than
// the display name: a rename must not look like a whole new tree and break
// dedup against previous snapshots.
func (s *spaceSource) Name() string { return s.space.ID }

// List returns the direct children of a space-relative directory.
func (s *spaceSource) List(ctx context.Context, dir string) ([]snapshot.Node, error) {
	entries, err := s.reader.ListDir(ctx, s.space, dir)
	if err != nil {
		return nil, err
	}

	nodes := make([]snapshot.Node, 0, len(entries))
	for _, e := range entries {
		nodes = append(nodes, snapshot.Node{
			Name:    path.Base(e.Path),
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: time.Unix(e.MTimeUnix, 0).UTC(),
		})
	}
	return nodes, nil
}

// Open streams a space-relative file from offset.
func (s *spaceSource) Open(ctx context.Context, p string, offset int64) (io.ReadCloser, error) {
	return s.reader.OpenFile(ctx, s.space, p, offset)
}
