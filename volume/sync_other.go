//go:build !unix

package volume

import "os"

// Go does not provide a portable directory-sync operation on these platforms.
// File sync and same-directory rename still apply.
func syncDirectory(_ *os.Root, _ string) error {
	return nil
}
