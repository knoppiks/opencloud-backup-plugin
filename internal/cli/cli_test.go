package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"
)

func newFlagSet() (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("tool", flag.ContinueOnError)
	name := fs.String("name", "", "a name")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: tool -name NAME")
		fs.PrintDefaults()
	}
	return fs, name
}

func TestParseAcceptsFlags(t *testing.T) {
	fs, name := newFlagSet()
	var stdout, stderr bytes.Buffer
	if err := Parse(fs, []string{"-name", "x"}, &stdout, &stderr); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *name != "x" {
		t.Fatalf("name = %q", *name)
	}
	if stdout.Len()+stderr.Len() != 0 {
		t.Fatalf("a good command line printed something: %q %q", stdout.String(), stderr.String())
	}
}

// Help is the output that was asked for, so it goes to stdout and the status
// is success.
func TestParseHelpGoesToStdout(t *testing.T) {
	for _, arg := range []string{"-h", "-help", "--help"} {
		fs, _ := newFlagSet()
		var stdout, stderr bytes.Buffer
		err := Parse(fs, []string{arg}, &stdout, &stderr)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("%s: err = %v", arg, err)
		}
		if !strings.Contains(stdout.String(), "usage: tool") || !strings.Contains(stdout.String(), "-name") {
			t.Fatalf("%s: stdout = %q", arg, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("%s: stderr = %q", arg, stderr.String())
		}
		if code := Report(&stderr, "tool", err); code != ExitOK || stderr.Len() != 0 {
			t.Fatalf("%s: Report = %d, stderr %q", arg, code, stderr.String())
		}
	}
}

func TestParseUnknownFlagIsAUsageErrorShownOnce(t *testing.T) {
	fs, _ := newFlagSet()
	var stdout, stderr bytes.Buffer
	err := Parse(fs, []string{"-nope"}, &stdout, &stderr)
	if ExitCode(err) != ExitUsage || !IsUsage(err) {
		t.Fatalf("err = %v, code %d", err, ExitCode(err))
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	shown := stderr.String()
	if !strings.Contains(shown, "-nope") || !strings.Contains(shown, "usage: tool") {
		t.Fatalf("stderr = %q", shown)
	}

	// The flag package already said it; Report must not say it again.
	if code := Report(&stderr, "tool", err); code != ExitUsage {
		t.Fatalf("Report = %d", code)
	}
	if stderr.String() != shown {
		t.Fatalf("Report repeated the error: %q", stderr.String())
	}
}

func TestParseRefusesPositionalArguments(t *testing.T) {
	fs, _ := newFlagSet()
	var stdout, stderr bytes.Buffer
	err := Parse(fs, []string{"-name", "x", "stray"}, &stdout, &stderr)
	if ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), `"stray"`) {
		t.Fatalf("err = %v", err)
	}

	if code := Report(&stderr, "tool", err); code != ExitUsage {
		t.Fatalf("Report = %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, `tool: unexpected argument "stray"`) ||
		!strings.Contains(got, "Run 'tool -h' for usage.") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestExitCodes(t *testing.T) {
	cause := errors.New("cause")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, ExitOK},
		{"help", flag.ErrHelp, ExitOK},
		{"wrapped help", fmt.Errorf("x: %w", flag.ErrHelp), ExitOK},
		{"usage", Usagef("-in is required"), ExitUsage},
		{"wrapped usage", fmt.Errorf("x: %w", Usagef("bad")), ExitUsage},
		{"failure", cause, ExitFailure},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestUsagefKeepsTheCause(t *testing.T) {
	cause := errors.New("cause")
	err := Usagef("wrapped: %w", cause)
	if !errors.Is(err, cause) || err.Error() != "wrapped: cause" {
		t.Fatalf("err = %v", err)
	}
	if IsUsage(cause) {
		t.Fatal("a plain error is not a usage error")
	}
}

func TestReportFailure(t *testing.T) {
	var stderr bytes.Buffer
	if code := Report(&stderr, "decrypt", errors.New("key does not match")); code != ExitFailure {
		t.Fatalf("Report = %d", code)
	}
	if got := stderr.String(); got != "decrypt: key does not match\n" {
		t.Fatalf("stderr = %q", got)
	}
	stderr.Reset()
	if code := Report(&stderr, "decrypt", nil); code != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Report(nil) = %d, %q", code, stderr.String())
	}
}
