//go:build !unix

package streamio

import "os"

// ReadFileFlags opens files for reading on systems without Unix FIFOs.
const ReadFileFlags = os.O_RDONLY
