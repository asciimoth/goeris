package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/prefetchtest"
)

func TestOpenFilePrefetch(t *testing.T) {
	blocks, _ := mountFixture(t)
	options := eris.BlockOptions{HighPriority: true}
	source := prefetchtest.New(blocks, 0, 1)
	image, err := OpenFS(t.Context(), pathTestCapability(t), eris.NewBlockCapabilityStore(source), options)
	if err != nil {
		t.Fatal(err)
	}
	openCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	file, err := image.OpenFile(openCtx, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	prefetcher, ok := file.(interface{ Prefetch(context.Context) error })
	if !ok {
		t.Fatal("open regular file lost Prefetch")
	}
	if _, err := file.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	before := len(source.Snapshot().Hints)
	if err := prefetcher.Prefetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	capability, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	hints := source.Snapshot().Hints[before:]
	want := eris.BlockRequest{Reference: capability.Root.Reference, TTL: eris.NoTTL, Options: options}
	if len(hints) != 1 || hints[0] != want {
		t.Fatalf("hints = %v, want [%v]", hints, want)
	}
	if pos, err := file.Seek(0, io.SeekCurrent); pos != 3 || err != nil {
		t.Fatal(pos, err)
	}
	cancel()
	if err := prefetcher.Prefetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prefetcher.Prefetch(openCtx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prefetcher.Prefetch(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestPrefetchImage(t *testing.T) {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	contentStore := eris.NewBlockCapabilityStore(blocks)
	data := make([]byte, 70*1024)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	nested := NewBuilder(contentStore, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddFile(ctx, "file", bytes.NewReader(data), false); err != nil {
		t.Fatal(err)
	}
	if err := nested.AddCompressedFile(ctx, "compressed", bytes.NewReader(data), false); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{Entries: []Entry{
		{Path: []string{"a"}, Type: Subdirectory, Capability: nestedCap},
		{Path: []string{"b"}, Type: Subdirectory, Capability: nestedCap},
		{Path: []string{"link"}, Type: Symlink, Target: "a"},
		{Path: []string{"loop"}, Type: Symlink, Target: "loop"},
		{Path: []string{"outside"}, Type: Symlink, Target: "../../outside"},
	}}
	cap, err := EncodeIndex(ctx, index, eris.Secret{}, contentStore, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	options := eris.BlockOptions{HighPriority: true}
	store := prefetchtest.New(blocks, 0, 2)
	image, err := OpenFS(ctx, cap, eris.NewBlockCapabilityStore(store), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewIOFS(ctx, image).Prefetch("."); err != nil {
		t.Fatal(err)
	}
	stats := store.Snapshot()
	reads := map[eris.Reference]int{}
	hints := map[eris.Reference]bool{}
	for _, r := range stats.Reads {
		reads[r.Reference]++
	}
	for _, r := range stats.Hints {
		hints[r.Reference] = true
		if r.Options != options || r.TTL != eris.NoTTL {
			t.Fatal("lost hint options")
		}
	}
	// Every leaf of each regular or compressed file must be hinted, but not read.
	var checkLeaves func(eris.Pair, eris.Level)
	checkLeaves = func(pair eris.Pair, level eris.Level) {
		if level == 0 {
			if !hints[pair.Reference] || reads[pair.Reference] != 0 {
				t.Fatal("file leaf must be hinted without a read")
			}
			return
		}
		plain, err := eris.DecryptNode(blocks.Blocks[pair.Reference].Data, pair, level)
		if err != nil {
			t.Fatal(err)
		}
		pairs, err := eris.ParseInternalNode(plain)
		if err != nil {
			t.Fatal(err)
		}
		for _, child := range pairs {
			checkLeaves(child, level-1)
		}
	}
	for _, entry := range nested.Index().Entries {
		checkLeaves(entry.Capability.Root, entry.Capability.Level)
	}
	// Opening a single-leaf index reads it once for size and once for content.
	if reads[nestedCap.Root.Reference] != 2 {
		t.Fatal("nested index decoded more than once", reads[nestedCap.Root.Reference])
	}
	for _, name := range []string{"a", "b"} {
		if image.root.children[name].children != nil {
			t.Errorf("prefetch kept the decoded subtree for %q", name)
		}
	}
	before := store.Snapshot().Cold
	for _, name := range []string{"a/file", "b/compressed"} {
		got, err := image.ReadFile(ctx, name)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal(name, err)
		}
	}
	if store.Snapshot().Cold != before {
		t.Fatal("prefetched file had cold reads")
	}
	sub, err := image.Sub(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Prefetch(ctx, "."); err != nil {
		t.Fatal(err)
	}
	if err := image.Prefetch(ctx, "link/file"); err != nil {
		t.Fatal(err)
	}
	if err := image.Prefetch(ctx, "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := image.Prefetch(canceled, "."); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPrefetchImageMissingContent(t *testing.T) {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	cap, _ := store.Encode(t.Context(), make([]byte, 4000), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	index := Index{Entries: []Entry{{Path: []string{"file"}, Type: Regular, Capability: cap, Size: 4000}}}
	root, err := EncodeIndex(ctx, index, eris.Secret{}, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(ctx, root, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the internal file root and the FS index only. Missing leaves cannot
	// prevent the explicit traversal from issuing hints.
	for ref := range blocks.Blocks {
		if ref != root.Root.Reference && ref != cap.Root.Reference {
			delete(blocks.Blocks, ref)
		}
	}
	if err := image.Prefetch(ctx, "."); err != nil {
		t.Fatal("leaf read", err)
	}
	delete(blocks.Blocks, cap.Root.Reference)
	if err := image.Prefetch(ctx, "."); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
	image.root.children["file"].kind = Subdirectory
	if err := image.Prefetch(ctx, "."); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
}

func TestNamedPrefetchReleasesIndexesUsedForLookup(t *testing.T) {
	ctx := t.Context()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	content := eris.NewBlockCapabilityStore(blocks)
	nested := NewBuilder(content, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddFile(ctx, "dir/file", bytes.NewReader([]byte("data")), false); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fileCap := nested.Index().Entries[0].Capability
	root := NewBuilder(content, eris.Secret{}, eris.BlockOptions{})
	if err := root.AddSubdirectory("nested", nestedCap); err != nil {
		t.Fatal(err)
	}
	if err := root.AddSymlink("alias", "nested/dir"); err != nil {
		t.Fatal(err)
	}
	capability, err := root.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		want error
	}{
		{"nested/dir/file", nil},
		{"alias", nil},
		{"nested/missing", stdfs.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := prefetchtest.New(blocks, 0, 1)
			image, err := OpenFS(ctx, capability, eris.NewBlockCapabilityStore(source), eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := image.Prefetch(ctx, tc.name); !errors.Is(err, tc.want) {
				t.Fatalf("Prefetch: %v, want %v", err, tc.want)
			}
			fileHints := 0
			for _, hint := range source.Snapshot().Hints {
				if hint.Reference == fileCap.Root.Reference {
					fileHints++
				}
			}
			wantHints := 0
			if tc.want == nil {
				wantHints = 1
			}
			if fileHints != wantHints {
				t.Errorf("hints for the file: %d, want %d", fileHints, wantHints)
			}
			if image.root.children["nested"].children != nil {
				t.Error("prefetch lookup kept the decoded nested index")
			}
			if data, err := image.ReadFile(ctx, "nested/dir/file"); err != nil || string(data) != "data" {
				t.Fatalf("ReadFile: %q, %v", data, err)
			}
		})
	}
}

func TestPrefetchSubDoesNotVisitOtherFiles(t *testing.T) {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	contentStore := eris.NewBlockCapabilityStore(blocks)
	cap, _ := contentStore.Encode(t.Context(), []byte("small"), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	missing := cap
	missing.Root.Reference[0] ^= 1
	index := Index{Entries: []Entry{
		{Path: []string{"dir", "file"}, Type: Regular, Capability: cap, Size: 5},
		{Path: []string{"dir", "copy"}, Type: Regular, Capability: cap, Size: 5},
		{Path: []string{"dir", "link"}, Type: Symlink, Target: "../outside"},
		{Path: []string{"outside"}, Type: Subdirectory, Capability: missing},
	}}
	root, err := EncodeIndex(ctx, index, eris.Secret{}, contentStore, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := prefetchtest.New(blocks, time.Microsecond, 1)
	image, err := OpenFS(ctx, root, eris.NewBlockCapabilityStore(store), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := image.Sub(ctx, "dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Prefetch(ctx, "."); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, r := range store.Snapshot().Hints {
		if r.Reference == cap.Root.Reference {
			count++
		}
		if r.Reference == missing.Root.Reference {
			t.Fatal("escaped subtree")
		}
	}
	if count != 1 {
		t.Fatal("duplicate capability hinted", count)
	}
}

func FuzzPrefetchImage(f *testing.F) {
	f.Add([]byte("file"), uint8(2), false)
	f.Add(make([]byte, 17000), uint8(3), true)
	f.Fuzz(func(t *testing.T, data []byte, limit uint8, compressed bool) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		ctx := context.Background()
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		builder := NewBuilder(eris.NewBlockCapabilityStore(blocks), eris.Secret{}, eris.BlockOptions{})
		var err error
		if compressed {
			err = builder.AddCompressedFile(ctx, "file", bytes.NewReader(data), false)
		} else {
			err = builder.AddFile(ctx, "file", bytes.NewReader(data), false)
		}
		if err != nil {
			t.Fatal(err)
		}
		cap, err := builder.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		store := prefetchtest.New(blocks, 0, int(limit%8))
		image, err := OpenFS(ctx, cap, eris.NewBlockCapabilityStore(store), eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := image.Prefetch(ctx, "."); err != nil {
			t.Fatal(err)
		}
		cold := store.Snapshot().Cold
		got, err := image.ReadFile(ctx, "file")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal(err)
		}
		if store.Snapshot().Cold != cold {
			t.Fatal("prefetch missed content")
		}
	})
}

// cancelDuringTraversal cancels after find has checked the context, before
// the first pending directory is processed.
type cancelDuringTraversal struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (ctx *cancelDuringTraversal) Err() error {
	ctx.calls++
	if ctx.calls > 1 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}
func TestPrefetchImageCancellationDuringWalk(t *testing.T) {
	image := &FS{root: &node{kind: directoryType}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := image.Prefetch(&cancelDuringTraversal{Context: ctx, cancel: cancel}, ".")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrentPrefetchAndRead(t *testing.T) {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	content := bytes.Repeat([]byte("concurrent prefetch"), 1000)
	child := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	if err := child.AddFile(ctx, "file", bytes.NewReader(content), false); err != nil {
		t.Fatal(err)
	}
	childCap, err := child.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootCap, err := EncodeIndex(ctx, Index{Entries: []Entry{{Path: []string{"child"}, Type: Subdirectory, Capability: childCap}}}, eris.Secret{}, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(ctx, rootCap, eris.NewBlockCapabilityStore(prefetchtest.New(blocks, 0, 1)), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 5 {
				if err := image.Prefetch(ctx, "."); err != nil {
					t.Error(err)
					return
				}
				got, err := image.ReadFile(ctx, "child/file")
				if err != nil || !bytes.Equal(got, content) {
					t.Errorf("read after concurrent prefetch: %v", err)
					return
				}
			}
		})
	}
	group.Wait()
}
