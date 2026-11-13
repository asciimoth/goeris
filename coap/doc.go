// Package coap implements ERIS block transport over the Constrained
// Application Protocol.
//
// The package implements the [ERIS over CoAP recommendation].
//
// It performs no direct network or file-system I/O. Clients use a
// caller-supplied coaperation Requester. Handlers use caller-supplied block
// storage and can be installed in caller-managed UDP or TCP endpoints.
//
// Client sends the RFC 8768 Hop-Limit option for GET and PUT. A zero Go TTL
// uses RequestConfig.HopLimit or, if unset, eris.DefaultTTL (16). Other values
// decrease by one before sending; 1 returns eris.ErrTTLExpired without network
// I/O. Every fragment of a block-wise transfer uses the same outgoing limit.
// Handler passes the received value unchanged to storage. A missing option
// permits local access only (1), as required by the ERIS CoAP recommendation
// and implemented by Kapla. Invalid values produce 4.00 Bad Request. Exhausted
// downstream TTLs produce 5.08 Hop Limit Reached. ResponseError matches
// eris.ErrTTLExpired via errors.Is; ErrHopLimitReached is the same error.
// NewGetRequest and NewPutRequest use RequestConfig.HopLimit as the wire value
// without decrementing it. PrepareForward handles raw-message forwarding.
//
// [ERIS over CoAP recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/coap/index.md
package coap
