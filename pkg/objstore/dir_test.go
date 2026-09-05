package objstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDirStorePutGetRoundTrip(t *testing.T) {
	d := DirStore{Root: t.TempDir()}
	ctx := context.Background()

	if err := d.Put(ctx, "spaces/space-1/kopia.repository", []byte("blob")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := d.Get(ctx, "spaces/space-1/kopia.repository")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "blob" {
		t.Fatalf("content = %q, want %q", got, "blob")
	}
}

func TestDirStoreGetMissingIsNotFound(t *testing.T) {
	d := DirStore{Root: t.TempDir()}

	_, err := d.Get(context.Background(), "spaces/space-1/absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDirStoreListFiltersByPrefix(t *testing.T) {
	d := DirStore{Root: t.TempDir()}
	ctx := context.Background()

	for _, key := range []string{
		"spaces/space-1/a",
		"spaces/space-1/nested/b",
		"spaces/space-2/c",
		"keys/space-1/recovery.ocbke",
	} {
		if err := d.Put(ctx, key, []byte(key)); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	got, err := d.List(ctx, "spaces/space-1/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	keys := make([]string, 0, len(got))
	for _, o := range got {
		keys = append(keys, o.Key)
	}
	sort.Strings(keys)

	want := []string{"spaces/space-1/a", "spaces/space-1/nested/b"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
}

func TestDirStoreListReportsSizes(t *testing.T) {
	d := DirStore{Root: t.TempDir()}
	ctx := context.Background()
	if err := d.Put(ctx, "spaces/space-1/a", []byte("12345")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := d.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Size != 5 {
		t.Fatalf("list = %+v, want one 5-byte object", got)
	}
}

func TestDirStoreListMissingRootIsEmpty(t *testing.T) {
	d := DirStore{Root: filepath.Join(t.TempDir(), "absent")}

	got, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("list = %+v, want empty", got)
	}
}

// A key from a remote listing must never be able to write outside Root.
func TestDirStoreRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	d := DirStore{Root: filepath.Join(root, "store")}

	if err := d.Put(context.Background(), "../escaped", []byte("nope")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped")); err == nil {
		t.Fatal("traversal key escaped the store root")
	}
	if _, err := os.Stat(filepath.Join(root, "store", "escaped")); err != nil {
		t.Fatalf("traversal key was not confined to the root: %v", err)
	}
}

func TestDirStoreRequiresRoot(t *testing.T) {
	var d DirStore
	if err := d.Put(context.Background(), "k", nil); err == nil {
		t.Fatal("Put without a root must fail")
	}
	if _, err := d.List(context.Background(), ""); err == nil {
		t.Fatal("List without a root must fail")
	}
}

func TestEndpointURL(t *testing.T) {
	cases := []struct {
		endpoint   string
		disableTLS bool
		want       string
	}{
		{"garage:3900", true, "http://garage:3900"},
		{"garage:3900", false, "https://garage:3900"},
		{"http://garage:3900", false, "http://garage:3900"},
		{"https://s3.example.org/", true, "https://s3.example.org"},
		{" garage:3900 ", true, "http://garage:3900"},
	}
	for _, c := range cases {
		if got := EndpointURL(c.endpoint, c.disableTLS); got != c.want {
			t.Errorf("EndpointURL(%q, %v) = %q, want %q", c.endpoint, c.disableTLS, got, c.want)
		}
	}
}
