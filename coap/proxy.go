package coap

import (
	"errors"

	protocol "github.com/asciimoth/coaperation"
)

// PrepareForward clones request and decrements its Hop-Limit. It preserves all
// URI and Proxy-Uri options and clears fields scoped to the incoming exchange.
// A request without Hop-Limit must be answered from local storage and is not
// eligible for forwarding. The caller must select the upstream remote address.
func PrepareForward(request *protocol.Message) (*protocol.Message, error) {
	if request == nil {
		return nil, ErrInvalidHopLimit
	}
	if err := validateHopLimit(request); err != nil {
		return nil, err
	}
	option, present := request.Option(protocol.OptionHopLimit)
	if !present {
		return nil, ErrNoHopLimit
	}
	// validateHopLimit already checked cardinality and representation.
	decremented, code, _ := protocol.DecrementHopLimit(option)
	if code == protocol.HopLimitReached {
		return nil, ErrHopLimitReached
	}
	clone := request.Clone()
	options := clone.Options[:0]
	for _, option := range clone.Options {
		if option.Number != protocol.OptionHopLimit {
			options = append(options, option)
		}
	}
	clone.Options = append(options, decremented)
	sortOptions(clone.Options)
	clone.Type = protocol.Confirmable
	clone.MessageID = 0
	clone.Token = nil
	clone.LocalAddr = nil
	clone.RemoteAddr = nil
	clone.Multicast = false
	clone.Separate = false
	return clone, nil
}

// ForwardErrorResponse maps a forwarding-policy error to a CoAP response. It
// returns false when err is not a forwarding-policy error.
func ForwardErrorResponse(err error) (*protocol.Message, bool) {
	switch {
	case errors.Is(err, ErrNoHopLimit):
		return &protocol.Message{Code: protocol.NotFound}, true
	case errors.Is(err, ErrInvalidHopLimit):
		return &protocol.Message{Code: protocol.BadRequest}, true
	case errors.Is(err, ErrHopLimitReached):
		return &protocol.Message{Code: protocol.HopLimitReached}, true
	default:
		return nil, false
	}
}
