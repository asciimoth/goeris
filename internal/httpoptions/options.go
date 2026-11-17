// Package httpoptions encodes advisory ERIS block options in HTTP fields.
package httpoptions

import (
	"net/http"
	"strings"

	"github.com/asciimoth/goeris"
)

// HighPriorityHeader carries the priority hint for ERIS over HTTP.
const HighPriorityHeader = "Eris-High-Priority"

// S3HighPriorityHeader uses S3 user metadata so ordinary object stores can retain the flag.
const S3HighPriorityHeader = "X-Amz-Meta-Eris-High-Priority"

// set replaces all case variants of the field, including entries made by
// direct map writes. It omits the field when the flag is false.
func set(headers http.Header, name string, enabled bool) {
	for key := range headers {
		if strings.EqualFold(key, name) {
			delete(headers, key)
		}
	}
	if enabled {
		headers.Set(name, "true")
	}
}

// parse accepts one true value. Missing, duplicate, or unknown values select
// false. Invalid advisory metadata must not prevent block access.
func parse(headers http.Header, name string) bool {
	var value string
	found := false
	for key, entries := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		if found || len(entries) != 1 {
			return false
		}
		found = true
		value = entries[0]
	}
	return found && strings.Trim(value, " \t") == "true"
}

// RootHeader marks object entry blocks for ERIS over HTTP.
const RootHeader = "Eris-Root"

// S3RootHeader stores the root flag in S3 user metadata.
const S3RootHeader = "X-Amz-Meta-Eris-Root"

// Set writes both flags and omits fields whose value is false.
func Set(headers http.Header, priorityName, rootName string, options eris.BlockOptions) {
	set(headers, priorityName, options.HighPriority)
	set(headers, rootName, options.Root)
}

// Parse reads each flag independently. Invalid fields mean false.
func Parse(headers http.Header, priorityName, rootName string) eris.BlockOptions {
	return eris.BlockOptions{HighPriority: parse(headers, priorityName), Root: parse(headers, rootName)}
}
