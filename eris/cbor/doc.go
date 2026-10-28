// Package cbor implements the ERIS CBOR serialization format.
//
// The format transports ERIS blocks, block references, reference-to-block
// maps, and read capabilities. This package performs only deterministic,
// in-memory operations. It does not access files, networks, or other streams.
//
// The format is specified at https://eris.codeberg.page/eer/cbor.xml.
package cbor

const (
	// Tag is the registered CBOR tag for an ERIS CBOR array or sequence marker.
	Tag uint64 = 1701996915
	// SelfDescribedTag identifies self-described CBOR as specified by RFC 8949.
	SelfDescribedTag uint64 = 55799
)
