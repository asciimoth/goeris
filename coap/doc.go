// Package coap implements ERIS block transport over the Constrained
// Application Protocol.
//
// The package implements the [ERIS over CoAP recommendation].
//
// It performs no direct network or file-system I/O. Clients use a
// caller-supplied coaperation Requester. Handlers use caller-supplied block
// storage and can be installed in caller-managed UDP or TCP endpoints.
//
// [ERIS over CoAP recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/coap/index.md
package coap
