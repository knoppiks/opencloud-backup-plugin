// Package bundleinstall copies the web extension's bundle out of its image
// into the volume OpenCloud loads extensions from.
//
// It replaces the `mkdir -p … && cp -R …` shell one-liner OpenCloud's own
// web-extensions images are run with, so the image needs no shell and no
// GPL-licensed busybox (phase-11 §11.1, #99). It keeps that one-liner's
// behaviour: the destination is created if missing, files the bundle has are
// overwritten, and files it does not have are left alone, so a restart over a
// volume that already holds an earlier copy succeeds.
package bundleinstall

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

// Result says what Install copied.
type Result struct {
	Files int
	Bytes int64
}

// Install copies the tree src into the directory dst, creating dst and every
// directory below it as needed. Only directories and regular files are
// copied; anything else in src (a symlink, a device) is refused, because the
// bundle has none and copying one could reach outside dst.
//
// dst is opened as an os.Root: nothing already in the volume (a symlink left
// by someone else) can redirect a write outside it.
func Install(src fs.FS, dst string) (Result, error) {
	if _, err := fs.Stat(src, "."); err != nil {
		return Result{}, fmt.Errorf("read the bundle: %w", err)
	}
	// World-readable: OpenCloud, which loads the bundle, runs as another user.
	if err := os.MkdirAll(dst, 0o755); err != nil { //nolint:gosec // G301: the bundle is public web assets
		return Result{}, fmt.Errorf("create %s: %w", dst, err)
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return Result{}, fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = root.Close() }()

	var res Result
	err = fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return mkdir(root, name)
		case d.Type().IsRegular():
			n, err := copyFile(src, root, name)
			if err != nil {
				return err
			}
			res.Files++
			res.Bytes += n
			return nil
		default:
			return fmt.Errorf("%s: not a regular file or directory (%s)", name, d.Type())
		}
	})
	if err != nil {
		return res, fmt.Errorf("install into %s: %w", dst, err)
	}
	if res.Files == 0 {
		return res, errors.New("the bundle is empty")
	}
	return res, nil
}

func mkdir(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	if err := root.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) { //nolint:gosec // G301: as in Install
		return fmt.Errorf("create directory %s: %w", name, err)
	}
	return nil
}

// copyFile copies src's file name to the same name below root, replacing
// any file of that name, with the source's permission bits.
func copyFile(src fs.FS, root *os.Root, name string) (int64, error) {
	in, err := src.Open(name)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	out, err := root.OpenFile(path.Clean(name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", name, err)
	}
	n, err := io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return n, fmt.Errorf("write %s: %w", name, err)
	}
	return n, nil
}
