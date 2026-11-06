package dns

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func testCapability() eris.Capability {
	var capability eris.Capability
	capability.BlockSize = eris.BlockSize1KiB
	capability.Level = 3
	for index := range capability.Root.Reference {
		capability.Root.Reference[index] = byte(index)
		capability.Root.Key[index] = byte(255 - index)
	}
	return capability
}

func TestLinkRoundTrip(t *testing.T) {
	t.Parallel()
	capability := testCapability()
	link, err := New(NamespaceERIS, capability)
	if err != nil {
		t.Fatal(err)
	}
	want := "dnslink=/eris/" + capability.String()[len(urnPrefix):]
	if got := link.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	text, err := link.MarshalText()
	if err != nil || string(text) != want {
		t.Fatalf("MarshalText() = %q, %v", text, err)
	}
	parsed, err := Parse(want)
	if err != nil || parsed != link {
		t.Fatalf("Parse() = %#v, %v", parsed, err)
	}
	namespace, decoded, err := Decode(want)
	if err != nil || namespace != NamespaceERIS || decoded != capability {
		t.Fatalf("Decode() = %q, %#v, %v", namespace, decoded, err)
	}
	encoded, err := Encode(NamespaceERIS, capability)
	if err != nil || encoded != want {
		t.Fatalf("Encode() = %q, %v", encoded, err)
	}

	var unmarshaled Link
	if err := unmarshaled.UnmarshalText(text); err != nil || unmarshaled != link {
		t.Fatalf("UnmarshalText() = %#v, %v", unmarshaled, err)
	}
}

func TestRecommendationExamples(t *testing.T) {
	t.Parallel()
	examples := []string{
		"dnslink=/eris/B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I",
		"dnslink=/erisfs/B4A6PNIT5KF4CLVDQ2UFC45UEVXS3LRPSJHSKYOLLGXPG4IADBJOAYIYTVNHFQ5LXUKO26S5DPZ64NGZ273AYIKYL62USS74VC2MJENMV4",
	}
	for _, example := range examples {
		link, err := Parse(example)
		if err != nil {
			t.Errorf("Parse(%q): %v", example, err)
			continue
		}
		if link.String() != example {
			t.Errorf("String() = %q, want %q", link.String(), example)
		}
	}
}

func TestInvalidLinks(t *testing.T) {
	t.Parallel()
	identifier := testCapability().String()[len(urnPrefix):]
	tests := []string{
		"", "dnslink=", "dnslink=eris/" + identifier,
		"dnslink=/ipfs/" + identifier, "dnslink=/eris/",
		"dnslink=/eris/" + identifier + "/x",
		"dnslink=/eris/" + identifier + " x",
		"dnslink=/eris/" + identifier[:len(identifier)-1],
		"dnslink=/eris/" + identifier[:1] + "a" + identifier[2:],
	}
	for _, test := range tests {
		if _, err := Parse(test); !errors.Is(err, ErrInvalidLink) {
			t.Errorf("Parse(%q) error = %v", test, err)
		}
		if namespace, capability, err := Decode(test); !errors.Is(err, ErrInvalidLink) || namespace != "" || capability != (eris.Capability{}) {
			t.Errorf("Decode(%q) = %q, %#v, %v", test, namespace, capability, err)
		}
	}

	invalid := Link{}
	if invalid.Validate() != ErrInvalidLink || invalid.String() != "" {
		t.Fatal("invalid link was accepted")
	}
	if text, err := invalid.MarshalText(); !errors.Is(err, ErrInvalidLink) || text != nil {
		t.Fatalf("MarshalText() = %q, %v", text, err)
	}
	if _, err := New("other", testCapability()); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("New() error = %v", err)
	}
	if got, err := Encode(NamespaceERIS, eris.Capability{}); !errors.Is(err, ErrInvalidLink) || got != "" {
		t.Fatalf("Encode() = %q, %v", got, err)
	}
	if Namespace("other").Valid() || !NamespaceERIS.Valid() || !NamespaceERISFS.Valid() {
		t.Fatal("Namespace.Valid returned an incorrect result")
	}
}

func TestUnmarshalTextDoesNotChangeReceiver(t *testing.T) {
	t.Parallel()
	original, _ := New(NamespaceERISFS, testCapability())
	receiver := original
	if err := receiver.UnmarshalText([]byte("invalid")); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if receiver != original {
		t.Fatal("UnmarshalText changed the receiver on error")
	}
	if err := (*Link)(nil).UnmarshalText([]byte(original.String())); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("nil UnmarshalText() error = %v", err)
	}
}

func TestExtract(t *testing.T) {
	t.Parallel()
	erisLink, _ := New(NamespaceERIS, testCapability())
	erisFSLink, _ := New(NamespaceERISFS, testCapability())
	got := Extract(
		"v=spf1  dnslink=/bad/value "+erisFSLink.String(),
		"\t"+erisLink.String()+"\nignored=value",
	)
	want := []Link{erisLink, erisFSLink}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract() = %#v, want %#v", got, want)
	}
	if got := Extract(); got == nil || len(got) != 0 {
		t.Fatalf("Extract() = %#v, want non-nil empty slice", got)
	}
	if got := Extract(erisLink.String(), erisLink.String()); !reflect.DeepEqual(got, []Link{erisLink, erisLink}) {
		t.Fatalf("Extract(duplicates) = %#v", got)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	old := testCapability()
	old.Level++
	oldERIS, _ := New(NamespaceERIS, old)
	newERIS, _ := New(NamespaceERIS, testCapability())
	erisFS, _ := New(NamespaceERISFS, old)
	input := "  v=spf1 " + oldERIS.String() + "\t" + erisFS.String() + " " + oldERIS.String() + " dnslink=/eris/bad "
	want := "v=spf1 " + erisFS.String() + " dnslink=/eris/bad " + newERIS.String()
	got, err := Update(input, newERIS)
	if err != nil || got != want {
		t.Fatalf("Update() = %q, %v; want %q", got, err, want)
	}
	if got, err := Update("", Link{}); !errors.Is(err, ErrInvalidLink) || got != "" {
		t.Fatalf("Update(invalid) = %q, %v", got, err)
	}
}

func TestUpdateRecords(t *testing.T) {
	t.Parallel()
	oldERIS, _ := New(NamespaceERIS, testCapability())
	newCapability := testCapability()
	newCapability.Level++
	newERIS, _ := New(NamespaceERIS, newCapability)
	erisFS, _ := New(NamespaceERISFS, testCapability())
	records := []string{
		oldERIS.String(),
		"key=value " + oldERIS.String() + " " + erisFS.String(),
		"dnslink=/eris/bad",
		"  key=value  ",
		"  ",
	}
	want := []string{
		"key=value " + erisFS.String(),
		"dnslink=/eris/bad",
		"  key=value  ",
		"  ",
		newERIS.String(),
	}
	got, err := UpdateRecords(records, newERIS)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateRecords() = %#v, %v; want %#v", got, err, want)
	}
	if got, err := UpdateRecords(records, Link{}); !errors.Is(err, ErrInvalidLink) || got != nil {
		t.Fatalf("UpdateRecords(invalid) = %#v, %v", got, err)
	}
}

type stubResolver struct {
	wantName string
	texts    []string
	err      error
	called   bool
}

func (resolver *stubResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	resolver.called = true
	if name != resolver.wantName {
		return nil, errors.New("incorrect lookup name")
	}
	return resolver.texts, resolver.err
}

func TestResolve(t *testing.T) {
	t.Parallel()
	link, _ := New(NamespaceERIS, testCapability())
	resolver := &stubResolver{
		wantName: "_dnslink.example.org.",
		texts:    []string{"unrelated", link.String()},
	}
	got, err := Resolve(context.Background(), resolver, "_dnslink.example.org.")
	if err != nil || !reflect.DeepEqual(got, []Link{link}) || !resolver.called {
		t.Fatalf("Resolve() = %#v, %v", got, err)
	}

	lookupErr := errors.New("lookup failed")
	resolver = &stubResolver{wantName: "_dnslink.example.org", err: lookupErr}
	if got, err := Resolve(context.Background(), resolver, "example.org"); !errors.Is(err, lookupErr) || got != nil {
		t.Fatalf("Resolve(lookup error) = %#v, %v", got, err)
	}
	resolver = &stubResolver{}
	if got, err := Resolve(context.Background(), resolver, "bad name"); !errors.Is(err, ErrInvalidName) || got != nil || resolver.called {
		t.Fatalf("Resolve(invalid name) = %#v, %v", got, err)
	}
	if got, err := Resolve(context.Background(), nil, "example.org"); !errors.Is(err, ErrNilResolver) || got != nil {
		t.Fatalf("Resolve(nil resolver) = %#v, %v", got, err)
	}
}

func TestOwnerName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"example.org":           "_dnslink.example.org",
		"example.org.":          "_dnslink.example.org.",
		"_dnslink.example.org":  "_dnslink.example.org",
		"_dnslink.example.org.": "_dnslink.example.org.",
		"_DNSLINK.example.org":  "_dnslink.example.org",
		"a_b.example":           "_dnslink.a_b.example",
	}
	for input, want := range tests {
		got, err := OwnerName(input)
		if err != nil || got != want {
			t.Errorf("OwnerName(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	maxName := strings.Join([]string{
		strings.Repeat("a", 63),
		strings.Repeat("b", 63),
		strings.Repeat("c", 63),
		strings.Repeat("d", 52),
	}, ".")
	if got, err := OwnerName(maxName); err != nil || len(got) != 253 {
		t.Errorf("OwnerName(maximum name) length = %d, error = %v", len(got), err)
	}
	longLabel := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	longName := "a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a.a"
	for _, input := range []string{"", ".", "example..org", "bad name", "bad/name", "café.example", "example.org..", longLabel + ".org", maxName + "e", longName} {
		if got, err := OwnerName(input); !errors.Is(err, ErrInvalidName) || got != "" {
			t.Errorf("OwnerName(%q) = %q, %v", input, got, err)
		}
	}
}

var _ TXTResolver = (*net.Resolver)(nil)
