package notify

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"opencloud-backup-plugin/internal/testutil"
	"opencloud-backup-plugin/pkg/state"
)

var epoch = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func newNotifier(t *testing.T, sinks ...Sink) (*Notifier, *StateStore, *testutil.FakeClock) {
	t.Helper()

	clock := testutil.NewFakeClock(epoch)
	store := NewStateStore(state.NewMemoryStore(), clock)
	n, err := New(store, Options{Sinks: sinks, Clock: clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n, store, clock
}

// recordingSink captures what was delivered.
type recordingSink struct {
	events []Event
	err    error
}

func (s *recordingSink) Deliver(_ context.Context, e Event) error {
	s.events = append(s.events, e)
	return s.err
}

func TestNotify_RecordsAndDelivers(t *testing.T) {
	sink := &recordingSink{}
	n, store, _ := newNotifier(t, sink)
	ctx := context.Background()

	got, err := n.SpaceEvent(ctx, KindRunFailed, "s1", "The last backup run failed.")
	if err != nil {
		t.Fatalf("SpaceEvent: %v", err)
	}
	if got.ID == "" || !got.CreatedAt.Equal(epoch) {
		t.Fatalf("event = %+v", got)
	}

	stored, err := store.List(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 1 || stored[0].ID != got.ID {
		t.Fatalf("stored = %+v", stored)
	}
	if len(sink.events) != 1 {
		t.Fatalf("delivered = %+v", sink.events)
	}
}

// The record is the part that must survive; delivery is best-effort.
func TestNotify_DeliveryFailureDoesNotLoseTheRecord(t *testing.T) {
	sink := &recordingSink{err: errors.New("smtp down")}
	n, store, _ := newNotifier(t, sink)
	ctx := context.Background()

	if _, err := n.SpaceEvent(ctx, KindRunFailed, "s1", "failed"); err != nil {
		t.Fatalf("SpaceEvent: %v", err)
	}
	stored, err := store.List(ctx, "s1", 0)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored = %+v (%v)", stored, err)
	}
}

// Decision #15: an operator event must never identify a Space.
func TestNotify_OperatorEventsCannotNameASpace(t *testing.T) {
	n, _, _ := newNotifier(t)
	ctx := context.Background()

	if _, err := n.Notify(ctx, Event{
		Kind:     KindTargetUnavailable,
		Audience: AudienceOperator,
		SpaceID:  "s1",
		Message:  "target unavailable",
	}); err == nil {
		t.Fatal("an operator event naming a space must be rejected")
	}

	// The convenience method has no space parameter at all.
	got, err := n.OperatorEvent(ctx, KindTargetUnavailable, "A backup target could not be used.")
	if err != nil {
		t.Fatalf("OperatorEvent: %v", err)
	}
	if got.SpaceID != "" {
		t.Fatalf("operator event carries a space id: %+v", got)
	}
}

func TestNotify_Validation(t *testing.T) {
	n, _, _ := newNotifier(t)
	ctx := context.Background()

	invalid := []Event{
		{Audience: AudienceSpaceMembers, SpaceID: "s1", Message: "m"},
		{Kind: KindRunFailed, Audience: AudienceSpaceMembers, Message: "m"},
		{Kind: KindRunFailed, Audience: AudienceSpaceMembers, SpaceID: "s1"},
		{Kind: KindRunFailed, Audience: "nobody", SpaceID: "s1", Message: "m"},
	}
	for _, e := range invalid {
		if _, err := n.Notify(ctx, e); err == nil {
			t.Fatalf("event %+v must be rejected", e)
		}
	}
}

// A member must never see another Space's events, and must never see the
// operator's.
func TestStore_ScopesAreSeparate(t *testing.T) {
	n, store, clock := newNotifier(t)
	ctx := context.Background()

	if _, err := n.SpaceEvent(ctx, KindRunFailed, "s1", "one"); err != nil {
		t.Fatalf("SpaceEvent: %v", err)
	}
	clock.Advance(time.Minute)
	if _, err := n.SpaceEvent(ctx, KindRunFailed, "s2", "two"); err != nil {
		t.Fatalf("SpaceEvent: %v", err)
	}
	clock.Advance(time.Minute)
	if _, err := n.OperatorEvent(ctx, KindTargetUnavailable, "three"); err != nil {
		t.Fatalf("OperatorEvent: %v", err)
	}

	forS1, err := store.List(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(forS1) != 1 || forS1[0].Message != "one" {
		t.Fatalf("s1 events = %+v", forS1)
	}

	operator, err := store.ListOperator(ctx, 0)
	if err != nil {
		t.Fatalf("ListOperator: %v", err)
	}
	if len(operator) != 1 || operator[0].Message != "three" {
		t.Fatalf("operator events = %+v", operator)
	}
}

func TestStore_ListNewestFirstWithLimit(t *testing.T) {
	n, store, clock := newNotifier(t)
	ctx := context.Background()

	for _, msg := range []string{"first", "second", "third"} {
		if _, err := n.SpaceEvent(ctx, KindRunFailed, "s1", msg); err != nil {
			t.Fatalf("SpaceEvent: %v", err)
		}
		clock.Advance(time.Minute)
	}

	got, err := store.List(ctx, "s1", 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Message != "third" || got[1].Message != "second" {
		t.Fatalf("events = %+v", got)
	}
}

func TestStore_PruneBefore(t *testing.T) {
	n, store, clock := newNotifier(t)
	ctx := context.Background()

	if _, err := n.SpaceEvent(ctx, KindRunFailed, "s1", "old"); err != nil {
		t.Fatalf("SpaceEvent: %v", err)
	}
	clock.Advance(48 * time.Hour)
	if _, err := n.SpaceEvent(ctx, KindRunFailed, "s1", "new"); err != nil {
		t.Fatalf("SpaceEvent: %v", err)
	}

	removed, err := store.PruneBefore(ctx, epoch.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	got, err := store.List(ctx, "s1", 0)
	if err != nil || len(got) != 1 || got[0].Message != "new" {
		t.Fatalf("events = %+v (%v)", got, err)
	}
}

func TestStateStore_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	backing := state.NewMemoryStore()
	clock := testutil.NewFakeClock(epoch)

	before := NewStateStore(backing, clock)
	if _, err := before.Append(ctx, Event{
		Kind:     KindBackupStale,
		Audience: AudienceSpaceMembers,
		SpaceID:  "s1",
		Message:  "stale",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	after := NewStateStore(backing, clock)
	got, err := after.List(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("List after restart: %v", err)
	}
	if len(got) != 1 || got[0].Message != "stale" {
		t.Fatalf("events after restart = %+v", got)
	}
}

func TestSMTPSink_MailsOperatorEventsOnly(t *testing.T) {
	var (
		sent []string
		body []byte
	)
	sink, err := NewSMTPSink(SMTPConfig{
		Host:       "smtp.example.org",
		Port:       587,
		From:       "backup@example.org",
		OperatorTo: "admin@example.org",
	})
	if err != nil {
		t.Fatalf("NewSMTPSink: %v", err)
	}
	sink.send = func(_ string, _ smtp.Auth, _ string, to []string, msg []byte) error {
		sent = append(sent, to...)
		body = msg
		return nil
	}

	ctx := context.Background()
	if err := sink.Deliver(ctx, Event{
		Kind:     KindRunFailed,
		Audience: AudienceSpaceMembers,
		SpaceID:  "s1",
		Message:  "member text",
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("a member event was mailed to %v", sent)
	}

	if err := sink.Deliver(ctx, Event{
		Kind:     KindTargetUnavailable,
		Audience: AudienceOperator,
		Message:  "A backup target could not be used.",
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(sent) != 1 || sent[0] != "admin@example.org" {
		t.Fatalf("recipients = %v", sent)
	}
	if !strings.Contains(string(body), "A backup target could not be used.") {
		t.Fatalf("message body = %q", body)
	}
	// The operator's mail must not identify a Space.
	if strings.Contains(string(body), "s1") {
		t.Fatalf("operator mail names a space: %q", body)
	}
}

func TestSMTPSink_RejectsIncompleteConfig(t *testing.T) {
	incomplete := []SMTPConfig{
		{Port: 587, From: "a@b", OperatorTo: "c@d"},
		{Host: "h", From: "a@b", OperatorTo: "c@d"},
		{Host: "h", Port: 587, OperatorTo: "c@d"},
		{Host: "h", Port: 587, From: "a@b"},
	}
	for _, cfg := range incomplete {
		if _, err := NewSMTPSink(cfg); err == nil {
			t.Fatalf("config %+v must be rejected", cfg)
		}
	}
}

// Header injection through a configured address must not be possible.
func TestBuildMessageStripsHeaderInjection(t *testing.T) {
	msg := string(buildMessage("a@b\r\nBcc: mallory@evil", "c@d", "subject\nX-Evil: yes", "body"))

	// Injection means a *new header line*; folding the attempt into the
	// existing header is fine.
	if strings.Contains(msg, "\r\nBcc:") || strings.Contains(msg, "\r\nX-Evil:") {
		t.Fatalf("header injection succeeded: %q", msg)
	}
	headers, _, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatalf("message has no header/body separator: %q", msg)
	}
	if got := len(strings.Split(headers, "\r\n")); got != 4 {
		t.Fatalf("message has %d headers, want the 4 it builds: %q", got, headers)
	}
}

func TestSMTPSinkErrorSaysNothingAboutCredentials(t *testing.T) {
	sink, err := NewSMTPSink(SMTPConfig{
		Host:       "smtp.example.org",
		Port:       587,
		Username:   "backup",
		Password:   "hunter2",
		From:       "backup@example.org",
		OperatorTo: "admin@example.org",
	})
	if err != nil {
		t.Fatalf("NewSMTPSink: %v", err)
	}
	sink.send = func(string, smtp.Auth, string, []string, []byte) error {
		return errors.New("535 auth failed for user backup with password hunter2")
	}

	err = sink.Deliver(context.Background(), Event{
		Kind:     KindTargetUnavailable,
		Audience: AudienceOperator,
		Message:  "target unavailable",
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaked the smtp password: %v", err)
	}
}
