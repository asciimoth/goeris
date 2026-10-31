package fs

import (
	"bytes"
	"errors"
	"io"
	stdfs "io/fs"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/eris-tools/eris"
)

// Builder creates an ERIS-FS image from readers and explicit metadata. It does
// not enumerate or read a host file system. A Builder is not safe for
// concurrent use.
type Builder struct {
	sink     eris.BlockSink
	secret   eris.Secret
	entries  []Entry
	paths    map[string]struct{}
	prefixes map[string]int
}

// NewBuilder returns a builder that writes all file and index blocks to sink.
func NewBuilder(sink eris.BlockSink, secret eris.Secret) *Builder {
	return &Builder{
		sink: sink, secret: secret,
		paths: make(map[string]struct{}), prefixes: make(map[string]int),
	}
}

// AddFile reads and encodes one regular file with the recommended block size.
// It buffers at most 16 KiB while it selects that size. Set executable to mark
// the file as executable. A failed call does not add an index entry, but blocks
// already accepted by the sink can remain there.
func (builder *Builder) AddFile(path string, content io.Reader, executable bool) error {
	if builder == nil || builder.sink == nil || content == nil {
		return ErrInvalidEntry
	}
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	if err = builder.checkPath(components); err != nil {
		return err
	}
	prefix := make([]byte, 1<<14)
	n, readErr := io.ReadFull(content, prefix)
	var input io.Reader
	var blockSize eris.BlockSize
	switch {
	case readErr == nil:
		blockSize = eris.BlockSize32KiB
		input = io.MultiReader(bytes.NewReader(prefix), content)
	case errors.Is(readErr, io.EOF), errors.Is(readErr, io.ErrUnexpectedEOF):
		blockSize = eris.BlockSize1KiB
		input = bytes.NewReader(prefix[:n])
	default:
		return readErr
	}
	capability, err := EncodeContent(input, builder.secret, blockSize, builder.sink)
	if err != nil {
		return err
	}
	kind := Regular
	if executable {
		kind = Executable
	}
	return builder.add(Entry{Path: components, Type: kind, Capability: capability})
}

// AddSymlink adds a symbolic link. The target is stored verbatim and can point
// outside the image. Applications must apply their own link safety policy.
func (builder *Builder) AddSymlink(path, target string) error {
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	return builder.add(Entry{Path: components, Type: Symlink, Target: target})
}

// AddSubdirectory adds a nested ERIS-FS image at path.
func (builder *Builder) AddSubdirectory(path string, capability eris.Capability) error {
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	return builder.add(Entry{Path: components, Type: Subdirectory, Capability: capability})
}

// Index returns an independent snapshot of the current index.
func (builder *Builder) Index() Index {
	if builder == nil {
		return Index{}
	}
	entries := make([]Entry, len(builder.entries))
	copy(entries, builder.entries)
	for i := range entries {
		entries[i].Path = slices.Clone(entries[i].Path)
	}
	return Index{Entries: entries}
}

// Finish encodes the index and returns its ERIS read capability.
func (builder *Builder) Finish() (eris.Capability, error) {
	if builder == nil || builder.sink == nil {
		return eris.Capability{}, ErrInvalidEntry
	}
	return EncodeIndex(builder.Index(), builder.secret, builder.sink)
}

func (builder *Builder) add(entry Entry) error {
	if builder == nil || builder.sink == nil {
		return ErrInvalidEntry
	}
	if err := validateEntry(entry); err != nil {
		return err
	}
	if err := builder.checkPath(entry.Path); err != nil {
		return err
	}
	builder.entries = append(builder.entries, entry)
	builder.recordPath(entry.Path)
	return nil
}

func (builder *Builder) checkPath(components []string) error {
	if builder == nil || builder.sink == nil {
		return ErrInvalidEntry
	}
	if builder.paths == nil {
		builder.paths = make(map[string]struct{})
		builder.prefixes = make(map[string]int)
	}
	candidate := strings.Join(components, "/")
	if _, exists := builder.paths[candidate]; exists || builder.prefixes[candidate] != 0 {
		return ErrPathConflict
	}
	for i := 1; i < len(components); i++ {
		if _, exists := builder.paths[strings.Join(components[:i], "/")]; exists {
			return ErrPathConflict
		}
	}
	return nil
}

func (builder *Builder) recordPath(components []string) {
	builder.paths[strings.Join(components, "/")] = struct{}{}
	for i := 1; i < len(components); i++ {
		builder.prefixes[strings.Join(components[:i], "/")]++
	}
}

func splitPath(path string) ([]string, error) {
	if !stdfs.ValidPath(path) || path == "." || !utf8.ValidString(path) {
		return nil, ErrInvalidPath
	}
	return strings.Split(path, "/"), nil
}
