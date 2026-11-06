package magnet

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func testCapability() eris.Capability {
	var reference eris.Reference
	var key eris.Key
	for i := range reference {
		reference[i] = byte(i)
		key[i] = byte(255 - i)
	}
	return eris.Capability{
		BlockSize: eris.BlockSize1KiB,
		Level:     2,
		Root:      eris.Pair{Reference: reference, Key: key},
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func TestNew(t *testing.T) {
	t.Parallel()

	capability := testCapability()
	uri, err := New(capability)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if uri.ExactTopic != capability {
		t.Fatalf("New() topic = %v, want %v", uri.ExactTopic, capability)
	}

	if _, err := New(eris.Capability{}); !errors.Is(err, ErrInvalidURI) {
		t.Fatalf("New(invalid) error = %v, want %v", err, ErrInvalidURI)
	}
}

func TestMarshalParseRoundTrip(t *testing.T) {
	t.Parallel()

	want := URI{
		ExactTopic:  testCapability(),
		DisplayName: "snowman ☃ & +.txt",
		ExactLength: uint64Pointer(539),
		Parameters: []Parameter{
			{Name: "xs", Value: "https://example.test/a?b=c"},
			{Name: "as", Value: "two"},
			{Name: "as", Value: "one"},
		},
	}
	text, err := want.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	wantText := "magnet:xt=" + want.ExactTopic.String() +
		"&dn=snowman%20%E2%98%83%20%26%20%2B.txt&xl=539" +
		"&as=one&as=two&xs=https:%2F%2Fexample.test%2Fa%3Fb%3Dc"
	if string(text) != wantText {
		t.Fatalf("MarshalText() = %q, want %q", text, wantText)
	}

	got, err := Parse(string(text))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want.Parameters = []Parameter{
		{Name: "as", Value: "one"},
		{Name: "as", Value: "two"},
		{Name: "xs", Value: "https://example.test/a?b=c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
	if got.String() != wantText {
		t.Fatalf("String() = %q, want %q", got.String(), wantText)
	}
}

func TestParseAcceptedForms(t *testing.T) {
	t.Parallel()

	topic := testCapability().String()
	tests := []struct {
		name string
		text string
		want URI
	}{
		{
			name: "question mark and mixed-case scheme",
			text: "MAGNET:?xt=" + topic,
			want: URI{ExactTopic: testCapability()},
		},
		{
			name: "parameter order and escapes",
			text: "magnet:xl=000&x%2Ey=a+b?c&dn=&xt=urn%3Aeris%3A" + strings.TrimPrefix(topic, "urn:eris:"),
			want: URI{
				ExactTopic:  testCapability(),
				ExactLength: uint64Pointer(0),
				Parameters:  []Parameter{{Name: "x.y", Value: "a b?c"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(test.text)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Parse() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseRejectsInvalidURI(t *testing.T) {
	t.Parallel()

	topic := testCapability().String()
	tests := []string{
		"",
		"magnet",
		"http:xt=" + topic,
		"magnet:",
		"magnet:?",
		"magnet:??xt=" + topic,
		"magnet:xt=" + topic + "#fragment",
		"magnet:xt=" + topic + "?extra",
		"magnet:xt",
		"magnet:=value&xt=" + topic,
		"magnet:%=value&xt=" + topic,
		"magnet:x=%&xt=" + topic,
		"magnet:x=%FF&xt=" + topic,
		"magnet:%FF=x&xt=" + topic,
		"magnet:dn=name",
		"magnet:xt=not-an-eris-urn",
		"magnet:xt=" + topic + "&xt=" + topic,
		"magnet:xt=" + topic + "&dn=a&dn=b",
		"magnet:xt=" + topic + "&xl=1&xl=2",
		"magnet:xt=" + topic + "&xl=",
		"magnet:xt=" + topic + "&xl=-1",
		"magnet:xt=" + topic + "&xl=%2B1",
		"magnet:xt=" + topic + "&xl=18446744073709551616",
		"magnet:xt=" + topic + "&",
	}
	for _, text := range tests {
		if _, err := Parse(text); !errors.Is(err, ErrInvalidURI) {
			t.Errorf("Parse(%q) error = %v, want %v", text, err, ErrInvalidURI)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	invalidUTF8 := string([]byte{0xff})
	tests := []URI{
		{},
		{ExactTopic: testCapability(), DisplayName: invalidUTF8},
		{ExactTopic: testCapability(), Parameters: []Parameter{{Name: "", Value: "x"}}},
		{ExactTopic: testCapability(), Parameters: []Parameter{{Name: "xt", Value: "x"}}},
		{ExactTopic: testCapability(), Parameters: []Parameter{{Name: "dn", Value: "x"}}},
		{ExactTopic: testCapability(), Parameters: []Parameter{{Name: "xl", Value: "x"}}},
		{ExactTopic: testCapability(), Parameters: []Parameter{{Name: invalidUTF8, Value: "x"}}},
		{ExactTopic: testCapability(), Parameters: []Parameter{{Name: "x", Value: invalidUTF8}}},
	}
	for i, uri := range tests {
		if err := uri.Validate(); !errors.Is(err, ErrInvalidURI) {
			t.Errorf("test %d: Validate() error = %v, want %v", i, err, ErrInvalidURI)
		}
		if text, err := uri.MarshalText(); !errors.Is(err, ErrInvalidURI) || text != nil {
			t.Errorf("test %d: MarshalText() = %q, %v", i, text, err)
		}
		if got := uri.String(); got != "" {
			t.Errorf("test %d: String() = %q, want empty string", i, got)
		}
	}
	if err := (URI{ExactTopic: testCapability()}).Validate(); err != nil {
		t.Fatalf("valid URI: Validate() error = %v", err)
	}
}

func TestUnmarshalText(t *testing.T) {
	t.Parallel()

	want := URI{ExactTopic: testCapability()}
	var got URI
	if err := got.UnmarshalText([]byte(want.String())); err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UnmarshalText() = %#v, want %#v", got, want)
	}

	before := got
	if err := got.UnmarshalText([]byte("invalid")); !errors.Is(err, ErrInvalidURI) {
		t.Fatalf("UnmarshalText(invalid) error = %v, want %v", err, ErrInvalidURI)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("UnmarshalText(invalid) changed receiver to %#v", got)
	}

	var nilURI *URI
	if err := nilURI.UnmarshalText([]byte(want.String())); !errors.Is(err, ErrInvalidURI) {
		t.Fatalf("nil UnmarshalText() error = %v, want %v", err, ErrInvalidURI)
	}
}

func TestMarshalTextDoesNotChangeParameterOrder(t *testing.T) {
	t.Parallel()

	uri := URI{
		ExactTopic: testCapability(),
		Parameters: []Parameter{{Name: "z", Value: "1"}, {Name: "a", Value: "2"}},
	}
	before := append([]Parameter(nil), uri.Parameters...)
	if _, err := uri.MarshalText(); err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if !reflect.DeepEqual(uri.Parameters, before) {
		t.Fatalf("MarshalText() changed Parameters to %#v", uri.Parameters)
	}
}
