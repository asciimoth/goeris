package fs

import (
	"context"
	"io"
	stdfs "io/fs"
	"math"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
)

// Builder creates an ERIS-FS image from readers and explicit metadata. It does
// not enumerate or read a host file system. A Builder is not safe for
// concurrent use. Storage operations pass ctx unchanged. Cancellation cannot
// stop a blocked input Read.
type Builder struct {
	store   Store
	secret  eris.Secret
	entries []Entry
	paths   builderPath
}

// Store path components once. A nil child marks a complete entry.
type builderPath map[string]*builderDirectory

type builderDirectory struct {
	name     string
	children builderPath
}

// NewBuilder returns a builder that writes files and indexes to content.
// It keeps a copy of options. File writes preserve the supplied Root flag;
// the encoder always sets Root on the index root. An explicit Root=true is
// preserved on all writes. Other options pass to every block write.
//
// For repeated operations, use [Store.NewBuilder].
func NewBuilder(content eris.CapabilityStore, secret eris.Secret, options eris.BlockOptions) *Builder {
	return &Builder{store: Store{content: content, options: options}, secret: secret}
}

// NewBuilder starts a v2 image. File writes preserve the caller's Root flag.
// Finish marks the image index root.
func (s *Store) NewBuilder(secret eris.Secret) (ImageBuilder, error) {
	if s.content == nil {
		return nil, ErrInvalidEntry
	}
	return &Builder{store: *s, secret: secret}, nil
}

// CheckPath reports whether a new entry can use path in the current index.
// It reads no content and does not reserve the path.
func (builder *Builder) CheckPath(path string) error {
	if builder == nil || builder.store.content == nil {
		return ErrInvalidEntry
	}
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	_, _, err = builder.insertionPoint(components)
	return err
}

// AddFile reads and encodes one regular file with the recommended block size.
// It buffers at most 16 KiB while it selects that size. Set executable to mark
// the file as executable. A failed call does not add an index entry, but blocks
// already accepted by the sink can remain there.
func (builder *Builder) AddFile(ctx context.Context, path string, content io.Reader, executable bool) error {
	if builder == nil || builder.store.content == nil || content == nil {
		return ErrInvalidEntry
	}
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	if _, _, err = builder.insertionPoint(components); err != nil {
		return err
	}
	counted := &countReader{reader: content}
	options := eris.EncodeOptions{BlockOptions: builder.store.options, PreserveRoot: true}
	capability, err := builder.store.content.EncodeContentWithOptions(ctx, counted, builder.secret, options)
	if err != nil {
		return err
	}
	kind := Regular
	if executable {
		kind = Executable
	}
	return builder.add(path, Entry{Path: components, Type: kind, Capability: capability, Size: counted.size})
}

// AddLzipFile stores an existing Lzip stream. Size is the uncompressed length.
// The stream is not decompressed or checked here; Open checks it when read.
// Use AddFile for files that need random access. A failed call can leave blocks
// in the sink but does not add an entry.
func (builder *Builder) AddLzipFile(ctx context.Context, path string, content io.Reader, size int64, executable bool) error {
	if size < 0 {
		return ErrInvalidEntry
	}
	if err := builder.AddFile(ctx, path, content, executable); err != nil {
		return err
	}
	entry := &builder.entries[len(builder.entries)-1]
	entry.Size = size
	entry.Compression = Lzip
	return nil
}

// AddCompressedFile writes a deterministic Lzip stream with an 8 MiB
// dictionary, then stores that stream as a version 2 file entry. It reads the
// source once and uses bounded memory. The ERIS block size is chosen from the
// compressed size. A failed call does not add an entry, though the sink can
// retain blocks from the incomplete stream. The ERIS-FS recommendation advises
// compression for small or sequentially read files of at least 1 KiB; the
// caller selects whether to compress each file.
func (builder *Builder) AddCompressedFile(ctx context.Context, path string, content io.Reader, executable bool) error {
	if content == nil {
		return ErrInvalidEntry
	}
	if err := builder.CheckPath(path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	compressCtx, stop := context.WithCancel(ctx)
	input, output := io.Pipe()
	done := make(chan int64, 1)
	go func() {
		size, err := writeLzip(compressCtx, output, content)
		_ = output.CloseWithError(err)
		done <- size
	}()
	err := func() (err error) {
		// The store can stop before the compressor next writes to the pipe.
		// Cancel further input reads and release any pipe write, even on panic.
		defer func() {
			stop()
			_ = input.CloseWithError(err)
		}()
		return builder.AddLzipFile(ctx, path, input, 0, executable)
	}()
	size := <-done
	if err != nil {
		return err
	}
	// A compressor error closes the pipe with that error, so AddLzipFile
	// cannot succeed unless the compressor also succeeded.
	builder.entries[len(builder.entries)-1].Size = size
	return nil
}

type countReader struct {
	reader io.Reader
	size   int64
}

func (reader *countReader) Read(p []byte) (int, error) {
	n, err := reader.reader.Read(p)
	if int64(n) > math.MaxInt64-reader.size {
		return 0, ErrInvalidEntry
	}
	reader.size += int64(n)
	return n, err
}

// AddSymlink stores the target verbatim, including absolute and dangling targets.
// Image lookup enforces the image boundary. Use [FS.ReadLink] to get a target
// for a native mount adapter.
func (builder *Builder) AddSymlink(path, target string) error {
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	return builder.add(path, Entry{Path: components, Type: Symlink, Target: target})
}

// AddSubdirectory adds a nested ERIS-FS image at path.
func (builder *Builder) AddSubdirectory(path string, capability eris.Capability) error {
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	return builder.add(path, Entry{Path: components, Type: Subdirectory, Capability: capability})
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
func (builder *Builder) Finish(ctx context.Context) (eris.Capability, error) {
	if builder == nil || builder.store.content == nil {
		return eris.Capability{}, ErrInvalidEntry
	}
	return builder.store.EncodeIndex(ctx, Index{Entries: builder.entries}, builder.secret)
}

func (builder *Builder) add(path string, entry Entry) error {
	if builder == nil || builder.store.content == nil {
		return ErrInvalidEntry
	}
	if err := validateEntry(entry); err != nil {
		return err
	}
	parent, suffix, err := builder.insertionPoint(entry.Path)
	if err != nil {
		return err
	}
	for _, component := range entry.Path[:len(entry.Path)-len(suffix)] {
		path = path[len(component)+1:]
	}
	ownPathComponents(path, suffix)
	entry.Target = strings.Clone(entry.Target)
	builder.entries = append(builder.entries, entry)
	if parent == nil {
		builder.paths = make(builderPath)
		parent = builder.paths
	}
	parent.addSuffix(suffix)
	return nil
}

// Copy input storage in small groups of complete path components. A snapshot's
// leaf name can then share a group without keeping a large parent name alive.
func ownPathComponents(path string, components []string) {
	for len(components) > 0 {
		count, size := 1, len(components[0])
		for count < len(components) && size+1+len(components[count]) <= maxBatchedTextBytes {
			size += 1 + len(components[count])
			count++
		}
		i := 0
		for component := range strings.SplitSeq(strings.Clone(path[:size]), "/") {
			components[i] = component
			i++
		}
		components = components[count:]
		if len(components) > 0 {
			path = path[size+1:]
		}
	}
}

// insertionPoint returns the existing parent and the missing path components.
// It replaces the existing prefix with the builder's copies of those names.
func (builder *Builder) insertionPoint(components []string) (builderPath, []string, error) {
	current := builder.paths
	for i, component := range components {
		next, exists := current[component]
		if !exists {
			return current, components[i:], nil
		}
		if next == nil {
			return nil, nil, ErrPathConflict
		}
		components[i] = next.name
		current = next.children
	}
	return nil, nil, ErrPathConflict
}

func (current builderPath) addSuffix(components []string) {
	for _, component := range components[:len(components)-1] {
		next := make(builderPath)
		current[component] = &builderDirectory{name: component, children: next}
		current = next
	}
	current[components[len(components)-1]] = nil
}

func splitPath(path string) ([]string, error) {
	if !stdfs.ValidPath(path) || path == "." {
		return nil, ErrInvalidPath
	}
	return strings.Split(path, "/"), nil
}
