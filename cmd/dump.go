package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/asciimoth/goeris/auto"
	"github.com/asciimoth/zpgo"
)

const dumpUsage = `Usage: goeris dump [storage flags]

Show the environment settings and the lookup order for block volumes,
capability mounts, and ERIS-FS images.
` + storageHelp

// dump reports the environment and the lookup order of the configured stores.
func dump(progress *zpgo.Progress, setup auto.DefaultSetup, paths storageFlags) error {
	var out bytes.Buffer
	out.WriteString("Environment\n")
	for _, key := range []string{
		"ERIS_BLOCK_PATH",
		"ERIS_FETCH",
		"ERIS_FS_MNT",
		"ERIS_MNT",
		"HOME",
		"LOCALAPPDATA",
		"ProgramData",
		"PWD",
		"XDG_CACHE_HOME",
		"XDG_CONFIG_DIRS",
		"XDG_CONFIG_HOME",
	} {
		if value, ok := os.LookupEnv(key); ok {
			_, _ = fmt.Fprintf(&out, "  %s=%q\n", key, value)
		} else {
			_, _ = fmt.Fprintf(&out, "  %s is unset\n", key)
		}
	}

	out.WriteString("\nStorage lookup order\n")
	_, _ = fmt.Fprintf(&out, "  Block volumes %q\n", paths.blocks.lookupOrder(setup.BlockVolumes))
	out.WriteString("  Capabilities\n")
	dumpMounts(&out, paths.capabilities, setup.CapabilityMountRoots, setup.CapabilitiesFromBlocks, "Block store")
	out.WriteString("  ERIS-FS images\n")
	dumpMounts(&out, paths.images, setup.ImageMountRoots, setup.ImagesFromCapabilities, "Capability store")

	_, err := progress.WriteStdout(out.Bytes())
	return err
}

// dumpMounts groups consecutive mount roots. A derived backend follows the
// prepended mounts and precedes the default mounts.
func dumpMounts(out *bytes.Buffer, paths storePaths, defaults []string, hasSource bool, source string) {
	if hasSource {
		if len(paths.before) > 0 {
			_, _ = fmt.Fprintf(out, "    Mount roots %q\n", paths.before)
		}
		_, _ = fmt.Fprintf(out, "    %s\n", source)
		paths.before = nil
	}
	_, _ = fmt.Fprintf(out, "    Mount roots %q\n", paths.lookupOrder(defaults))
}
