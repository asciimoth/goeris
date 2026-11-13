// Package eris implements version 1.0.0 of the Encoding for Robust
// Immutable Storage (ERIS) specification.
//
// See the [ERIS 1.0.0 specification].
//
// The package provides deterministic encoding and decoding, storage interfaces,
// an in-memory BlockSet, and a CombinedBlockStore that tries caller-supplied
// stores in order. Applications supply persistent storage, network transports,
// content I/O, and secret generation.
//
// [ERIS 1.0.0 specification]: https://github.com/asciimoth/goeris/blob/master/specs/eris-v1.0.0.html
package eris

// SpecificationVersion is the ERIS specification version implemented here.
const SpecificationVersion = "1.0.0"
