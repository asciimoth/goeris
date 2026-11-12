// Package eris implements version 1.0.0 of the Encoding for Robust
// Immutable Storage (ERIS) specification.
//
// See the [ERIS 1.0.0 specification].
//
// The package contains only deterministic, in-memory operations. It does not
// read content, generate secrets, or store and transport blocks. Applications
// must do those operations outside this package.
//
// [ERIS 1.0.0 specification]: https://github.com/asciimoth/goeris/blob/master/specs/eris-v1.0.0.html
package eris

// SpecificationVersion is the ERIS specification version implemented here.
const SpecificationVersion = "1.0.0"
