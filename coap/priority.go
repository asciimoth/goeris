package coap

import (
	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

// ExperimentalHighPriorityOption is an elective, safe-to-forward option that
// is not part of the cache key. Its empty value means true; absence means false.
// 65020 is in the RFC 7252 experimental range, not an IANA allocation. Enable
// this extension only in coordinated experiments, never in operational use.
const ExperimentalHighPriorityOption protocol.OptionNumber = 65020

func setPriority(message *protocol.Message, options eris.BlockOptions, enabled bool) {
	if enabled && options.HighPriority {
		message.Options = append(message.Options, protocol.NewOption(ExperimentalHighPriorityOption, nil))
		sortOptions(message.Options)
	}
}

// Ignore malformed or repeated hints, as with an unknown elective option.
func priorityOptions(message *protocol.Message, enabled bool) eris.BlockOptions {
	if !enabled {
		return eris.BlockOptions{}
	}
	value, present, err := singleOptionValue(message, ExperimentalHighPriorityOption)
	return eris.BlockOptions{HighPriority: err == nil && present && len(value) == 0}
}
