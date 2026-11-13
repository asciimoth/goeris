package coap

import protocol "github.com/asciimoth/coaperation"

// MaxResponseOptionSize bounds response metadata before reassembly copies it.
// Each option uses eight bytes of the limit in addition to its value.
const MaxResponseOptionSize = 64 << 10

// validateResponseOptions runs before reassembly can remove transfer options.
func validateResponseOptions(response *protocol.Message) error {
	seen := make(map[protocol.OptionNumber]bool, 4)
	remaining := MaxResponseOptionSize
	for _, option := range response.Options {
		if remaining < 8 || len(option.Value) > remaining-8 {
			return ErrResponseTooLarge
		}
		remaining -= 8 + len(option.Value)
		if option.Number&1 != 0 && option.Number != protocol.OptionBlock1 && option.Number != protocol.OptionBlock2 {
			return ErrInvalidResponseOption
		}
		maximum := 4
		switch option.Number {
		case protocol.OptionContentFormat:
			maximum = 2
		case protocol.OptionBlock1, protocol.OptionBlock2:
			maximum = 3
			if _, err := protocol.ParseBlock(option.Value); err != nil {
				return ErrInvalidResponseOption
			}
		case protocol.OptionSize1, protocol.OptionSize2:
		default:
			continue
		}
		if seen[option.Number] || len(option.Value) > maximum {
			if option.Number == protocol.OptionContentFormat {
				return ErrUnexpectedContentFormat
			}
			return ErrInvalidResponseOption
		}
		seen[option.Number] = true
	}
	return nil
}

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
