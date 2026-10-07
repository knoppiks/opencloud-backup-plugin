package ocversion

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource answers with whatever the test sets, and counts calls.
type fakeSource struct {
	mu    sync.Mutex
	info  Info
	err   error
	calls int
}

func (f *fakeSource) set(info Info, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info, f.err = info, err
}

func (f *fakeSource) Fetch(context.Context) (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.info, f.err
}

func (f *fakeSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// syncBuffer lets Run's goroutine log while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestMonitor(src Source) (*Monitor, *syncBuffer) {
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	return NewMonitor(src, validPins().Window(), logger), logs
}

func TestMonitorInsideWindow(t *testing.T) {
	src := &fakeSource{info: Info{Version: "8.2.3", Edition: "stable"}}
	m, logs := newTestMonitor(src)
	if m.Status().Known {
		t.Fatal("known before the first check")
	}
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if !st.Known || !st.InWindow || st.Version != "8.2.3" || st.Edition != "stable" {
		t.Errorf("status = %+v", st)
	}
	out := logs.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "version=8.2.3") {
		t.Errorf("version not logged:\n%s", out)
	}
	if strings.Contains(out, "level=WARN") {
		t.Errorf("warned inside the window:\n%s", out)
	}
}

func TestMonitorWarnsOutsideWindowAndKeepsGoing(t *testing.T) {
	for _, version := range []string{"9.0.0", "8.2.0-rc.1", "daily"} {
		t.Run(version, func(t *testing.T) {
			m, logs := newTestMonitor(&fakeSource{info: Info{Version: version, Edition: "rolling"}})
			// "Never refuses": outside the window is a status, not an error.
			if err := m.Check(context.Background()); err != nil {
				t.Fatalf("Check failed outside the window: %v", err)
			}
			st := m.Status()
			if !st.Known || st.InWindow {
				t.Errorf("status = %+v", st)
			}
			if !strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("no warning:\n%s", logs.String())
			}
		})
	}
}

func TestMonitorLogsVersionOnlyOnChange(t *testing.T) {
	src := &fakeSource{info: Info{Version: "8.1.0", Edition: "rolling"}}
	m, logs := newTestMonitor(src)
	for range 3 {
		_ = m.Check(context.Background())
	}
	if n := strings.Count(logs.String(), `msg="OpenCloud version"`); n != 1 {
		t.Errorf("logged the version %d times for one version", n)
	}
	src.set(Info{Version: "8.0.1", Edition: "rolling"}, nil)
	_ = m.Check(context.Background())
	if n := strings.Count(logs.String(), `msg="OpenCloud version"`); n != 2 {
		t.Errorf("an upgrade was not logged (%d lines)", n)
	}
}

func TestMonitorKeepsLastKnownVersionOnFailure(t *testing.T) {
	src := &fakeSource{info: Info{Version: "8.1.0", Edition: "rolling"}}
	m, logs := newTestMonitor(src)
	_ = m.Check(context.Background())

	src.set(Info{}, errors.New("connection refused"))
	for range 3 {
		if err := m.Check(context.Background()); err == nil {
			t.Fatal("expected the fetch error")
		}
	}
	if st := m.Status(); !st.Known || st.Version != "8.1.0" {
		t.Errorf("a failed fetch lost the version: %+v", st)
	}
	if n := strings.Count(logs.String(), "could not read the OpenCloud version"); n != 1 {
		t.Errorf("an outage was logged %d times, want once", n)
	}
}

func TestMonitorQuietOnShutdown(t *testing.T) {
	m, logs := newTestMonitor(&fakeSource{err: context.Canceled})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = m.Check(ctx)
	if logs.String() != "" {
		t.Errorf("logged during shutdown:\n%s", logs.String())
	}
}

func TestMonitorRunRetriesUntilKnownThenSlowsDown(t *testing.T) {
	src := &fakeSource{err: errors.New("not up yet")}
	m, _ := newTestMonitor(src)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx, time.Hour, time.Millisecond)
		close(done)
	}()

	waitFor(t, func() bool { return src.count() >= 3 })
	src.set(Info{Version: "8.1.0", Edition: "rolling"}, nil)
	waitFor(t, func() bool { return m.Status().Known })

	// Known now: the next check is an hour away.
	settled := src.count()
	time.Sleep(20 * time.Millisecond)
	if src.count() != settled {
		t.Errorf("kept retrying after OpenCloud answered (%d -> %d calls)", settled, src.count())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
