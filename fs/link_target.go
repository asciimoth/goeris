package fs

import (
	"errors"
	stdfs "io/fs"
	"path"
	"strings"
)

// A missing image entry is distinct from a backend failure to load its index.
var errDangling = errors.New("eris-fs: missing path component")

type linkLookup int

const (
	followLinks linkLookup = iota
	keepFinalLink
	exportFinalLink
)

// resolveLinks walks slash-separated components in order. The child lookup
// returns errDangling for a missing entry and preserves backend failures.
func resolveLinks[T any](root T, name string, mode linkLookup, child func(T, string) (T, EntryType, string, error)) (T, string, error) {
	// Keep one unprocessed string per followed link. Splitting and copying
	// all target components can multiply memory use when a link points to
	// itself followed by a long suffix, even with the 40-link limit.
	type linkPath struct {
		path string
		root int // Depth of the image that contains this link.
	}
	pending := []linkPath{{name, 0}}
	var zero T
	var exported string
	haveTarget := false
	missing := func() (T, string, error) {
		if !haveTarget {
			return zero, "", stdfs.ErrNotExist
		}
		// A dangling target is safe only after every parent step is resolved.
		for _, frame := range pending {
			for part := range strings.SplitSeq(frame.path, "/") {
				if part == ".." {
					return zero, "", stdfs.ErrNotExist
				}
			}
		}
		return zero, exported, nil
	}
	type step struct {
		value T
		kind  EntryType
		root  int
	}
	ancestors := []step{{root, directoryType, 0}}
	links := 0
	for len(pending) > 0 {
		current := ancestors[len(ancestors)-1]
		if current.kind != directoryType && current.kind != Subdirectory {
			return missing()
		}
		last := len(pending) - 1
		frame := pending[last]
		component, rest, more := strings.Cut(frame.path, "/")
		if more {
			pending[last].path = rest
		} else {
			pending = pending[:last]
		}
		switch component {
		case "", ".":
			continue
		case "..":
			if len(ancestors)-1 == frame.root {
				return zero, "", stdfs.ErrNotExist
			}
			ancestors = ancestors[:len(ancestors)-1]
			continue
		}
		next, kind, target, err := child(current.value, component)
		if err == errDangling {
			return missing()
		}
		if err != nil {
			return zero, "", err
		}
		if kind != Symlink || (len(pending) == 0 && mode == keepFinalLink) {
			boundary := current.root
			if kind == Subdirectory {
				boundary = len(ancestors)
			}
			ancestors = append(ancestors, step{next, kind, boundary})
			continue
		}
		if links == 40 {
			return zero, "", stdfs.ErrInvalid
		}
		links++
		if mode == exportFinalLink && len(pending) == 0 && !haveTarget {
			exported, haveTarget = target, true
		}
		if target == "" {
			return missing()
		}
		if strings.HasPrefix(target, "/") {
			return zero, "", stdfs.ErrNotExist
		}
		if mode == exportFinalLink {
			// Cleaning the rest of this target could erase this directory
			// link and make the client continue in a different directory.
			if more && climbsAbove(rest, 0) {
				return zero, "", stdfs.ErrNotExist
			}
			if err := checkNativeLinkTarget(target, len(ancestors)-1-current.root); err != nil {
				return zero, "", err
			}
		}
		// Resolve each target component before processing its parent segments.
		// Cleaning the text first can select a different file through a link.
		// Each nested index is an image. Its links have the same boundary
		// here as when the index is opened by its own capability.
		pending = append(pending, linkPath{target, current.root})
	}
	if mode == exportFinalLink && !haveTarget {
		return zero, "", stdfs.ErrInvalid
	}
	return ancestors[len(ancestors)-1].value, exported, nil
}

// A mount can serve clients on a different operating system. Check the target
// before exporting it, even when the serving host treats these names literally.
func checkNativeLinkTarget(target string, depth int) error {
	if climbsAbove(target, depth) {
		return stdfs.ErrNotExist
	}
	// Plan 9 treats a leading '#' as a device path.
	if strings.Contains(target, "#") && (target[0] == '#' || strings.HasPrefix(path.Clean(target), "#")) {
		return stdfs.ErrInvalid
	}
	for part := range strings.SplitSeq(target, "/") {
		switch part {
		case "", ".", "..":
			continue
		}
		// Windows can remove trailing dots and spaces during lookup.
		if strings.ContainsAny(part, "\\:\x00") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || windowsDeviceName(part) {
			return stdfs.ErrInvalid
		}
	}
	return nil
}

// climbsAbove reports whether lexical resolution takes a parent step
// above a boundary that starts depth directories above the current one.
func climbsAbove(target string, depth int) bool {
	for part := range strings.SplitSeq(target, "/") {
		switch part {
		case "", ".":
		case "..":
			if depth == 0 {
				return true
			}
			depth--
		default:
			depth++
		}
	}
	return false
}

func windowsDeviceName(name string) bool {
	// A device can also have an extension or spaces before the extension.
	base, _, _ := strings.Cut(name, ".")
	base = strings.TrimRight(base, " ")
	if len(base) < 3 || len(base) > len("CONOUT$") {
		return false
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if !strings.EqualFold(base[:3], "COM") && !strings.EqualFold(base[:3], "LPT") {
		return false
	}
	// Windows also recognizes superscript 1, 2, and 3 as device numbers.
	switch base[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}
