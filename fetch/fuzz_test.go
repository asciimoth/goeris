package fetch

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Check identity, value order, and schema decisions after canonicalization.
func FuzzSource(f *testing.F) {
	for _, s := range []string{opening, "\xef\xbb\xbftype x", gitShared + "branch main\r", opening + "name  A#b \nerisfs.compression lzip-v1", opening + "alt https://x\nalt https://x", "type archive\rformat gzip\nerisfs.use false\rurl file:///C:/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		s, err := ParseSource(strings.NewReader(text))
		if err != nil {
			return
		}
		again := source(t, s.Canonical())
		if !reflect.DeepEqual(s, again) || s.ID() != again.ID() {
			t.Fatal("canonical source changed")
		}
		for _, lzip := range []bool{true, false} {
			a, e := s.Validate(lzip)
			b, e2 := again.Validate(lzip)
			if (e == nil) != (e2 == nil) || a != b {
				t.Fatal("schema changed")
			}
		}
	})
}

// Mutate complete frames, including markers and capability bits. Every
// accepted wire representation must already have its canonical spelling.
func FuzzResponse(f *testing.F) {
	for _, s := range []string{okFrame, refinement, "eris-fetch/1 fail\ncode unavailable\nkey url\n", "eris-fetch/1 refine\n@default k a%20b\ntype x\n@end\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, wire string) {
		r, err := ParseResponse(strings.NewReader(wire))
		if err != nil {
			return
		}
		got, err := r.MarshalText()
		if err != nil || !bytes.Equal(got, []byte(wire)) {
			t.Fatal("frame round trip", err)
		}
		// Exercise assembly without expanding a large shared block for each choice.
		if r.Status == "refine" {
			for _, i := range []int{0, len(r.Choices) - 1} {
				if _, err := r.Proposal(i); err != nil {
					t.Fatal(err)
				}
			}
		}
		_ = r.ValidateFor(source(t, gitShared), true)
	})
}

// Place arbitrary bytes inside a valid frame to exercise encoder and decoder
// agreement for labels that cannot be represented as ordinary UTF-8 text.
func FuzzChoiceLabel(f *testing.F) {
	for _, s := range []string{"main", "v1.2.3", "\xff\x00\n", "%2F", "élément/a"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, label string) {
		r := Response{Status: "refine", Shared: gitShared, Choices: []Choice{{"tip", label, commit}}}
		wire, err := r.MarshalText()
		if label == "" || len(label) > MaxMarkerSize {
			if err == nil {
				t.Fatal("invalid label")
			}
			return
		}
		if err != nil {
			if 3*len(label)+len("@default tip ") <= MaxMarkerSize {
				t.Fatal(err)
			}
			return
		}
		parsed, err := ParseResponse(bytes.NewReader(wire))
		if err != nil || !reflect.DeepEqual(r, parsed) {
			t.Fatal("lost label bytes", err)
		}
	})
}

func FuzzRouting(f *testing.F) {
	for _, s := range []string{"git.exec tool\ngit.exec\n", "url.http https://[::1]:1", "\n# ok\r", "git.exec bad/path", "archive.http http://host:99/a?b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		routes, err := ParseConfig(strings.NewReader(text))
		if err != nil {
			return
		}
		var b strings.Builder
		for typ, r := range routes {
			if r.HTTP != "" {
				b.WriteString(typ + ".http " + r.HTTP + "\n")
			}
			for _, arg := range r.Exec {
				b.WriteString(typ + ".exec")
				if arg != "" {
					b.WriteString(" " + arg)
				}
				b.WriteByte('\n')
			}
		}
		again, err := ParseConfig(strings.NewReader(b.String()))
		if b.Len() <= MaxSourceSize && (err != nil || !reflect.DeepEqual(routes, again)) {
			t.Fatal("route round trip", err)
		}
	})
}

func FuzzFileList(f *testing.F) {
	for _, s := range []string{"", "/x;/y;;", `"/s;a";"/a""b"`, `C:\folder\f;\\server\share\g`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		paths, err := ParseFileList(text)
		if err != nil {
			return
		}
		var encoded []string
		for _, p := range paths {
			encoded = append(encoded, "\""+strings.ReplaceAll(p, "\"", "\"\"")+"\"")
		}
		again, err := ParseFileList(strings.Join(encoded, ";"))
		if err != nil || !slices.Equal(paths, again) {
			t.Fatal("file list round trip", err)
		}
	})
}

// Keep the surrounding document well formed while mutating individual fields.
// This reaches URI, ref-name, checksum and output-mode validation directly.
func FuzzStandardField(f *testing.F) {
	for _, tc := range []struct{ typ, key, value string }{
		{"url", "url", "https://[::1]:443/a?x"}, {"git", "commit", "sha256:" + strings.Repeat("a", 64)}, {"git", "tag", "v1.0.0"}, {"git", "subdir", "a/b"},
		{"url", "checksum", helloChecksum}, {"url", "alt", "file:///C:/foo%2Fbar"}, {"url", "name", " x#y\t"}, {"archive", "format", "tar.gz"}, {"archive", "erisfs.compression", "lzip-v1"},
	} {
		f.Add(tc.typ, tc.key, tc.value)
	}
	f.Fuzz(func(t *testing.T, typ, key, value string) {
		if !typePattern.MatchString(typ) || !keyPattern.MatchString(key) || !validText(value) {
			return
		}
		wire := "type " + typ + "\n"
		if key != "url" {
			wire += "url https://e\n"
		}
		wire += key + " " + value
		s, err := ParseSource(strings.NewReader(wire))
		if err != nil {
			return
		}
		o, err := s.Validate(true)
		if err != nil {
			return
		}
		b, err := s.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		twin, err := ParseJSON(b)
		if err != nil || twin.ID() != s.ID() {
			t.Fatal("identity", err)
		}
		if o2, err := twin.Validate(true); err != nil || o2 != o {
			t.Fatal("options", err)
		}
	})
}
