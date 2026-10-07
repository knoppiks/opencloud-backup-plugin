package main

// The command line. With no arguments backupd is the service; with one it is
// an operator command. The maintenance commands ship in the same binary
// because they need the same configuration, the same state Space and the
// same custody keys.
//
// A command's flags are read before the configuration, so asking for help,
// or getting the command line wrong, needs no environment at all. Exit status
// is internal/cli's: 0 success and help, 1 failure, 2 wrong usage.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"opencloud-backup-plugin/internal/buildinfo"
	"opencloud-backup-plugin/internal/cli"
	"opencloud-backup-plugin/internal/config"
)

// program is the name messages are signed with.
const program = "backupd"

// action is a command whose command line has been read, ready to run against
// the configuration. stdout receives the command's output and nothing else;
// logs go to the logger, which writes to stderr.
type action func(ctx context.Context, cfg config.Backupd, logger *slog.Logger, stdout io.Writer) error

// command is one operator command.
type command struct {
	name    string
	summary string
	// parse reads the command's flags. Help comes back as flag.ErrHelp and a
	// malformed command line as a *cli.UsageError.
	parse func(args []string, stdout, stderr io.Writer) (action, error)
}

// commands are the operator commands, in the order help lists them.
func commands() []command {
	return []command{
		{
			name:    "provision-state-space",
			summary: "create the Space the service keeps its state in, and print its id",
			parse:   parseProvision,
		},
		{
			name:    srwRotation.name,
			summary: "re-wrap every Space's server key envelope from SRW_KEY_OLD to SRW_KEY",
			parse:   parseRotate(srwRotation),
		},
		{
			name:    twRotation.name,
			summary: "re-wrap every target's credentials from TW_KEY_OLD to TW_KEY",
			parse:   parseRotate(twRotation),
		},
	}
}

// lookupCommand finds an operator command by name.
func lookupCommand(name string) (command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// isVersion and isHelp accept the spellings people try first.
func isVersion(arg string) bool { return arg == "version" || arg == "-version" || arg == "--version" }
func isHelp(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "-help" || arg == "--help"
}

// runCommandLine handles everything but the service itself. It returns the
// exit status.
func runCommandLine(args, environ []string, stdout, stderr io.Writer) int {
	name := args[0]
	switch {
	case isVersion(name):
		if len(args) > 1 {
			return cli.Report(stderr, program, cli.Usagef("version takes no arguments"))
		}
		_, _ = fmt.Fprintln(stdout, buildinfo.Get().Line(program))
		return cli.ExitOK
	case isHelp(name):
		return help(args[1:], stdout, stderr)
	}

	cmd, ok := lookupCommand(name)
	if !ok {
		return cli.Report(stderr, program, unknownCommand(name))
	}
	act, err := cmd.parse(args[1:], stdout, stderr)
	if err != nil {
		return cli.Report(stderr, program+" "+name, err)
	}

	logger := newLogger(stderr)
	cfg, err := config.LoadBackupd(environ)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		return cli.ExitFailure
	}
	if err := act(context.Background(), cfg, logger, stdout); err != nil {
		logger.Error("command failed", "command", name, "err", err)
		return cli.ExitFailure
	}
	return cli.ExitOK
}

// unknownCommand is the usage error for a name no command has. It lists the
// ones that exist, so a typo is not a dead end.
func unknownCommand(name string) error {
	var names []string
	for _, c := range commands() {
		names = append(names, c.name)
	}
	return cli.Usagef("unknown command %q; the commands are %s, version and help",
		name, strings.Join(names, ", "))
}

// help writes the overview, or one command's own help.
func help(args []string, stdout, stderr io.Writer) int {
	switch len(args) {
	case 0:
		_, _ = io.WriteString(stdout, overview())
		return cli.ExitOK
	case 1:
		cmd, ok := lookupCommand(args[0])
		if !ok {
			return cli.Report(stderr, program, unknownCommand(args[0]))
		}
		_, err := cmd.parse([]string{"-h"}, stdout, stderr)
		return cli.ExitCode(err)
	default:
		return cli.Report(stderr, program, cli.Usagef("help takes at most one command"))
	}
}

// overview is `backupd help`.
func overview() string {
	var b strings.Builder
	b.WriteString(`backupd — the Backup Vault service and its operator commands.

Usage:
  backupd                      run the service
  backupd <command> [flags]    run an operator command
  backupd help <command>       show a command's flags

Commands:
`)
	for _, c := range commands() {
		fmt.Fprintf(&b, "  %-24s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(&b, "  %-24s %s\n", "version", "print the version")
	fmt.Fprintf(&b, "  %-24s %s\n", "help", "show this help")
	b.WriteString(`
The service and every command are configured through the environment
(docs/reference/environment.md). Operator commands need the same environment
as the service.

Exit status: 0 success, 1 failure, 2 the command line was wrong.
`)
	return b.String()
}
