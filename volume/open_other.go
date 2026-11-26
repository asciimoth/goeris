//go:build !unix

package volume

func directoryOpenPath(name string) string {
	// In particular, do not append a separator to a Windows drive-relative
	// path such as C:. That would select the drive root instead.
	return name
}
