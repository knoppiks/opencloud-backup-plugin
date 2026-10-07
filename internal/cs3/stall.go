package cs3

// Inactivity deadlines on the data path.
//
// The HTTP client bounds the phases *before* a body starts flowing — connect,
// TLS, response headers. Nothing bounds the body itself, and nothing can: a
// single large file may legitimately take an hour, so an overall timeout would
// have to be set so high it would never fire.
//
// What is never legitimate is a transfer that stops making progress. A gateway
// that accepts the request and then falls silent holds the run open forever,
// which is how a scheduled backup ends up occupying a run slot until the
// process is restarted. The guard below measures exactly that: bytes moved
// since the last check, not time since the transfer began.

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultStallTimeout is how long a transfer may make no progress at all before
// it is treated as failed. Generous: a busy storage backend may take a while to
// answer, and a false positive costs a whole run.
const DefaultStallTimeout = 2 * time.Minute

// ErrTransferStalled is returned when a transfer made no progress for longer
// than the stall timeout.
var ErrTransferStalled = errors.New("cs3: transfer stalled")

// stallGuard fails a transfer that stops making progress.
//
// It cancels the request rather than returning early from Read: the reader
// underneath belongs to the HTTP transport and is only unblocked by cancelling
// the request it came from. Returning an error while the transport is still
// blocked would leak the connection instead of freeing it.
type stallGuard struct {
	r       io.Reader
	cancel  context.CancelFunc
	timeout time.Duration

	timer *time.Timer
	fired atomic.Bool
	once  sync.Once
}

// newStallGuard starts watching r. cancel must cancel the request r came from.
func newStallGuard(r io.Reader, cancel context.CancelFunc, timeout time.Duration) *stallGuard {
	g := &stallGuard{r: r, cancel: cancel, timeout: timeout}
	if timeout <= 0 {
		return g
	}
	g.timer = time.AfterFunc(timeout, func() {
		g.fired.Store(true)
		cancel()
	})
	return g
}

// Read passes bytes through and restarts the clock whenever any arrive.
func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	switch {
	case errors.Is(err, io.EOF):
		// Every byte that was expected has arrived. What happens after that —
		// a server finalising a large upload, a caller sitting on a body it has
		// finished with — is not a stalled transfer and is bounded elsewhere.
		g.pause()
	case n > 0 && g.timer != nil:
		g.timer.Reset(g.timeout)
	}
	if err != nil && !errors.Is(err, io.EOF) && g.fired.Load() {
		// The cancellation was ours, so say what actually happened rather than
		// reporting the context error it surfaced as.
		return n, ErrTransferStalled
	}
	return n, err
}

// pause stops watching without ending the request.
func (g *stallGuard) pause() {
	if g.timer != nil {
		g.timer.Stop()
	}
}

// stop releases the guard and the request behind it. It is safe to call twice.
func (g *stallGuard) stop() {
	g.once.Do(func() {
		g.pause()
		g.cancel()
	})
}

// stalled reports whether this guard was the one that ended the transfer.
func (g *stallGuard) stalled() bool { return g.fired.Load() }

// guardedBody is a response body whose Close also releases the guard.
type guardedBody struct {
	*stallGuard
	body io.Closer
}

// Close ends the transfer, whether it finished or the caller gave up on it.
func (g *guardedBody) Close() error {
	g.stop()
	return g.body.Close()
}

// stallTimeout is the configured inactivity limit.
func (c *Client) stallTimeout() time.Duration {
	if c.stall > 0 {
		return c.stall
	}
	return DefaultStallTimeout
}
