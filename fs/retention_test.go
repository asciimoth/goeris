package fs

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"
	"weak"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
)

type retentionContextKey struct{}

func TestOpenLocalFileReleasesUncancelableContext(t *testing.T) {
	for _, raw := range []bool{false, true} {
		file, marker := openLocalFileWithUncancelableContext(t, raw)
		runtime.GC()
		if marker.Value() != nil {
			t.Errorf("local file retained an uncancelable context, raw block: %v", raw)
		}
		if _, err := file.Stat(); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}
}

func openLocalFileWithUncancelableContext(t *testing.T, raw bool) (stdfs.File, weak.Pointer[[1024]byte]) {
	t.Helper()
	marker := new([1024]byte)
	ctx := context.WithValue(context.Background(), retentionContextKey{}, marker)
	var file stdfs.File
	var err error
	if raw {
		blocks, _ := mountFixture(t)
		capability := pathTestCapability(t)
		file, err = NewMount(ctx, blocks, eris.BlockOptions{}).Open(capability.Root.Reference.String())
	} else {
		image := &mountedImage{root: t.TempDir(), prefix: "."}
		file, err = image.Open(ctx, ".")
	}
	if err != nil {
		t.Fatal(err)
	}
	return file, weak.Make(marker)
}

func TestClosedFileReleasesContext(t *testing.T) {
	blocks, _ := mountFixture(t)
	image, err := OpenFS(t.Context(), pathTestCapability(t), eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	packed := compressedImage(t, lzipMember(t, []byte("payload")), 7)
	mounted := &mountedImage{root: t.TempDir(), prefix: "."}
	for _, test := range []struct {
		name, path string
		image      FileSystem
	}{
		{"regular", "hello.txt", image},
		{"lzip", "file", packed},
		{"mounted", ".", mounted},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, marker := openAndCloseWithContext(t, test.image, test.path)
			runtime.GC()
			if marker.Value() != nil {
				t.Error("closed handle retained its open context")
			}
			if _, err := file.Stat(); !errors.Is(err, stdfs.ErrClosed) {
				t.Errorf("closed Stat: %v", err)
			}
			runtime.KeepAlive(file)
		})
	}
}

func openAndCloseWithContext(t *testing.T, image FileSystem, name string) (stdfs.File, weak.Pointer[[1024]byte]) {
	t.Helper()
	marker := new([1024]byte)
	ctx := context.WithValue(t.Context(), retentionContextKey{}, marker)
	file, err := image.Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file, weak.Make(marker)
}

func TestClosedFileReleasesCapabilityReader(t *testing.T) {
	capability := pathTestCapability(t)
	for _, test := range []struct {
		name        string
		content     string
		compression Compression
	}{
		{"regular", "payload", Uncompressed},
		{"lzip", string(lzipMember(t, []byte("payload"))), Lzip},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := (Index{Entries: []Entry{{
				Path: []string{"file"}, Capability: capability, Size: 7, Compression: test.compression,
			}}}).MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			store := &imageResultStore{index: data}
			image, err := OpenFS(t.Context(), capability, store, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			file, reader := openAndCloseWithReader(t, image, store, test.content)
			runtime.GC()
			if reader.Value() != nil {
				t.Error("closed handle retained its capability reader")
			}
			runtime.KeepAlive(file)
		})
	}
}

func openAndCloseWithReader(t *testing.T, image *FS, store *imageResultStore, content string) (stdfs.File, weak.Pointer[imageResultReader]) {
	t.Helper()
	reader := &imageResultReader{Reader: strings.NewReader(content), size: int64(len(content))}
	store.reader = reader
	file, err := image.Open(t.Context(), "file")
	store.reader = nil
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if !reader.closed {
		t.Fatal("capability reader was left open")
	}
	return file, weak.Make(reader)
}

func TestLzipTerminalErrorReleasesDecoder(t *testing.T) {
	content := []byte("payload")
	member := lzipMember(t, content)
	member[len(member)-20] ^= 1 // Break the CRC.
	image := compressedImage(t, member, int64(len(content)))
	opened, err := image.Open(t.Context(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if _, err := io.ReadAll(opened); !errors.Is(err, ErrInvalidCompression) {
		t.Fatalf("ReadAll: %v", err)
	}
	file := opened.(*lzipFile)
	if file.decoder != nil || file.input.reader != nil {
		t.Error("terminal read error retained the decoder or its input buffer")
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, ErrInvalidCompression) {
		t.Fatalf("read after decoder failure: %v", err)
	}
	if info, err := file.Stat(); err != nil || info.Size() != int64(len(content)) {
		t.Fatalf("Stat after decoder failure: %v, %v", info, err)
	}
}

func TestLzipReleasesCanceledContext(t *testing.T) {
	for _, operation := range []string{"read", "stat", "stat after EOF"} {
		t.Run(operation, func(t *testing.T) {
			file, marker := observeLzipCancellation(t, operation)
			runtime.GC()
			if marker.Value() != nil {
				t.Error("compressed file retained its canceled context")
			}
			if _, err := file.Read(nil); !errors.Is(err, context.Canceled) {
				t.Errorf("later Read: %v", err)
			}
			if _, err := file.Stat(); !errors.Is(err, context.Canceled) {
				t.Errorf("later Stat: %v", err)
			}
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestLzipTerminalReadReleasesUncancelableContext(t *testing.T) {
	for _, broken := range []bool{false, true} {
		file, marker := openTerminalLzipWithUncancelableContext(t, broken)
		runtime.GC()
		if marker.Value() != nil {
			t.Errorf("terminal compressed file retained an uncancelable context, broken stream: %v", broken)
		}
		if info, err := file.Stat(); err != nil || info.Size() != 4 {
			t.Errorf("Stat: %v, %v", info, err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}
}

func openTerminalLzipWithUncancelableContext(t *testing.T, broken bool) (stdfs.File, weak.Pointer[[1024]byte]) {
	t.Helper()
	member := lzipMember(t, []byte("data"))
	want := io.EOF
	if broken {
		member[len(member)-20] ^= 1
		want = ErrInvalidCompression
	}
	image := compressedImage(t, member, 4)
	marker := new([1024]byte)
	ctx := context.WithValue(context.Background(), retentionContextKey{}, marker)
	file, err := image.Open(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Read(make([]byte, 4)); !errors.Is(err, want) {
		t.Fatalf("terminal Read: %v, want %v", err, want)
	}
	return file, weak.Make(marker)
}

func observeLzipCancellation(t *testing.T, operation string) (stdfs.File, weak.Pointer[[1024]byte]) {
	t.Helper()
	image := compressedImage(t, lzipMember(t, []byte("data")), 4)
	marker := new([1024]byte)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), retentionContextKey{}, marker))
	defer cancel()
	file, err := image.Open(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if operation == "stat after EOF" {
		if _, err := io.Copy(io.Discard, file); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	if operation == "read" {
		_, err = file.Read(make([]byte, 1))
	} else {
		_, err = file.Stat()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%s: %v", operation, err)
	}
	return file, weak.Make(marker)
}

func TestLzipTerminalReadReleasesCapabilityReader(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"EOF", nil},
		{"invalid CRC", ErrInvalidCompression},
		{"canceled context", context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, reader, store, closes := readLzipToTerminal(t, test.err)
			runtime.GC()
			if reader.Value() != nil || *closes != 1 {
				t.Errorf("terminal read retained the capability reader or left it open (%d closes)", *closes)
			}
			if store.Value() != nil {
				t.Error("terminal read retained the capability store")
			}
			want := test.err
			if want == nil {
				want = io.EOF
			}
			if _, err := file.Read(make([]byte, 1)); !errors.Is(err, want) {
				t.Errorf("later Read: %v, want %v", err, want)
			}
			if err := file.Close(); !errors.Is(err, errTestReaderClose) {
				t.Errorf("Close: %v", err)
			}
			if err := file.Close(); err != nil || *closes != 1 {
				t.Errorf("repeated Close: %v, %d backend closes", err, *closes)
			}
		})
	}
}

var errTestReaderClose = errors.New("test reader close")

type closeProbeReader struct {
	imageResultReader
	closes *int
}

func (r *closeProbeReader) Close() error {
	(*r.closes)++
	return errTestReaderClose
}

func readLzipToTerminal(t *testing.T, want error) (stdfs.File, weak.Pointer[closeProbeReader], weak.Pointer[imageResultStore], *int) {
	t.Helper()
	member := lzipMember(t, []byte("payload"))
	if want == ErrInvalidCompression {
		member[len(member)-20] ^= 1
	}
	capability := pathTestCapability(t)
	index, err := (Index{Entries: []Entry{{
		Path: []string{"file"}, Capability: capability, Compression: Lzip, Size: 7,
	}}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	closes := new(int)
	reader := &closeProbeReader{
		imageResultReader: imageResultReader{Reader: strings.NewReader(string(member)), size: int64(len(member))},
		closes:            closes,
	}
	store := &imageResultStore{index: index, reader: reader}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	image, err := OpenFS(ctx, capability, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Use an independently canceled context for the third case only.
	readCtx := t.Context()
	if want == context.Canceled {
		readCtx = ctx
	}
	file, err := image.Open(readCtx, "file")
	store.reader = nil
	if err != nil {
		t.Fatal(err)
	}
	if want == context.Canceled {
		cancel()
	}
	if _, err := io.ReadAll(file); !errors.Is(err, want) {
		_ = file.Close()
		t.Fatalf("ReadAll: %v, want %v", err, want)
	}
	return file, weak.Make(reader), weak.Make(store), closes
}

func TestImageMetadataReleasesNestedTree(t *testing.T) {
	content := newTestStore()
	nested := NewBuilder(content, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddSymlink("child", "missing"); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(content, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddFile(t.Context(), "file", strings.NewReader("data"), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSubdirectory("nested", nestedCap); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"stat", "lstat", "directory entry", "sibling entry"} {
		t.Run(operation, func(t *testing.T) {
			metadata, child := retainImageMetadata(t, content, capability, operation)
			runtime.GC()
			if child.Value() != nil {
				t.Error("retained metadata kept the nested tree alive")
			}
			if entry, ok := metadata.(stdfs.DirEntry); ok {
				if info, err := entry.Info(); err != nil || info.Name() != entry.Name() || info.IsDir() != entry.IsDir() {
					t.Errorf("entry Info: %v, %v", info, err)
				}
			}
			runtime.KeepAlive(metadata)
		})
	}
}

func retainImageMetadata(t *testing.T, content eris.CapabilityStore, capability eris.Capability, operation string) (any, weak.Pointer[node]) {
	t.Helper()
	image, err := OpenFS(t.Context(), capability, content, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := image.ReadDir(t.Context(), "nested"); err != nil {
		t.Fatal(err)
	}
	child := weak.Make(image.root.children["nested"].children["child"])
	var metadata any
	switch operation {
	case "stat":
		metadata, err = image.Stat(t.Context(), ".")
	case "lstat":
		metadata, err = image.Lstat(t.Context(), "nested")
	default:
		var entries []stdfs.DirEntry
		entries, err = image.ReadDir(t.Context(), ".")
		if err == nil {
			metadata = entries[0] // file
			if operation == "directory entry" {
				metadata = entries[1] // nested
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return metadata, child
}

func TestStaticEntryReleasesSiblingLookup(t *testing.T) {
	entry, store := retainLegacyLinkEntry(t)
	runtime.GC()
	if store.Value() != nil {
		t.Error("link entry kept a sibling's capability store alive")
	}
	if info, err := entry.Info(); err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
		t.Errorf("link metadata: %v, %v", info, err)
	}
	runtime.KeepAlive(entry)
}

func TestLegacyEntryReleasesStoreAfterInfo(t *testing.T) {
	marker := new([1024]byte)
	entry, store := retainResolvedLegacyEntry(t, context.WithValue(context.Background(), retentionContextKey{}, marker))
	weakMarker := weak.Make(marker)
	runtime.GC()
	if store.Value() != nil {
		t.Error("resolved legacy directory entry retained its store")
	}
	if weakMarker.Value() != nil {
		t.Error("resolved legacy directory entry retained its uncancelable context")
	}
	if info, err := entry.Info(); err != nil || info.Name() != "hello.txt" || info.Size() != 4 {
		t.Errorf("second Info: %v, %v", info, err)
	}
}

func TestResolvedLegacyEntryChecksCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entry, _ := retainResolvedLegacyEntry(t, ctx)
	cancel()
	if _, err := entry.Info(); !errors.Is(err, context.Canceled) {
		t.Errorf("second Info: %v", err)
	}
}

func retainResolvedLegacyEntry(t *testing.T, ctx context.Context) (stdfs.DirEntry, weak.Pointer[imageResultStore]) {
	t.Helper()
	_, index := mountFixture(t)
	store := &imageResultStore{index: index, reader: &imageResultReader{Reader: strings.NewReader("data"), size: 4}}
	image, err := OpenFS(t.Context(), pathTestCapability(t), store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := image.ReadDir(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "hello.txt" {
			if info, err := entry.Info(); err != nil || info.Size() != 4 {
				t.Fatalf("first Info: %v, %v", info, err)
			}
			return entry, weak.Make(store)
		}
	}
	t.Fatal("legacy file is missing from the listing")
	return nil, weak.Pointer[imageResultStore]{}
}

func TestExhaustedDirectoryReleasesTree(t *testing.T) {
	for _, count := range []int{-1, 1} {
		file, child := openExhaustedDirectory(t, t.Context(), count)
		runtime.GC()
		if child.Value() != nil {
			t.Errorf("after ReadDir(%d), exhausted handle retained its tree", count)
		}
		if info, err := file.Stat(); err != nil || info.Name() != "." || !info.IsDir() {
			t.Errorf("Stat: %v, %v", info, err)
		}
		if entries, err := file.ReadDir(1); len(entries) != 0 || !errors.Is(err, io.EOF) {
			t.Errorf("ReadDir(1): %v, %v", entries, err)
		}
		if entries, err := file.ReadDir(0); len(entries) != 0 || err != nil {
			t.Errorf("ReadDir(0): %v, %v", entries, err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestExhaustedDirectoryReleasesUncancelableContext(t *testing.T) {
	marker := new([1024]byte)
	ctx := context.WithValue(context.Background(), retentionContextKey{}, marker)
	file, _ := openExhaustedDirectory(t, ctx, 1)
	weakMarker := weak.Make(marker)
	runtime.GC()
	if weakMarker.Value() != nil {
		t.Error("exhausted directory retained an uncancelable context")
	}
	if info, err := file.Stat(); err != nil || !info.IsDir() {
		t.Errorf("Stat: %v, %v", info, err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
}

func openExhaustedDirectory(t *testing.T, ctx context.Context, count int) (stdfs.ReadDirFile, weak.Pointer[node]) {
	t.Helper()
	index := Index{Entries: []Entry{{Path: []string{"dir", "link"}, Type: Symlink}}}
	image := &FS{root: buildTree(index, nil, ".", DefaultMaxIndexSize), prefix: "."}
	file, err := image.Open(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	dir := file.(stdfs.ReadDirFile)
	if entries, err := dir.ReadDir(count); err != nil || len(entries) != 1 || entries[0].Name() != "dir" {
		t.Fatalf("ReadDir(%d): %v, %v", count, entries, err)
	}
	return dir, weak.Make(image.root.children["dir"].children["link"])
}

func retainLegacyLinkEntry(t *testing.T) (stdfs.DirEntry, weak.Pointer[imageResultStore]) {
	t.Helper()
	_, index := mountFixture(t)
	store := &imageResultStore{index: index}
	image, err := OpenFS(t.Context(), pathTestCapability(t), store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := image.ReadDir(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "latest.txt" {
			return entry, weak.Make(store)
		}
	}
	t.Fatal("legacy link is missing from the listing")
	return nil, weak.Pointer[imageResultStore]{}
}

func TestMountBlockFileInfoReleasesBytes(t *testing.T) {
	info, data := retainMountBlockInfo(t)
	runtime.GC()
	if data.Value() != nil {
		t.Error("retained metadata kept the closed file's block alive")
	}
	if !info.Mode().IsRegular() || info.Size() != int64(eris.BlockSize1KiB) {
		t.Errorf("block metadata: %v", info)
	}
	runtime.KeepAlive(info)
}

func retainMountBlockInfo(t *testing.T) (stdfs.FileInfo, weak.Pointer[byte]) {
	t.Helper()
	blocks, _ := mountFixture(t)
	capability, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	mount := NewMount(t.Context(), blocks, eris.BlockOptions{})
	opened, err := mount.Open(capability.Root.Reference.String())
	if err != nil {
		t.Fatal(err)
	}
	file := opened.(*mountBlockFile)
	data := weak.Make(&file.info.block[0])
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return info, data
}

func TestMountedImageEntryReleasesListingCache(t *testing.T) {
	entry, cache := retainMountedImageEntry(t)
	runtime.GC()
	if cache.Value() != nil {
		t.Error("retained image entry kept the mount listing cache alive")
	}
	if info, err := entry.Info(); err != nil || !info.IsDir() || info.Name() != entry.Name() {
		t.Errorf("entry Info: %v, %v", info, err)
	}
	runtime.KeepAlive(entry)
}

func retainMountedImageEntry(t *testing.T) (stdfs.DirEntry, weak.Pointer[mountListingCache]) {
	t.Helper()
	capability := pathTestCapability(t)
	root := t.TempDir()
	makePathTestTree(t, root, capability)
	mount := withMountCache(t, NewMountWithStore(t.Context(), NewMountStore(root), nil, eris.BlockOptions{}), 1, 0)
	if _, err := mount.Stat("fs/" + capability.String()); err != nil {
		t.Fatal(err)
	}
	entries, err := mount.ReadDir("fs")
	if err != nil || len(entries) != 1 {
		t.Fatalf("mount ReadDir: %v, %v", entries, err)
	}
	return entries[0], weak.Make(mount.listings)
}

func TestLegacyTextPathReleasesParentName(t *testing.T) {
	info, parent := retainLegacyLeafInfo(t)
	runtime.GC()
	if parent.Value() != nil {
		t.Error("leaf metadata kept the legacy path's parent name alive")
	}
	if info.Name() != "leaf" || info.Mode()&stdfs.ModeSymlink == 0 {
		t.Errorf("leaf metadata: %v", info)
	}
	runtime.KeepAlive(info)
}

func retainLegacyLeafInfo(t *testing.T) (stdfs.FileInfo, weak.Pointer[byte]) {
	t.Helper()
	path := strings.Repeat("a", 1<<20) + "/leaf"
	data := append([]byte(MagicPrefix), 0x82, 1)
	data = cborwire.AppendHead(data, 5, 1)
	data = appendText(data, path)
	data = append(data, 0x82, byte(Symlink), 0x80)
	index, err := ParseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	parent := weak.Make(unsafe.StringData(index.Entries[0].Path[0]))
	image := &FS{root: buildTree(index, nil, ".", DefaultMaxIndexSize), prefix: "."}
	info, err := image.Lstat(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return info, parent
}

func TestSmallIndexPathReleasesLargeSibling(t *testing.T) {
	path, sibling := retainSmallIndexPath(t)
	runtime.GC()
	if sibling.Value() != nil {
		t.Error("small path kept a large sibling path alive")
	}
	if len(path) != 1 || path[0] != "a" {
		t.Fatalf("retained path: %q", path)
	}
	runtime.KeepAlive(path)
}

func retainSmallIndexPath(t *testing.T) ([]string, weak.Pointer[byte]) {
	t.Helper()
	data, err := (Index{Entries: []Entry{
		{Path: []string{"a"}, Type: Symlink},
		{Path: []string{strings.Repeat("z", 1<<20)}, Type: Symlink},
	}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	index, err := ParseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	return index.Entries[0].Path, weak.Make(unsafe.StringData(index.Entries[1].Path[0]))
}

func TestSmallDirectoryEntryReleasesLargeSibling(t *testing.T) {
	for _, size := range []int64{0, -1} {
		name := "known size"
		if size < 0 {
			name = "lazy size"
		}
		t.Run(name, func(t *testing.T) {
			entry, sibling := retainSmallDirectoryEntry(t, size)
			runtime.GC()
			if sibling.Value() != nil {
				t.Error("small directory entry kept a large sibling name alive")
			}
			if info, err := entry.Info(); err != nil || info.Name() != "a" || info.Size() != 0 {
				t.Fatalf("entry Info: %v, %v", info, err)
			}
			runtime.KeepAlive(entry)
		})
	}
}

func retainSmallDirectoryEntry(t *testing.T, size int64) (stdfs.DirEntry, weak.Pointer[byte]) {
	t.Helper()
	largeName := strings.Repeat("z", 1<<20)
	capability := pathTestCapability(t)
	index := Index{Entries: []Entry{
		{Path: []string{"a"}, Capability: capability, Size: size},
		{Path: []string{largeName}, Capability: capability, Size: size},
	}}
	store := NewStore(&imageResultStore{reader: &imageResultReader{Reader: strings.NewReader("")}}, eris.BlockOptions{})
	image := &FS{root: buildTree(index, store, ".", DefaultMaxIndexSize), prefix: "."}
	entries, err := image.ReadDir(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	return entries[0], weak.Make(unsafe.StringData(largeName))
}

func TestOpenFileReleasesLargeSiblingLink(t *testing.T) {
	file, sibling := retainOpenFile(t)
	defer func() { _ = file.Close() }()
	runtime.GC()
	if sibling.Value() != nil {
		t.Error("open file kept a sibling's large link target alive")
	}
	if data, err := io.ReadAll(file); err != nil || string(data) != "data" {
		t.Fatalf("file content: %q, %v", data, err)
	}
	runtime.KeepAlive(file)
}

func retainOpenFile(t *testing.T) (stdfs.File, weak.Pointer[byte]) {
	t.Helper()
	capability := pathTestCapability(t)
	data, err := (Index{Entries: []Entry{
		{Path: []string{"a"}, Capability: capability, Size: 4},
		{Path: []string{"b"}, Type: Symlink, Target: strings.Repeat("z", 1<<20)},
	}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	store := &imageResultStore{index: data, reader: &imageResultReader{Reader: strings.NewReader("data"), size: 4}}
	image, err := OpenFS(t.Context(), capability, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := image.Open(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	return file, weak.Make(unsafe.StringData(image.root.children["b"].target))
}

func TestStaticEntryReleasesUncancelableContext(t *testing.T) {
	entry, marker := retainEntryWithUncancelableContext(t)
	runtime.GC()
	if marker.Value() != nil {
		t.Error("static directory entry kept an uncancelable context alive")
	}
	if info, err := entry.Info(); err != nil || info.Name() != "link" {
		t.Fatalf("entry Info: %v, %v", info, err)
	}
	runtime.KeepAlive(entry)
}

func retainEntryWithUncancelableContext(t *testing.T) (stdfs.DirEntry, weak.Pointer[[1024]byte]) {
	t.Helper()
	marker := new([1024]byte)
	ctx := context.WithValue(context.Background(), retentionContextKey{}, marker)
	index := Index{Entries: []Entry{{Path: []string{"link"}, Type: Symlink}}}
	image := &FS{root: buildTree(index, nil, ".", DefaultMaxIndexSize), prefix: "."}
	entries, err := image.ReadDir(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	return entries[0], weak.Make(marker)
}

func TestClosedNamedDirectoryReleasesBackendHandle(t *testing.T) {
	file, backend := retainClosedNamedDirectory(t)
	runtime.GC()
	if backend.Value() != nil {
		t.Error("closed mount directory kept its backend handle alive")
	}
	if _, err := file.Stat(); !errors.Is(err, ErrClosed) {
		t.Errorf("closed Stat: %v", err)
	}
	runtime.KeepAlive(file)
}

type singleDirectoryStore struct {
	ImageStore
	view FileSystem
}

func (s singleDirectoryStore) OpenFS(context.Context, eris.Capability) (FileSystem, error) {
	return s.view, nil
}

type singleDirectoryImage struct {
	FileSystem
	directory stdfs.File
}

func (i singleDirectoryImage) Open(context.Context, string) (stdfs.File, error) {
	return i.directory, nil
}

func retainClosedNamedDirectory(t *testing.T) (stdfs.File, weak.Pointer[directoryFile]) {
	t.Helper()
	dir := &directoryFile{ctx: t.Context(), node: &node{name: ".", kind: directoryType}}
	store := singleDirectoryStore{view: singleDirectoryImage{directory: dir}}
	mount := NewMountWithStore(t.Context(), store, nil, eris.BlockOptions{})
	file, err := mount.Open("fs/" + pathTestCapability(t).String())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := file.Stat(); err != nil || !info.IsDir() || info.Name() != pathTestCapability(t).String() {
		t.Fatalf("mount directory Stat: %v, %v", info, err)
	}
	if err := file.Close(); err != nil || !dir.closed {
		t.Fatalf("Close: %v, backend closed: %v", err, dir.closed)
	}
	return file, weak.Make(dir)
}

func TestBuilderSnapshotLeafReleasesLargeParent(t *testing.T) {
	leaf, parent := retainBuilderSnapshotLeaf(t)
	runtime.GC()
	if parent.Value() != nil {
		t.Error("builder snapshot leaf kept a large parent component alive")
	}
	if leaf != "leaf" {
		t.Fatalf("leaf name: %q", leaf)
	}
	runtime.KeepAlive(leaf)
}

func retainBuilderSnapshotLeaf(t *testing.T) (string, weak.Pointer[byte]) {
	t.Helper()
	builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddSymlink(strings.Repeat("a", 1<<20)+"/leaf", "target"); err != nil {
		t.Fatal(err)
	}
	path := builder.Index().Entries[0].Path
	return path[1], weak.Make(unsafe.StringData(path[0]))
}

func TestBuilderReleasesInputStringStorage(t *testing.T) {
	builder, path, target := buildWithSlicedStrings(t)
	runtime.GC()
	if path.Value() != nil {
		t.Error("builder retained the backing allocation of a short path")
	}
	if target.Value() != nil {
		t.Error("builder retained the backing allocation of a short target")
	}
	entry := builder.Index().Entries[0]
	if len(entry.Path) != 1 || entry.Path[0] != "link" || entry.Target != "file" {
		t.Fatalf("builder entry: %+v", entry)
	}
}

func TestBuilderSharesRepeatedDirectoryNames(t *testing.T) {
	parent := strings.Repeat("d", 1<<20)
	builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddSymlink(parent+"/first", "second"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(t.Context(), parent+"/second", strings.NewReader("data"), false); err != nil {
		t.Fatal(err)
	}
	entries := builder.Index().Entries
	for i, leaf := range []string{"first", "second"} {
		if path := entries[i].Path; len(path) != 2 || path[0] != parent || path[1] != leaf {
			t.Fatalf("entry %d has the wrong path", i)
		}
	}
	if unsafe.StringData(entries[0].Path[0]) != unsafe.StringData(entries[1].Path[0]) {
		t.Error("builder retained a separate 1 MiB directory name for each file")
	}
}

func buildWithSlicedStrings(t *testing.T) (*Builder, weak.Pointer[byte], weak.Pointer[byte]) {
	t.Helper()
	path := "link" + strings.Repeat("x", 1<<20)
	target := "file" + strings.Repeat("y", 1<<20)
	builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddSymlink(path[:4], target[:4]); err != nil {
		t.Fatal(err)
	}
	return builder, weak.Make(unsafe.StringData(path)), weak.Make(unsafe.StringData(target))
}

func TestSubReleasesInputPathStorage(t *testing.T) {
	ctx := t.Context()
	index := Index{Entries: []Entry{{Path: []string{"dir", "child"}, Type: Symlink}}}
	image := &FS{root: buildTree(index, nil, ".", DefaultMaxIndexSize), prefix: "."}
	mountedRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(mountedRoot, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	mounted := &mountedImage{root: mountedRoot, prefix: "."}
	blocks, _ := mountFixture(t)
	for _, tc := range []struct {
		name string
		fsys stdfs.FS
		path string
	}{
		{"image", NewIOFS(ctx, image), "dir"},
		{"mounted", NewIOFS(ctx, mounted), "dir"},
		{"virtual mount", NewMount(ctx, blocks, eris.BlockOptions{}), "fs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub, storage := subWithSlicedPath(t, tc.fsys, tc.path)
			runtime.GC()
			if storage.Value() != nil {
				t.Error("sub view retained the backing allocation of its input path")
			}
			if info, err := stdfs.Stat(sub, "."); err != nil || !info.IsDir() {
				t.Fatalf("sub Stat: %v, %v", info, err)
			}
		})
	}
}

func subWithSlicedPath(t *testing.T, fsys stdfs.FS, path string) (stdfs.FS, weak.Pointer[byte]) {
	t.Helper()
	input := path + strings.Repeat("z", 1<<20)
	sub, err := stdfs.Sub(fsys, input[:len(path)])
	if err != nil {
		t.Fatal(err)
	}
	return sub, weak.Make(unsafe.StringData(input))
}

func TestNestedSubReleasesEnclosingImage(t *testing.T) {
	ctx := t.Context()
	store := newTestStore()
	nested := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddFile(ctx, "sibling", strings.NewReader("nested content"), false); err != nil {
		t.Fatal(err)
	}
	if err := nested.AddSymlink("a/b/link", "../../sibling"); err != nil {
		t.Fatal(err)
	}
	if err := nested.AddSymlink("outside", "../outside"); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outer := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	if err := outer.AddSubdirectory("inside/nested", nestedCap); err != nil {
		t.Fatal(err)
	}
	if err := outer.AddSymlink("alias", "inside/nested"); err != nil {
		t.Fatal(err)
	}
	if err := outer.AddFile(ctx, "inside/outside", strings.NewReader("outer content"), false); err != nil {
		t.Fatal(err)
	}
	outerCap, err := outer.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"alias", "alias/a/b"} {
		t.Run(path, func(t *testing.T) {
			sub, parent := retainNestedSub(t, store, outerCap, path)
			runtime.GC()
			if parent.Value() != nil {
				t.Error("nested sub view retained the enclosing image")
			}
			link := "a/b/link"
			if path == "alias/a/b" {
				link = "link"
				if _, err := sub.ReadLink(ctx, link); !errors.Is(err, stdfs.ErrNotExist) {
					t.Errorf("ReadLink above the sub view: %v", err)
				}
			} else if _, err := sub.ReadFile(ctx, "outside"); !errors.Is(err, stdfs.ErrNotExist) {
				t.Errorf("ReadFile above the nested image: %v", err)
			}
			if data, err := sub.ReadFile(ctx, link); err != nil || string(data) != "nested content" {
				t.Errorf("ReadFile inside the nested image: %q, %v", data, err)
			}
		})
	}
}

func retainNestedSub(t *testing.T, store eris.CapabilityStore, capability eris.Capability, path string) (FileSystem, weak.Pointer[node]) {
	t.Helper()
	image, err := OpenFS(t.Context(), capability, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := image.Sub(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return sub, weak.Make(image.root)
}

func TestMountMetadataReleasesInputPathStorage(t *testing.T) {
	blocks, _ := mountFixture(t)
	mount := NewMount(t.Context(), blocks, eris.BlockOptions{})
	capability, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"fs", "fs/" + pathTestCapability(t).String(), capability.String(), capability.Root.Reference.String()} {
		info, storage := statWithSlicedPath(t, mount, path)
		runtime.GC()
		if storage.Value() != nil {
			t.Errorf("metadata for %q retained the backing allocation of its input path", path)
		}
		if info.Name() != filepath.Base(path) {
			t.Errorf("metadata name: %q, want %q", info.Name(), filepath.Base(path))
		}
	}
}

func statWithSlicedPath(t *testing.T, fsys stdfs.FS, path string) (stdfs.FileInfo, weak.Pointer[byte]) {
	t.Helper()
	input := path + strings.Repeat("z", 1<<20)
	info, err := stdfs.Stat(fsys, input[:len(path)])
	if err != nil {
		t.Fatal(err)
	}
	return info, weak.Make(unsafe.StringData(input))
}

func TestPathErrorReleasesInputStringStorage(t *testing.T) {
	image := &FS{root: buildTree(Index{}, nil, ".", DefaultMaxIndexSize), prefix: "."}
	for _, tc := range []struct {
		name string
		fsys stdfs.FS
	}{
		{"image", NewIOFS(t.Context(), image)},
		{"mounted", NewIOFS(t.Context(), &mountedImage{root: t.TempDir(), prefix: "."})},
		{"mount", NewMountWithStore(t.Context(), nil, nil, eris.BlockOptions{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage, err := openMissingSlicedPath(t, tc.fsys)
			runtime.GC()
			if storage.Value() != nil {
				t.Error("path error retained the backing allocation of its input path")
			}
			var pathErr *stdfs.PathError
			if !errors.Is(err, stdfs.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Path != "missing" {
				t.Errorf("Open: %v", err)
			}
		})
	}
}

func openMissingSlicedPath(t *testing.T, fsys stdfs.FS) (weak.Pointer[byte], error) {
	t.Helper()
	const name = "missing"
	input := name + strings.Repeat("z", 1<<20)
	file, err := fsys.Open(input[:len(name)])
	if err == nil {
		_ = file.Close()
		t.Fatal("Open succeeded for a missing file")
	}
	return weak.Make(unsafe.StringData(input)), err
}
