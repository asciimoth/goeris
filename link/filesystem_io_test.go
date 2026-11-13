package link_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

// observedFile can return a final read error together with data. It also
// records ownership and byte use without trusting the reported file size.
type observedFile struct {
	fs.File
	reader    io.Reader
	readErr   error
	statErr   error
	closeErr  error
	readBytes int
	closes    int
}

func (file *observedFile) Read(p []byte) (int, error) {
	n, err := file.reader.Read(p)
	file.readBytes += n
	if file.readErr != nil {
		err, file.readErr = file.readErr, nil
	}
	return n, err
}

func (file *observedFile) Stat() (fs.FileInfo, error) {
	if file.statErr != nil {
		return nil, file.statErr
	}
	return file.File.Stat()
}

func (file *observedFile) Close() error {
	file.closes++
	_ = file.File.Close()
	return file.closeErr
}

type openFSFunc func(string) (fs.File, error)

func (open openFSFunc) Open(name string) (fs.File, error) { return open(name) }

func observedBase(t *testing.T, data []byte) (*observedFile, fs.FS) {
	t.Helper()
	// Deliberately report zero bytes for all files.
	file, err := (fstest.MapFS{"file": {}}).Open("file")
	if err != nil {
		t.Fatal(err)
	}
	observed := &observedFile{File: file, reader: bytes.NewReader(data)}
	return observed, openFSFunc(func(string) (fs.File, error) { return observed, nil })
}

func TestFSPrefixReadErrors(t *testing.T) {
	for _, data := range [][]byte{[]byte("short"), []byte("ordinary long file"), []byte(link.MagicPrefix)} {
		for _, readErr := range []error{io.ErrUnexpectedEOF, fs.ErrPermission} {
			file, base := observedBase(t, data)
			file.readErr = readErr
			wrapped := newLinkFS(t, base, eris.BlockSet{})
			opened, err := wrapped.Open("file")
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, readErr) {
				t.Errorf("prefix %q: got %v, want %v", data, err, readErr)
			}
			if file.closes != 1 {
				t.Errorf("close count = %d, want 1", file.closes)
			}
		}
	}
}

func TestFSFileOwnershipAndLimits(t *testing.T) {
	encoded, blocks := encodeFSLink(t, []byte("hello"))
	for _, test := range []struct {
		name                    string
		data                    []byte
		statErr, closeErr, want error
		limit                   int64
	}{
		{name: "valid link", data: encoded},
		{name: "invalid link", data: []byte(link.MagicPrefix), want: link.ErrInvalidLink},
		{name: "stat error", data: encoded, statErr: fs.ErrPermission, want: fs.ErrPermission},
		{name: "close error", data: encoded, closeErr: fs.ErrPermission, want: fs.ErrPermission},
		{name: "oversized link", data: encoded, limit: 32, want: link.ErrLinkTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, base := observedBase(t, test.data)
			file.statErr, file.closeErr = test.statErr, test.closeErr
			limit := test.limit
			if limit == 0 {
				limit = link.DefaultMaxLinkSize
			}
			wrapped, err := link.NewFSWithLimit(base, blocks, limit)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := wrapped.Open("file")
			if !errors.Is(err, test.want) {
				t.Fatalf("Open: %v, want %v", err, test.want)
			}
			if file.closes != 1 {
				t.Fatalf("close count = %d, want 1", file.closes)
			}
			if opened != nil {
				got, err := io.ReadAll(opened)
				if err != nil || string(got) != "hello" {
					t.Fatalf("resolved read: %q, %v", got, err)
				}
				_ = opened.Close()
				if file.closes != 1 {
					t.Fatalf("base closed again: %d", file.closes)
				}
			}
			if test.limit > 0 && int64(file.readBytes) > test.limit+1 {
				t.Fatalf("read %d bytes beyond limit %d", file.readBytes, test.limit)
			}
		})
	}
	file, base := observedBase(t, []byte("ordinary content"))
	opened, err := newLinkFS(t, base, blocks).Open("file")
	if err != nil {
		t.Fatal(err)
	}
	if file.closes != 0 {
		t.Fatal("ordinary file closed before caller finished")
	}
	got, err := io.ReadAll(opened)
	if err != nil || string(got) != "ordinary content" {
		t.Fatalf("ordinary read: %q, %v", got, err)
	}
	_ = opened.Close()
	if file.closes != 1 {
		t.Fatalf("ordinary close count = %d", file.closes)
	}
}

// A finite stall makes a missing progress check fail without hanging the test.
type stalledReader struct{ remaining int }

func (reader *stalledReader) Read([]byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	reader.remaining--
	return 0, nil
}

func TestFSStopsEmptyReads(t *testing.T) {
	encoded, blocks := encodeFSLink(t, []byte("hello"))
	for _, test := range []struct {
		name   string
		prefix []byte
		rest   []byte
		read   bool
	}{
		{"prefix", nil, encoded, false},
		{"link body", encoded[:link.HeaderSize], encoded[link.HeaderSize:], false},
		{"ordinary content", []byte("ordinary prefix"), []byte("tail"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, base := observedBase(t, nil)
			stall := &stalledReader{remaining: 1000}
			file.reader = io.MultiReader(bytes.NewReader(test.prefix), stall, bytes.NewReader(test.rest))
			wrapped := newLinkFS(t, base, blocks)
			var err error
			if test.read {
				var data []byte
				data, err = wrapped.ReadFile("file")
				if !bytes.Equal(data, test.prefix) {
					t.Errorf("partial data = %q, want %q", data, test.prefix)
				}
			} else {
				var opened fs.File
				opened, err = wrapped.Open("file")
				if opened != nil {
					_ = opened.Close()
				}
			}
			if !errors.Is(err, io.ErrNoProgress) {
				t.Fatalf("error = %v, want ErrNoProgress", err)
			}
			if stall.remaining == 0 || file.closes != 1 {
				t.Fatalf("remaining empty reads = %d, closes = %d", stall.remaining, file.closes)
			}
		})
	}
}

func TestFSToleratesTransientEmptyReads(t *testing.T) {
	encoded, blocks := encodeFSLink(t, []byte("hello"))
	readers := make([]io.Reader, 0, 2*len(encoded))
	for i := range encoded {
		readers = append(readers, &stalledReader{remaining: 10}, bytes.NewReader(encoded[i:i+1]))
	}
	file, base := observedBase(t, nil)
	file.reader = io.MultiReader(readers...)
	data, err := newLinkFS(t, base, blocks).ReadFile("file")
	if err != nil || string(data) != "hello" {
		t.Fatalf("read after transient stalls: %q, %v", data, err)
	}
}

type invalidCountReader struct {
	prefix   string
	negative bool
	err      error
}

func (reader *invalidCountReader) Read(p []byte) (int, error) {
	if len(reader.prefix) > 0 {
		n := copy(p, reader.prefix)
		reader.prefix = reader.prefix[n:]
		return n, nil
	}
	if reader.negative {
		return -1, reader.err
	}
	return len(p) + 1, reader.err
}

func TestFSRejectsInvalidReadCounts(t *testing.T) {
	for _, prefix := range []string{"", link.MagicPrefix, "ordinary content"} {
		for _, negative := range []bool{false, true} {
			for _, sourceErr := range []error{nil, io.EOF, fs.ErrPermission} {
				file, base := observedBase(t, nil)
				file.reader = &invalidCountReader{prefix: prefix, negative: negative, err: sourceErr}
				data, err := newLinkFS(t, base, eris.BlockSet{}).ReadFile("file")
				if !errors.Is(err, fs.ErrInvalid) {
					t.Fatalf("prefix %q, negative %v, source error %v: got %v", prefix, negative, sourceErr, err)
				}
				if sourceErr != nil && !errors.Is(err, sourceErr) {
					t.Fatalf("lost source error %v: %v", sourceErr, err)
				}
				want := ""
				if prefix == "ordinary content" {
					want = prefix
				}
				if string(data) != want || file.closes != 1 {
					t.Fatalf("partial data = %q, closes = %d", data, file.closes)
				}
			}
		}
	}
}

// EOF ends this read even if the base would return more data or errors later.
type finalReader struct {
	data  []byte
	reads int
}

func (reader *finalReader) Read(p []byte) (int, error) {
	reader.reads++
	if reader.reads > 1 {
		return 0, fs.ErrPermission
	}
	return copy(p, reader.data), io.EOF
}

func TestFSPreservesPrefixEOF(t *testing.T) {
	for _, data := range []string{"", "short", "ordinary!", link.MagicPrefix} {
		t.Run(data, func(t *testing.T) {
			file, base := observedBase(t, nil)
			reader := &finalReader{data: []byte(data)}
			file.reader = reader
			got, err := newLinkFS(t, base, eris.BlockSet{}).ReadFile("file")
			if data == link.MagicPrefix {
				if !errors.Is(err, link.ErrInvalidLink) {
					t.Fatalf("truncated link: %v", err)
				}
			} else if err != nil || string(got) != data {
				t.Fatalf("completed ordinary file: %q, %v", got, err)
			}
			if reader.reads != 1 || file.closes != 1 {
				t.Fatalf("reads = %d, closes = %d; want 1 each", reader.reads, file.closes)
			}
		})
	}
}

type rewindFile struct {
	*observedFile
	position int64
	err      error
}

func (file *rewindFile) Seek(int64, int) (int64, error) {
	return file.position, file.err
}

func TestFSRejectsFailedRewind(t *testing.T) {
	for _, test := range []struct {
		position int64
		err      error
		want     error
	}{
		{-1, nil, fs.ErrInvalid},
		{1, nil, fs.ErrInvalid},
		{0, fs.ErrPermission, fs.ErrPermission},
	} {
		observed, _ := observedBase(t, []byte("ordinary content"))
		base := openFSFunc(func(string) (fs.File, error) {
			return &rewindFile{observedFile: observed, position: test.position, err: test.err}, nil
		})
		file, err := newLinkFS(t, base, eris.BlockSet{}).Open("file")
		if file != nil {
			_ = file.Close()
			t.Error("failed rewind returned a file")
		}
		if !errors.Is(err, test.want) || observed.closes != 1 {
			t.Errorf("position %d, error %v: got %v, closes %d", test.position, test.err, err, observed.closes)
		}
	}
}

func TestFSPrefixFileChecksMetadata(t *testing.T) {
	for _, missing := range []bool{false, true} {
		observed, base := observedBase(t, []byte("ordinary content"))
		file, err := newLinkFS(t, base, eris.BlockSet{}).Open("file")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		want := fs.ErrPermission
		if missing {
			observed.File = nilInfoFile{File: observed.File}
			want = fs.ErrInvalid
		} else {
			observed.statErr = want
		}
		info, err := file.Stat()
		var pathErr *fs.PathError
		if info != nil || !errors.Is(err, want) || !errors.As(err, &pathErr) ||
			pathErr.Op != "stat" || pathErr.Path != "file" {
			t.Fatalf("Stat: %v, %v", info, err)
		}
	}
}

func TestFSPrefixFileCloseDoesNotReuseBase(t *testing.T) {
	for _, closeErr := range []error{nil, fs.ErrPermission} {
		observed, base := observedBase(t, []byte("ordinary content"))
		observed.closeErr = closeErr
		file, err := newLinkFS(t, base, eris.BlockSet{}).Open("file")
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("first Close: %v", err)
		}
		// If Stat reaches the base after Close, this error reveals that call.
		observed.statErr = fs.ErrPermission
		if info, err := file.Stat(); info != nil || !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("closed Stat: %v, %v", info, err)
		}
		if n, err := file.Read(make([]byte, 1)); n != 0 || !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("closed Read: %d, %v", n, err)
		}
		if err := file.Close(); !errors.Is(err, fs.ErrClosed) || observed.closes != 1 {
			t.Fatalf("second Close: %v, base closes %d", err, observed.closes)
		}
	}
}
