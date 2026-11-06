package magnet

import (
	"reflect"
	"slices"
	"testing"
	"unicode/utf8"
)

func FuzzParse(f *testing.F) {
	valid := URI{
		ExactTopic:  testCapability(),
		DisplayName: "file name",
		ExactLength: uint64Pointer(42),
		Parameters:  []Parameter{{Name: "xs", Value: "https://example.test/"}},
	}.String()
	f.Add(valid)
	f.Add("magnet:?xt=" + testCapability().String())
	f.Add("magnet:xt=%")
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		uri, err := Parse(text)
		if err != nil {
			return
		}
		canonical, err := uri.MarshalText()
		if err != nil {
			t.Fatalf("accepted URI does not marshal: %v", err)
		}
		reparsed, err := Parse(string(canonical))
		if err != nil {
			t.Fatalf("canonical URI does not parse: %v", err)
		}
		if !reflect.DeepEqual(reparsed, uri) {
			t.Fatalf("round trip = %#v, want %#v", reparsed, uri)
		}
		if reparsed.String() != string(canonical) {
			t.Fatalf("canonical text changed from %q to %q", canonical, reparsed.String())
		}
	})
}

func FuzzUnmarshalText(f *testing.F) {
	f.Add([]byte(URI{ExactTopic: testCapability()}.String()))
	f.Add([]byte("invalid"))
	f.Add([]byte{0xff, 0x00})
	f.Fuzz(func(t *testing.T, text []byte) {
		before := URI{
			ExactTopic: testCapability(),
			Parameters: []Parameter{{Name: "sentinel", Value: "unchanged"}},
		}
		uri := before
		err := uri.UnmarshalText(text)
		if err != nil && !reflect.DeepEqual(uri, before) {
			t.Fatalf("failed unmarshal changed receiver to %#v", uri)
		}
		if err == nil && uri.Validate() != nil {
			t.Fatal("successful unmarshal produced invalid URI")
		}
	})
}

func FuzzMarshalParse(f *testing.F) {
	f.Add("name", uint64(0), true, "x", "y", "x", "z")
	f.Add("☃", ^uint64(0), false, "xs", "https://example.test/a?b=c", "as", "https://example.test/")
	f.Add("", uint64(1), true, "dn", "reserved", "x", "y")
	f.Add(string([]byte{0xff}), uint64(2), false, "x", "y", "z", "w")
	f.Fuzz(func(
		t *testing.T,
		name string,
		length uint64,
		hasLength bool,
		firstName string,
		firstValue string,
		secondName string,
		secondValue string,
	) {
		uri := URI{
			ExactTopic:  testCapability(),
			DisplayName: name,
			Parameters: []Parameter{
				{Name: firstName, Value: firstValue},
				{Name: secondName, Value: secondValue},
			},
		}
		if hasLength {
			uri.ExactLength = &length
		}
		text, err := uri.MarshalText()
		valid := utf8.ValidString(name) && validFuzzParameter(firstName, firstValue) &&
			validFuzzParameter(secondName, secondValue)
		if !valid {
			if err == nil {
				t.Fatalf("MarshalText() accepted invalid URI as %q", text)
			}
			return
		}
		if err != nil {
			t.Fatalf("MarshalText() error = %v", err)
		}
		parsed, err := Parse(string(text))
		if err != nil {
			t.Fatalf("Parse(MarshalText()) error = %v", err)
		}
		want := uri
		want.Parameters = slices.Clone(uri.Parameters)
		slices.SortFunc(want.Parameters, compareParameters)
		if !reflect.DeepEqual(parsed, want) {
			t.Fatalf("round trip = %#v, want %#v", parsed, want)
		}
	})
}

func validFuzzParameter(name, value string) bool {
	return utf8.ValidString(name) && utf8.ValidString(value) && name != "" && !isReserved(name)
}
