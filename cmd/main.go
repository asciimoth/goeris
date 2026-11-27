// Command goeris provides a command-line interface for the Go ERIS library.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/asciimoth/goeris/auto"
)

const usage = `Usage: goeris [command]

  dump              Show environment settings and the current storage setup.
  help, -h, --help  Show help.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) (code int) {
	if wantsHelp(args) {
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	}

	stores, err := auto.Open()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "goeris: %v\n", err)
		return 1
	}
	defer func() {
		if err := stores.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "goeris: close stores: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}()

	switch args[0] {
	case "dump":
		if len(args) != 1 {
			_, _ = fmt.Fprintf(stderr, "goeris: dump: unexpected arguments: %q\n", args[1:])
			break
		}
		if err := dump(stdout, stores.DefaultSetup()); err != nil {
			_, _ = fmt.Fprintf(stderr, "goeris: dump: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "goeris: unknown command %q\n", args[0])
	}
	_, _ = fmt.Fprint(stdout, usage)
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
