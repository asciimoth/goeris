// Command goeris provides a command-line interface for the Go ERIS library.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/asciimoth/zpgo"
)

const usage = `Usage: goeris [storage flags] command [storage flags]

  dump              Show environment settings and the current storage setup.
  help, -h, --help  Show help.

Storage flags take semicolon-separated paths.
  --prepend-blocks PATHS        Add block volumes before the default volumes.
  --append-blocks PATHS         Add block volumes after the default volumes.
  --replace-blocks PATHS        Replace the default block volumes.
  --prepend-capabilities PATHS  Add capability mounts before the default stores.
  --append-capabilities PATHS   Add capability mounts after the default stores.
  --replace-capabilities PATHS  Replace the default capability stores.
  --prepend-fs PATHS            Add ERIS-FS mounts before the default stores.
  --append-fs PATHS             Add ERIS-FS mounts after the default stores.
  --replace-fs PATHS            Replace the default ERIS-FS stores.

Prepends and appends keep their order. The last replacement supplies the
middle of the chain. Use an empty replacement, such as --replace-blocks=,
to disable that chain's defaults.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) (code int) {
	progress, err := zpgo.NewAuto()
	if err != nil {
		var disabled zpgo.Progress
		_, _ = disabled.PrintfStderr("goeris: set up progress: %v\n", err)
		return 1
	}
	// Progress reports are best effort. Close the reporter after store cleanup.
	defer func() { _ = progress.Close() }()

	// Use stdout for the command's main output data and help text. Use stderr
	// for related human-readable messages and errors. Write both through the reporter.
	// Long-running commands, such as those that copy data, can use its root
	// node to report progress.
	command, paths, err := parseCommand(args)
	if errors.Is(err, flag.ErrHelp) {
		if _, err := progress.PrintStdout(usage); err != nil {
			_, _ = progress.PrintfStderr("goeris: write help: %v\n", err)
			return 1
		}
		return 0
	}
	if err != nil {
		_, _ = progress.PrintfStderr("goeris: %v\n", err)
		_, _ = progress.PrintStdout(usage)
		return 2
	}

	stores, err := paths.open()
	if err != nil {
		_, _ = progress.PrintfStderr("goeris: %v\n", err)
		return 1
	}
	defer func() {
		if err := stores.Close(); err != nil {
			_, _ = progress.PrintfStderr("goeris: close stores: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}()

	switch command {
	case "dump":
		if err := dump(progress, stores.DefaultSetup(), paths); err != nil {
			_, _ = progress.PrintfStderr("goeris: dump: %v\n", err)
			return 1
		}
	}
	return 0
}

// parseCommand reads common flags on both sides of the command name. Both
// passes use the same flag values so repeated options keep their input order.
// flag.ErrHelp requests help without opening any stores.
func parseCommand(args []string) (string, storageFlags, error) {
	var paths storageFlags
	flags := flag.NewFlagSet("goeris", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	paths.register(flags)
	if err := flags.Parse(args); err != nil {
		return "", paths, err
	}
	args = flags.Args()
	if len(args) == 0 || args[0] == "help" {
		return "", paths, flag.ErrHelp
	}
	command := args[0]
	if command != "dump" {
		return "", paths, fmt.Errorf("unknown command %q", command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return "", paths, fmt.Errorf("%s: %w", command, err)
	}
	if flags.NArg() > 0 {
		return "", paths, fmt.Errorf("%s: unexpected arguments: %q", command, flags.Args())
	}
	return command, paths, nil
}
