package objstore

// Why a connection check failed, for the operator's log.
//
// "Endpoint not reachable" covers a name that does not resolve, a refused
// connection and a certificate the service does not trust. Those are fixed in
// three different places, and only the service sees the network and the trust
// store it actually uses. So a failed check logs its cause: the innermost
// error that names it, rather than the SDK's retry wrapping around it.
//
// Credentials never reach the line. S3 signs in headers, so neither the
// request URL nor an error built from it carries them; the key pair is still
// redacted from the text as a second line of defence.

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/aws/smithy-go"
)

// maxCauseLength caps the logged cause. An endpoint can answer with anything.
const maxCauseLength = 300

// minRedactLength is the shortest credential redact looks for. Real S3 keys
// are far longer; replacing a one-letter test value would shred the message.
const minRedactLength = 8

// report logs a failed check. A successful one says nothing.
func (c S3Checker) report(outcome CheckOutcome, cfg S3Config, err error) {
	if c.Logger == nil || outcome == CheckOK {
		return
	}
	c.Logger.Warn("target connection check failed",
		"outcome", string(outcome),
		"endpoint_host", endpointHost(cfg),
		"bucket", cfg.Bucket,
		"cause", redact(checkCause(err), cfg))
}

// checkCause names what went wrong as specifically as the error allows.
func checkCause(err error) string {
	if err == nil {
		return "no error reported"
	}
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
		dns              *net.DNSError
		op               *net.OpError
		api              smithy.APIError
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "no answer before the check's deadline"
	case errors.As(err, &unknownAuthority):
		return "certificate not trusted: " + unknownAuthority.Error() +
			" (a private CA has to be added to the service's trust store)"
	case errors.As(err, &hostname):
		return "certificate does not match the host: " + hostname.Error()
	case errors.As(err, &invalid):
		return "certificate invalid: " + invalid.Error()
	case errors.As(err, &dns):
		return "name lookup failed: " + dns.Error()
	case errors.As(err, &op):
		return "connection failed: " + op.Error()
	case errors.As(err, &api):
		return fmt.Sprintf("the endpoint answered %s: %s", api.ErrorCode(), api.ErrorMessage())
	default:
		return err.Error()
	}
}

// endpointHost is the host (and port) the check talked to, without scheme,
// path or query.
func endpointHost(cfg S3Config) string {
	if cfg.Endpoint == "" {
		return ""
	}
	u, err := url.Parse(EndpointURL(cfg.Endpoint, cfg.DisableTLS))
	if err != nil {
		return "unparseable endpoint"
	}
	return u.Host
}

// redact removes the target's own credentials from text and caps its length.
func redact(text string, cfg S3Config) string {
	for _, secret := range []string{cfg.SecretAccessKey, cfg.AccessKeyID} {
		if len(secret) >= minRedactLength {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	if runes := []rune(text); len(runes) > maxCauseLength {
		text = string(runes[:maxCauseLength]) + "…"
	}
	return text
}
