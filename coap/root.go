package coap

import (
	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

// ExperimentalRootOption is an elective, safe-to-forward option that
// is not part of the cache key. Its empty value means true; absence means false.
// 65052 is in the RFC 7252 experimental range, not an IANA allocation. Enable
// this extension only in coordinated experiments, never in operational use.
const ExperimentalRootOption protocol.OptionNumber = 65052

func setRoot(message *protocol.Message, options eris.BlockOptions, enabled bool) {
	if enabled && options.Root {
		message.Options = append(message.Options, protocol.NewOption(ExperimentalRootOption, nil))
		sortOptions(message.Options)
	}
}

// Ignore malformed or repeated hints, as with an unknown elective option.
func rootOptions(message *protocol.Message, enabled bool) eris.BlockOptions {
	if !enabled {
		return eris.BlockOptions{}
	}
	value, present, err := singleOptionValue(message, ExperimentalRootOption)
	return eris.BlockOptions{Root: err == nil && present && len(value) == 0}
}

func blockOptions(message *protocol.Message, priorityEnabled, rootEnabled bool) eris.BlockOptions {
	options := priorityOptions(message, priorityEnabled)
	options.Root = rootOptions(message, rootEnabled).Root
	return options
}
