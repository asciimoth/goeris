// Package httppriority encodes the optional ERIS priority hint in HTTP fields.
package httppriority

import (
	"net/http"
	"strings"

	"github.com/asciimoth/goeris"
)

// Header carries the priority hint for ERIS over HTTP.
const Header = "Eris-High-Priority"

// S3Header uses S3 user metadata so ordinary object stores can retain the flag.
const S3Header = "X-Amz-Meta-Eris-High-Priority"

// Set replaces all case variants of the field, including entries made by
// direct map writes. It omits the field when priority is false.
func Set(headers http.Header, name string, options eris.BlockOptions) {
	for key := range headers {
		if strings.EqualFold(key, name) {
			delete(headers, key)
		}
	}
	if options.HighPriority {
		headers.Set(name, "true")
	}
}

// Parse accepts one true value. Missing, duplicate, or unknown values select
// the default priority. Invalid advisory metadata must not prevent block access.
func Parse(headers http.Header, name string) eris.BlockOptions {
	var value string
	found := false
	for key, entries := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		if found || len(entries) != 1 {
			return eris.BlockOptions{}
		}
		found = true
		value = entries[0]
	}
	return eris.BlockOptions{HighPriority: found && strings.Trim(value, " \t") == "true"}
}
