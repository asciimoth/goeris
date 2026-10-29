package http

import (
	"errors"
	"net/url"
	"strings"

	"github.com/asciimoth/eris-tools/eris"
)

const (
	// N2RPath is the RFC 2169 Name-to-Resource endpoint used by ERIS.
	N2RPath = "/uri-res/N2R"
	// ContentType is the media type used for encrypted blocks and for decoded
	// content when the content type is not known.
	ContentType = "application/octet-stream"
)

var (
	// ErrNilHTTPClient means that NewClient received no HTTP client.
	ErrNilHTTPClient = errors.New("eris http: nil HTTP client")
	// ErrNilBaseURL means that a URL function received no base URL.
	ErrNilBaseURL = errors.New("eris http: nil base URL")
	// ErrInvalidURN means that input is not a supported canonical ERIS URN.
	ErrInvalidURN = errors.New("eris http: invalid URN")
	// ErrResponseTooLarge means that a block response is larger than an ERIS
	// block.
	ErrResponseTooLarge = errors.New("eris http: response is too large")
	// ErrUnexpectedContentType means that a block response does not have the
	// required application/octet-stream media type.
	ErrUnexpectedContentType = errors.New("eris http: unexpected content type")
)

// ResourceURL returns the N2R URL for urn at the origin in base. It does not
// change base. The URN is put directly in the query, as required by RFC 2169.
func ResourceURL(base *url.URL, urn string) (*url.URL, error) {
	if base == nil {
		return nil, ErrNilBaseURL
	}
	if !validURN(urn) {
		return nil, ErrInvalidURN
	}
	endpoint := base.ResolveReference(&url.URL{Path: N2RPath})
	endpoint.RawQuery = urn
	return endpoint, nil
}

func validURN(urn string) bool {
	if strings.HasPrefix(urn, "urn:blake2b:") {
		_, err := eris.ParseReferenceURN(urn)
		return err == nil
	}
	if strings.HasPrefix(urn, "urn:eris:") {
		_, err := eris.ParseURN(urn)
		return err == nil
	}
	return false
}

func supportedURN(urn string) bool {
	return strings.HasPrefix(urn, "urn:eris:") || strings.HasPrefix(urn, "urn:blake2b:")
}
