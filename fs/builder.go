package fs

import (
	"context"
	"io"
	stdfs "io/fs"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

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

// Store path components once. Storing every joined prefix would use quadratic
// memory and processing time for a deeply nested path from an untrusted caller.
type builderPath struct {
	children map[string]*builderPath
	entry    bool
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
	return builder.checkPath(components)
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
	if err = builder.checkPath(components); err != nil {
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
	return builder.add(Entry{Path: components, Type: kind, Capability: capability, Size: counted.size})
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
	input, output := io.Pipe()
	done := make(chan int64, 1)
	go func() {
		size, err := writeLzip(ctx, output, content)
		_ = output.CloseWithError(err)
		done <- size
	}()
	err := builder.AddLzipFile(ctx, path, input, 0, executable)
	_ = input.CloseWithError(err)
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
func (builder *Builder) Finish(ctx context.Context) (eris.Capability, error) {
	if builder == nil || builder.store.content == nil {
		return eris.Capability{}, ErrInvalidEntry
	}
	return builder.store.EncodeIndex(ctx, builder.Index(), builder.secret)
}

func (builder *Builder) add(entry Entry) error {
	if builder == nil || builder.store.content == nil {
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
	current := &builder.paths
	for _, component := range components {
		if current.entry {
			return ErrPathConflict
		}
		current = current.children[component]
		if current == nil {
			return nil
		}
	}
	// This path is already an entry or a parent of an existing entry.
	return ErrPathConflict
}

func (builder *Builder) recordPath(components []string) {
	current := &builder.paths
	for _, component := range components {
		if current.children == nil {
			current.children = make(map[string]*builderPath)
		}
		next := current.children[component]
		if next == nil {
			next = &builderPath{}
			current.children[component] = next
		}
		current = next
	}
	current.entry = true
}

func splitPath(path string) ([]string, error) {
	if !stdfs.ValidPath(path) || path == "." || !utf8.ValidString(path) {
		return nil, ErrInvalidPath
	}
	return strings.Split(path, "/"), nil
}
