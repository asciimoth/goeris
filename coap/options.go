package coap

import protocol "github.com/asciimoth/coaperation"

// MaxResponseOptionSize bounds response metadata before reassembly copies it.
// Each option uses eight bytes of the limit in addition to its value.
const MaxResponseOptionSize = 64 << 10

// MaxTransferResponseOptionSize bounds the sum of response options in one
// client transfer, including options repeated on Block1 and Block2 messages.
const MaxTransferResponseOptionSize = 1 << 20

// The transport library supports tokens of at most eight bytes.
const maxTokenSize = 8

// validateResponseOptions runs before reassembly can remove transfer options.
func validateResponseOptions(response *protocol.Message) error {
	_, err := validateResponseOptionsWithSize(response)
	return err
}

func validateResponseOptionsWithSize(response *protocol.Message) (int, error) {
	if len(response.Token) > maxTokenSize {
		return 0, ErrResponseTooLarge
	}
	seen := make(map[protocol.OptionNumber]bool, 4)
	remaining := MaxResponseOptionSize
	for _, option := range response.Options {
		if remaining < 8 || len(option.Value) > remaining-8 {
			return 0, ErrResponseTooLarge
		}
		remaining -= 8 + len(option.Value)
		if option.Number&1 != 0 && option.Number != protocol.OptionBlock1 && option.Number != protocol.OptionBlock2 {
			return 0, ErrInvalidResponseOption
		}
		maximum := 4
		switch option.Number {
		case protocol.OptionContentFormat:
			maximum = 2
		case protocol.OptionBlock1, protocol.OptionBlock2:
			maximum = 3
			if _, err := protocol.ParseBlock(option.Value); err != nil {
				return 0, ErrInvalidResponseOption
			}
		case protocol.OptionSize1, protocol.OptionSize2:
		default:
			continue
		}
		if seen[option.Number] || len(option.Value) > maximum || !canonicalUint(option.Value) {
			if option.Number == protocol.OptionContentFormat {
				return 0, ErrUnexpectedContentFormat
			}
			return 0, ErrInvalidResponseOption
		}
		seen[option.Number] = true
	}
	return MaxResponseOptionSize - remaining, nil
}

// CoAP integer options use the shortest encoding. Zero has an empty value.
func canonicalUint(value []byte) bool {
	return len(value) == 0 || value[0] != 0
}

// rejectCriticalOptions applies RFC 7252 section 5.4.1. Unknown elective
// options can be ignored, but unknown critical options must stop processing.
// In particular, an unsupported write condition must not become an
// unconditional write. Transfer options are checked by the wrapper or the
// direct resource handler.
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
