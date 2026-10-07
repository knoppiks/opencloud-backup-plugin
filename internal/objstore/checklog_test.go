package objstore

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Built from parts so no key-shaped literal sits in the source.
var (
	testAccessKey = strings.Join([]string{"GK", "checklog", "access"}, "-")
	testSecretKey = strings.Join([]string{"checklog", "secret", "value"}, "-")
)

// checkLog runs a real check against endpoint and returns the logged records.
func checkLog(t *testing.T, endpoint string) (CheckOutcome, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	checker := S3Checker{
		// The SDK makes up to three attempts with random backoff of up to
		// 2 s and then 4 s, so a failing check can take 6 s before its real
		// cause is known. Anything shorter turns that cause into "timeout"
		// at random.
		Timeout: 30 * time.Second,
		Logger:  slog.New(slog.NewJSONHandler(&buf, nil)),
	}
	cfg := S3Config{
		Endpoint:        endpoint,
		Region:          "garage",
		Bucket:          "household",
		AccessKeyID:     testAccessKey,
		SecretAccessKey: testSecretKey,
		UsePathStyle:    true,
	}
	outcome := checker.Check(context.Background(), cfg, "vault/")

	if strings.Contains(buf.String(), testSecretKey) || strings.Contains(buf.String(), testAccessKey) {
		t.Fatalf("credentials in the log: %s", buf.String())
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return outcome, records
}

func onlyRecord(t *testing.T, records []map[string]any) map[string]any {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("logged %d records, want 1: %v", len(records), records)
	}
	rec := records[0]
	if rec["level"] != "WARN" || rec["msg"] != "target connection check failed" {
		t.Fatalf("record = %v", rec)
	}
	return rec
}

// The case that started this: a certificate from a CA the service does not
// trust. The admin is told "not reachable"; the operator is told why.
func TestCheckLogsAnUntrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()

	outcome, records := checkLog(t, srv.URL)
	if outcome != CheckUnreachable {
		t.Fatalf("outcome = %q, want unreachable", outcome)
	}
	rec := onlyRecord(t, records)
	if cause, _ := rec["cause"].(string); !strings.Contains(cause, "certificate not trusted") {
		t.Fatalf("cause = %q", cause)
	}
	if rec["outcome"] != "unreachable" || rec["bucket"] != "household" {
		t.Fatalf("record = %v", rec)
	}
	if host := rec["endpoint_host"]; host != strings.TrimPrefix(srv.URL, "https://") {
		t.Fatalf("endpoint_host = %v", host)
	}
}

func TestCheckLogsARefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	outcome, records := checkLog(t, "http://"+addr)
	if outcome != CheckUnreachable {
		t.Fatalf("outcome = %q, want unreachable", outcome)
	}
	if cause, _ := onlyRecord(t, records)["cause"].(string); !strings.Contains(cause, "connection failed") ||
		!strings.Contains(cause, "refused") {
		t.Fatalf("cause = %q", cause)
	}
}

func TestCheckLogsAFailedNameLookup(t *testing.T) {
	outcome, records := checkLog(t, "http://backup-host.invalid:3900")
	if outcome != CheckUnreachable {
		t.Fatalf("outcome = %q, want unreachable", outcome)
	}
	if cause, _ := onlyRecord(t, records)["cause"].(string); !strings.Contains(cause, "name lookup failed") {
		t.Fatalf("cause = %q", cause)
	}
}

func TestCheckLogsWhatTheEndpointAnswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<Error><Code>AccessDenied</Code><Message>key may not read this bucket</Message></Error>`))
	}))
	defer srv.Close()

	outcome, records := checkLog(t, srv.URL)
	if outcome != CheckDenied {
		t.Fatalf("outcome = %q, want denied", outcome)
	}
	cause, _ := onlyRecord(t, records)["cause"].(string)
	if !strings.Contains(cause, "AccessDenied") || !strings.Contains(cause, "key may not read this bucket") {
		t.Fatalf("cause = %q", cause)
	}
}

func TestCheckLogsNothingWhenItSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ListBucketResult><Name>household</Name><KeyCount>0</KeyCount><IsTruncated>false</IsTruncated></ListBucketResult>`))
	}))
	defer srv.Close()

	outcome, records := checkLog(t, srv.URL)
	if outcome != CheckOK {
		t.Fatalf("outcome = %q, want ok", outcome)
	}
	if len(records) != 0 {
		t.Fatalf("logged on success: %v", records)
	}
}

// A configuration that cannot become a client was silently "unknown" before.
func TestCheckLogsAnUnusableConfiguration(t *testing.T) {
	var buf bytes.Buffer
	checker := S3Checker{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	if got := checker.Check(context.Background(), S3Config{Endpoint: "example.invalid"}, ""); got != CheckUnknown {
		t.Fatalf("outcome = %q", got)
	}
	if !strings.Contains(buf.String(), "bucket is required") {
		t.Fatalf("log = %s", buf.String())
	}
}

func TestRedactRemovesCredentialsAndCapsLength(t *testing.T) {
	cfg := S3Config{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}
	got := redact("signed with "+testAccessKey+" and "+testSecretKey, cfg)
	if strings.Contains(got, testAccessKey) || strings.Contains(got, testSecretKey) {
		t.Fatalf("redact = %q", got)
	}
	if got := redact("a certificate", S3Config{AccessKeyID: "a"}); got != "a certificate" {
		t.Fatalf("a short value shredded the text: %q", got)
	}
	long := redact(strings.Repeat("x", 1000), S3Config{})
	if n := len([]rune(long)); n != maxCauseLength+1 {
		t.Fatalf("capped length = %d", n)
	}
}

func TestEndpointHost(t *testing.T) {
	cases := map[S3Config]string{
		{Endpoint: "https://lu-s3.home.arpa/some/path?x=1"}: "lu-s3.home.arpa",
		{Endpoint: "garage:3900", DisableTLS: true}:         "garage:3900",
		{}: "",
	}
	for cfg, want := range cases {
		if got := endpointHost(cfg); got != want {
			t.Errorf("endpointHost(%+v) = %q, want %q", cfg, got, want)
		}
	}
}

func TestCheckCauseWithoutAnError(t *testing.T) {
	if got := checkCause(nil); got == "" {
		t.Fatal("empty cause")
	}
	if got := checkCause(context.DeadlineExceeded); !strings.Contains(got, "deadline") {
		t.Fatalf("cause = %q", got)
	}
}
