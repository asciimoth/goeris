package fetch

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"golang.org/x/crypto/blake2b"
)

const opening = "type url\nurl https://example.org/hello\n"
const helloChecksum = "sha256:c0535e4be2b79ffd93291305436bf889314e4a3faec05ecffcbb7df31ad9e51a"

func source(t testing.TB, text string) Source {
	t.Helper()
	s, err := ParseSource(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSourceIDs(t *testing.T) {
	a, b := "alt https://a.example/hello\n", "alt https://b.example/hello\n"
	for _, tc := range []struct{ text, id string }{
		{opening, "7BSALWD5ZKAQCPOZAD3UJZQ2W3G2ITP3BLBIV57CJXBHXXZYGHPQ"},
		{"# note\r\nurl https://example.org/hello\n\rtype url", "7BSALWD5ZKAQCPOZAD3UJZQ2W3G2ITP3BLBIV57CJXBHXXZYGHPQ"},
		{opening + "checksum " + helloChecksum + "\n", "OKJKPCCUMKORAS4SGS47VBP7FXUL2ZI3PQLLM74GZ2F4TROMUHZA"},
		{opening + a + b, "5UZVXYW5TYYW5QBH35FNTI2L5U36S5D3WR3CD3NTRGCAQLZYXLGA"},
		{opening + b + a, "VXQJBNKSSWY765UPRBL6YUMJR3YDDULPPXRVGPOSFZFE42XABFJA"},
	} {
		if id := source(t, tc.text).ID(); id != tc.id+":blake2" {
			t.Errorf("%q: %s", tc.text, id)
		}
	}
	if string(source(t, opening).RawKey()) != "eris-fetch/source/v1\x00"+opening {
		t.Fatal("raw key")
	}
	empty := source(t, opening+"eris.secret")
	if empty.ID() != source(t, opening+"eris.secret ").ID() || empty.ID() == source(t, opening).ID() || empty.ID() == source(t, opening+"eris.secret "+strings.Repeat("0", 64)).ID() {
		t.Fatal("empty secret identity")
	}
	s := source(t, " \n"+opening+"name  A#b \nalt a\n\t \ralt a\nalt z")
	v := s.Values("alt")
	v[0] = "modified"
	if !reflect.DeepEqual(s.Values("alt"), []string{"a", "a", "z"}) || s.value("name") != " A#b " {
		t.Fatal(s.Canonical())
	}
}

func TestSourceLimitsAndReads(t *testing.T) {
	fullLine := "#" + strings.Repeat("a", MaxLineSize-1)
	for _, tc := range []string{fullLine + "\r" + opening, strings.Repeat(fullLine+"\n", 15) + opening + "#" + strings.Repeat(" ", 65536-len(opening)-1)} {
		if _, err := ParseSource(strings.NewReader(tc)); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{opening + fullLine + "x", strings.Repeat("\n", MaxSourceSize) + opening,
		// At EOF, adding the final LF would exceed the canonical size.
		opening + strings.Repeat("a "+strings.Repeat("x", 65533)+"\n", 15) + "b " + strings.Repeat("y", 65536-len(opening)-2),
	} {
		if _, err := ParseSource(strings.NewReader(text)); !errors.Is(err, errLimit) {
			t.Fatalf("limit: %v", err)
		}
	}
	for _, text := range []string{"", "type", "type URL", "type url\ntype url", "\ufeff" + opening, "type\turl", opening + "a..b", opening + " a", opening + "x\nx-y\nx.y", opening + "#\xff", opening + "#\x7f", opening + "#\x00"} {
		if _, err := ParseSource(strings.NewReader(text)); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	s, err := ParseSource(iotest.OneByteReader(strings.NewReader(opening)))
	if err != nil || s.Canonical() != opening {
		t.Fatal(s, err)
	}
	_, err = ParseSource(io.MultiReader(strings.NewReader(opening), iotest.ErrReader(io.ErrUnexpectedEOF)))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}

func TestSchema(t *testing.T) {
	for _, tc := range []struct{ typ, extra, code, key string }{
		{"x", "", "unsupported-type", "type"}, {"url", "Tag yes", "unknown-key", "Tag"}, {"url", "url https://e", "invalid-source", "url"},
		{"url", "alt https://ok\nalt bad", "invalid-source", "alt"}, {"url", "eris.secret A", "invalid-source", "eris.secret"},
		{"url", "erisfs.use True", "invalid-source", "erisfs.use"}, {"url", "erisfs.version 2", "unsupported-option", "erisfs.version"},
		{"url", "erisfs.use true", "invalid-source", "name"}, {"url", "name ..", "invalid-source", "name"}, {"url", "name ok\nerisfs.use false", "invalid-source", "name"},
		{"url", "executable true", "invalid-source", "executable"}, {"url", "checksum sha256:FF", "invalid-source", "checksum"},
		{"url", "checksum sha256:ff", "invalid-source", "checksum"}, {"url", "checksum sha1:0", "unsupported-option", "checksum"},
		{"archive", "erisfs.layout nested", "unsupported-option", "erisfs.layout"}, {"archive", "erisfs.compression lzip-v1", "unsupported-option", "erisfs.compression"},
		{"archive", "format gzip", "unsupported-option", "format"}, {"archive", "format rar", "unsupported-option", "format"},
		{"archive", "strip-root false\nerisfs.use false", "unsupported-option", "strip-root"},
		{"git", "erisfs.use false", "unsupported-option", "erisfs.use"}, {"git", "commit abc", "invalid-source", "commit"}, {"git", "branch main\ntag v1", "invalid-source", "tag"},
		{"git", "tag a.lock/b", "invalid-source", "tag"}, {"git", "branch foo..bar", "invalid-source", "branch"}, {"git", "branch a\tb", "invalid-source", "branch"},
		{"git", "subdir a//b", "invalid-source", "subdir"},
	} {
		s := source(t, "type "+tc.typ+"\nurl https://example.org\n"+tc.extra)
		_, err := s.Validate(false)
		if !errors.Is(err, Failure{tc.code, tc.key}) {
			t.Errorf("%s %s: %v", tc.typ, tc.extra, err)
		}
	}
	for _, tc := range []struct {
		text, kind, comp string
		direct           bool
	}{
		{opening, "blob", "", true}, {opening + "name a\\b\neris.secret " + strings.Repeat("ab", 32) + "\nexecutable true\nerisfs.version 2\nerisfs.layout flat\nerisfs.compression lzip-v1", "erisfs", "lzip-v1", true},
		{opening + "alt custom:example\nchecksum " + helloChecksum, "blob", "", true},
		{"type archive\nurl https://e\nformat zip", "erisfs", "none", true}, {"type archive\nurl https://e\nformat gzip\nerisfs.use false", "blob", "", true},
		{"type git\nurl https://e\nsubdir a/b\nbranch main", "erisfs", "none", false}, {"type git\nurl https://e\ntag releases/v1", "erisfs", "none", true},
	} {
		o, err := source(t, tc.text).Validate(true)
		if err != nil || o.Kind != tc.kind || o.Compression != tc.comp || o.Direct != tc.direct {
			t.Error(tc, o, err)
		}
		if strings.Contains(tc.text, "eris.secret") && o.Secret[31] != 0xab {
			t.Fatal("secret")
		}
	}
	for _, text := range []string{"type url", "type git\nurl file:/C:/repo"} {
		if _, err := source(t, text).Validate(false); err == nil {
			t.Fatal(text)
		}
	}
}

func TestURI(t *testing.T) {
	for _, s := range []string{
		"https://x", "http://[::1]:65535/p%20x?q=@/?", "http://[v7.test]:1", "https://%65.test:00080",
		"custom:arg", "custom://:123/path", "ftp://host/p;type=i", "ftp://e/p%3Btype=a;type=I",
		"ftps://e/p", "sftp://e/%FF", "file://localhost/C:/a", "file:///C:/a",
	} {
		if !validURI(s, false) {
			t.Errorf("rejected %q", s)
		}
	}
	for _, s := range []string{
		"host", "HTTP://e", "https:foo", "http://e@host", "http://e#", "http://e/%zz", "http://e/%0", "http://e/ä", "http://e/\\",
		"http://e:", "http://e:0", "http://e:-1", "http://e:65536", "http://:23", "http://[bad]", "http://[::", "http://[::1]x", "http://[fe80::1%25eth0]", "http://host/a b",
		"custom://:0/path", "ftp://:21/path", "ftp://e/path?", "ftp://e/path;type=a", "ftp://e/path;type=d", "ftp://e/path;type=i/file", "ftp://e/path;other", "ftps://e/dir;name/file",
		"file://a/C:/f", "file:///C:/%2f", "file:///C:/%00", "file:x", "sftp://e", "sftp:/x", "sftp://:22/x", "sftp://e/a?",
	} {
		if validURI(s, false) {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestRemoteFileURI(t *testing.T) {
	for _, uri := range []string{"file:///tmp/input", "file:////server/share", "file:///C:/a%5Cb", "file:///C:/%ff"} {
		for _, key := range []string{"url", "alt"} {
			wire := "type url\n"
			if key == "alt" {
				wire += "url https://example.org/input\n"
			}
			s := source(t, wire+key+" "+uri)
			if _, err := s.validate(false, true); !errors.Is(err, Failure{"invalid-source", key}) {
				t.Fatalf("Windows handler accepted %s %s: %v", key, uri, err)
			}
			_, localErr := s.Validate(false)
			if (localErr != nil) != (runtime.GOOS == "windows") {
				t.Fatalf("local handler validation for %s: %v", uri, localErr)
			}
			data, err := s.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			again, err := ParseJSON(data)
			if err != nil || again.ID() != s.ID() {
				t.Fatalf("JSON round trip for %s: %v", uri, err)
			}
			r, err := ParseResponse(strings.NewReader(okFrame))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.ValidateFor(s, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, uri := range []string{"file:/C:/pkg", "file://localhost/D:/a%20b", "file:///z:/"} {
		if _, err := source(t, "type url\nurl "+uri).validate(false, true); err != nil {
			t.Fatal(uri, err)
		}
	}
}

func TestChecksum(t *testing.T) {
	for _, s := range []string{helloChecksum, fmt.Sprintf("blake2b-256:%x", blake2b.Sum256([]byte("Hello world!")))} {
		c, err := ParseChecksum(s)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Verify(strings.NewReader("Hello world!")); err != nil {
			t.Fatal(err)
		}
		if err = c.Verify(strings.NewReader("changed")); !errors.Is(err, Failure{Code: "integrity"}) {
			t.Fatal(err)
		}
		if err = c.Verify(iotest.ErrReader(io.ErrUnexpectedEOF)); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal(err)
		}
	}
	if (Checksum{}).Verify(strings.NewReader("")) == nil {
		t.Fatal("zero checksum")
	}
	if (Failure{Code: "limit"}).Error() != "limit" || (Failure{"invalid-source", "type"}).Error() != "invalid-source (type)" {
		t.Fatal("error text")
	}
}

func TestReadFailureHasNoID(t *testing.T) {
	s, err := ParseSource(io.MultiReader(strings.NewReader(opening), iotest.ErrReader(io.ErrUnexpectedEOF)))
	if !errors.Is(err, Failure{Code: "unavailable"}) || s.ID() != "" || s.RawKey() != nil {
		t.Fatal("partial source", err)
	}
}

func TestReadFailureDuringRecord(t *testing.T) {
	// The final dot could start another key segment if the read completed.
	input := io.MultiReader(strings.NewReader(opening+"eris."), iotest.ErrReader(io.ErrUnexpectedEOF))
	s, err := ParseSource(input)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, Failure{Code: "unavailable"}) || s.ID() != "" {
		t.Fatalf("incomplete record: ID %q, error %v", s.ID(), err)
	}
}

func TestSourceReaderFailureCode(t *testing.T) {
	cause := Failure{Code: "denied"}
	input := io.MultiReader(strings.NewReader(opening), iotest.ErrReader(cause))
	s, err := ParseSource(input)
	var failure Failure
	if !errors.As(err, &failure) || failure.Code != "unavailable" || !errors.Is(err, cause) || s.ID() != "" {
		t.Fatalf("source ID %q, error %v", s.ID(), err)
	}
}

// Stop the test even if verification keeps reading an idle stream.
type idleReader struct{ reads int }

func (r *idleReader) Read([]byte) (int, error) {
	r.reads++
	if r.reads > 100 {
		return 0, io.ErrUnexpectedEOF
	}
	return 0, nil
}

func TestChecksumReadProgress(t *testing.T) {
	c, err := ParseChecksum(helloChecksum)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(&idleReader{}); !errors.Is(err, io.ErrNoProgress) || !errors.Is(err, Failure{Code: "unavailable"}) {
		t.Fatalf("idle input: %v", err)
	}
	// Matching bytes cannot establish integrity until the stream completes.
	input := io.MultiReader(strings.NewReader("Hello world!"), iotest.ErrReader(io.ErrUnexpectedEOF))
	if err := c.Verify(input); !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, Failure{Code: "unavailable"}) {
		t.Fatalf("incomplete input: %v", err)
	}
}

func TestChecksumAcrossReads(t *testing.T) {
	for _, size := range []int{0, 32 << 10, 32<<10 + 1} {
		data := strings.Repeat("a", size)
		c, err := ParseChecksum(fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data))))
		if err != nil {
			t.Fatal(err)
		}
		input := iotest.DataErrReader(strings.NewReader(data))
		if err := c.Verify(input); err != nil {
			t.Errorf("%d bytes with EOF on the final read: %v", size, err)
		}
	}
}

func TestMissingReader(t *testing.T) {
	s, err := ParseSource(nil)
	if !errors.Is(err, Failure{Code: "unavailable"}) || s.ID() != "" {
		t.Errorf("missing source reader: ID %q, error %v", s.ID(), err)
	}
	if _, err := ParseResponse(nil); !errors.Is(err, ErrProtocol) {
		t.Errorf("missing frame reader: %v", err)
	}
	if routes, err := ParseConfig(nil); routes != nil || !errors.Is(err, ErrConfig) {
		t.Errorf("missing config reader: routes %v, error %v", routes, err)
	}
	c, err := ParseChecksum(helloChecksum)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(nil); !errors.Is(err, Failure{Code: "unavailable"}) {
		t.Errorf("missing resource reader: %v", err)
	}
}
