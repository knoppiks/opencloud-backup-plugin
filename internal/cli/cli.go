// Package cli holds what the three command-line programs (backupd, takeout,
// decrypt) agree on: how flags are parsed and what the exit status means.
//
// Exit status:
//
//	0  success, and asking for help (-h)
//	1  the program ran and failed (wrong key, unreachable store, bad
//	   configuration in the environment)
//	2  the command line was wrong (unknown flag or command, a missing
//	   required flag, a stray argument); nothing was attempted
//
// The package never exits and never writes to the process's own streams; it
// is handed the writers, so every path is testable.
package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
)

// Exit statuses.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// UsageError is a command line that cannot be acted on. Nothing was
// attempted, and running the same thing again will fail the same way.
type UsageError struct {
	Err error
	// shown is set when the problem has already been written out (the flag
	// package prints its own errors, with the usage text).
	shown bool
}

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// Usagef reports a command line that cannot be acted on.
func Usagef(format string, a ...any) error {
	return &UsageError{Err: fmt.Errorf(format, a...)}
}

// IsUsage reports whether err is a usage error or a request for help, i.e.
// something that is about the command line rather than the work.
func IsUsage(err error) bool {
	var usage *UsageError
	return errors.As(err, &usage) || errors.Is(err, flag.ErrHelp)
}

// Parse parses args into fs. None of these programs takes positional
// arguments, so one is refused rather than silently ignored.
//
// Asked for help (-h, -help), it writes the usage text to stdout, because
// that is the output asked for, and returns flag.ErrHelp. A malformed command
// line has its error and the usage text written to stderr and comes back as a
// *UsageError.
func Parse(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) error {
	var out bytes.Buffer
	fs.SetOutput(&out)
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		_, _ = out.WriteTo(stdout)
		return flag.ErrHelp
	case err != nil:
		_, _ = out.WriteTo(stderr)
		return &UsageError{Err: err, shown: true}
	case fs.NArg() > 0:
		return Usagef("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

// ExitCode is the exit status for the outcome err.
func ExitCode(err error) int {
	var usage *UsageError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.As(err, &usage):
		return ExitUsage
	default:
		return ExitFailure
	}
}

// Report writes err to stderr the way a person at a terminal reads it, unless
// it was written already or is a request for help, and returns the exit
// status. program is what the user typed, e.g. "backupd rotate-srw"; a usage
// error ends with a pointer to its help.
func Report(stderr io.Writer, program string, err error) int {
	code := ExitCode(err)
	if code == ExitOK {
		return code
	}
	var usage *UsageError
	if errors.As(err, &usage) {
		if !usage.shown {
			_, _ = fmt.Fprintf(stderr, "%s: %v\nRun '%s -h' for usage.\n", program, err, program)
		}
		return code
	}
	_, _ = fmt.Fprintf(stderr, "%s: %v\n", program, err)
	return code
}
