//go:build !linux

package memfs

// Inspect has no portable answer; see ErrUnsupported.
func Inspect(string) (Filesystem, error) {
	return Filesystem{}, ErrUnsupported
}
