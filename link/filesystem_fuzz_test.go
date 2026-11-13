package link_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

// Exercise detection, size limits, short reads, and lazy block access together.
func FuzzFSOpen(f *testing.F) {
	capability, _, err := eris.Encode([]byte("hello"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		f.Fatal(err)
	}
	encoded, err := (link.Link{Capability: capability, Size: 5}).MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
	for _, data := range [][]byte{nil, []byte("ordinary content"), []byte(link.MagicPrefix), encoded} {
		for _, seekable := range []bool{false, true} {
			f.Add(data, uint16(1024), uint8(1), seekable)
		}
	}
	for _, limit := range []int{1, link.HeaderSize - 1, link.HeaderSize, len(encoded) - 1, len(encoded)} {
		f.Add(encoded, uint16(limit-1), uint8(3), false)
	}
	f.Fuzz(func(t *testing.T, data []byte, rawLimit uint16, chunk uint8, seekable bool) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		limit := int64(rawLimit) + 1
		source := &countingSource{err: eris.ErrMissingBlock}
		// The non-seeking base reports a zero size and supplies short reads.
		var observed *observedFile
		var base fs.FS = fstest.MapFS{"file": {Data: data}}
		if !seekable {
			observed, base = observedBase(t, nil)
			observed.reader = &chunkReader{reader: bytes.NewReader(data), size: int(chunk) + 1}
		}
		files, err := link.NewFSWithLimit(base, source, limit)
		if err != nil {
			t.Fatal(err)
		}
		file, openErr := files.Open("file")
		if source.calls.Load() != 0 {
			t.Fatal("Open fetched linked content")
		}
		if !seekable && observed.readBytes > max(link.HeaderSize, int(limit)+1) {
			t.Fatalf("Open read %d bytes with limit %d", observed.readBytes, limit)
		}
		if !seekable && (openErr != nil || link.HasHeader(data)) && observed.closes != 1 {
			t.Fatalf("base file closed %d times", observed.closes)
		}
		if file != nil {
			defer func() { _ = file.Close() }()
		}
		if !link.HasHeader(data) {
			if openErr != nil {
				t.Fatalf("ordinary file rejected: %v", openErr)
			}
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("ordinary data changed: %v", err)
			}
			return
		}
		if int64(len(data)) > limit {
			if !errors.Is(openErr, link.ErrLinkTooLarge) {
				t.Fatalf("oversized link: %v", openErr)
			}
			return
		}
		metadata, parseErr := link.Parse(data)
		if parseErr != nil || metadata.Size > math.MaxInt64 {
			if !errors.Is(openErr, link.ErrInvalidLink) {
				t.Fatalf("invalid link: %v", openErr)
			}
			return
		}
		if openErr != nil {
			// A valid codec value can declare a size that cannot fit its tree.
			if !errors.Is(openErr, link.ErrSizeMismatch) {
				t.Fatalf("unexpected Open error: %v", openErr)
			}
			return
		}
		info, err := file.Stat()
		if err != nil || info.Size() != int64(metadata.Size) {
			t.Fatalf("resolved metadata changed: %v", err)
		}
		if n, err := file.Read(make([]byte, 1)); n != 0 || !errors.Is(err, eris.ErrMissingBlock) {
			t.Fatalf("missing content: %d, %v", n, err)
		}
	})
}

type chunkReader struct {
	reader *bytes.Reader
	size   int
}

func (reader *chunkReader) Read(p []byte) (int, error) {
	return reader.reader.Read(p[:min(len(p), reader.size)])
}
