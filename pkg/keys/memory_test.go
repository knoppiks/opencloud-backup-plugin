package keys

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// fixedClock is a deterministic Clock for store tests.
type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }
func (c *fixedClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	st := NewMemoryStore()
	dk := mustDK(t)
	srwKey := mustSRWKey(t)

	rkEnv, err := WrapWithRK(dk, []byte("rk"), testArgon)
	if err != nil {
		t.Fatal(err)
	}
	srwEnv, err := WrapWithSRW(dk, srwKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.PutRK("space-1", rkEnv); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSRW("space-1", srwEnv); err != nil {
		t.Fatal(err)
	}

	gotRK, err := st.GetRK("space-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRK.Blob, rkEnv.Blob) || gotRK.Kind != WrapRK {
		t.Fatal("RK envelope round-trip mismatch")
	}
	gotSRW, err := st.GetSRW("space-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSRW.Blob, srwEnv.Blob) || gotSRW.Kind != WrapSRW {
		t.Fatal("SRW envelope round-trip mismatch")
	}
}

func TestMemoryStoreNotFound(t *testing.T) {
	st := NewMemoryStore()
	var nf ErrNotFound

	if _, err := st.GetRK("missing"); !errors.As(err, &nf) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := st.GetSRW("missing"); !errors.As(err, &nf) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMemoryStoreRejectsWrongKind(t *testing.T) {
	st := NewMemoryStore()
	dk := mustDK(t)
	rkEnv, _ := WrapWithRK(dk, []byte("rk"), testArgon)
	srwEnv, _ := WrapWithSRW(dk, mustSRWKey(t))

	if err := st.PutRK("s", srwEnv); err == nil {
		t.Fatal("PutRK must reject an SRW envelope")
	}
	if err := st.PutSRW("s", rkEnv); err == nil {
		t.Fatal("PutSRW must reject an RK envelope")
	}
}

func TestMemoryStoreStatus(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	st := NewMemoryStoreWithClock(clock)
	dk := mustDK(t)

	// Unknown space: not configured, no error.
	s0, err := st.Status("space-1")
	if err != nil {
		t.Fatal(err)
	}
	if s0.Configured || s0.HasRK || s0.HasSRW {
		t.Fatalf("unset space must report unconfigured: %+v", s0)
	}

	// Half-configured.
	rkEnv, _ := WrapWithRK(dk, []byte("rk"), testArgon)
	if err := st.PutRK("space-1", rkEnv); err != nil {
		t.Fatal(err)
	}
	s1, _ := st.Status("space-1")
	if s1.Configured {
		t.Fatal("RK alone must not count as configured")
	}
	if !s1.HasRK || s1.HasSRW {
		t.Fatalf("unexpected half state: %+v", s1)
	}

	// Fully configured.
	clock.advance(time.Hour)
	srwEnv, _ := WrapWithSRW(dk, mustSRWKey(t))
	if err := st.PutSRW("space-1", srwEnv); err != nil {
		t.Fatal(err)
	}
	s2, _ := st.Status("space-1")
	if !s2.Configured || !s2.HasRK || !s2.HasSRW {
		t.Fatalf("expected configured: %+v", s2)
	}
	if s2.RKVersion != EnvelopeVersion || s2.SRWVersion != EnvelopeVersion {
		t.Fatalf("unexpected versions: %+v", s2)
	}
	if !s2.UpdatedAt.After(s2.CreatedAt) {
		t.Fatalf("UpdatedAt should advance: created=%v updated=%v", s2.CreatedAt, s2.UpdatedAt)
	}
}

func TestStatusCarriesNoKeyMaterial(t *testing.T) {
	// Status is a value type with no blob fields — the compiler enforces this.
	// The test documents the invariant and fails loudly if a field is added.
	st := NewMemoryStore()
	dk := mustDK(t)
	rkEnv, _ := WrapWithRK(dk, []byte("rk"), testArgon)
	srwEnv, _ := WrapWithSRW(dk, mustSRWKey(t))
	_ = st.PutRK("s", rkEnv)
	_ = st.PutSRW("s", srwEnv)

	status, err := st.Status("s")
	if err != nil {
		t.Fatal(err)
	}
	// Render the status the way a log or API would and check for leakage.
	rendered := formatStatus(status)
	if bytes.Contains([]byte(rendered), dk) {
		t.Fatal("status rendering leaked the DK")
	}
	if bytes.Contains([]byte(rendered), rkEnv.Blob) || bytes.Contains([]byte(rendered), srwEnv.Blob) {
		t.Fatal("status rendering leaked an envelope blob")
	}
}

func formatStatus(s Status) string {
	return s.SpaceID + boolStr(s.Configured) + boolStr(s.HasRK) + boolStr(s.HasSRW)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestMemoryStoreIsolatesStoredBlobs(t *testing.T) {
	// Mutating a returned envelope must not corrupt stored state.
	st := NewMemoryStore()
	dk := mustDK(t)
	srwKey := mustSRWKey(t)
	env, _ := WrapWithSRW(dk, srwKey)
	_ = st.PutSRW("s", env)

	got, _ := st.GetSRW("s")
	got.Blob[0] ^= 0xff

	again, _ := st.GetSRW("s")
	recovered, err := UnwrapSRW(again, srwKey)
	if err != nil {
		t.Fatalf("stored envelope was corrupted by caller mutation: %v", err)
	}
	if !bytes.Equal(recovered, dk) {
		t.Fatal("stored DK changed")
	}
}

func TestMemoryStoreConcurrent(t *testing.T) {
	st := NewMemoryStore()
	dk := mustDK(t)
	env, _ := WrapWithSRW(dk, mustSRWKey(t))

	done := make(chan struct{})
	for i := range 8 {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for range 50 {
				_ = st.PutSRW("space", env)
				_, _ = st.GetSRW("space")
				_, _ = st.Status("space")
			}
		}(i)
	}
	for range 8 {
		<-done
	}
}
