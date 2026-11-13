package coap

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

const (
	// ContentType is the media type for encrypted ERIS blocks.
	ContentType = "application/octet-stream"
	// ContentFormatOctetStream is the CoAP content-format number for
	// application/octet-stream.
	ContentFormatOctetStream uint32 = 42
	// ImmutableMaxAge is the largest Max-Age value.
	ImmutableMaxAge uint32 = 0xffffffff
)

// ReferenceEncoding selects the Uri-Query representation of a block
// reference.
type ReferenceEncoding uint8

const (
	// ReferenceRaw encodes a reference as 32 octets. This is the preferred
	// representation and the zero value.
	ReferenceRaw ReferenceEncoding = iota
	// ReferenceBase32 encodes a reference as 52 uppercase Base32 characters.
	ReferenceBase32
)

// RequestConfig controls deterministic request construction.
type RequestConfig struct {
	// ReferenceEncoding selects the block-reference query encoding.
	ReferenceEncoding ReferenceEncoding
	// SizeHint adds Size1 to a GET request. Zero omits it.
	SizeHint eris.BlockSize
	// ProxyURI adds an absolute Proxy-Uri without a fragment. Low-level request
	// builders require a nonzero HopLimit; Client sets it from the operation TTL.
	ProxyURI string
	// HopLimit adds Hop-Limit. Valid values are from 1 through 255. In Client,
	// it sets the initial wire value for operations with eris.NoTTL; zero uses
	// eris.DefaultTTL. An explicit operation TTL overrides this field.
	// Low-level request builders send this value as is; zero omits the option.
	HopLimit uint8
}

// NewGetRequest constructs a GET request for ref. The result has no token,
// message ID, or remote address, so repeated calls with the same arguments
// return equivalent messages.
func NewGetRequest(store StoreURL, ref eris.Reference, config RequestConfig) (*protocol.Message, error) {
	options, err := store.requestOptions()
	if err != nil {
		return nil, err
	}
	query, err := referenceQuery(ref, config.ReferenceEncoding)
	if err != nil {
		return nil, err
	}
	options = append(options, protocol.NewOption(protocol.OptionURIQuery, query))
	options, err = addRoutingOptions(options, config)
	if err != nil {
		return nil, err
	}
	if config.SizeHint != 0 {
		if !config.SizeHint.Valid() {
			return nil, eris.ErrInvalidBlockSize
		}
		options = append(options, protocol.UintOption(protocol.OptionSize1, uint32(config.SizeHint)))
	}
	sortOptions(options)
	return &protocol.Message{Type: protocol.Confirmable, Code: protocol.GET, Options: options}, nil
}

// NewPutRequest constructs a PUT request containing block. An optional expected
// reference is sent as a URI query. Size1 and the application/octet-stream
// content format are always included.
func NewPutRequest(store StoreURL, block []byte, config RequestConfig, expected ...eris.Reference) (*protocol.Message, error) {
	if !validBlockLength(len(block)) {
		return nil, errors.Join(eris.ErrInvalidBlock, eris.ErrInvalidBlockSize)
	}
	if len(expected) > 1 {
		return nil, eris.ErrInvalidBlock
	}
	options, err := store.requestOptions()
	if err != nil {
		return nil, err
	}
	options = append(options, protocol.UintOption(protocol.OptionContentFormat, ContentFormatOctetStream))
	if len(expected) == 1 {
		query, err := referenceQuery(expected[0], config.ReferenceEncoding)
		if err != nil {
			return nil, err
		}
		options = append(options, protocol.NewOption(protocol.OptionURIQuery, query))
	}
	options, err = addRoutingOptions(options, config)
	if err != nil {
		return nil, err
	}
	options = append(options, protocol.UintOption(protocol.OptionSize1, uint32(len(block))))
	sortOptions(options)
	return &protocol.Message{Type: protocol.Confirmable, Code: protocol.PUT, Options: options, Payload: append([]byte(nil), block...)}, nil
}

func validBlockLength(length int) bool {
	return length == int(eris.BlockSize1KiB) || length == int(eris.BlockSize32KiB)
}

func referenceQuery(ref eris.Reference, encoding ReferenceEncoding) ([]byte, error) {
	switch encoding {
	case ReferenceRaw:
		return ref[:], nil
	case ReferenceBase32:
		return []byte(ref.String()), nil
	default:
		return nil, ErrInvalidReference
	}
}

func addRoutingOptions(options []protocol.Option, config RequestConfig) ([]protocol.Option, error) {
	if config.ProxyURI != "" {
		parsed, err := url.Parse(config.ProxyURI)
		if err != nil || parsed.Scheme == "" || !parsed.IsAbs() || strings.Contains(config.ProxyURI, "#") || config.HopLimit == 0 {
			return nil, ErrInvalidProxyURI
		}
		options = append(options, protocol.StringOption(protocol.OptionProxyURI, config.ProxyURI))
	}
	if config.HopLimit != 0 {
		options = append(options, protocol.UintOption(protocol.OptionHopLimit, uint32(config.HopLimit)))
	}
	return options, nil
}

func sortOptions(options []protocol.Option) {
	slices.SortStableFunc(options, func(left, right protocol.Option) int {
		return int(left.Number) - int(right.Number)
	})
}

func parseReferenceQuery(request *protocol.Message) (eris.Reference, error) {
	var zero eris.Reference
	if request == nil {
		return zero, ErrInvalidReference
	}
	query, present, err := singleOptionValue(request, protocol.OptionURIQuery)
	if err != nil || !present {
		return zero, ErrInvalidReference
	}
	if len(query) == len(zero) {
		copy(zero[:], query)
		return zero, nil
	}
	if len(query) == 52 {
		ref, err := eris.ParseReference(string(query))
		if err == nil {
			return ref, nil
		}
	}
	return zero, ErrInvalidReference
}

func oneUintOption(message *protocol.Message, number protocol.OptionNumber) (uint32, bool, error) {
	raw, present, err := singleOptionValue(message, number)
	if err != nil || !present {
		return 0, present, err
	}
	value, err := (protocol.Option{Number: number, Value: raw}).Uint32()
	if err == nil && !bytes.Equal(protocol.UintOption(number, value).Value, raw) {
		return 0, true, fmt.Errorf("eris coap: option %d has a non-canonical integer", number)
	}
	return value, true, err
}

func validateHopLimit(message *protocol.Message) error {
	value, present, err := oneUintOption(message, protocol.OptionHopLimit)
	if !present {
		return nil
	}
	if err != nil || value == 0 || value > 255 {
		return ErrInvalidHopLimit
	}
	return nil
}

// requestTTL is used only after validateHopLimit succeeds. The ERIS CoAP
// specification permits local access only when Hop-Limit is absent.
func requestTTL(message *protocol.Message) eris.TTL {
	value, present, _ := oneUintOption(message, protocol.OptionHopLimit)
	if !present {
		return eris.LocalTTL
	}
	return eris.TTL(value)
}

// singleOptionValue borrows one value for validation. Do not copy untrusted
// values before their length and cardinality have been checked.
func singleOptionValue(message *protocol.Message, number protocol.OptionNumber) ([]byte, bool, error) {
	var value []byte
	present := false
	for _, option := range message.Options {
		if option.Number != number {
			continue
		}
		if present {
			return nil, true, fmt.Errorf("eris coap: option %d is repeated", number)
		}
		value, present = option.Value, true
	}
	return value, present, nil
}

// matchesPath compares decoded path segments without copying request values.
func matchesPath(request *protocol.Message, prefix []string, last string) bool {
	index := 0
	for _, option := range request.Options {
		if option.Number != protocol.OptionURIPath {
			continue
		}
		want := last
		if index < len(prefix) {
			want = prefix[index]
		} else if index > len(prefix) {
			return false
		}
		if string(option.Value) != want {
			return false
		}
		index++
	}
	return index == len(prefix)+1
}
