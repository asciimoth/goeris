package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/asciimoth/goeris/auto"
	"github.com/asciimoth/zpgo"
)

func dump(progress *zpgo.Progress, setup auto.DefaultSetup) error {
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
	_, _ = fmt.Fprintf(&out, "  Block volumes %q\n", setup.BlockVolumes)
	out.WriteString("  Capabilities\n")
	if setup.CapabilitiesFromBlocks {
		out.WriteString("    Block store\n")
	}
	_, _ = fmt.Fprintf(&out, "    Mount roots %q\n", setup.CapabilityMountRoots)
	out.WriteString("  ERIS-FS images\n")
	if setup.ImagesFromCapabilities {
		out.WriteString("    Capability store\n")
	}
	_, _ = fmt.Fprintf(&out, "    Mount roots %q\n", setup.ImageMountRoots)

	_, err := progress.WriteStdout(out.Bytes())
	return err
}
