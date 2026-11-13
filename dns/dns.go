package dns

import (
	"context"
	"encoding"
	"errors"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
)

const (
	// NamespaceERIS identifies arbitrary ERIS content.
	NamespaceERIS Namespace = "eris"
	// NamespaceERISFS identifies an ERIS-FS image.
	NamespaceERISFS Namespace = "erisfs"
	// RecordPrefix starts a DNSLink TXT value.
	RecordPrefix = "dnslink=/"
	// OwnerPrefix starts a DNSLink DNS owner name.
	OwnerPrefix = "_dnslink."
)

var (
	// ErrInvalidLink means that input is not a canonical ERIS DNSLink value.
	ErrInvalidLink = errors.New("eris dns: invalid link")
	// ErrInvalidName means that a DNS name cannot identify a DNSLink owner.
	ErrInvalidName = errors.New("eris dns: invalid name")
	// ErrNilResolver means that Resolve received no resolver.
	ErrNilResolver = errors.New("eris dns: nil resolver")
)

// Namespace is an ERIS DNSLink namespace.
type Namespace string

// Valid reports whether namespace is defined by the ERIS DNSLink
// recommendation.
func (namespace Namespace) Valid() bool {
	return namespace == NamespaceERIS || namespace == NamespaceERISFS
}

// Link is one ERIS DNSLink value.
type Link struct {
	Namespace  Namespace
	Capability eris.Capability
}

// New returns a validated link.
func New(namespace Namespace, capability eris.Capability) (Link, error) {
	link := Link{Namespace: namespace, Capability: capability}
	if err := link.Validate(); err != nil {
		return Link{}, err
	}
	return link, nil
}

// Validate checks all fields of link.
func (link Link) Validate() error {
	if !link.Namespace.Valid() || link.Capability.Validate() != nil {
		return ErrInvalidLink
	}
	return nil
}

// MarshalText returns the canonical DNS TXT value for link.
func (link Link) MarshalText() ([]byte, error) {
	if err := link.Validate(); err != nil {
		return nil, err
	}
	urn, _ := link.Capability.MarshalText()
	out := make([]byte, 0, len(RecordPrefix)+len(link.Namespace)+1+len(urn)-len(eris.CapabilityURNPrefix))
	out = append(out, RecordPrefix...)
	out = append(out, link.Namespace...)
	out = append(out, '/')
	out = append(out, urn[len(eris.CapabilityURNPrefix):]...)
	return out, nil
}

// UnmarshalText parses one canonical ERIS DNSLink value. It does not change
// link on error.
func (link *Link) UnmarshalText(text []byte) error {
	if link == nil {
		return ErrInvalidLink
	}
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}
	*link = parsed
	return nil
}

// String returns the canonical DNS TXT value. It returns an empty string if
// link is invalid.
func (link Link) String() string {
	text, err := link.MarshalText()
	if err != nil {
		return ""
	}
	return string(text)
}

// Parse parses one canonical ERIS DNSLink value.
func Parse(text string) (Link, error) {
	if !strings.HasPrefix(text, RecordPrefix) {
		return Link{}, ErrInvalidLink
	}
	remainder := text[len(RecordPrefix):]
	namespaceText, identifier, ok := strings.Cut(remainder, "/")
	namespace := Namespace(namespaceText)
	if !ok || !namespace.Valid() || identifier == "" || strings.ContainsAny(identifier, "/ \t\r\n") {
		return Link{}, ErrInvalidLink
	}
	capability, err := eris.ParseURN(eris.CapabilityURNPrefix + identifier)
	if err != nil {
		return Link{}, ErrInvalidLink
	}
	return Link{Namespace: namespace, Capability: capability}, nil
}

// Encode returns a canonical ERIS DNSLink TXT value.
func Encode(namespace Namespace, capability eris.Capability) (string, error) {
	link, err := New(namespace, capability)
	if err != nil {
		return "", err
	}
	return link.String(), nil
}

// Decode parses one canonical ERIS DNSLink TXT value.
func Decode(text string) (Namespace, eris.Capability, error) {
	link, err := Parse(text)
	if err != nil {
		return "", eris.Capability{}, err
	}
	return link.Namespace, link.Capability, nil
}

// Extract returns all valid ERIS DNSLink values in texts. Each input can hold
// multiple whitespace-separated TXT fields. Unrelated and malformed fields
// are ignored. The result is sorted by its canonical text representation so
// DNS response order does not affect it. Duplicate links are preserved.
func Extract(texts ...string) []Link {
	links := make([]Link, 0)
	for _, text := range texts {
		for field := range strings.FieldsSeq(text) {
			link, err := Parse(field)
			if err == nil {
				links = append(links, link)
			}
		}
	}
	slices.SortFunc(links, func(left, right Link) int {
		return strings.Compare(left.String(), right.String())
	})
	return links
}

// Update replaces all valid ERIS DNSLink fields for link.Namespace in text.
// It preserves unrelated fields, removes duplicate old values, normalizes
// whitespace to single spaces, and appends link. Malformed DNSLink fields are
// unrelated and are preserved.
func Update(text string, link Link) (string, error) {
	encoded, err := link.MarshalText()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(text)
	updated := fields[:0]
	for _, field := range fields {
		old, parseErr := Parse(field)
		if parseErr == nil && old.Namespace == link.Namespace {
			continue
		}
		updated = append(updated, field)
	}
	updated = append(updated, string(encoded))
	return strings.Join(updated, " "), nil
}

// UpdateRecords updates a set of TXT record values. It removes old values for
// link.Namespace and appends the new value as a separate TXT record. A record
// that contains only removed values is omitted. A changed record is rebuilt
// with single spaces. All records without a removed value are preserved
// byte-for-byte and in their input order. The function does not modify the
// input slice.
func UpdateRecords(records []string, link Link) ([]string, error) {
	if err := link.Validate(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(records)+1)
	for _, record := range records {
		updated, changed := removeNamespace(record, link.Namespace)
		if !changed {
			result = append(result, record)
			continue
		}
		if updated != "" {
			result = append(result, updated)
		}
	}
	result = append(result, link.String())
	return result, nil
}

func removeNamespace(text string, namespace Namespace) (string, bool) {
	fields := strings.Fields(text)
	kept := fields[:0]
	changed := false
	for _, field := range fields {
		link, err := Parse(field)
		if err == nil && link.Namespace == namespace {
			changed = true
			continue
		}
		kept = append(kept, field)
	}
	return strings.Join(kept, " "), changed
}

// TXTResolver is the part of the Go resolver API required by Resolve.
//
// Implementations must follow CNAME chains during LookupTXT and report CNAME
// loops as errors. *net.Resolver has these DNS semantics and implements
// TXTResolver directly. Resolve intentionally does not make a separate
// LookupCNAME call because a DNSLink owner can have TXT and no address records.
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Resolve looks up all ERIS DNSLink values for name. It queries the required
// _dnslink owner name. A name that already starts with _dnslink is accepted.
// The caller controls all DNS I/O through resolver. Unrelated and malformed
// TXT fields are ignored, and valid links are returned in canonical order.
func Resolve(ctx context.Context, resolver TXTResolver, name string) ([]Link, error) {
	if resolver == nil {
		return nil, ErrNilResolver
	}
	owner, err := OwnerName(name)
	if err != nil {
		return nil, err
	}
	texts, err := resolver.LookupTXT(ctx, owner)
	if err != nil {
		return nil, err
	}
	return Extract(texts...), nil
}

// OwnerName returns the DNS owner name used for a DNSLink lookup. It accepts
// one optional trailing root dot and preserves that dot in the result.
func OwnerName(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return "", ErrInvalidName
	}
	absolute := strings.HasSuffix(name, ".")
	name = strings.TrimSuffix(name, ".")
	if len(name) >= len(OwnerPrefix) && strings.EqualFold(name[:len(OwnerPrefix)], OwnerPrefix) {
		name = name[len(OwnerPrefix):]
	}
	if !validDNSName(name) {
		return "", ErrInvalidName
	}
	owner := OwnerPrefix + name
	if absolute {
		owner += "."
	}
	return owner, nil
}

func validDNSName(name string) bool {
	if name == "" || len(name) > 253-len(OwnerPrefix) {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for index := range len(label) {
			char := label[index]
			if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
				char >= '0' && char <= '9' || char == '-' || char == '_' {
				continue
			}
			return false
		}
	}
	return true
}

var (
	_ encoding.TextMarshaler   = Link{}
	_ encoding.TextUnmarshaler = (*Link)(nil)
)
