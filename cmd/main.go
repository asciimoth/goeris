// Command goeris provides a command-line interface for the Go ERIS library.
package main

import (
	"fmt"
	"os"
)

const usage = `Usage: goeris [--help]

  -h, --help  Show help.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
		fmt.Print(usage)
		return
	}

	fmt.Fprintf(os.Stderr, "goeris: unexpected arguments: %q\n%s", args, usage)
	os.Exit(2)
}
