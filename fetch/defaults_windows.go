package fetch

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func defaultFiles() []string {
	var paths []string
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_RoamingAppData, windows.FOLDERID_ProgramData} {
		if p, err := windows.KnownFolderPath(id, 0); err == nil && absolute(p) {
			paths = append(paths, filepath.Join(p, "ERIS", "fetch.conf"))
		}
	}
	return paths
}
