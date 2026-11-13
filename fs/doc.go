// Package fs writes version 2 of the [ERIS-FS recommendation] and reads
// versions 1 and 2. It also reads the text symlink targets and length-first
// map order used by earlier releases of this library.
//
// The low-level API contains Index, Entry, and the index codecs. It uses
// the block source and sink interfaces in the eris package so applications can
// control storage and transport.
//
// The high-level API contains Builder, FileSystem, and OpenFS. It exposes an
// encoded image through the standard io/fs interfaces without extraction.
// OpenFile also gives typed access to seeking and random-access reads for
// uncompressed files. AddCompressedFile creates deterministic Lzip content;
// compressed files are read sequentially with Open.
// AddAutoFile selects compression with the fixed ERIS-FS-AUTO-1 sample policy.
// ChooseCompression exposes the same decision without encoding a file.
//
// The package operates only on io.Reader values and abstract ERIS block
// stores. It does not access the host file system, network, clock, or random
// source.
// Encoding returns io.ErrNoProgress after 100 consecutive empty reads from
// an input. Temporary empty reads below this limit do not change the output.
// Encoding stops at the first io.EOF, including EOF returned with data.
//
// For untrusted images, DecodeIndex and OpenFS limit the encoded CBOR bytes
// of each index. This limit does not bound the memory used by the parsed tree
// or the total size of nested indexes. ParseIndex and UnmarshalBinary do not
// apply a size limit to their input slices. Applications must bound those
// inputs before parsing them.
//
// File sizes from version 2 indexes are untrusted metadata. Stat does not
// validate file content. Read through EOF to check the complete file, including
// compression checksums. ReadFile has no content size limit; use Open with a
// bounded reader when only a limited amount of output is needed. Block sources
// must verify that each returned block matches its requested ERIS reference,
// as required by eris.BlockSource.
//
// An output limit does not limit all input work. Opening a compressed file or
// finding a version 1 file's size scans its ERIS tree. A Lzip stream can also
// contain many empty members. Applications that need a total I/O budget or a
// deadline must enforce it in their block source and return an error when it
// is reached. MaxLzipDictionarySize limits each decoder, not total process
// memory or the number of open files.
//
// Paths use io/fs rules. Backslashes and names that are special on a host
// operating system are not converted or filtered. Applications that extract
// an image must check native paths and symlink targets before writing them.
// Open follows links only inside the image, but Sub does not confine links to
// the selected subdirectory.
//
// [ERIS-FS recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/eris-fs/index.md
package fs

const (
	// SpecificationVersion is the ERIS-FS format version.
	SpecificationVersion = 2
	// LegacySpecificationVersion is the earlier format version that readers support.
	LegacySpecificationVersion = 1
	// LzipDictionarySize is the dictionary size used by this library's encoder.
	LzipDictionarySize = 8 << 20
	// LzipLengthTag marks an uncompressed file length for Lzip content.
	LzipLengthTag uint64 = 1280985424
	// CBORTag is the registered ERIS-FS CBOR tag.
	CBORTag uint64 = 1701996916
	// MagicPrefix is the five-byte CBOR tag prefix of an ERIS-FS index.
	MagicPrefix = "\xdaerit"
	// MIMEType is the media type for an ERIS-FS index.
	MIMEType = "application/x-eris-fs+cbor"
	// DefaultMaxIndexSize is the largest decoded index accepted by DecodeIndex
	// and OpenFS. Use the limit-specific functions when another bound is needed.
	DefaultMaxIndexSize int64 = 64 << 20
)
