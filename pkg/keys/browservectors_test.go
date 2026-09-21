package keys

// Go opens what the browser sealed.
//
// testdata/vectors.json proves the browser reproduces this package's bytes from
// this package's inputs. It says nothing about the salts, nonces, Recovery Keys
// and Argon2id parameters the browser chooses on its own — and those are what a
// real user's envelope is made of.
//
// web/testdata/browser-vectors.json is produced by the browser crypto (`pnpm
// vectors` in web/) and opened here, with the same Recovery Key a user would
// type. A browser that starts producing envelopes this package cannot read
// fails this test, rather than failing a recovery years later.
//
// The file is deliberately not skipped when missing: the whole point is that
// the guarantee is checked, and a test that quietly does nothing is worse than
// no test, because it reports success.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

const browserVectorsPath = "../../web/testdata/browser-vectors.json"

type browserVectorFile struct {
	Comment     string          `json:"_comment"`
	GeneratedBy string          `json:"generated_by"`
	Vectors     []browserVector `json:"vectors"`
}

type browserVector struct {
	Name string `json:"name"`
	Note string `json:"note"`
	// RecoveryKey is a throwaway key for a throwaway envelope, committed
	// because it is the only way to open the blob. It protects nothing.
	RecoveryKey     string    `json:"recovery_key"`
	DataKeyHex      string    `json:"data_key_hex"`
	EnvelopeB64     string    `json:"envelope_b64"`
	Argon           argonJSON `json:"argon"`
	AcceptedBySetup bool      `json:"accepted_by_setup"`
}

func TestBrowserVectors(t *testing.T) {
	raw, err := os.ReadFile(browserVectorsPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with `pnpm vectors` in web/): %v", browserVectorsPath, err)
	}
	var file browserVectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", browserVectorsPath, err)
	}
	if len(file.Vectors) == 0 {
		t.Fatal("no browser vectors: the interop guarantee would pass while checking nothing")
	}

	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			blob, err := base64.StdEncoding.DecodeString(v.EnvelopeB64)
			if err != nil {
				t.Fatalf("decode envelope: %v", err)
			}

			// Go through the display string, exactly as the decrypt CLI does
			// when a user types their key. Decoding it here also pins the
			// browser's encoder against this package's decoder.
			secret, err := DecodeRecoveryKey(v.RecoveryKey)
			if err != nil {
				t.Fatalf("DecodeRecoveryKey: %v", err)
			}

			info, err := Inspect(blob)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info.Kind != WrapRK {
				t.Fatalf("kind = %v, want %v", info.Kind, WrapRK)
			}
			if info.KDF != "argon2id" {
				t.Fatalf("kdf = %q, want argon2id", info.KDF)
			}
			if got, want := toArgonJSON(info.Argon), v.Argon; got != want {
				t.Fatalf("argon params = %+v, want %+v (the browser recorded costs it did not use)", got, want)
			}

			dk, err := UnwrapRK(WrappedDK{Version: info.Version, Kind: WrapRK, Blob: blob}, secret)
			if err != nil {
				t.Fatalf("UnwrapRK: %v", err)
			}
			want, err := hex.DecodeString(v.DataKeyHex)
			if err != nil {
				t.Fatalf("decode data key: %v", err)
			}
			if !bytes.Equal(dk, want) {
				t.Fatal("unwrapped data key does not match the one the browser sealed")
			}
			if len(dk) != DKSize {
				t.Fatalf("data key = %d bytes, want %d", len(dk), DKSize)
			}

			_, err = CheckRecoveryEnvelope(blob)
			if accepted := err == nil; accepted != v.AcceptedBySetup {
				t.Fatalf("CheckRecoveryEnvelope accepted = %v (%v), want %v", accepted, err, v.AcceptedBySetup)
			}
		})
	}
}
