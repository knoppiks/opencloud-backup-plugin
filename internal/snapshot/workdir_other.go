//go:build !linux

package snapshot

// memoryBackedFilesystem has no portable answer. The check exists for the
// containerised deployment, which is Linux; everywhere else the caller is told
// it cannot be determined rather than being given a guess.
func memoryBackedFilesystem(string) (bool, error) {
	return false, ErrWorkDirFilesystemUnknown
}
