// Command goeris provides a command-line interface for the Go ERIS library.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `Usage: goeris [--help]

  -h, --help  Show help.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	}

	_, _ = fmt.Fprintf(stderr, "goeris: unexpected arguments: %q\n%s", args, usage)
	return 2
}
