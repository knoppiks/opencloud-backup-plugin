package ocversion

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Status is the monitor's latest answer.
type Status struct {
	// Known is false until OpenCloud has answered once.
	Known   bool
	Version string
	Edition string
	// InWindow is false for a version outside Window and for one that is not
	// MAJOR.MINOR.PATCH (a development or pre-release build was not tested).
	InWindow bool
	// Window is what this build was tested against.
	Window Window
}

// Monitor keeps track of the OpenCloud version and warns when it is outside
// the window compiled into the build. It never refuses anything: a household
// that upgraded OpenCloud a day before the plugin caught up must still get its
// nightly backup (compatibility-policy.md §3, "warns and runs").
type Monitor struct {
	source Source
	logger *slog.Logger

	mu     sync.Mutex
	status Status
	// failing remembers that the last fetch failed, so an unreachable
	// OpenCloud is logged once rather than on every retry.
	failing bool
}

// NewMonitor returns a monitor that has not checked yet.
func NewMonitor(source Source, window Window, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Monitor{source: source, logger: logger, status: Status{Window: window}}
}

// Status returns the latest result. Safe for concurrent use.
func (m *Monitor) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Check asks OpenCloud once and logs the result. A failed fetch keeps the
// last known version: a transient outage says nothing about the version.
func (m *Monitor) Check(ctx context.Context) error {
	info, err := m.source.Fetch(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	if err != nil {
		// Shutting down is not OpenCloud being unreachable.
		if ctx.Err() != nil {
			return err
		}
		if !m.failing {
			m.logger.Warn("could not read the OpenCloud version; will retry", "err", err)
		}
		m.failing = true
		return err
	}
	m.failing = false

	previous := m.status
	m.status = Status{
		Known:    true,
		Version:  info.Version,
		Edition:  info.Edition,
		InWindow: m.contains(info.Version),
		Window:   previous.Window,
	}
	if !previous.Known || previous.Version != info.Version || previous.Edition != info.Edition {
		m.logger.Info("OpenCloud version",
			"version", info.Version, "edition", info.Edition, "supported", previous.Window.String())
	}
	if !m.status.InWindow {
		m.logger.Warn("this OpenCloud version was not tested with this build of the backup service; "+
			"backups continue, but check the release notes for a newer build",
			"version", info.Version, "supported", previous.Window.String())
	}
	return nil
}

func (m *Monitor) contains(raw string) bool {
	v, err := ParseVersion(raw)
	return err == nil && m.status.Window.Contains(v)
}

// Run checks now and then every interval until ctx ends. Until OpenCloud has
// answered once it retries every retry instead: the service may well start
// before OpenCloud does.
func (m *Monitor) Run(ctx context.Context, interval, retry time.Duration) {
	for {
		wait := interval
		if err := m.Check(ctx); err != nil && !m.Status().Known {
			wait = retry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
