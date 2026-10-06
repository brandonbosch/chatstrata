// Package cli implements the chatstrata command line.
//
// Commands, flags and output follow the Python CLI (chatstrata/cli.py) so
// scripts, scheduled jobs and agents keep working across the switch.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/sources/claudecode"
)

// Version is set at build time with -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

// sources available in the Go implementation.
var sources = map[string]model.Source{
	"claude_code": claudecode.Source{},
}

// notYetPorted are Python adapters the Go implementation doesn't have yet.
var notYetPorted = []string{"claude_export", "codex_cli", "hermes_agent", "omp", "opencode"}

type env struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
}

// exitError carries a process exit status up to Run.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// usageError is reported with the command's usage hint and exit status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

type command struct {
	name    string
	summary string
	run     func(e *env, args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"ingest", "Ingest conversations from a source", runIngest},
		{"query", "Run a read-only SQL query against the archive", runQuery},
		{"search", "Search conversations by keyword", runSearch},
		{"stats", "Show a summary of what's in the archive", runStats},
		{"doctor", "Run basic sanity checks on the archive", runDoctor},
		{"reindex", "Rebuild the full-text search index", runReindex},
		{"rebuild", "Rebuild the archive from its observation log", runRebuild},
		{"import-legacy", "Import a Python-era archive into the observation log", runImportLegacy},
		{"sources", "List available source adapters", runSources},
		{"version", "Print the version", runVersion},
	}
}

// Run executes the CLI and returns the process exit status.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	e := &env{ctx: ctx, stdout: stdout, stderr: stderr}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(stdout)
		return 0
	}
	for _, c := range commands {
		if c.name != args[0] {
			continue
		}
		err := c.run(e, args[1:])
		var exit exitError
		var usage usageError
		switch {
		case err == nil:
			return 0
		case errors.Is(err, flag.ErrHelp):
			return 0
		case errors.As(err, &exit):
			return exit.code
		case errors.As(err, &usage):
			fmt.Fprintf(stderr, "Error: %s\nRun 'chatstrata %s --help' for usage.\n", usage.msg, c.name)
			return 2
		default:
			fmt.Fprintf(stderr, "Error: %s\n", err)
			return 1
		}
	}
	fmt.Fprintf(stderr, "Error: no such command %q\n\n", args[0])
	printUsage(stderr)
	return 2
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: chatstrata COMMAND [ARGS]...")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A personal, queryable archive of your AI conversations.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
}

// parse parses flags that may appear before, between or after positional
// arguments, as click allows, and returns the positionals.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		// flag stops at "--": everything after it is positional.
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func newFlagSet(e *env, name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprintf(e.stderr, "Usage: chatstrata %s\n\nOptions:\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

func runSources(e *env, args []string) error {
	fs := newFlagSet(e, "sources", "sources")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(e.stdout, "Available sources:")
	for _, name := range names {
		s := sources[name]
		fmt.Fprintf(e.stdout, "  %-20s %-25s v%s\n", name, s.DisplayName(), s.Version())
	}
	fmt.Fprintln(e.stdout)
	fmt.Fprintf(e.stdout, "Not yet in the Go version (use the Python chatstrata): %s\n",
		strings.Join(notYetPorted, ", "))
	return nil
}

func runVersion(e *env, args []string) error {
	fmt.Fprintf(e.stdout, "chatstrata %s\n", Version)
	return nil
}
