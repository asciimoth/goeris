package eris

import "errors"

// TTL limits the number of network forwards along each path of a block
// operation. It is not a duration or a block expiry time. Zero means that no
// limit was specified. One permits local storage access only. Larger values
// permit forwarding.
//
// Terminal stores, such as BlockSet, may ignore TTL. Middleware must pass it
// unchanged to every downstream operation, including retries and copied writes.
// Network clients must call Forward once before sending a request. Servers pass
// the received value unchanged to storage. A missing wire value means LocalTTL,
// so a forwarded request cannot reset its budget to the client default.
type TTL uint8

const (
	// NoTTL lets callers leave the limit unspecified. Network clients insert
	// a finite default; this value does not request unlimited forwarding.
	NoTTL TTL = 0
	// LocalTTL permits local access but prevents another network request.
	LocalTTL TTL = 1
	// DefaultTTL is the initial wire value used by network clients for NoTTL.
	DefaultTTL TTL = 16
)

// ErrTTLExpired means that an operation cannot make another network forward.
var ErrTTLExpired = errors.New("eris: TTL expired")

// Forward returns the TTL to send to the next node. An unspecified TTL starts
// a new request with DefaultTTL. A specified TTL decreases by one. LocalTTL
// returns ErrTTLExpired without permitting any network I/O.
func (ttl TTL) Forward() (TTL, error) {
	switch ttl {
	case NoTTL:
		return DefaultTTL, nil
	case LocalTTL:
		return 0, ErrTTLExpired
	default:
		return ttl - 1, nil
	}
}
