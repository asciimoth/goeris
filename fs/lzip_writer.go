package fs

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"math"

	"github.com/ulikunitz/xz/lzma"
)

// writeLzip streams one deterministic Lzip member. The LZMA writer emits a
// 13-byte .lzma header, which Lzip replaces with its own six-byte header.
// The 20-byte Lzip trailer contains CRC-32, decoded size, and member size.
func writeLzip(output io.Writer, content io.Reader) (int64, error) {
	header := [...]byte{'L', 'Z', 'I', 'P', 1, 23} // 2^23 = 8 MiB dictionary.
	if err := writeAll(output, header[:]); err != nil {
		return 0, err
	}
	body := &lzipBodyWriter{output: output, discard: lzma.HeaderLen}
	// Set every bitstream parameter explicitly. Lzip requires lc=3, lp=0,
	// pb=2 and an end marker. The matcher and lookahead are part of this
	// library's encoding profile; changing either can change capabilities.
	// Fixed fixtures in lzip-conformance-v1.json guard dependency updates too.
	compressor, err := (lzma.WriterConfig{
		Properties: &lzma.Properties{LC: 3, LP: 0, PB: 2},
		DictCap:    LzipDictionarySize,
		BufSize:    4096,
		Matcher:    lzma.HashTable4,
		EOSMarker:  true,
		// Keep the size unknown even when the input reader exposes its length.
		SizeInHeader: false,
		Size:         0,
	}).NewWriter(body)
	if err != nil {
		return 0, err
	}
	checksum := crc32.NewIEEE()
	counted := &countReader{reader: io.TeeReader(content, checksum)}
	var buffer [32 << 10]byte
	for {
		n, err := readFullContent(counted, buffer[:])
		if err != nil && err != io.EOF {
			return 0, err
		}
		if n > 0 {
			if err := writeAll(compressor, buffer[:n]); err != nil {
				return 0, err
			}
		}
		if err != nil {
			break
		}
	}
	if err := compressor.Close(); err != nil {
		return 0, err
	}
	if body.discard != 0 || body.written > math.MaxInt64-26 {
		return 0, ErrInvalidCompression
	}
	var trailer [20]byte
	binary.LittleEndian.PutUint32(trailer[:4], checksum.Sum32())
	binary.LittleEndian.PutUint64(trailer[4:12], uint64(counted.size))
	binary.LittleEndian.PutUint64(trailer[12:], uint64(6+body.written+int64(len(trailer))))
	if err := writeAll(output, trailer[:]); err != nil {
		return 0, err
	}
	return counted.size, nil
}

type lzipBodyWriter struct {
	output  io.Writer
	discard int
	written int64
}

func (writer *lzipBodyWriter) Write(data []byte) (int, error) {
	dropped := min(writer.discard, len(data))
	writer.discard -= dropped
	data = data[dropped:]
	if len(data) == 0 {
		return dropped, nil
	}
	n, err := writer.output.Write(data)
	writer.written += int64(n)
	return dropped + n, err
}

func writeAll(output io.Writer, data []byte) error {
	n, err := output.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
