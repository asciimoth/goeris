package url

import (
	"iter"
	"strings"

	eris "github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/fetch"
	"github.com/asciimoth/goeris/fetch/internal/sourcehash"
)

// lockHashes visits the exact request first, then a minified description
// for each supplied location. The source must have passed validation.
func lockHashes(exact [32]byte, s fetch.Source, opts fetch.Options, aliases []string) iter.Seq[[32]byte] {
	return func(yield func([32]byte) bool) {
		if !yield(exact) || len(aliases) == 0 {
			return
		}
		hashURL := sourcehash.WithPrefix(minifiedURLPrefix(s, opts))
		seen := make(map[string]bool)
		for _, uri := range aliases {
			if seen[uri] {
				continue
			}
			seen[uri] = true
			hash := hashURL(uri + "\n")
			if hash != exact && !yield(hash) {
				return
			}
		}
	}
}

// Emit the retained settings in canonical key order. The validated source
// retains the original spelling of the secret and file name.
func minifiedURLPrefix(s fetch.Source, opts fetch.Options) string {
	var records strings.Builder
	if opts.Secret != (eris.Secret{}) {
		records.WriteString("eris.secret ")
		records.WriteString(s.Values("eris.secret")[0])
		records.WriteByte('\n')
	}
	if opts.Compression == "auto" {
		records.WriteString("erisfs.compression auto\n")
	}
	if mode := s.Values("executable"); len(mode) != 0 && mode[0] == "true" {
		records.WriteString("executable true\n")
	}
	if name := s.Values("name"); len(name) != 0 {
		records.WriteString("name ")
		records.WriteString(name[0])
		records.WriteByte('\n')
	}
	records.WriteString("type url\nurl ")
	return records.String()
}
