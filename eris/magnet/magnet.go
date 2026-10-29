package magnet

import (
	"encoding"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/eris-tools/eris"
)

const scheme = "magnet:"

// ErrInvalidURI means that text is not a valid ERIS Magnet URI.
var ErrInvalidURI = errors.New("eris magnet: invalid URI")

// Parameter is a Magnet URI parameter that is not xt, dn, or xl.
// Name and Value contain decoded text.
type Parameter struct {
	Name  string
	Value string
}

// URI identifies ERIS content and contains optional Magnet metadata.
//
// ExactTopic must be a valid ERIS capability. An empty DisplayName is omitted.
// ExactLength is nil when the xl parameter is absent. Parameters contain all
// parameters other than xt, dn, and xl, and it can contain repeated names.
// MarshalText sorts Parameters by name and value, so its output does not depend
// on their input order. Parse returns Parameters in the same order.
type URI struct {
	ExactTopic  eris.Capability
	DisplayName string
	ExactLength *uint64
	Parameters  []Parameter
}

// New returns a URI for capability.
func New(capability eris.Capability) (URI, error) {
	uri := URI{ExactTopic: capability}
	if err := uri.Validate(); err != nil {
		return URI{}, err
	}
	return uri, nil
}

// Validate checks that all URI fields can be represented without ambiguity.
func (uri URI) Validate() error {
	if err := uri.ExactTopic.Validate(); err != nil {
		return ErrInvalidURI
	}
	if !utf8.ValidString(uri.DisplayName) {
		return ErrInvalidURI
	}
	for _, parameter := range uri.Parameters {
		if parameter.Name == "" || isReserved(parameter.Name) ||
			!utf8.ValidString(parameter.Name) || !utf8.ValidString(parameter.Value) {
			return ErrInvalidURI
		}
	}
	return nil
}

// Parse parses an ERIS Magnet URI.
//
// It accepts both magnet:xt=... and magnet:?xt=.... Parameter names are case
// sensitive. The URI must contain exactly one xt parameter and no more than one
// dn or xl parameter.
func Parse(text string) (URI, error) {
	colon := strings.IndexByte(text, ':')
	if colon < 0 || !strings.EqualFold(text[:colon], "magnet") {
		return URI{}, ErrInvalidURI
	}
	query := strings.TrimPrefix(text[colon+1:], "?")
	if query == "" || strings.Contains(query, "#") {
		return URI{}, ErrInvalidURI
	}

	var uri URI
	var hasTopic, hasName, hasLength bool
	for component := range strings.SplitSeq(query, "&") {
		name, value, ok := strings.Cut(component, "=")
		if !ok || name == "" {
			return URI{}, ErrInvalidURI
		}
		name, err := url.QueryUnescape(name)
		if err != nil || name == "" || !utf8.ValidString(name) {
			return URI{}, ErrInvalidURI
		}
		value, err = url.QueryUnescape(value)
		if err != nil || !utf8.ValidString(value) {
			return URI{}, ErrInvalidURI
		}

		switch name {
		case "xt":
			if hasTopic {
				return URI{}, ErrInvalidURI
			}
			capability, parseErr := eris.ParseURN(value)
			if parseErr != nil {
				return URI{}, ErrInvalidURI
			}
			uri.ExactTopic = capability
			hasTopic = true
		case "dn":
			if hasName {
				return URI{}, ErrInvalidURI
			}
			uri.DisplayName = value
			hasName = true
		case "xl":
			if hasLength {
				return URI{}, ErrInvalidURI
			}
			length, valid := parseExactLength(value)
			if !valid {
				return URI{}, ErrInvalidURI
			}
			uri.ExactLength = &length
			hasLength = true
		default:
			uri.Parameters = append(uri.Parameters, Parameter{Name: name, Value: value})
		}
	}
	if !hasTopic || uri.Validate() != nil {
		return URI{}, ErrInvalidURI
	}
	slices.SortFunc(uri.Parameters, compareParameters)
	return uri, nil
}

// MarshalText returns the canonical text form of uri.
//
// The canonical form uses the magnet:xt=... spelling from the ERIS Magnet
// recommendation. It orders reserved parameters first and sorts all extension
// parameters by name and value.
func (uri URI) MarshalText() ([]byte, error) {
	if err := uri.Validate(); err != nil {
		return nil, err
	}
	topic := uri.ExactTopic.String()

	out := make([]byte, 0, len(scheme)+3+len(topic))
	out = append(out, scheme...)
	out = appendParameter(out, "xt", topic)
	if uri.DisplayName != "" {
		out = append(out, '&')
		out = appendParameter(out, "dn", uri.DisplayName)
	}
	if uri.ExactLength != nil {
		out = append(out, '&')
		out = append(out, "xl="...)
		out = strconv.AppendUint(out, *uri.ExactLength, 10)
	}

	parameters := slices.Clone(uri.Parameters)
	slices.SortFunc(parameters, compareParameters)
	for _, parameter := range parameters {
		out = append(out, '&')
		out = appendParameter(out, parameter.Name, parameter.Value)
	}
	return out, nil
}

// UnmarshalText parses an ERIS Magnet URI. It does not change uri on error.
func (uri *URI) UnmarshalText(text []byte) error {
	if uri == nil {
		return ErrInvalidURI
	}
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}
	*uri = parsed
	return nil
}

// String returns the canonical text form of uri. It returns an empty string if
// uri is invalid.
func (uri URI) String() string {
	text, err := uri.MarshalText()
	if err != nil {
		return ""
	}
	return string(text)
}

func appendParameter(dst []byte, name, value string) []byte {
	dst = appendEscaped(dst, name)
	dst = append(dst, '=')
	return appendEscaped(dst, value)
}

func appendEscaped(dst []byte, text string) []byte {
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(text); i++ {
		char := text[i]
		if isUnescaped(char) {
			dst = append(dst, char)
			continue
		}
		dst = append(dst, '%', hex[char>>4], hex[char&0x0f])
	}
	return dst
}

func isUnescaped(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' || char == '-' || char == '.' ||
		char == '_' || char == '~' || char == ':'
}

func isReserved(name string) bool {
	return name == "xt" || name == "dn" || name == "xl"
}

func parseExactLength(value string) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	length, err := strconv.ParseUint(value, 10, 64)
	return length, err == nil
}

func compareParameters(a, b Parameter) int {
	if result := strings.Compare(a.Name, b.Name); result != 0 {
		return result
	}
	return strings.Compare(a.Value, b.Value)
}

var (
	_ encoding.TextMarshaler   = URI{}
	_ encoding.TextUnmarshaler = (*URI)(nil)
)
