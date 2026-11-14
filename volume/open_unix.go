//go:build unix

package volume

import "syscall"

// Do not wait for a writer if a file is replaced with a FIFO between Stat and
// OpenFile. Callers check the opened file type before they read any bytes.
const readOpenFlags = syscall.O_NONBLOCK

func directoryOpenPath(name string) string {
	// Keep the final dot component: it requires a directory even if the
	// path is replaced with a FIFO after Stat. filepath.Join removes it.
	return name + "/."
}
