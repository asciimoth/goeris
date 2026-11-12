package fs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	stdfs "io/fs"
	"sync"

	"github.com/asciimoth/goeris/internal/erisreader"
	"github.com/ulikunitz/xz/lzma"
)

// ErrInvalidCompression means that a Lzip stream is malformed, has an invalid
// checksum, or exceeds MaxLzipDictionarySize.
var ErrInvalidCompression = errors.New("eris-fs: invalid Lzip content")

// MaxLzipDictionarySize limits the memory used by each Lzip decoder. The
// ERIS-FS recommendation uses 8 MiB; this reader accepts up to 64 MiB.
const MaxLzipDictionarySize = 64 << 20

// lzipFile reads members in order without buffering compressed or decoded
// files. Lzip uses LZMA1 with fixed properties and a mandatory end marker.
// See https://www.nongnu.org/lzip/manual/lzip_manual.html#File-format.
type lzipFile struct {
	mu         sync.Mutex
	node       *node
	raw        *erisreader.Reader
	input      countReader
	decoder    *lzma.Reader
	start      int64
	memberSize uint64
	crc        uint32
	total      int64
	err        error
	closed     bool
}

func openLzipFile(item *node) (*lzipFile, error) {
	raw, err := erisreader.NewReader(item.capability, item.source)
	if err != nil {
		return nil, err
	}
	// LZMA requests individual bytes. Buffer them so each request does not
	// traverse the ERIS tree or acquire the underlying reader's mutex.
	file := &lzipFile{node: item, raw: raw, input: countReader{reader: bufio.NewReader(raw)}}
	if err := file.nextMember(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return file, nil
}

func (file *lzipFile) nextMember() error {
	file.start = file.input.size
	var header [6]byte
	if _, err := io.ReadFull(&file.input, header[:]); err != nil {
		return err
	}
	if string(header[:4]) != "LZIP" || header[4] != 1 {
		return ErrInvalidCompression
	}
	dict := uint32(1) << (header[5] & 31)
	dict -= (dict / 16) * uint32(header[5]>>5)
	if dict < 4096 || dict > MaxLzipDictionarySize {
		return ErrInvalidCompression
	}
	// A member cannot need more history than the remaining file output.
	// Keep one extra byte for the excess-output probe, and the LZMA minimum.
	// Check the advertised dictionary above before reducing it: an invalid
	// header must still fail. This also bounds decoder read-ahead for small
	// files and avoids large allocations for repeated empty members.
	if remaining := file.node.size - file.total; remaining < int64(dict) {
		dict = uint32(max(4096, remaining+1))
	}
	var synthetic [lzma.HeaderLen]byte
	synthetic[0] = 0x5d // lc=3, lp=0, pb=2.
	binary.LittleEndian.PutUint32(synthetic[1:5], dict)
	for i := 5; i < len(synthetic); i++ {
		synthetic[i] = 0xff // Unknown member size: require the end marker.
	}
	decoder, err := (lzma.ReaderConfig{DictCap: MaxLzipDictionarySize}).NewReader(
		io.MultiReader(bytes.NewReader(synthetic[:]), &file.input))
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	file.decoder, file.memberSize, file.crc = decoder, 0, 0
	return nil
}

func (file *lzipFile) read(p []byte) (int, error) {
	for {
		if file.decoder == nil {
			if err := file.nextMember(); err != nil {
				return 0, err
			}
		}
		n, err := file.decoder.Read(p)
		file.memberSize += uint64(n)
		file.crc = crc32.Update(file.crc, crc32.IEEETable, p[:n])
		if err == io.EOF {
			var trailer [20]byte
			if _, err = io.ReadFull(&file.input, trailer[:]); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return n, err
			}
			if !file.decoder.EOSMarker() || binary.LittleEndian.Uint32(trailer[:4]) != file.crc ||
				binary.LittleEndian.Uint64(trailer[4:12]) != file.memberSize ||
				binary.LittleEndian.Uint64(trailer[12:]) != uint64(file.input.size-file.start) {
				return n, ErrInvalidCompression
			}
			file.decoder = nil
			err = nil
		}
		if n != 0 || err != nil {
			return n, err
		}
	}
}

func (file *lzipFile) Read(p []byte) (int, error) {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.closed {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if file.err != nil {
		return 0, file.err
	}
	remaining := file.node.size - file.total
	p = p[:min(int64(len(p)), remaining)]
	var n int
	var err error
	if len(p) > 0 {
		n, err = file.read(p)
		file.total += int64(n)
	}
	if err == nil && file.total == file.node.size {
		// Finish the member and verify its trailer even if the caller stops at
		// the declared size. A one-byte probe also detects excess output.
		var probe [1]byte
		var extra int
		extra, err = file.read(probe[:])
		if extra != 0 {
			err = ErrSizeMismatch
		}
	}
	if err == io.EOF && file.total != file.node.size {
		err = ErrSizeMismatch
	}
	file.err = err
	return n, err
}

func (file *lzipFile) Stat() (stdfs.FileInfo, error) {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.closed {
		return nil, ErrClosed
	}
	return file.node.info()
}

func (file *lzipFile) Close() error {
	file.mu.Lock()
	defer file.mu.Unlock()
	file.closed = true
	file.decoder = nil
	file.input.reader = nil
	return file.raw.Close()
}
