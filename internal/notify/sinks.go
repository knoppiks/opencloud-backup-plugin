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
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"
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
	// Timeout bounds one delivery, from dialling to QUIT. Zero uses
	// DefaultSMTPTimeout.
	Timeout time.Duration
}

// DefaultSMTPTimeout bounds one mail delivery. A mail server that accepts the
// connection and then says nothing must not hold a scheduler slot, or the
// shutdown drain, for longer than this (review-2026-10.md F7).
const DefaultSMTPTimeout = 30 * time.Second

// Valid reports whether the configuration can send anything.
func (c SMTPConfig) Valid() bool {
	return c.Host != "" && c.Port > 0 && c.From != "" && c.OperatorTo != ""
}

// SMTPSink mails operator events.
type SMTPSink struct {
	cfg SMTPConfig
	// send is the transport, injected so tests need no mail server.
	send func(ctx context.Context, addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

var _ Sink = (*SMTPSink)(nil)

// NewSMTPSink constructs a sink from operator configuration.
func NewSMTPSink(cfg SMTPConfig) (*SMTPSink, error) {
	if !cfg.Valid() {
		return nil, fmt.Errorf("notify: smtp needs a host, port, sender and operator recipient")
	}
	sink := &SMTPSink{cfg: cfg}
	sink.send = sink.sendMail
	return sink, nil
}

// Deliver mails an event to its audience. Events with no email path are a
// no-op, not an error.
func (s *SMTPSink) Deliver(ctx context.Context, e Event) error {
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
	if err := s.send(ctx, addr, auth, s.cfg.From, []string{s.cfg.OperatorTo}, msg); err != nil {
		return fmt.Errorf("notify: could not send mail: %s", describeSMTPFailure(err))
	}
	return nil
}

// describeSMTPFailure says what went wrong with a mail delivery in words safe
// for the service log (review-2026-10.md F3).
//
// What the *server* said is never repeated: a reply to AUTH can quote what the
// client sent, which is the password in base64, so a server reply is reported
// by its numeric code alone. Everything else here is produced locally — the
// step that failed, a connection error naming the address, a TLS verification
// error, a timeout — and says nothing about the credentials. An error of a
// kind not listed is reported as unexpected, without its text.
func describeSMTPFailure(err error) string {
	step := ""
	var stepErr smtpStepError
	if errors.As(err, &stepErr) {
		step = stepErr.step + ": "
		err = stepErr.err
	}

	var reply *textproto.Error
	var opErr *net.OpError
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.As(err, &reply):
		return fmt.Sprintf("%sserver replied %d", step, reply.Code)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return step + "timed out"
	case errors.Is(err, context.Canceled):
		return step + "cancelled"
	case errors.As(err, &certErr), errors.As(err, &recordErr), errors.As(err, &opErr),
		errors.Is(err, errNoSMTPAuth), errors.Is(err, io.EOF):
		return step + err.Error()
	case isLocalSMTPAuthRefusal(err):
		return step + "refused to send credentials: " + err.Error()
	default:
		return step + "unexpected failure"
	}
}

// isLocalSMTPAuthRefusal recognises net/smtp's own refusals to authenticate
// (smtp.PlainAuth over an unencrypted connection, or to another host). They
// are produced locally and quote nothing, but are not exported to match on.
func isLocalSMTPAuthRefusal(err error) bool {
	msg := err.Error()
	return msg == "unencrypted connection" || msg == "wrong host name"
}

// smtpStepError names the step of the SMTP conversation that failed.
type smtpStepError struct {
	step string
	err  error
}

func (e smtpStepError) Error() string { return e.step + ": " + e.err.Error() }
func (e smtpStepError) Unwrap() error { return e.err }

// atStep tags err with the conversation step it came from.
func atStep(step string, err error) error {
	if err == nil {
		return nil
	}
	return smtpStepError{step: step, err: err}
}

var errNoSMTPAuth = errors.New("smtp server does not support AUTH")

// sendMail is net/smtp.SendMail with a deadline. SendMail itself has none: it
// dials without a timeout and waits on every reply for as long as the server
// cares to take. Here the whole conversation is bounded by the sink's timeout
// and by ctx, whichever ends first. Like SendMail it upgrades to TLS when the
// server offers STARTTLS, and smtp.PlainAuth still refuses to send a password
// over a connection that did not.
func (s *SMTPSink) sendMail(
	ctx context.Context, addr string, a smtp.Auth, from string, to []string, msg []byte,
) error {
	timeout := s.cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultSMTPTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return atStep("connect", err)
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return atStep("connect", err)
	}
	// A cancellation before the deadline closes the connection too.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return endedBy(ctx, atStep("greeting", err))
	}
	defer func() { _ = c.Close() }()
	return endedBy(ctx, converse(c, s.cfg.Host, a, from, to, msg))
}

// endedBy reports a failure caused by ctx ending as that, at the step it
// interrupted. Ending ctx closes the connection, and the read in flight then
// fails with "use of closed network connection", which hides the reason.
func endedBy(ctx context.Context, err error) error {
	cause := ctx.Err()
	if err == nil || cause == nil {
		return err
	}
	var stepErr smtpStepError
	if errors.As(err, &stepErr) {
		return smtpStepError{step: stepErr.step, err: cause}
	}
	return cause
}

// converse runs one SMTP transaction on an open client. Each failure names the
// step it happened in (describeSMTPFailure).
func converse(c *smtp.Client, host string, a smtp.Auth, from string, to []string, msg []byte) error {
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return atStep("starttls", err)
		}
	}
	if a != nil {
		if ok, _ := c.Extension("AUTH"); !ok {
			return atStep("auth", errNoSMTPAuth)
		}
		if err := c.Auth(a); err != nil {
			return atStep("auth", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return atStep("mail from", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return atStep("rcpt to", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return atStep("data", err)
	}
	if _, err := w.Write(msg); err != nil {
		return atStep("data", err)
	}
	if err := w.Close(); err != nil {
		return atStep("data", err)
	}
	return atStep("quit", c.Quit())
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
