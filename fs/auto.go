package fs

import (
	"bytes"
	"encoding/binary"
	"io"
)

const (
	// AutoCompressionVersion identifies the fixed automatic compression policy.
	AutoCompressionVersion = 1
	// AutoCompressionSampleSize is the number of leading bytes used by the policy.
	AutoCompressionSampleSize = 32 << 10
)

// ChooseCompression applies the ERIS-FS auto compression policy v1. Supply the
// complete file if it is shorter than AutoCompressionSampleSize, or at least
// that many leading bytes otherwise. Bytes after the sample are ignored.
// Files below 1 KiB stay uncompressed. Other files use Lzip when a fixed LZ77
// probe, with 26 bytes of container overhead, saves at least one eighth of the
// sample. The probe does not depend on the installed Lzip encoder.
//
// The exact algorithm and test vectors are in specs/eris-fs-auto-compression.md.
// This is a size heuristic; use Uncompressed when a file needs random access.
func ChooseCompression(prefix []byte) Compression {
	prefix = prefix[:min(len(prefix), AutoCompressionSampleSize)]
	if len(prefix) < 1024 {
		return Uncompressed
	}
	if 8*(autoProbeSize(prefix)+26) <= 7*len(prefix) {
		return Lzip
	}
	return Uncompressed
}

// AddAutoFile selects compression with policy v1, then encodes the whole file.
// It reads the source once, buffers at most 32 KiB for the decision, and replays
// those bytes into AddFile or AddCompressedFile. It does not seek the source or
// store probe blocks. The decision is fixed after the sample, even if later
// bytes have different compression properties. A failed call adds no entry,
// though the sink can retain blocks from the incomplete file.
func (builder *Builder) AddAutoFile(path string, content io.Reader, executable bool) error {
	if builder == nil || builder.sink == nil || content == nil {
		return ErrInvalidEntry
	}
	components, err := splitPath(path)
	if err != nil {
		return err
	}
	if err := builder.checkPath(components); err != nil {
		return err
	}
	prefix := make([]byte, AutoCompressionSampleSize)
	n, err := readFullContent(content, prefix)
	if err != nil && err != io.EOF {
		return err
	}
	prefix = prefix[:n]
	var input io.Reader = bytes.NewReader(prefix)
	if err != io.EOF {
		input = io.MultiReader(input, content)
	}
	// Replay the sample, but do not read the source again after EOF.
	if ChooseCompression(prefix) == Lzip {
		return builder.AddCompressedFile(path, input, executable)
	}
	return builder.AddFile(path, input, executable)
}

// autoProbeSize counts the virtual token stream defined by policy v1. Its
// input is at most 32 KiB. A literal run of 1..128 bytes costs length+1 bytes;
// a match of 4..131 bytes costs 3 bytes. Only the most recent position in each
// hash slot is examined. Positions inside matches are inserted in order.
func autoProbeSize(sample []byte) int {
	var latest [1 << 14]uint16 // Position+1; zero means no candidate.
	encoded, literals := 0, 0
	for position := 0; position < len(sample); {
		length := 0
		if position+4 <= len(sample) {
			key := binary.LittleEndian.Uint32(sample[position:])
			previous := latest[autoProbeHash(key)]
			if previous != 0 {
				candidate := int(previous) - 1
				if binary.LittleEndian.Uint32(sample[candidate:]) == key {
					length = 4
					for length < min(131, len(sample)-position) && sample[candidate+length] == sample[position+length] {
						length++
					}
				}
			}
		}
		if length >= 4 {
			if literals != 0 {
				encoded += literals + 1
				literals = 0
			}
			encoded += 3
		} else {
			length = 1
			literals++
			if literals == 128 {
				encoded += 129
				literals = 0
			}
		}
		for end := position + length; position < end; position++ {
			if position+4 <= len(sample) {
				key := binary.LittleEndian.Uint32(sample[position:])
				latest[autoProbeHash(key)] = uint16(position + 1)
			}
		}
	}
	if literals != 0 {
		encoded += literals + 1
	}
	return encoded
}

func autoProbeHash(key uint32) uint32 {
	return (key * 0x9e3779b1) >> 18
}
