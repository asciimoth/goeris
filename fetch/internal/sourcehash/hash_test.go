package sourcehash

import (
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func FuzzWithPrefix(f *testing.F) {
	f.Add("type url\nurl ", "http://a.test/\n", "https://b.test\n")
	f.Add("name "+strings.Repeat("a", 65530)+"\ntype url\nurl ", "x:a\n", "x:b\n")
	f.Fuzz(func(t *testing.T, records, a, b string) {
		hash := WithPrefix(records)
		for _, suffix := range []string{a, b, a} {
			want := blake2b.Sum256([]byte(Prefix + records + suffix))
			if got := hash(suffix); got != want {
				t.Fatalf("hash = %x, want %x", got, want)
			}
		}
	})
}
