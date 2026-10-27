//go:build linux

package memfs

import (
	"math"
	"testing"
)

func TestCapacity(t *testing.T) {
	cases := map[string]struct {
		blocks    uint64
		blockSize int64
		want      int64
	}{
		"ordinary":        {blocks: 65536, blockSize: 4096, want: 256 << 20},
		"no size (ramfs)": {blocks: 0, blockSize: 4096, want: 0},
		"no block size":   {blocks: 10, blockSize: 0, want: 0},
		"saturates":       {blocks: math.MaxUint64, blockSize: 4096, want: math.MaxInt64},
	}
	for name, c := range cases {
		if got := capacity(c.blocks, c.blockSize); got != c.want {
			t.Errorf("%s: capacity = %d, want %d", name, got, c.want)
		}
	}
}

func TestIsMemoryType(t *testing.T) {
	if !isMemoryType(tmpfsMagic) || !isMemoryType(ramfsMagic) {
		t.Fatal("tmpfs and ramfs are memory-backed")
	}
	const ext4Magic = 0xef53
	if isMemoryType(ext4Magic) {
		t.Fatal("ext4 is not memory-backed")
	}
}
