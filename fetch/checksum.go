package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"strings"

	"github.com/asciimoth/goeris/internal/streamio"
	"golang.org/x/crypto/blake2b"
)

// Checksum is a publisher's assertion about the downloaded resource bytes.
type Checksum struct {
	algorithm string
	digest    [32]byte
}

// ParseChecksum accepts the two v1 algorithms and their lowercase hex digests.
func ParseChecksum(s string) (Checksum, error) {
	alg, h, _ := strings.Cut(s, ":")
	if !tokenPattern.MatchString(alg) || !lowerHex.MatchString(h) {
		return Checksum{}, Failure{"invalid-source", "checksum"}
	}
	if alg != "sha256" && alg != "blake2b-256" {
		return Checksum{}, Failure{"unsupported-option", "checksum"}
	}
	var c Checksum
	if len(h) != 64 {
		return c, Failure{"invalid-source", "checksum"}
	}
	c.algorithm = alg
	_, _ = hex.Decode(c.digest[:], []byte(h))
	return c, nil
}

// Verify reads the candidate to EOF. A failed read gives unavailable. A
// complete stream with a different digest gives integrity. After 100
// consecutive empty reads it reports io.ErrNoProgress.
func (c Checksum) Verify(r io.Reader) error {
	var h hash.Hash
	switch c.algorithm {
	case "sha256":
		h = sha256.New()
	case "blake2b-256":
		h, _ = blake2b.New256(nil)
	default:
		return Failure{"invalid-source", "checksum"}
	}
	if r == nil {
		return Failure{Code: "unavailable"}
	}
	var buffer [32 << 10]byte
	for {
		n, err := streamio.ReadFull(context.Background(), r, buffer[:])
		if err != nil && err != io.EOF {
			return errors.Join(Failure{Code: "unavailable"}, err)
		}
		_, _ = h.Write(buffer[:n])
		if err == io.EOF {
			break
		}
	}
	if !bytes.Equal(h.Sum(nil), c.digest[:]) {
		return Failure{Code: "integrity"}
	}
	return nil
}
