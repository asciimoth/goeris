package coap

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	protocol "github.com/asciimoth/coaperation"
)

const blocksSegment = "blocks"

// StoreURL is a canonical CoAP block-store URL. It supports plain CoAP over
// UDP and plain CoAP over TCP.
type StoreURL struct {
	scheme string
	host   string
	port   uint16
	path   []string
}

// ParseStoreURL parses a coap or coap+tcp block-store URL. It rejects user
// information, queries, and fragments. It resolves dot segments and removes a
// trailing path separator.
func ParseStoreURL(text string) (StoreURL, error) {
	if !validRawAuthority(text) {
		return StoreURL{}, ErrInvalidStoreURL
	}
	request, target, err := protocol.NewRequest(protocol.GET, text, nil)
	if err != nil || len(request.URIQueries()) != 0 || target.Host == "" || !utf8.ValidString(target.Host) {
		return StoreURL{}, ErrInvalidStoreURL
	}
	path := request.URIPath()
	for len(path) > 0 && path[len(path)-1] == "" {
		path = path[:len(path)-1]
	}
	store := StoreURL{scheme: target.Scheme, host: target.Host, port: target.Port, path: path}
	if store.validate() != nil {
		return StoreURL{}, ErrInvalidStoreURL
	}
	return store, nil
}

func validRawAuthority(text string) bool {
	schemeEnd := strings.Index(text, "://")
	if schemeEnd < 1 {
		return false
	}
	start := schemeEnd + 3
	end := len(text)
	if offset := strings.IndexAny(text[start:], "/?#"); offset >= 0 {
		end = start + offset
	}
	authority := text[start:end]
	if authority == "" || strings.Contains(authority, "@") {
		return false
	}
	if strings.HasPrefix(authority, "[") {
		return strings.Count(authority, "[") == 1 && strings.Count(authority, "]") == 1
	}
	return strings.Count(authority, ":") <= 1
}

// NewStoreURL constructs a store URL from a transport target and decoded URI
// path segments.
func NewStoreURL(target protocol.Target, path []string) (StoreURL, error) {
	store := StoreURL{scheme: target.Scheme, host: target.Host, port: target.Port, path: append([]string(nil), path...)}
	if store.validate() != nil {
		return StoreURL{}, ErrInvalidStoreURL
	}
	return ParseStoreURL(store.String())
}

// String returns the canonical store URL. The root store has a trailing slash;
// other store paths do not.
func (store StoreURL) String() string {
	if store.validate() != nil {
		return ""
	}
	host := canonicalHost(store.host)
	if store.port != protocol.DefaultPort {
		host += ":" + strconv.Itoa(int(store.port))
	}
	escapedPath := "/"
	if len(store.path) > 0 {
		escaped := make([]string, len(store.path))
		for index, segment := range store.path {
			escaped[index] = url.PathEscape(segment)
		}
		escapedPath += strings.Join(escaped, "/")
	}
	return store.scheme + "://" + host + escapedPath
}

func (store StoreURL) validate() error {
	if store.scheme != "coap" && store.scheme != "coap+tcp" || store.host == "" || !utf8.ValidString(store.host) || store.port == 0 {
		return ErrInvalidStoreURL
	}
	for index, segment := range store.path {
		if !utf8.ValidString(segment) || segment == "." || segment == ".." || index == len(store.path)-1 && segment == "" {
			return ErrInvalidStoreURL
		}
	}
	return nil
}

// Scheme returns coap or coap+tcp.
func (store StoreURL) Scheme() string { return store.scheme }

// Host returns the host without brackets or a port.
func (store StoreURL) Host() string { return store.host }

// Port returns the transport port.
func (store StoreURL) Port() uint16 { return store.port }

// Path returns a copy of the decoded store path segments.
func (store StoreURL) Path() []string { return append([]string(nil), store.path...) }

// Target returns the transport target for store. Resolving or connecting to
// the target is the caller's responsibility.
func (store StoreURL) Target() protocol.Target {
	return protocol.Target{Scheme: store.scheme, Host: store.host, Port: store.port}
}

func (store StoreURL) requestOptions() ([]protocol.Option, error) {
	if err := store.validate(); err != nil {
		return nil, err
	}
	options := []protocol.Option{protocol.StringOption(protocol.OptionURIHost, store.host)}
	if store.port != protocol.DefaultPort {
		options = append(options, protocol.UintOption(protocol.OptionURIPort, uint32(store.port)))
	}
	for _, segment := range store.path {
		options = append(options, protocol.StringOption(protocol.OptionURIPath, segment))
	}
	options = append(options, protocol.StringOption(protocol.OptionURIPath, blocksSegment))
	return options, nil
}

func canonicalHost(host string) string {
	address, zone, hasZone := strings.Cut(host, "%")
	if ip := net.ParseIP(address); ip != nil {
		literal := ip.String()
		if hasZone {
			literal += "%25" + escapeHostPart(zone, false)
		}
		if strings.Contains(literal, ":") {
			return "[" + literal + "]"
		}
		return literal
	}
	return escapeHostPart(host, true)
}

func escapeHostPart(text string, subdelimiters bool) string {
	const hexadecimal = "0123456789ABCDEF"
	var output strings.Builder
	for index := 0; index < len(text); index++ {
		char := text[index]
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", rune(char))
		if subdelimiters {
			allowed = allowed || strings.ContainsRune("!$&'()*+,;=", rune(char))
		}
		if allowed {
			output.WriteByte(char)
		} else {
			output.WriteByte('%')
			output.WriteByte(hexadecimal[char>>4])
			output.WriteByte(hexadecimal[char&0xf])
		}
	}
	return output.String()
}

func storeFromURL(parsed *url.URL) (StoreURL, error) {
	if parsed == nil {
		return StoreURL{}, ErrInvalidStoreURL
	}
	store, err := ParseStoreURL(parsed.String())
	if err != nil {
		return StoreURL{}, err
	}
	return store, nil
}
