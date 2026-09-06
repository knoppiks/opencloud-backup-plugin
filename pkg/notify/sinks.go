package notify

// Delivery sinks.
//
// v1 ships two: structured logs (always) and SMTP (when the operator configures
// it). The phase plan's first choice was OpenCloud's own notification service,
// but no Phase-0 spike established a usable API for an external plugin, so it
// is not implemented on speculation — it becomes another Sink the day its API
// is verified, with nothing else changing.
//
// Recipient resolution is deliberately conservative. Operator events go to the
// operator address the deployment configures. Member events currently have no
// email path: resolving a Space's members to email addresses needs OpenCloud's
// user directory, which this service does not read yet. Those events are
// recorded and served through the API, which is where the status board picks
// them up; they are not silently dropped, they are simply not emailed.

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
)

// LogSink writes events to the service log. It is always wired: an operator
// reading logs must be able to see what users were told.
type LogSink struct {
	Logger *slog.Logger
}

var _ Sink = LogSink{}

// Deliver logs one event.
func (s LogSink) Deliver(_ context.Context, e Event) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// Space id is an identifier, not content; message is already sanitized.
	logger.Warn("backup notification",
		"kind", string(e.Kind),
		"audience", string(e.Audience),
		"space", e.SpaceID,
		"message", e.Message,
	)
	return nil
}

// SMTPConfig is the operator's mail configuration. The password arrives from a
// Secret-backed environment variable and is never logged.
type SMTPConfig struct {
	// Host and Port address the mail server, e.g. "smtp.example.org", 587.
	Host string
	Port int
	// Username and Password authenticate; leave empty for an open relay on a
	// trusted network.
	Username string
	Password string
	// From is the envelope sender.
	From string
	// OperatorTo receives operator-audience events. When empty, operator events
	// are recorded but not mailed.
	OperatorTo string
}

// Valid reports whether the configuration can send anything.
func (c SMTPConfig) Valid() bool {
	return c.Host != "" && c.Port > 0 && c.From != "" && c.OperatorTo != ""
}

// SMTPSink mails operator events.
type SMTPSink struct {
	cfg SMTPConfig
	// send is the transport, injected so tests need no mail server.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

var _ Sink = (*SMTPSink)(nil)

// NewSMTPSink constructs a sink from operator configuration.
func NewSMTPSink(cfg SMTPConfig) (*SMTPSink, error) {
	if !cfg.Valid() {
		return nil, fmt.Errorf("notify: smtp needs a host, port, sender and operator recipient")
	}
	return &SMTPSink{cfg: cfg, send: smtp.SendMail}, nil
}

// Deliver mails an event to its audience. Events with no email path are a
// no-op, not an error.
func (s *SMTPSink) Deliver(_ context.Context, e Event) error {
	if e.Audience != AudienceOperator {
		// See the package note: member events have no address to go to yet.
		return nil
	}

	msg := buildMessage(s.cfg.From, s.cfg.OperatorTo, subjectFor(e), e.Message)
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}
	if err := s.send(addr, auth, s.cfg.From, []string{s.cfg.OperatorTo}, msg); err != nil {
		// The transport error can quote credentials on some servers; report the
		// shape of the failure only.
		return fmt.Errorf("notify: could not send mail")
	}
	return nil
}

// subjectFor gives an event a short, non-revealing subject line.
func subjectFor(e Event) string {
	switch e.Kind {
	case KindTargetUnavailable:
		return "[backup] a backup target is unavailable"
	case KindBackupStale:
		return "[backup] a backup is out of date"
	case KindRunFailed:
		return "[backup] a backup run failed"
	default:
		return "[backup] notification"
	}
}

// buildMessage renders a minimal RFC 5322 message. Headers are built from
// configuration and fixed strings; the body is the sanitized message.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sanitizeHeader(from))
	fmt.Fprintf(&b, "To: %s\r\n", sanitizeHeader(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", sanitizeHeader(subject))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\r\n", "\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}

// sanitizeHeader strips CR/LF so nothing can inject extra headers.
func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}
