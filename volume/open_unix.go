//go:build unix

package volume

func directoryOpenPath(name string) string {
	// Keep the final dot component: it requires a directory even if the
	// path is replaced with a FIFO after Stat. filepath.Join removes it.
	return name + "/."
}
