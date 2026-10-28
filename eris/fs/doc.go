// Package fs implements version 1 of the ERIS-FS format.
//
// The low-level API contains Index, Entry, the index codecs, Reader, and the
// abstract block source and sink interfaces. These values let applications
// control storage and transport.
//
// The high-level API contains Builder, FileSystem, and OpenFS. It exposes an
// encoded image through the standard io/fs interfaces without extraction.
// OpenFile also gives typed access to seeking and random-access reads.
//
// The package operates only on io.Reader values and abstract ERIS block
// stores. It does not access the host file system, network, clock, or random
// source.
package fs

const (
	// SpecificationVersion is the ERIS-FS format version.
	SpecificationVersion = 1
	// CBORTag is the registered ERIS-FS CBOR tag.
	CBORTag uint64 = 1701996916
	// MIMEType is the media type for an ERIS-FS index.
	MIMEType = "application/x-eris-fs+cbor"
	// DefaultMaxIndexSize is the largest decoded index accepted by DecodeIndex
	// and OpenFS. Use the limit-specific functions when another bound is needed.
	DefaultMaxIndexSize int64 = 64 << 20
)
