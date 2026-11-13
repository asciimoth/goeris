package coap

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/coaperation/linkformat"
)

const (
	// MaxDiscoveryDocumentSize limits discovery input before parsing allocates
	// links and attributes. Callers must also limit transport reassembly.
	MaxDiscoveryDocumentSize = 1 << 20

	// InterfaceDescription identifies a block-store link.
	InterfaceDescription = "http://purl.org/eris/coap"
	// CorePath is the CoRE Resource Discovery path.
	CorePath = "/.well-known/core"
)

var corePathSegments = []string{".well-known", "core"}

// Advertisement describes one discovered store or cross-network proxy.
type Advertisement struct {
	// Target is a relative URI reference or an absolute store URL.
	Target string
	// Base is empty, coap:, or coap+tcp:. It selects the transport scheme.
	Base string
	// ProxySchemes lists the URI schemes reachable through a cross-network
	// proxy. An empty list describes a store rather than a proxy.
	ProxySchemes []string
}

// NewStoreAdvertisement validates and returns one store advertisement. Target
// can be a relative URI reference or an absolute supported store URL. Base can
// be empty, coap:, or coap+tcp:.
func NewStoreAdvertisement(target, base string) (Advertisement, error) {
	advertisement := Advertisement{Target: target, Base: base}
	if err := advertisement.validate(); err != nil {
		return Advertisement{}, err
	}
	return advertisement, nil
}

// NewProxyAdvertisement validates and returns one cross-network proxy
// advertisement.
func NewProxyAdvertisement(target string, schemes ...string) (Advertisement, error) {
	advertisement := Advertisement{Target: target, ProxySchemes: append([]string(nil), schemes...)}
	if err := advertisement.validate(); err != nil || len(schemes) == 0 {
		return Advertisement{}, ErrInvalidAdvertisement
	}
	return advertisement, nil
}

func (advertisement Advertisement) validate() error {
	if advertisement.Target == "" || strings.Contains(advertisement.Target, "#") {
		return ErrInvalidAdvertisement
	}
	canonicalTarget := advertisement.Target
	if hasURIScheme(advertisement.Target) {
		store, err := ParseStoreURL(advertisement.Target)
		if err != nil {
			return ErrInvalidAdvertisement
		}
		canonicalTarget = store.String()
	} else {
		target, err := url.Parse(advertisement.Target)
		if err != nil || target.Fragment != "" || target.RawQuery != "" || target.ForceQuery || target.User != nil {
			return ErrInvalidAdvertisement
		}
	}
	if _, err := linkformat.Marshal([]linkformat.Link{{Target: canonicalTarget}}); err != nil {
		return ErrInvalidAdvertisement
	}
	if advertisement.Base != "" && advertisement.Base != "coap:" && advertisement.Base != "coap+tcp:" {
		return ErrInvalidAdvertisement
	}
	for _, scheme := range advertisement.ProxySchemes {
		if !validScheme(scheme) {
			return ErrInvalidAdvertisement
		}
	}
	return nil
}

func hasURIScheme(text string) bool {
	colon := strings.IndexByte(text, ':')
	if colon < 1 {
		return false
	}
	delimiter := strings.IndexAny(text, "/?#")
	return delimiter < 0 || colon < delimiter
}

func validScheme(scheme string) bool {
	if scheme == "" || scheme[0] < 'A' || scheme[0] > 'Z' && scheme[0] < 'a' || scheme[0] > 'z' {
		return false
	}
	for _, char := range scheme[1:] {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("+-.", char) {
			continue
		}
		return false
	}
	return true
}

func (advertisement Advertisement) link() (linkformat.Link, error) {
	if err := advertisement.validate(); err != nil {
		return linkformat.Link{}, err
	}
	target := advertisement.Target
	if hasURIScheme(target) {
		store, _ := ParseStoreURL(target)
		target = store.String()
	}
	link := linkformat.Link{Target: target, Attributes: []linkformat.Attribute{{Name: "if", Value: InterfaceDescription, Quoted: true}}}
	if advertisement.Base != "" {
		link.Attributes = append(link.Attributes, linkformat.Attribute{Name: "base", Value: advertisement.Base})
	}
	if len(advertisement.ProxySchemes) > 0 {
		link.Attributes = append(link.Attributes, linkformat.Attribute{Name: "rt", Value: "core.proxy"})
		for _, scheme := range advertisement.ProxySchemes {
			link.Attributes = append(link.Attributes, linkformat.Attribute{Name: "proxy-scheme", Value: scheme})
		}
	}
	return link, nil
}

// MarshalAdvertisements returns a deterministic CoRE Link Format document in
// input order.
func MarshalAdvertisements(advertisements []Advertisement) ([]byte, error) {
	links := make([]linkformat.Link, len(advertisements))
	for index, advertisement := range advertisements {
		link, err := advertisement.link()
		if err != nil {
			return nil, err
		}
		links[index] = link
	}
	return linkformat.Marshal(links)
}

// ParseAdvertisements parses and returns links with the required interface
// description. It ignores unrelated links and copies all returned values.
// Documents larger than MaxDiscoveryDocumentSize are rejected.
func ParseAdvertisements(data []byte) ([]Advertisement, error) {
	if len(data) > MaxDiscoveryDocumentSize {
		return nil, errors.Join(ErrInvalidAdvertisement, ErrResponseTooLarge)
	}
	links, err := linkformat.Parse(data)
	if err != nil {
		return nil, errors.Join(ErrInvalidAdvertisement, err)
	}
	result := make([]Advertisement, 0, len(links))
	for _, link := range links {
		hasInterface := false
		for _, value := range link.Values("if") {
			if slices.Contains(strings.Fields(value), InterfaceDescription) {
				hasInterface = true
			}
		}
		if !hasInterface {
			continue
		}
		bases := link.Values("base")
		if len(bases) > 1 {
			return nil, ErrInvalidAdvertisement
		}
		proxySchemes := link.Values("proxy-scheme")
		isProxy := false
		for _, value := range link.Values("rt") {
			if slices.Contains(strings.Fields(value), "core.proxy") {
				isProxy = true
			}
		}
		if isProxy != (len(proxySchemes) > 0) {
			return nil, ErrInvalidAdvertisement
		}
		// Parser strings can refer to the complete document. Retain only the
		// selected values, so unrelated links and attributes can be released.
		advertisement := Advertisement{Target: strings.Clone(link.Target)}
		for _, scheme := range proxySchemes {
			advertisement.ProxySchemes = append(advertisement.ProxySchemes, strings.Clone(scheme))
		}
		if len(bases) == 1 {
			advertisement.Base = strings.Clone(bases[0])
		}
		if err := advertisement.validate(); err != nil {
			return nil, err
		}
		result = append(result, advertisement)
	}
	return result, nil
}

// Resolve resolves an advertisement against the endpoint origin. A base
// transport indication changes the scheme of the resolved target.
func (advertisement Advertisement) Resolve(origin StoreURL) (StoreURL, error) {
	if err := advertisement.validate(); err != nil {
		return StoreURL{}, ErrInvalidAdvertisement
	}
	if err := origin.validate(); err != nil {
		return StoreURL{}, ErrInvalidStoreURL
	}
	if hasURIScheme(advertisement.Target) {
		store, _ := ParseStoreURL(advertisement.Target)
		if advertisement.Base != "" {
			return NewStoreURL(protocol.Target{Scheme: strings.TrimSuffix(advertisement.Base, ":"), Host: store.Host(), Port: store.Port()}, store.Path())
		}
		return store, nil
	}
	target, _ := url.Parse(advertisement.Target)
	// net/url rejects some escaped registered hosts accepted by CoAP. Resolve
	// the path against a placeholder, then retain the original authority unless
	// the advertisement supplies its own. Do not parse origin.String() here.
	base := &url.URL{Scheme: origin.Scheme(), Host: "origin.invalid", Path: CorePath}
	resolved := base.ResolveReference(target)
	if advertisement.Base != "" {
		resolved.Scheme = strings.TrimSuffix(advertisement.Base, ":")
	}
	store, err := storeFromURL(resolved)
	if err != nil {
		return StoreURL{}, err
	}
	if target.Host == "" {
		authority := origin.Target()
		authority.Scheme = store.Scheme()
		return NewStoreURL(authority, store.Path())
	}
	return store, nil
}

// NewDiscoveryRequest constructs a deterministic resource-discovery request.
func NewDiscoveryRequest() *protocol.Message {
	return &protocol.Message{
		Type: protocol.NonConfirmable,
		Code: protocol.GET,
		Options: []protocol.Option{
			protocol.StringOption(protocol.OptionURIPath, corePathSegments[0]),
			protocol.StringOption(protocol.OptionURIPath, corePathSegments[1]),
		},
	}
}

// ParseDiscoveryResponse validates and parses one complete discovery response.
// Callers must reassemble block-wise responses before calling it and limit
// reassembly to MaxDiscoveryDocumentSize bytes.
func ParseDiscoveryResponse(response *protocol.Message) ([]Advertisement, error) {
	if response == nil || response.Code != protocol.Content {
		if response == nil {
			return nil, ErrInvalidAdvertisement
		}
		return nil, &ResponseError{Code: response.Code}
	}
	if err := validateResponseOptions(response); err != nil {
		return nil, err
	}
	if option, present := response.Option(protocol.OptionBlock2); present {
		block, err := protocol.ParseBlock(option.Value)
		if err != nil || block.Number != 0 || block.More {
			return nil, ErrInvalidAdvertisement
		}
	}
	format, present, err := oneUintOption(response, protocol.OptionContentFormat)
	if err != nil || !present || format != protocol.ContentFormatLinkFormat {
		return nil, ErrUnexpectedContentFormat
	}
	return ParseAdvertisements(response.Payload)
}

// DiscoveryHandler serves a discovery document. Callers must not mutate
// Advertisements while ServeCOAP is active. Unsupported critical options
// are rejected before the document is built. Request bodies and Block1 options
// are not supported.
type DiscoveryHandler struct {
	// Advertisements are returned in their current order.
	Advertisements []Advertisement
	// Next receives requests for resources other than CorePath. A nil Next
	// produces 4.04 Not Found.
	Next protocol.Handler
}

// ServeCOAP implements coaperation.Handler.
func (handler DiscoveryHandler) ServeCOAP(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	if request == nil {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	if !matchesPath(request, corePathSegments[:1], corePathSegments[1]) {
		if handler.Next != nil {
			return handler.Next.ServeCOAP(ctx, request)
		}
		return &protocol.Message{Code: protocol.NotFound}, nil
	}
	if response, rejected := rejectCriticalOptions(request); rejected {
		return response, nil
	}
	if err := validateHopLimit(request); err != nil {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	if request.Code != protocol.GET {
		return &protocol.Message{Code: protocol.MethodNotAllowed}, nil
	}
	if _, present := request.Option(protocol.OptionBlock1); present {
		return &protocol.Message{Code: protocol.BadOption}, nil
	}
	if len(request.Payload) != 0 {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	if _, present := request.Option(protocol.OptionURIQuery); request.Multicast && present {
		// RFC 6690 says that a multicast server without query filtering should
		// not respond to a filtered discovery request.
		return nil, nil
	}
	payload, err := MarshalAdvertisements(handler.Advertisements)
	if err != nil {
		return &protocol.Message{Code: protocol.InternalServerError}, nil
	}
	return &protocol.Message{
		Code:    protocol.Content,
		Options: []protocol.Option{protocol.UintOption(protocol.OptionContentFormat, protocol.ContentFormatLinkFormat)},
		Payload: payload,
	}, nil
}

var _ protocol.Handler = DiscoveryHandler{}
