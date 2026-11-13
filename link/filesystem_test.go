package link_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

type countingSource struct {
	eris.BlockSource
	calls atomic.Int64
	err   error
}

func (source *countingSource) GetBlock(ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	source.calls.Add(1)
	if ttl != eris.NoTTL {
		return nil, errors.New("unexpected TTL")
	}
	if source.err != nil {
		return nil, source.err
	}
	return source.BlockSource.GetBlock(ref, ttl)
}

func encodeFSLink(t *testing.T, payload []byte) ([]byte, eris.BlockSet) {
	t.Helper()
	capability, blocks, err := eris.Encode(payload, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (link.Link{Capability: capability, Size: uint64(len(payload)), MIME: "application/octet-stream"}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return data, blocks
}

func newLinkFS(t *testing.T, base fs.FS, source eris.BlockSource) *link.FS {
	t.Helper()
	wrapped, err := link.NewFS(base, source)
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}

func TestFS(t *testing.T) {
	payload := patternedPayload(70*1024 + 29)
	encoded, blocks := encodeFSLink(t, payload)
	base := fstest.MapFS{
		"plain.eris":  {Data: []byte("ordinary file")},
		"short":       {Data: []byte(link.MagicPrefix[:5])},
		"empty":       {},
		"dir/content": {Data: encoded, Mode: 0o755, ModTime: time.Unix(1234, 0)},
	}
	wrapped := newLinkFS(t, base, blocks)
	if err := fstest.TestFS(wrapped, "plain.eris", "short", "empty", "dir/content"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plain.eris", "short", "empty", "dir/content"} {
		want := base[name].Data
		if name == "dir/content" {
			want = payload
		}
		got, err := fs.ReadFile(wrapped, name)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("ReadFile(%q): %d bytes, %v", name, len(got), err)
		}
	}
	sub, err := fs.Sub(wrapped, "dir")
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(sub, "content")
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("sub ReadFile: %d bytes, %v", len(got), err)
	}
	original, err := fs.ReadFile(base, "dir/content")
	if err != nil || !bytes.Equal(original, encoded) {
		t.Fatal("base link changed")
	}
}

func TestFSMetadataAndLazyReads(t *testing.T) {
	payload := patternedPayload(70*1024 + 29)
	encoded, blocks := encodeFSLink(t, payload)
	source := &countingSource{BlockSource: blocks}
	base := fstest.MapFS{"content.eris": {Data: encoded, Mode: 0o751, ModTime: time.Unix(1234, 0)}}
	wrapped := newLinkFS(t, base, source)
	file, err := wrapped.Open("content.eris")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	checkInfo := func(info fs.FileInfo, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if info.Name() != "content.eris" || info.Size() != int64(len(payload)) || info.Mode() != 0o751 || !info.ModTime().Equal(base["content.eris"].ModTime) {
			t.Fatalf("wrong metadata: %+v", info)
		}
	}
	checkInfo(file.Stat())
	checkInfo(fs.Stat(wrapped, "content.eris"))
	entries, err := fs.ReadDir(wrapped, ".")
	if err != nil {
		t.Fatal(err)
	}
	checkInfo(entries[0].Info())
	dir, err := wrapped.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	entries, err = dir.(fs.ReadDirFile).ReadDir(1)
	if err != nil {
		t.Fatal(err)
	}
	checkInfo(entries[0].Info())
	if source.calls.Load() != 0 {
		t.Fatal("metadata fetched blocks")
	}
	buffer := make([]byte, 31)
	if n, err := file.(io.ReaderAt).ReadAt(buffer, 1020); n != len(buffer) || err != nil || !bytes.Equal(buffer, payload[1020:1051]) {
		t.Fatalf("ReadAt: %d, %v", n, err)
	}
	if source.calls.Load() == 0 {
		t.Fatal("ReadAt did not fetch blocks")
	}
	if _, err := io.ReadFull(file, buffer); err != nil || !bytes.Equal(buffer, payload[:len(buffer)]) {
		t.Fatalf("ReadAt changed position: %v", err)
	}
	if _, err := file.(io.Seeker).Seek(-31, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, payload[len(payload)-31:]) {
		t.Fatalf("read after seek: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Read(buffer); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("read closed: %v", err)
	}
	if _, err := file.Stat(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("stat closed: %v", err)
	}
	if _, err := file.(io.ReaderAt).ReadAt(buffer, 0); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("ReadAt closed: %v", err)
	}
	if _, err := file.(io.Seeker).Seek(0, io.SeekStart); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("seek closed: %v", err)
	}
}

func TestFSLinkErrors(t *testing.T) {
	encoded, blocks := encodeFSLink(t, []byte("hello"))
	for _, test := range []struct {
		name string
		size uint64
		want error
	}{
		{"short", 4, link.ErrSizeMismatch},
		{"long", 6, link.ErrSizeMismatch},
		{"empty", 0, link.ErrSizeMismatch},
		{"wrong tree level", 2048, link.ErrSizeMismatch},
		{"overflow", math.MaxUint64, link.ErrInvalidLink},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := link.Parse(encoded)
			if err != nil {
				t.Fatal(err)
			}
			parsed.Size = test.size
			data, err := parsed.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			wrapped := newLinkFS(t, fstest.MapFS{"bad": {Data: data}}, blocks)
			_, err = fs.ReadFile(wrapped, "bad")
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			var pathErr *fs.PathError
			if !errors.As(err, &pathErr) || pathErr.Path != "bad" {
				t.Fatalf("missing path: %v", err)
			}
		})
	}
	base := fstest.MapFS{"bad": {Data: []byte(link.MagicPrefix)}, "good": {Data: encoded}}
	wrapped := newLinkFS(t, base, blocks)
	if _, err := wrapped.Open("bad"); !errors.Is(err, link.ErrInvalidLink) {
		t.Fatalf("malformed link: %v", err)
	}
	entries, err := wrapped.ReadDir(".")
	if err != nil || len(entries) != 2 {
		t.Fatalf("ReadDir: %v", err)
	}
	if _, err := entries[0].Info(); !errors.Is(err, link.ErrInvalidLink) {
		t.Fatalf("invalid entry Info: %v", err)
	}
	for _, sourceErr := range []error{eris.ErrMissingBlock, io.EOF} {
		source := &countingSource{err: sourceErr}
		wrapped := newLinkFS(t, base, source)
		file, err := wrapped.Open("good")
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(file)
		_ = file.Close()
		want := sourceErr
		if want == io.EOF {
			want = io.ErrUnexpectedEOF
		}
		if !errors.Is(err, want) {
			t.Fatalf("source error: %v, want %v", err, want)
		}
	}
}

func TestFSLinkLimit(t *testing.T) {
	encoded, blocks := encodeFSLink(t, []byte("hello"))
	base := fstest.MapFS{"link": {Data: encoded}, "plain": {Data: bytes.Repeat([]byte("x"), 1000)}}
	for _, limit := range []int64{1, int64(len(encoded) - 1), int64(len(encoded)), math.MaxInt64} {
		wrapped, err := link.NewFSWithLimit(base, blocks, limit)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fs.ReadFile(wrapped, "link")
		if limit < int64(len(encoded)) {
			if !errors.Is(err, link.ErrLinkTooLarge) {
				t.Fatalf("limit %d: %v", limit, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if got, err := fs.ReadFile(wrapped, "plain"); err != nil || len(got) != 1000 {
			t.Fatalf("limit applied to ordinary file: %v", err)
		}
	}
	large, err := link.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	large.Attributes = map[string]link.Value{"large": link.BytesValue(make([]byte, link.DefaultMaxLinkSize))}
	data, err := large.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	wrapped := newLinkFS(t, fstest.MapFS{"large": {Data: data}}, blocks)
	if _, err := wrapped.Open("large"); !errors.Is(err, link.ErrLinkTooLarge) {
		t.Fatalf("default limit: %v", err)
	}
}

func TestFSOneLayer(t *testing.T) {
	inner, _ := encodeFSLink(t, []byte("target"))
	outer, blocks := encodeFSLink(t, inner)
	wrapped := newLinkFS(t, fstest.MapFS{"link": {Data: outer}}, blocks)
	got, err := fs.ReadFile(wrapped, "link")
	if err != nil || !bytes.Equal(got, inner) {
		t.Fatalf("nested link changed: %v", err)
	}
	for _, payload := range [][]byte{nil, make([]byte, 1024)} {
		encoded, blocks := encodeFSLink(t, payload)
		wrapped := newLinkFS(t, fstest.MapFS{"link": {Data: encoded}}, blocks)
		got, err := fs.ReadFile(wrapped, "link")
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("boundary length %d: %v", len(payload), err)
		}
	}
}

type plainFile struct{ fs.File }
type plainFS struct{ fs.FS }

func (base plainFS) Open(name string) (fs.File, error) {
	file, err := base.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return plainFile{file}, nil
}

func TestFSNonSeekingBase(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("abc"), []byte(link.MagicPrefix[:8]), bytes.Repeat([]byte("x"), 100)} {
		wrapped := newLinkFS(t, plainFS{fstest.MapFS{"plain": {Data: data}}}, eris.BlockSet{})
		file, err := wrapped.Open("plain")
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("prefix lost: %q, %v", got, err)
		}
		_ = file.Close()
	}
	wrapped := newLinkFS(t, plainFS{fstest.MapFS{"plain": {Data: []byte("abc")}}}, eris.BlockSet{})
	file, err := wrapped.Open("plain")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("closed prefix read: %v", err)
	}
	encoded, blocks := encodeFSLink(t, []byte("resolved"))
	wrapped = newLinkFS(t, plainFS{fstest.MapFS{"link": {Data: encoded}}}, blocks)
	if got, err := fs.ReadFile(wrapped, "link"); err != nil || string(got) != "resolved" {
		t.Fatalf("non-seeking link: %v", err)
	}
}

func TestFSInvalidArguments(t *testing.T) {
	base := fstest.MapFS{}
	blocks := eris.BlockSet{}
	if _, err := link.NewFS(nil, blocks); !errors.Is(err, fs.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := link.NewFS(base, nil); !errors.Is(err, fs.ErrInvalid) {
		t.Fatal(err)
	}
	for _, limit := range []int64{0, -1} {
		if _, err := link.NewFSWithLimit(base, blocks, limit); !errors.Is(err, fs.ErrInvalid) {
			t.Fatal(err)
		}
	}
	for _, wrapped := range []*link.FS{newLinkFS(t, base, blocks), {}, nil} {
		for _, name := range []string{"", "/absolute", "a/../b", "a//b"} {
			if _, err := wrapped.Open(name); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("Open(%q): %v", name, err)
			}
			if _, err := wrapped.ReadDir(name); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("ReadDir(%q): %v", name, err)
			}
			if _, err := wrapped.Lstat(name); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("Lstat(%q): %v", name, err)
			}
			if _, err := wrapped.ReadLink(name); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("ReadLink(%q): %v", name, err)
			}
		}
	}
}

func TestFSSymlinkMethodsWithoutBaseSupport(t *testing.T) {
	encoded, blocks := encodeFSLink(t, []byte("hello"))
	base := plainFS{fstest.MapFS{"file": {Data: encoded}}}
	wrapped := newLinkFS(t, base, blocks)
	info, err := wrapped.Lstat("file")
	if err != nil || info.Size() != 5 {
		t.Fatalf("Lstat fallback: %v, %v", info, err)
	}
	if _, err := wrapped.ReadLink("file"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("unsupported ReadLink: %v", err)
	}
	_, err = wrapped.Lstat("missing")
	var pathErr *fs.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Op != "lstat" || pathErr.Path != "missing" {
		t.Fatalf("Lstat error context: %v", err)
	}
}
