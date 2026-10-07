package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DirStore is a Store over a local directory, mapping an object key onto a
// relative file path. It backs unit tests and local development, and gives the
// offline decrypt CLI a way to read a Take-Out that was produced as a plain
// directory tree.
type DirStore struct {
	// Root is the directory that plays the role of the bucket.
	Root string
}

var _ Store = DirStore{}

// Get streams the file at key.
func (d DirStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := d.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("objstore: open %s: %w", key, err)
	}
	return f, nil
}

// Put writes body at key, creating parent directories.
func (d DirStore) Put(_ context.Context, key string, body []byte) error {
	p, err := d.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("objstore: create directory for %s: %w", key, err)
	}
	if err := os.WriteFile(p, body, 0o600); err != nil {
		return fmt.Errorf("objstore: write %s: %w", key, err)
	}
	return nil
}

// List walks the directory tree and returns every file whose key starts with
// prefix. Keys are slash-separated regardless of platform.
func (d DirStore) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	if d.Root == "" {
		return nil, fmt.Errorf("objstore: directory root not configured")
	}

	var out []ObjectInfo
	err := filepath.WalkDir(d.Root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(d.Root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out = append(out, ObjectInfo{Key: key, Size: info.Size()})
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("objstore: list %s: %w", prefix, err)
	}
	return out, nil
}

// resolve maps an object key onto a path inside Root, rejecting keys that would
// escape it. Keys come from remote listings, so traversal is not hypothetical.
func (d DirStore) resolve(key string) (string, error) {
	if d.Root == "" {
		return "", fmt.Errorf("objstore: directory root not configured")
	}
	clean := path.Clean("/" + strings.TrimPrefix(key, "/"))
	if clean == "/" {
		return "", fmt.Errorf("objstore: empty key")
	}
	return filepath.Join(d.Root, filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
}
