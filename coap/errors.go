package coap

import (
	"errors"
	"fmt"

	protocol "github.com/asciimoth/coaperation"
)

var (
	// ErrInvalidResponseOption means that a response contains an unsupported
	// critical option or an invalid transfer option.
	ErrInvalidResponseOption = errors.New("eris coap: invalid response option")
	// ErrInvalidStoreURL means that a URL does not identify a supported CoAP
	// block store.
	ErrInvalidStoreURL = errors.New("eris coap: invalid store URL")
	// ErrNilRequester means that NewClient received no CoAP requester.
	ErrNilRequester = errors.New("eris coap: nil requester")
	// ErrInvalidReference means that a request has no valid block reference.
	ErrInvalidReference = errors.New("eris coap: invalid block reference")
	// ErrUnexpectedContentFormat means that a response does not have the
	// content format required by its resource.
	ErrUnexpectedContentFormat = errors.New("eris coap: unexpected content format")
	// ErrResponseTooLarge means that a response body exceeds the ERIS block
	// limit, its options exceed MaxResponseOptionSize or
	// MaxTransferResponseOptionSize, or its token exceeds eight bytes.
	ErrResponseTooLarge = errors.New("eris coap: response is too large")
	// ErrNoHopLimit means that a request cannot be forwarded because it has no
	// Hop-Limit option.
	ErrNoHopLimit = errors.New("eris coap: request has no Hop-Limit")
	// ErrInvalidHopLimit means that a request has an invalid or repeated
	// Hop-Limit option.
	ErrInvalidHopLimit = errors.New("eris coap: invalid Hop-Limit")
	// ErrInvalidProxyURI means that a request configuration has no valid,
	// absolute Proxy-Uri value without a fragment or has no Hop-Limit.
	ErrInvalidProxyURI = errors.New("eris coap: invalid Proxy-Uri")
	// ErrHopLimitReached means that forwarding would exhaust Hop-Limit.
	ErrHopLimitReached = errors.New("eris coap: Hop-Limit reached")
	// ErrInvalidAdvertisement means that discovery data does not describe a
	// valid block store or proxy.
	ErrInvalidAdvertisement = errors.New("eris coap: invalid advertisement")
)

// ResponseError reports an unsuccessful or unexpected CoAP response code.
type ResponseError struct {
	// Code is the response code returned by the remote endpoint.
	Code protocol.Code
}

// Error implements error.
func (err *ResponseError) Error() string {
	return fmt.Sprintf("eris coap: response code %s", err.Code)
}
