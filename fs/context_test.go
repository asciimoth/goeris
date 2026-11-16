package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/asciimoth/goeris"
)

type contextBlocks struct {
	eris.BlockSet
	t             *testing.T
	want          context.Context
	reads, writes int
}

//nolint:staticcheck // ST1008: BlockSource requires options as the third return value.
func (s *contextBlocks) GetBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	s.t.Helper()
	if ctx != s.want {
		s.t.Fatal("read context changed")
	}
	s.reads++
	if err := ctx.Err(); err != nil {
		return nil, err, eris.BlockOptions{}
	}
	return s.BlockSet.GetBlock(ctx, ref, ttl, eris.BlockOptions{})
}
func (s *contextBlocks) PutBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, refs ...eris.Reference) (eris.Reference, error) {
	s.t.Helper()
	if ctx != s.want {
		s.t.Fatal("write context changed")
	}
	s.writes++
	if err := ctx.Err(); err != nil {
		return eris.Reference{}, err
	}
	return s.BlockSet.PutBlock(ctx, block, ttl, eris.BlockOptions{}, refs...)
}

func contextImage(t *testing.T) (eris.Capability, eris.BlockSet) {
	t.Helper()
	ctx := context.Background()
	blocks := eris.BlockSet{}
	nested := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddFile(ctx, "file", strings.NewReader("payload"), false); err != nil {
		t.Fatal(err)
	}
	if err := nested.AddCompressedFile(ctx, "compressed", strings.NewReader(strings.Repeat("data", 1000)), false); err != nil {
		t.Fatal(err)
	}
	if err := nested.AddSymlink("link", "file"); err != nil {
		t.Fatal(err)
	}
	cap, err := nested.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := root.AddSubdirectory("nested", cap); err != nil {
		t.Fatal(err)
	}
	cap, err = root.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return cap, blocks
}

func TestFSOperationContexts(t *testing.T) {
	cap, blocks := contextImage(t)
	for name, operation := range map[string]func(context.Context, *FS) error{
		"open": func(ctx context.Context, f *FS) error {
			file, err := f.Open(ctx, "nested/compressed")
			if err == nil {
				_, err = io.ReadAll(file)
				_ = file.Close()
			}
			return err
		},
		"openfile": func(ctx context.Context, f *FS) error {
			file, err := f.OpenFile(ctx, "nested/file")
			if err == nil {
				_, err = io.ReadAll(file)
				_ = file.Close()
			}
			return err
		},
		"readfile": func(ctx context.Context, f *FS) error { _, err := f.ReadFile(ctx, "nested/file"); return err },
		"stat":     func(ctx context.Context, f *FS) error { _, err := f.Stat(ctx, "nested/file"); return err },
		"lstat":    func(ctx context.Context, f *FS) error { _, err := f.Lstat(ctx, "nested/link"); return err },
		"readlink": func(ctx context.Context, f *FS) error { _, err := f.ReadLink(ctx, "nested/link"); return err },
		"readdir":  func(ctx context.Context, f *FS) error { _, err := f.ReadDir(ctx, "nested"); return err },
		"sub":      func(ctx context.Context, f *FS) error { _, err := f.Sub(ctx, "nested"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			initial, finish := context.WithCancel(context.Background())
			defer finish()
			source := &contextBlocks{BlockSet: blocks, t: t, want: initial}
			image, err := OpenFS(initial, cap, source, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source.want, source.reads = ctx, 0
			if err := operation(ctx, image); err != nil {
				t.Fatal(err)
			}
			if source.reads == 0 {
				t.Fatal("nested index was not read")
			}
			cancel()
			if err := operation(ctx, image); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled operation: %v", err)
			}
		})
	}
}

func TestIOFSContextAndSub(t *testing.T) {
	cap, blocks := contextImage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &contextBlocks{BlockSet: blocks, t: t, want: ctx}
	image, err := OpenFS(ctx, cap, source, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := NewIOFS(ctx, image)
	if err := fstest.TestFS(wrapped, "nested/file", "nested/compressed", "nested/link"); err != nil {
		t.Fatal(err)
	}
	sub, err := stdfs.Sub(wrapped, "nested")
	if err != nil {
		t.Fatal(err)
	}
	file, err := sub.Open("file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	cancel()
	if _, err := io.ReadAll(file); !errors.Is(err, context.Canceled) {
		t.Fatalf("late read: %v", err)
	}
	if _, err := stdfs.ReadFile(sub, "file"); !errors.Is(err, context.Canceled) {
		t.Fatalf("sub read: %v", err)
	}
	// A wrapper must not change the context on the shared image.
	fresh := context.Background()
	source.want = fresh
	if got, err := NewIOFS(fresh, image).ReadFile("nested/file"); err != nil || string(got) != "payload" {
		t.Fatalf("fresh wrapper: %q, %v", got, err)
	}
}

func TestDirectoryEntryRetainsContext(t *testing.T) {
	blocks := eris.BlockSet{}
	data := bytes.Repeat([]byte("metadata"), 500)
	cap, err := eris.EncodeContent(context.Background(), bytes.NewReader(data), eris.Secret{}, eris.BlockSize1KiB, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	index := storeV1Index(t, Index{Entries: []Entry{{Path: []string{"file"}, Capability: cap}}}, blocks)
	for _, fromOpen := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		source := &contextBlocks{BlockSet: blocks, t: t, want: ctx}
		image, err := OpenFS(ctx, index, source, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var entries []stdfs.DirEntry
		if fromOpen {
			dir, err := NewIOFS(ctx, image).Open(".")
			if err != nil {
				t.Fatal(err)
			}
			entries, err = dir.(stdfs.ReadDirFile).ReadDir(-1)
			_ = dir.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else {
			entries, err = image.ReadDir(ctx, ".")
			if err != nil {
				t.Fatal(err)
			}
		}
		before := source.reads
		info, err := entries[0].Info()
		if err != nil || info.Size() != int64(len(data)) || source.reads == before {
			t.Fatalf("lazy metadata: %v", err)
		}
		cancel()
		// Use a fresh image to ensure the size is still unknown at cancellation.
		source.want = context.Background()
		image, err = OpenFS(source.want, index, source, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		active, stop := context.WithCancel(context.Background())
		entries, err = image.ReadDir(active, ".")
		if err != nil {
			t.Fatal(err)
		}
		stop()
		if _, err := entries[0].Info(); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled metadata: %v", err)
		}
	}
}

func TestBuilderContexts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &contextBlocks{BlockSet: eris.BlockSet{}, t: t, want: ctx}
	builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	for name, add := range map[string]func(context.Context, string, io.Reader, bool) error{
		"file": builder.AddFile, "compressed": builder.AddCompressedFile, "auto": builder.AddAutoFile,
	} {
		before := store.writes
		if err := add(ctx, name, strings.NewReader(strings.Repeat("payload", 5000)), false); err != nil {
			t.Fatal(err)
		}
		if store.writes == before {
			t.Fatal("no blocks written")
		}
	}
	if err := builder.AddLzipFile(ctx, "stored-stream", strings.NewReader("already compressed"), 0, false); err != nil {
		t.Fatal(err)
	}
	cap, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeIndex(ctx, cap, store, eris.BlockOptions{}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := builder.Finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled finish: %v", err)
	}
}

// The first full read supplies a compressible sample. Cancellation on the
// second read tests the compressor as well as the initial sampling path.
type cancelingInput struct {
	cancel   context.CancelFunc
	cancelAt int
	reads    int
}

func (input *cancelingInput) Read(p []byte) (int, error) {
	input.reads++
	if input.reads > input.cancelAt {
		return 0, errors.New("input read after cancellation")
	}
	clear(p)
	if input.reads == input.cancelAt {
		input.cancel()
		return 1, nil
	}
	return len(p), nil
}

func TestBuilderCancellationDuringInput(t *testing.T) {
	for name, add := range map[string]func(*Builder, context.Context, io.Reader) error{
		"plain": func(b *Builder, ctx context.Context, input io.Reader) error {
			return b.AddFile(ctx, "file", input, false)
		},
		"compressed": func(b *Builder, ctx context.Context, input io.Reader) error {
			return b.AddCompressedFile(ctx, "file", input, false)
		},
		"auto": func(b *Builder, ctx context.Context, input io.Reader) error {
			return b.AddAutoFile(ctx, "file", input, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, cancelAt := range []int{1, 2} {
				ctx, cancel := context.WithCancel(context.Background())
				input := &cancelingInput{cancel: cancel, cancelAt: cancelAt}
				builder := NewBuilder(eris.BlockSet{}, eris.Secret{}, eris.BlockOptions{})
				err := add(builder, ctx, input)
				cancel()
				if !errors.Is(err, context.Canceled) || input.reads != cancelAt || len(builder.Index().Entries) != 0 {
					t.Fatalf("cancel on read %d: error %v, input reads %d, entries %d", cancelAt, err, input.reads, len(builder.Index().Entries))
				}
			}
		})
	}
}

type sourceWithContext func(context.Context, eris.Reference, eris.TTL) ([]byte, error)

//nolint:staticcheck // ST1008: BlockSource requires options as the third return value.
func (source sourceWithContext) GetBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	blockResult, blockErr := source(ctx, ref, ttl)
	return blockResult, blockErr, eris.BlockOptions{}
}

func TestNestedIndexWaitCanBeCanceled(t *testing.T) {
	capability, blocks := contextImage(t)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	started := make(chan struct{})
	source := sourceWithContext(func(ctx context.Context, ref eris.Reference, ttl eris.TTL) ([]byte, error) {
		if ctx == firstCtx {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		blockResult, blockErr, _ := blocks.GetBlock(ctx, ref, ttl, eris.BlockOptions{})
		return blockResult, blockErr
	})
	image, err := OpenFS(context.Background(), capability, source, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := image.ReadDir(firstCtx, "nested")
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first read did not start")
	}
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelSecond()
	secondDone := make(chan error, 1)
	go func() {
		_, err := image.ReadDir(secondCtx, "nested")
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiting read: %v", err)
		}
	case <-time.After(time.Second):
		cancelFirst()
		t.Errorf("canceled read waited for another caller's storage operation: %v", <-secondDone)
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first read: %v", err)
	}
	// A canceled load must not poison the shared image or keep its context.
	if data, err := image.ReadFile(context.Background(), "nested/file"); err != nil || string(data) != "payload" {
		t.Fatalf("retry: %q, %v", data, err)
	}
}
