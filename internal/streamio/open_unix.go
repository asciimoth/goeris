//go:build unix

package streamio

import (
	"os"
	"syscall"
)

// ReadFileFlags opens files for reading without waiting for a FIFO writer.
// Check the opened file type before reading.
const ReadFileFlags = os.O_RDONLY | syscall.O_NONBLOCK
