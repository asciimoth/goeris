// Package sourcehash computes ERIS Fetch Protocol v1 source digests.
package sourcehash

import (
	"encoding"

	"golang.org/x/crypto/blake2b"
)

// Prefix separates source hashes from other BLAKE2b-256 hashes.
const Prefix = "eris-fetch/source/v1\x00"

// Sum256 hashes a complete canonical description.
func Sum256(records string) [32]byte {
	return blake2b.Sum256([]byte(Prefix + records))
}

// WithPrefix hashes shared canonical records once. Each call to the returned
// function completes a separate source digest with the supplied suffix.
func WithPrefix(records string) func(string) [32]byte {
	h, _ := blake2b.New256(nil)
	_, _ = h.Write([]byte(Prefix + records))
	// Unkeyed BLAKE2b supports saving and restoring its state. A long file
	// name can occur in every mirror key of a single request.
	state, _ := h.(encoding.BinaryMarshaler).MarshalBinary()
	return func(suffix string) (digest [32]byte) {
		_ = h.(encoding.BinaryUnmarshaler).UnmarshalBinary(state)
		_, _ = h.Write([]byte(suffix))
		return [32]byte(h.Sum(digest[:0]))
	}
}
