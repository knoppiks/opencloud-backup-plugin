package targets

// Frozen sealed credentials: the proof that today's code still opens target
// credentials an earlier release sealed (compatibility-policy.md §2, "TW-wrapped
// credential blob").
//
// The round-trip tests in credsealer_test.go seal and open with the code of the
// same commit, so a change that stops reading an old payload shape or envelope
// version passes them. The files listed in frozenCredentials were written once
// and are never regenerated; each holds a throwaway Target Wrap key generated
// for it, the sealed blobs, and the credentials they must open to (made-up
// values, allowlisted in .gitleaks.toml).
//
// Every new envelope version or payload shape adds a file of its own; none is
// ever deleted. To add one, pick a new name and run:
//
//	go test ./pkg/targets -run TestFreezeSealedCredentials -freeze <name>
//
// then add the name to frozenCredentials. The generator refuses to overwrite.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/pkg/keys"
)

// frozenCredentials lists every file ever frozen. Append only.
var frozenCredentials = []string{
	"sealed-credentials-v1",
}

var freezeName = flag.String("freeze", "", "write new frozen sealed credentials to testdata/<name>.json (never overwrites)")

const frozenDir = "testdata"

type frozenCredFile struct {
	Comment  string           `json:"_comment"`
	Written  string           `json:"written"`
	TWKeyHex string           `json:"tw_key_hex"`
	Blobs    []frozenCredBlob `json:"blobs"`
}

type frozenCredBlob struct {
	Name string `json:"name"`
	Note string `json:"note"`
	// Version is what Seal reported, and what a target record stores beside
	// the blob.
	Version int    `json:"version"`
	BlobB64 string `json:"blob_b64"`
	Want    struct {
		Backup      frozenPair `json:"backup"`
		Maintenance frozenPair `json:"maintenance"`
	} `json:"want"`
}

type frozenPair struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// frozenShapes are the payload shapes sealed so far: the single pair every
// target had before roles existed, and the pair plus a maintenance pair.
func frozenShapes() []struct {
	name, note string
	set        CredentialSet
} {
	return []struct {
		name, note string
		set        CredentialSet
	}{
		{
			name: "backup-only",
			note: "One pair, serialised flat: the shape of every record sealed before roles existed.",
			set:  CredentialSet{Backup: PlainCreds{AccessKeyID: "frozen-backup-id", SecretAccessKey: "frozen-backup-secret"}},
		},
		{
			name: "backup-and-maintenance",
			note: "The backup pair flat plus a nested maintenance pair (credential split, Phase 7).",
			set: CredentialSet{
				Backup:      PlainCreds{AccessKeyID: "frozen-backup-id", SecretAccessKey: "frozen-backup-secret"},
				Maintenance: PlainCreds{AccessKeyID: "frozen-maintenance-id", SecretAccessKey: "frozen-maintenance-secret"},
			},
		},
	}
}

func TestFrozenCredentialsAreAllListed(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(frozenDir, "sealed-credentials-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, f := range files {
		found = append(found, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	for _, name := range frozenCredentials {
		if !slices.Contains(found, name) {
			t.Errorf("frozen credentials %q are gone; frozen fixtures are never deleted", name)
		}
	}
	for _, name := range found {
		if !slices.Contains(frozenCredentials, name) {
			t.Errorf("%s.json is not in frozenCredentials; list it so its deletion is noticed", name)
		}
	}
}

func TestFrozenCredentialsOpen(t *testing.T) {
	for _, name := range frozenCredentials {
		file := readFrozenCredentials(t, name)
		sealer := mustSealer(t, mustHexKey(t, file.TWKeyHex))
		for _, b := range file.Blobs {
			t.Run(name+"/"+b.Name, func(t *testing.T) {
				blob, err := base64.StdEncoding.DecodeString(b.BlobB64)
				if err != nil {
					t.Fatalf("decode blob: %v", err)
				}
				got, err := sealer.Open(blob)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				want := CredentialSet{
					Backup:      PlainCreds(b.Want.Backup),
					Maintenance: PlainCreds(b.Want.Maintenance),
				}
				if got != want {
					t.Fatalf("opened %+v, want %+v", got, want)
				}
				info, err := keys.Inspect(blob)
				if err != nil || info.Version != b.Version || info.Kind != keys.WrapTW {
					t.Fatalf("Inspect = %+v, %v; want a version %d TW envelope", info, err, b.Version)
				}
			})
		}
	}
}

// Every payload shape at the current envelope version must have a frozen blob,
// so a new version or a new shape is frozen by the change that introduces it.
func TestFrozenCredentialsCoverTheCurrentFormat(t *testing.T) {
	covered := map[string]bool{}
	for _, name := range frozenCredentials {
		for _, b := range readFrozenCredentials(t, name).Blobs {
			if b.Version == keys.EnvelopeVersion {
				covered[b.Name] = true
			}
		}
	}
	for _, shape := range frozenShapes() {
		if !covered[shape.name] {
			t.Errorf("no frozen %q blob at envelope version %d; freeze one (see this file's comment)",
				shape.name, keys.EnvelopeVersion)
		}
	}
}

// TestFreezeSealedCredentials writes a new frozen file with today's code. It
// runs only when asked to (-freeze <name>).
func TestFreezeSealedCredentials(t *testing.T) {
	if *freezeName == "" {
		t.Skip("run with -freeze <name> to write new frozen sealed credentials")
	}
	path := filepath.Join(frozenDir, *freezeName+".json")
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s exists; frozen fixtures are never rewritten, pick a new name", path)
	}

	key := mustTWKey(t)
	sealer := mustSealer(t, key)
	file := frozenCredFile{
		Comment: "Frozen sealed target credentials, written once by `go test ./pkg/targets -run TestFreezeSealedCredentials -freeze " +
			*freezeName + "`. Never regenerate or edit it. The Target Wrap key is a throwaway generated for this file; the credentials are made up.",
		Written:  time.Now().UTC().Format(time.DateOnly),
		TWKeyHex: hex.EncodeToString(key),
	}
	for _, shape := range frozenShapes() {
		blob, version, err := sealer.Seal(shape.set)
		if err != nil {
			t.Fatalf("Seal %s: %v", shape.name, err)
		}
		b := frozenCredBlob{Name: shape.name, Note: shape.note, Version: version, BlobB64: base64.StdEncoding.EncodeToString(blob)}
		b.Want.Backup = frozenPair(shape.set.Backup)
		b.Want.Maintenance = frozenPair(shape.set.Maintenance)
		file.Blobs = append(file.Blobs, b)
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(frozenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("froze %s; add %q to frozenCredentials", path, *freezeName)
}

func readFrozenCredentials(t *testing.T, name string) frozenCredFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(frozenDir, name+".json"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var file frozenCredFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(file.Blobs) == 0 {
		t.Fatalf("%s holds no blobs", name)
	}
	return file
}

func mustHexKey(t *testing.T, s string) []byte {
	t.Helper()
	k, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	return k
}
