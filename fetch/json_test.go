package fetch

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestJSONFieldErrors(t *testing.T) {
	for _, tc := range []struct {
		member string
		want   Failure
	}{
		{`"extra":{"nested":{"name":"value"}}`, Failure{"unknown-key", "extra.nested.name"}},
		{`"extra":[]`, Failure{"unknown-key", "extra"}},
		{`"extra":["value"]`, Failure{"unknown-key", "extra"}},
		{`"name":["file"]`, Failure{"invalid-source", "name"}},
		{`"alt":"https://example.org/mirror"`, Failure{"invalid-source", "alt"}},
	} {
		s, err := ParseJSON([]byte(`{` + tc.member + `,"url":"https://example.org","type":"url"}`))
		if !errors.Is(err, tc.want) || s.ID() != "" {
			t.Errorf("%s: got ID %q and error %v, want %v", tc.member, s.ID(), err, tc.want)
		}
	}
}

func TestJSONNestedKeyLimit(t *testing.T) {
	prefix := strings.Repeat("a", MaxLineSize-2)
	parse := func(leaf string) error {
		_, err := ParseJSON([]byte(`{"type":"url","url":"https://example.org","` + prefix + `":{"` + leaf + `":""}}`))
		return err
	}
	if err := parse("b"); !errors.Is(err, Failure{"unknown-key", prefix + ".b"}) {
		t.Errorf("flattened key at the limit: %.80v", err)
	}
	if err := parse("bb"); !errors.Is(err, errLimit) {
		t.Errorf("flattened key over the limit: %.80v", err)
	}
}

func TestJSONCanonicalSizeLimit(t *testing.T) {
	const mirror = "https://mirror.example/"
	full := mirror + strings.Repeat("a", MaxLineSize-len("alt ")-len(mirror))
	mirrors := slices.Repeat([]string{full}, 16)
	// With LF, each full mirror takes 65,536 bytes. Leave room for the
	// type and primary URL in the last record.
	mirrors[15] = full[:len(full)-len(opening)]
	input := map[string]any{"type": "url", "url": "https://example.org/hello", "alt": mirrors}
	parse := func() (Source, error) {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return ParseJSON(data)
	}
	s, err := parse()
	if err != nil || len(s.Canonical()) != MaxSourceSize {
		t.Fatalf("at limit: canonical size %d, error %v", len(s.Canonical()), err)
	}
	mirrors[15] += "b"
	if _, err := parse(); !errors.Is(err, errLimit) {
		t.Errorf("over limit: %v", err)
	}
}

func TestJSON(t *testing.T) {
	for _, tc := range []struct{ json, wire string }{
		{`{"type":"url","url":"https://example.org/hello","alt":[]}`, opening},
		{`{"type":"url","url":"https://example.org/hello","name":"\ud83d\ude80","alt":["ftp://x","ftp://x"],"eris":{"secret":""},"erisfs":{"use":"true","layout":"flat"}}`, opening + "name 🚀\nalt ftp://x\nalt ftp://x\neris.secret\nerisfs.use true\nerisfs.layout flat\n"},
		{`{"type":"git","url":"https://example.org/project.git"}`, gitShared},
		{`{"type":"url","url":"https://example.org/hello","name":"\\uD800 \u0061\t"}`, opening + "name \\uD800 a\t\n"},
	} {
		s, err := ParseJSON([]byte(tc.json))
		if err != nil || s.Canonical() != source(t, tc.wire).Canonical() {
			t.Error(tc, s, err)
			continue
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseJSON(b)
		if err != nil || again.ID() != s.ID() {
			t.Fatal(string(b), err)
		}
		if s.Type() == "url" && s.fields["alt"] == nil && !strings.Contains(string(b), `"alt":[]`) {
			t.Fatal(string(b))
		}
	}
	for _, input := range []string{`{`, `[]`, `{}`, `{} {}`, `{"type":"url","\u0074ype":"url"}`, `{"eris.secret":""}`, `{"eris":{}}`, `{"extra":{"x":""}}`, `{"type":true}`, `{"type":[]}`, `{"alt":"x"}`, `{"alt":[["x"]]}`, `{"url":"\udc00"}`, `{"url":"\ud800"}`, `{"url":"\ud800\u1234"}`, `{"eris":{"secret":"a\nb"}}`, `{"alt":["a\r"]}`, `{"name":"ok"}`, `{"type":"url"}`, `{"type":"git","url":"https://e","alt":[]}`, "{\"name\":\"\xff\"}", `{"name":"` + strings.Repeat("x", 65535) + `"}`} {
		if _, err := ParseJSON([]byte(input)); err == nil {
			t.Errorf("accepted %.100q", input)
		}
	}
	if _, err := source(t, "type x").MarshalJSON(); err == nil {
		t.Fatal("unknown schema")
	}
}

func TestUnmarshalJSON(t *testing.T) {
	want := source(t, opening)
	wire, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	s := source(t, gitShared)
	if err := json.Unmarshal(wire, &s); err != nil || s.ID() != want.ID() {
		t.Fatalf("decode source: %v, got %q", err, s.Canonical())
	}
	if err := json.Unmarshal([]byte(`{"type":"url"}`), &s); err == nil || s.ID() != want.ID() {
		t.Fatalf("invalid replacement: %v, got %q", err, s.Canonical())
	}
}

func FuzzJSON(f *testing.F) {
	for _, s := range []string{`{"url":"https://example.org","type":"url","alt":[]}`, `{"type":"git","url":"https://e","branch":"stable","erisfs":{"use":"true"}}`, `{"name":"\uD83D\uDCA1"}`, `{"a":["x",null]}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		s, err := ParseJSON([]byte(text))
		if err != nil {
			return
		}
		b, err := s.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		round, err := ParseJSON(b)
		if err != nil || round.Canonical() != s.Canonical() {
			t.Fatal("JSON round trip", err)
		}
	})
}
