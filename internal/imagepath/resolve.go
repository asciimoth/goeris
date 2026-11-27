// Package imagepath resolves symlinks within read-only image views.
package imagepath

import (
	"errors"
	stdfs "io/fs"
	"path"
	"strings"
)

// ErrMissingEntry marks a missing directory entry. A failure to load the
// directory itself must retain its original error.
var ErrMissingEntry = errors.New("eris-fs: missing path component")

// Kind describes how a component participates in path resolution.
type Kind int

const (
	File Kind = iota
	Directory
	Link
	ImageRoot // A directory with its own boundary for links.
)

// Mode selects how Resolve handles the final link and which path it returns.
type Mode int

const (
	Follow Mode = iota
	KeepFinal
	Export
	Canonical // Follow links and return the resolved path from root.
)

// Resolve walks slash-separated components in order. The child lookup
// returns ErrMissingEntry for a missing entry and preserves backend failures.
func Resolve[T comparable](root T, name string, mode Mode, child func(T, string) (T, Kind, string, error)) (T, string, error) {
	// Keep one unprocessed string per followed link. Splitting and copying
	// all target components can multiply memory use when a link points to
	// itself followed by a long suffix, even with the 40-link limit.
	type directoryLink struct {
		parent T
		floor  int
	}
	type linkPath struct {
		path string
		root int // Depth of the image that contains this link.
		via  *directoryLink
	}
	pending := []linkPath{{path: name}}
	var zero T
	var components []string
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
		kind  Kind
		root  int
	}
	// Keep shallow paths on the stack. Deeper paths grow as needed.
	var local [8]step
	ancestors := append(local[:0], step{root, Directory, 0})
	links := 0
	exportFloor := 0
	for len(pending) > 0 {
		current := ancestors[len(ancestors)-1]
		if current.kind != Directory && current.kind != ImageRoot {
			return missing()
		}
		last := len(pending) - 1
		frame := pending[last]
		if frame.via != nil {
			// A client can clean a target relative to the spelling it used
			// for this directory. A parent step can remove the directory
			// link only if both spellings lead to the same parent.
			exportFloor = frame.via.floor
			if len(ancestors) < 2 || ancestors[len(ancestors)-2].value != frame.via.parent {
				if climbsAbove(frame.path, 0) {
					return zero, "", stdfs.ErrNotExist
				}
				exportFloor = len(ancestors) - 1
			}
			pending[last].via = nil
		}
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
			if mode == Canonical {
				components = components[:len(components)-1]
			}
			continue
		}
		next, kind, target, err := child(current.value, component)
		if err == ErrMissingEntry {
			return missing()
		}
		if err != nil {
			return zero, "", err
		}
		if kind != Link || (len(pending) == 0 && mode == KeepFinal) {
			boundary := current.root
			if kind == ImageRoot {
				boundary = len(ancestors)
			}
			ancestors = append(ancestors, step{next, kind, boundary})
			if mode == Canonical {
				components = append(components, component)
			}
			continue
		}
		if links == 40 {
			return zero, "", stdfs.ErrInvalid
		}
		links++
		if mode == Export && len(pending) == 0 && !haveTarget {
			exported, haveTarget = target, true
		}
		if target == "" {
			return missing()
		}
		if strings.HasPrefix(target, "/") {
			return zero, "", stdfs.ErrNotExist
		}
		if mode == Export {
			// Cleaning the rest of this target could erase this directory
			// link and make the client continue in a different directory.
			if more {
				pending[last].via = &directoryLink{current.value, exportFloor}
			}
			if climbsAbove(target, len(ancestors)-1-max(current.root, exportFloor)) {
				return zero, "", stdfs.ErrNotExist
			}
			if err := CheckNative(target); err != nil {
				return zero, "", err
			}
		}
		// Resolve each target component before processing its parent segments.
		// Cleaning the text first can select a different file through a link.
		// Each nested index is an image. Its links have the same boundary
		// here as when the index is opened by its own capability.
		pending = append(pending, linkPath{path: target, root: current.root})
	}
	if mode == Canonical {
		resolved := "."
		if len(components) > 0 {
			resolved = strings.Join(components, "/")
		}
		return ancestors[len(ancestors)-1].value, resolved, nil
	}
	if mode == Export && !haveTarget {
		return zero, "", stdfs.ErrInvalid
	}
	return ancestors[len(ancestors)-1].value, exported, nil
}

// CheckNative checks that a path has the same components on native clients.
// A mount can serve clients on a different operating system.
func CheckNative(name string) error {
	// Plan 9 treats a leading '#' as a device path. A client can remove
	// leading parent steps when it joins the target to the link's directory.
	if strings.Contains(name, "#") {
		clean := path.Clean(name)
		for strings.HasPrefix(clean, "../") {
			clean = clean[3:]
		}
		if name[0] == '#' || strings.HasPrefix(clean, "#") {
			return stdfs.ErrInvalid
		}
	}
	for part := range strings.SplitSeq(name, "/") {
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
