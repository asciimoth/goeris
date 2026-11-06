package coap

import (
	"context"
	"errors"
	"net"

	protocol "github.com/asciimoth/coaperation"
)

// RequesterFunc adapts a function to coaperation.Requester.
type RequesterFunc func(context.Context, *protocol.Message) (*protocol.Message, error)

// Request implements coaperation.Requester.
func (function RequesterFunc) Request(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	if function == nil {
		return nil, ErrNilRequester
	}
	return function(ctx, request)
}

// RemoteRequester adds a fixed remote address to requests before passing them
// to Requester. It is useful with a caller-managed UDP endpoint and performs
// no name resolution or network I/O itself.
type RemoteRequester struct {
	// Requester receives a clone of each request.
	Requester protocol.Requester
	// RemoteAddr is set on the cloned request.
	RemoteAddr net.Addr
}

// Request implements coaperation.Requester.
func (requester RemoteRequester) Request(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	if requester.Requester == nil || requester.RemoteAddr == nil {
		return nil, errors.New("eris coap: nil remote requester or address")
	}
	if request == nil {
		return nil, errors.New("eris coap: nil request")
	}
	clone := request.Clone()
	clone.RemoteAddr = requester.RemoteAddr
	return requester.Requester.Request(ctx, clone)
}

var (
	_ protocol.Requester = RequesterFunc(nil)
	_ protocol.Requester = RemoteRequester{}
)
