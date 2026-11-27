// Command goeris provides a command-line interface for the Go ERIS library.
package main

import (
	"os"

	"github.com/asciimoth/goeris/auto"
	"github.com/asciimoth/zpgo"
)

const usage = `Usage: goeris [command]

  dump              Show environment settings and the current storage setup.
  help, -h, --help  Show help.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) (code int) {
	help := wantsHelp(args)
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
	if help {
		if _, err := progress.PrintStdout(usage); err != nil {
			_, _ = progress.PrintfStderr("goeris: write help: %v\n", err)
			return 1
		}
		return 0
	}

	stores, err := auto.Open()
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

	switch args[0] {
	case "dump":
		if len(args) != 1 {
			_, _ = progress.PrintfStderr("goeris: dump: unexpected arguments: %q\n", args[1:])
			break
		}
		if err := dump(progress, stores.DefaultSetup()); err != nil {
			_, _ = progress.PrintfStderr("goeris: dump: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = progress.PrintfStderr("goeris: unknown command %q\n", args[0])
	}
	_, _ = progress.PrintStdout(usage)
	return 2
}

func wantsHelp(args []string) bool {
	if len(args) == 0 || args[0] == "help" {
		return true
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}
