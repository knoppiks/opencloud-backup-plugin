package notify

// Turning a run outcome into notifications.
//
// The split enforced here is decision #15's: the member of a Space learns their
// backup failed; the operator learns that a target is unusable, with no space
// id, no user and no counts attached. Which failures are operational is a
// judgement about the *cause*, and causes are the pipeline's vocabulary, so the
// classifier is injected by whoever wires the service rather than hard-coded
// here — this package must not depend on the backup pipeline to be testable.

import (
	"context"
	"log/slog"
)

// Classification says what a failed run means and to whom.
type Classification struct {
	// MemberMessage is the user-safe text shown to the Space's members.
	MemberMessage string
	// Operational reports whether the operator has to act. When true, an
	// anonymous operator event is recorded alongside the member event.
	Operational bool
	// OperatorMessage is the operator's text. It must not identify a Space or
	// a user; the Reporter rejects the event if it somehow does.
	OperatorMessage string
	// Silent suppresses notification entirely. Some "failures" are not news to
	// anybody — a run skipped because another was already going, or one cut
	// short by shutdown.
	Silent bool
}

// Classifier maps a run error onto a Classification.
type Classifier func(err error) Classification

// DefaultClassification is what an unrecognised failure means: tell the member
// something went wrong, do not speculate, do not page the operator.
func DefaultClassification() Classification {
	return Classification{MemberMessage: "The last backup run failed."}
}

// Reporter records notifications for run outcomes.
type Reporter struct {
	notifier *Notifier
	classify Classifier
	logger   *slog.Logger
}

// NewReporter constructs a Reporter. A nil classifier uses the default.
func NewReporter(n *Notifier, classify Classifier, logger *slog.Logger) *Reporter {
	if classify == nil {
		classify = func(error) Classification { return DefaultClassification() }
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Reporter{notifier: n, classify: classify, logger: logger}
}

// RunFinished records the notifications a finished run warrants. A successful
// run is deliberately silent: backups that work are not news.
func (r *Reporter) RunFinished(ctx context.Context, spaceID string, err error) {
	if r == nil || r.notifier == nil || err == nil {
		return
	}

	class := r.classify(err)
	if class.Silent {
		return
	}
	if class.MemberMessage == "" {
		class.MemberMessage = DefaultClassification().MemberMessage
	}

	if _, notifyErr := r.notifier.SpaceEvent(ctx, KindRunFailed, spaceID, class.MemberMessage); notifyErr != nil {
		r.logger.Error("could not record run-failure notification", "space", spaceID, "err", notifyErr)
	}

	if !class.Operational {
		return
	}
	message := class.OperatorMessage
	if message == "" {
		message = "A backup target could not be used. Check the target's endpoint and credentials."
	}
	if _, notifyErr := r.notifier.OperatorEvent(ctx, KindTargetUnavailable, message); notifyErr != nil {
		r.logger.Error("could not record operator notification", "err", notifyErr)
	}
}
