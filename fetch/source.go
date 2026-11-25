package fetch

import (
	"bufio"
	"encoding/base32"
	"errors"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/goeris/fetch/internal/sourcehash"
)

// Wire limits count bytes, including comments and separators for document limits.
const (
	MaxSourceSize   = 1 << 20
	MaxLineSize     = 65535
	MaxResponseSize = 64 << 20
	MaxMarkerSize   = 1 << 18
)

// Failure is a protocol failure code with an optional offending key.
type Failure struct{ Code, Key string }

func (f Failure) Error() string {
	if f.Key != "" {
		return f.Code + " (" + f.Key + ")"
	}
	return f.Code
}

var (
	keyPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z][A-Za-z0-9_-]*)*$`)
	typePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	tokenPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	errLimit     = Failure{Code: "limit"}
	errInvalid   = Failure{Code: "invalid-source"}
	// The scanner also returns errors from the reader. Keep its size signal
	// private so a reader's own Failure cannot be mistaken for a parser error.
	errScanLimit = errors.New("fetch input exceeds wire limit")
	errRecord    = errors.New("invalid fetch record")
	errNoReader  = errors.New("missing fetch input reader")
	// ErrProtocol indicates an invalid response frame or a result incompatible with its request.
	ErrProtocol = errors.New("protocol-error")
)

// Source holds a parsed description. Its accessors return independent values.
type Source struct {
	fields    map[string][]string
	canonical string
}

// Type returns the source type.
func (s Source) Type() string { return s.value("type") }

// Values returns the records for key in encounter order.
func (s Source) Values(key string) []string { return slices.Clone(s.fields[key]) }
func (s Source) value(key string) string {
	if v := s.fields[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Canonical returns sorted records without comments or inserted defaults.
func (s Source) Canonical() string { return s.canonical }

// RawKey returns the versioned byte string used by a raw-key lock store.
// It returns nil for the zero Source.
func (s Source) RawKey() []byte {
	if s.canonical == "" {
		return nil
	}
	return []byte(sourcehash.Prefix + s.canonical)
}

// Hash returns the binary source ID used by LockDB, or all zeros for a zero Source.
func (s Source) Hash() [32]byte {
	if s.canonical == "" {
		return [32]byte{}
	}
	return sourcehash.Sum256(s.canonical)
}

// ID returns the prehashed Locks v1 identifier, or "" for the zero Source.
func (s Source) ID() string {
	if s.canonical == "" {
		return ""
	}
	d := s.Hash()
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(d[:]) + ":blake2"
}

type documentKind uint8

const (
	sourceDocument documentKind = iota
	configDocument
	responseFrame
)

// readLines enforces both byte budgets before handing a line to its parser.
// A source permits CR and an unterminated last line. A frame requires LF.
func readLines(r io.Reader, kind documentKind, visit func(string) error) error {
	if r == nil {
		return errNoReader
	}
	frame := kind == responseFrame
	total := MaxSourceSize
	if frame {
		total = MaxResponseSize
	}
	lr := &io.LimitedReader{R: r, N: int64(total) + 1}
	sc := bufio.NewScanner(lr)
	sc.Buffer(nil, MaxMarkerSize+2)
	// A short read can extend a line one byte at a time. Resume the scan
	// at the first new byte to keep the work linear.
	scanned := 0
	sc.Split(func(data []byte, eof bool) (int, []byte, error) {
		if lr.N == 0 {
			return 0, nil, errScanLimit
		}
		limit := MaxLineSize
		if frame {
			// A fail frame can identify a maximum-length source key.
			limit += len("key ")
		}
		if frame && len(data) > 0 && data[0] == '@' {
			limit = MaxMarkerSize
		}
		for i := scanned; i < len(data); i++ {
			b := data[i]
			if b == '\n' || (!frame && b == '\r') {
				scanned = 0
				return i + 1, data[:i], nil
			}
			if i == limit {
				return 0, nil, errScanLimit
			}
		}
		scanned = len(data)
		if eof && len(data) > 0 {
			if frame {
				return 0, nil, ErrProtocol
			}
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for sc.Scan() {
		// Scanner can return a partial final line after a read error.
		// Preserve that error before interpreting the incomplete record.
		if sc.Err() != nil {
			break
		}
		line := sc.Text()
		if !validText(line) {
			return errRecord
		}
		if err := visit(line); err != nil {
			return err
		}
	}
	err := sc.Err()
	if err != nil && err != errScanLimit && kind == sourceDocument {
		return errors.Join(Failure{Code: "unavailable"}, err)
	}
	return err
}

func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for i := range len(s) {
		if b := s[i]; b == 127 || b < 32 && b != '\t' {
			return false
		}
	}
	return true
}

func readRecords(r io.Reader, kind documentKind) (map[string][]string, error) {
	fields := make(map[string][]string)
	canonicalSize := 0
	err := readLines(r, kind, func(line string) error {
		if strings.HasPrefix(line, "#") || strings.Trim(line, " \t") == "" {
			return nil
		}
		k, v, _ := strings.Cut(line, " ")
		if !keyPattern.MatchString(k) {
			return errRecord
		}
		canonicalSize += len(k) + 1 // key and LF
		if v != "" {
			canonicalSize += 1 + len(v)
		}
		if canonicalSize > MaxSourceSize {
			return errScanLimit
		}
		fields[k] = append(fields[k], v)
		return nil
	})
	return fields, err
}

// ParseSource reads one complete source. It checks the text grammar, type
// identifier, prefix conflicts, and size of both the input and canonical text.
func ParseSource(r io.Reader) (Source, error) {
	fields, err := readRecords(r, sourceDocument)
	switch err {
	case nil:
		return makeSource(fields)
	case errRecord:
		return Source{}, errInvalid
	case errScanLimit:
		return Source{}, errLimit
	case errNoReader:
		return Source{}, Failure{Code: "unavailable"}
	default:
		return Source{}, err
	}
}

func makeSource(fields map[string][]string) (Source, error) {
	s := Source{fields: fields}
	if len(fields["type"]) != 1 || !typePattern.MatchString(s.Type()) {
		return Source{}, Failure{"invalid-source", "type"}
	}
	keys := slices.Sorted(maps.Keys(fields))
	var b strings.Builder
	for _, k := range keys {
		// An intervening key such as a-b can sort between a and a.b.
		i, _ := slices.BinarySearch(keys, k+".")
		if i < len(keys) && strings.HasPrefix(keys[i], k+".") {
			return Source{}, errInvalid
		}
		for _, v := range fields[k] {
			b.WriteString(k)
			if v != "" {
				b.WriteByte(' ')
				b.WriteString(v)
			}
			b.WriteByte('\n')
		}
	}
	s.canonical = b.String()
	return s, nil
}
