// Package httpttl implements the optional ERIS request hop limit for HTTP.
package httpttl

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/asciimoth/goeris"
)

// Header is an optional request header, not persistent object metadata.
const Header = "Eris-TTL"

// Parse returns a received budget. A missing header permits local access only.
// Reject ambiguous values instead of allowing a forwarding budget to reset.
func Parse(headers http.Header) (eris.TTL, error) {
	var values []string
	for name, entries := range headers {
		if strings.EqualFold(name, Header) {
			values = append(values, entries...)
		}
	}
	if len(values) == 0 {
		return eris.LocalTTL, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, errors.New("eris: invalid TTL header")
	}
	for _, ch := range values[0] {
		if ch < '0' || ch > '9' {
			return 0, errors.New("eris: invalid TTL header")
		}
	}
	n, err := strconv.ParseUint(values[0], 10, 8)
	if err != nil || n == 0 {
		return 0, errors.New("eris: invalid TTL header")
	}
	return eris.TTL(n), nil
}

// Set consumes one forward and sets the outgoing header.
func Set(headers http.Header, ttl eris.TTL) error {
	next, err := ttl.Forward()
	if err != nil {
		return err
	}
	headers.Set(Header, strconv.Itoa(int(next)))
	return nil
}
