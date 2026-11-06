// Package cborwire provides CBOR wire encoding shared by ERIS formats.
package cborwire

import (
	"encoding/binary"
	"math"
)

// AppendHead appends a CBOR major type and argument in preferred form.
func AppendHead(dst []byte, major byte, value uint64) []byte {
	switch {
	case value < 24:
		return append(dst, major<<5|byte(value))
	case value <= math.MaxUint8:
		return append(dst, major<<5|24, byte(value))
	case value <= math.MaxUint16:
		dst = append(dst, major<<5|25, 0, 0)
		binary.BigEndian.PutUint16(dst[len(dst)-2:], uint16(value))
	case value <= math.MaxUint32:
		dst = append(dst, major<<5|26, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(dst[len(dst)-4:], uint32(value))
	default:
		dst = append(dst, major<<5|27, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(dst[len(dst)-8:], value)
	}
	return dst
}
