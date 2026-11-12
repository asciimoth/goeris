package coap

import protocol "github.com/asciimoth/coaperation"

// rejectCriticalOptions applies RFC 7252 section 5.4.1. Unknown elective
// options can be ignored, but unknown critical options must stop processing.
// In particular, an unsupported write condition must not become an
// unconditional write. Block1 and Block2 are handled by the transfer wrapper.
func rejectCriticalOptions(request *protocol.Message) (*protocol.Message, bool) {
	for _, option := range request.Options {
		if option.Number&1 == 0 {
			continue
		}
		switch option.Number {
		case protocol.OptionURIHost, protocol.OptionURIPort, protocol.OptionURIPath,
			protocol.OptionURIQuery, protocol.OptionBlock1, protocol.OptionBlock2,
			protocol.OptionProxyURI:
			continue
		}
		if request.Type == protocol.NonConfirmable {
			return nil, true
		}
		return &protocol.Message{Code: protocol.BadOption}, true
	}
	return nil, false
}
