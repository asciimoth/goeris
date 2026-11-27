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

const usage = `Usage: goeris [storage flags] command [options] [arguments]

  block put           Store one 1 KiB or 32 KiB block from stdin. Print its hash.
  block get [HASH...]  Write raw blocks to stdout in the order given.
  dump                Show environment settings and the current storage setup.
  help, -h, --help    Show help.

Block get accepts --batch-size N before the hashes. Use 0 for automatic
batching (the default) or 1 for one request at a time.

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
	command, paths, parseErr := parseCommand(args)
	var progress *zpgo.Progress
	var err error
	if command.name == "block put" || command.name == "block get" {
		progress, err = blockProgress(len(command.hashes))
	} else {
		progress, err = zpgo.NewAuto()
	}
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
	if errors.Is(parseErr, flag.ErrHelp) {
		if _, err := progress.PrintStdout(usage); err != nil {
			_, _ = progress.PrintfStderr("goeris: write help: %v\n", err)
			return 1
		}
		return 0
	}
	if parseErr != nil {
		_, _ = progress.PrintfStderr("goeris: %v\n", parseErr)
		if command.name == "" || command.name == "dump" {
			_, _ = progress.PrintStdout(usage)
		}
		return 2
	}
	if command.name == "block get" && len(command.hashes) == 0 {
		return 0
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

	switch command.name {
	case "dump":
		err = dump(progress, stores.DefaultSetup(), paths)
	case "block put":
		err = blockPut(progress, stores.Blocks, os.Stdin)
	case "block get":
		err = blockGet(progress, stores.Blocks, command.hashes, command.batchSize)
	}
	if err != nil {
		_, _ = progress.PrintfStderr("goeris: %s: %v\n", command.name, err)
		return 1
	}
	return 0
}

type commandOptions struct {
	name      string
	hashes    []string
	batchSize int
}

// parseCommand reads common flags before and after each command name, up to
// the first positional argument. Reusing the flag set preserves flag order.
// flag.ErrHelp requests help without opening any stores.
func parseCommand(args []string) (commandOptions, storageFlags, error) {
	var command commandOptions
	var paths storageFlags
	flags := flag.NewFlagSet("goeris", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	paths.register(flags)
	if err := flags.Parse(args); err != nil {
		return command, paths, err
	}
	args = flags.Args()
	if len(args) == 0 || args[0] == "help" {
		return command, paths, flag.ErrHelp
	}
	switch args[0] {
	case "block":
		command, err := parseBlockCommand(flags, args[1:])
		return command, paths, err
	case "dump":
		command.name = "dump"
	default:
		return command, paths, fmt.Errorf("unknown command %q", args[0])
	}
	if err := flags.Parse(args[1:]); err != nil {
		return command, paths, fmt.Errorf("dump: %w", err)
	}
	if flags.NArg() > 0 {
		return command, paths, fmt.Errorf("dump: unexpected arguments: %q", flags.Args())
	}
	return command, paths, nil
}
