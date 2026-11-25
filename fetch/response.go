package fetch

import (
	"errors"
	"io"
	"slices"
	"strings"

	eris "github.com/asciimoth/goeris"
)

// Choice is one refinement. Label contains the decoded bytes, which can be
// non-UTF-8. Records is the canonical text added to the shared records.
type Choice struct{ Kind, Label, Records string }

// Response represents one complete frame. Status selects the active fields:
// "ok" uses Kind and Cap, "fail" uses Failure, and "refine" uses Shared and
// Choices. The first choice is the default.
type Response struct {
	Status, Kind string
	Cap          eris.Capability
	Failure      Failure
	Shared       string
	Choices      []Choice
}

// Proposal assembles the selected source. ValidateFor checks the entire set
// against the request before the caller selects an index.
func (r Response) Proposal(index int) (Source, error) {
	if index < 0 || index >= len(r.Choices) || r.Status != "refine" {
		return Source{}, ErrProtocol
	}
	return ParseSource(io.MultiReader(strings.NewReader(r.Shared), strings.NewReader(r.Choices[index].Records)))
}

const literalLabel = unreserved + "/"
const upperDigits = "0123456789ABCDEF"

func decodeLabel(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(literalLabel, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		if c != '%' || len(s)-i < 3 {
			return "", false
		}
		hi := strings.IndexByte(upperDigits, s[i+1])
		lo := strings.IndexByte(upperDigits, s[i+2])
		if hi < 0 || lo < 0 {
			return "", false
		}
		c = byte(hi<<4 | lo)
		if strings.IndexByte(literalLabel, c) >= 0 {
			return "", false
		}
		b.WriteByte(c)
		i += 2
	}
	return b.String(), b.Len() > 0
}

// Sort by components (NUL sorts before all key bytes). All descendants of
// a key then immediately follow it. A binary search can test a choice key
// against the shared block without re-reading that block for each choice.
type recordBlock struct {
	text  strings.Builder
	keys  []string
	last  string
	types int
}

func (b *recordBlock) add(line string) error {
	k, v, sp := strings.Cut(line, " ")
	if len(line) > MaxLineSize || !keyPattern.MatchString(k) || sp && v == "" || k < b.last || len(line)+1 > MaxSourceSize-b.text.Len() {
		return ErrProtocol
	}
	if b.last != k {
		b.keys = append(b.keys, strings.ReplaceAll(k, ".", "\x00"))
		b.last = k
	}
	if k == "type" {
		if !typePattern.MatchString(v) {
			return ErrProtocol
		}
		b.types++
	}
	b.text.WriteString(line)
	b.text.WriteByte('\n')
	return nil
}

func (b *recordBlock) finish() bool {
	slices.Sort(b.keys)
	for i := 1; i < len(b.keys); i++ {
		if descendant(b.keys[i], b.keys[i-1]) {
			return false
		}
	}
	return true
}

func descendant(key, parent string) bool {
	return len(key) > len(parent) && key[len(parent)] == 0 && strings.HasPrefix(key, parent)
}

func (b *recordBlock) compatible(other *recordBlock) bool {
	if b.types+other.types != 1 || b.text.Len()+other.text.Len() > MaxSourceSize {
		return false
	}
	for _, k := range other.keys {
		i, found := slices.BinarySearch(b.keys, k)
		if found || i > 0 && descendant(k, b.keys[i-1]) || i < len(b.keys) && descendant(b.keys[i], k) {
			return false
		}
	}
	return true
}

type frameReader struct {
	result         Response
	lines          int
	shared, choice recordBlock
	seen           map[[2]string]bool
	ended          bool
}

func (p *frameReader) line(line string) error {
	p.lines++
	r := &p.result
	if p.lines == 1 {
		switch line {
		case "eris-fetch/1 ok", "eris-fetch/1 fail", "eris-fetch/1 refine":
			r.Status = strings.TrimPrefix(line, "eris-fetch/1 ")
			return nil
		}
		return ErrProtocol
	}
	k, v, _ := strings.Cut(line, " ")
	switch r.Status {
	case "ok":
		if p.lines == 2 && k == "kind" && (v == "blob" || v == "erisfs") {
			r.Kind = v
			return nil
		}
		if p.lines == 3 && k == "cap" {
			cap, err := eris.ParseURN(v)
			if err == nil {
				r.Cap = cap
				return nil
			}
		}
	case "fail":
		if p.lines == 2 && k == "code" && tokenPattern.MatchString(v) {
			r.Failure.Code = v
			return nil
		}
		if p.lines == 3 && k == "key" && keyPattern.MatchString(v) {
			r.Failure.Key = v
			return nil
		}
	case "refine":
		return p.refine(line)
	}
	return ErrProtocol
}

func (p *frameReader) refine(line string) error {
	if p.ended {
		return ErrProtocol
	}
	r := &p.result
	first := len(r.Choices) == 0
	if !strings.HasPrefix(line, "@") {
		if first {
			return p.shared.add(line)
		}
		return p.choice.add(line)
	}
	if first {
		if !strings.HasPrefix(line, "@default ") || !p.shared.finish() {
			return ErrProtocol
		}
		r.Shared = p.shared.text.String()
		p.seen = make(map[[2]string]bool)
	} else {
		if !p.choice.finish() || !p.shared.compatible(&p.choice) {
			return ErrProtocol
		}
		r.Choices[len(r.Choices)-1].Records = p.choice.text.String()
		p.choice = recordBlock{}
		if line == "@end" {
			p.ended = true
			return nil
		}
		if !strings.HasPrefix(line, "@alt ") {
			return ErrProtocol
		}
	}
	_, rest, _ := strings.Cut(line, " ")
	kind, encoded, _ := strings.Cut(rest, " ")
	label, ok := decodeLabel(encoded)
	pair := [2]string{kind, label}
	if !ok || !tokenPattern.MatchString(kind) || p.seen[pair] {
		return ErrProtocol
	}
	p.seen[pair] = true
	r.Choices = append(r.Choices, Choice{Kind: kind, Label: label})
	return nil
}

// ParseResponse checks framing, canonical records, complete input, and the
// size and key grammar of every assembled source. Use ValidateFor for the
// type-specific checks. Transport completion is the caller's responsibility.
func ParseResponse(r io.Reader) (Response, error) {
	var p frameReader
	err := readLines(r, responseFrame, p.line)
	if err != nil {
		if err == ErrProtocol {
			return Response{}, err
		}
		return Response{}, errors.Join(ErrProtocol, err)
	}
	switch p.result.Status {
	case "ok":
		if p.lines == 3 {
			return p.result, nil
		}
	case "fail":
		if p.lines >= 2 {
			return p.result, nil
		}
	case "refine":
		if p.ended {
			return p.result, nil
		}
	}
	return Response{}, ErrProtocol
}

// ValidateFor checks a parsed frame against a standard request. It checks the
// kind, permitted Git edits, object format, labels, and branch tip position.
// A valid fail frame also returns nil. Inspect Status to determine the fetch
// outcome. Checking reachability and tag ordering requires repository data.
func (r Response) ValidateFor(s Source, autoCompression bool) error {
	if r.Status == "fail" {
		return nil
	}
	opt, err := s.validate(autoCompression, false)
	if err != nil {
		return err
	}
	if r.Status == "ok" && r.Kind == opt.Kind && opt.Direct {
		return nil
	}
	if r.Status != "refine" || opt.Direct || len(r.Choices) == 0 {
		return ErrProtocol
	}
	shared := s.Canonical()
	if branch := s.value("branch"); branch != "" {
		// Match the whole record even when a value contains the text "branch ".
		shared = strings.TrimPrefix(strings.Replace("\n"+shared, "\nbranch "+branch+"\n", "\n", 1), "\n")
	}
	if shared != r.Shared {
		return ErrProtocol
	}
	format := ""
	for i, c := range r.Choices {
		commit := strings.TrimSuffix(strings.TrimPrefix(c.Records, "commit "), "\n")
		if c.Records != "commit "+commit+"\n" || !validCommit(commit) || !validRef(c.Label) {
			return ErrProtocol
		}
		alg, _, _ := strings.Cut(commit, ":")
		if format != "" && format != alg {
			return ErrProtocol
		}
		format = alg
		if i == len(r.Choices)-1 {
			if c.Kind != "tip" || s.fields["branch"] != nil && c.Label != s.value("branch") {
				return ErrProtocol
			}
		} else if c.Kind != "tag" {
			return ErrProtocol
		}
	}
	return nil
}

// MarshalText serializes a frame, checking the same grammar and limits as the
// receiver. Shared and per-choice blocks must already be canonical text.
func (r Response) MarshalText() ([]byte, error) {
	var b strings.Builder
	b.WriteString("eris-fetch/1 ")
	switch r.Status {
	case "ok":
		if r.Kind != "blob" && r.Kind != "erisfs" || r.Cap.Validate() != nil {
			return nil, ErrProtocol
		}
		b.WriteString("ok\nkind " + r.Kind + "\ncap " + r.Cap.String() + "\n")
	case "fail":
		if !tokenPattern.MatchString(r.Failure.Code) {
			return nil, ErrProtocol
		}
		b.WriteString("fail\ncode " + r.Failure.Code + "\n")
		if key := r.Failure.Key; key != "" {
			if len(key) > MaxLineSize || !keyPattern.MatchString(key) {
				return nil, ErrProtocol
			}
			b.WriteString("key " + key + "\n")
		}
	case "refine":
		if len(r.Shared) > MaxSourceSize {
			return nil, ErrProtocol
		}
		b.WriteString("refine\n")
		b.WriteString(r.Shared)
		for i, c := range r.Choices {
			marker := "@alt "
			if i == 0 {
				marker = "@default "
			}
			if !tokenPattern.MatchString(c.Kind) || len(c.Label) > MaxMarkerSize || len(c.Records) > MaxSourceSize {
				return nil, ErrProtocol
			}
			start := b.Len()
			b.WriteString(marker + c.Kind + " ")
			for j := range len(c.Label) {
				v := c.Label[j]
				if strings.IndexByte(literalLabel, v) >= 0 {
					b.WriteByte(v)
				} else {
					b.WriteByte('%')
					b.WriteByte(upperDigits[v>>4])
					b.WriteByte(upperDigits[v&15])
				}
			}
			if b.Len()-start > MaxMarkerSize || len(c.Records)+6 > MaxResponseSize-b.Len() {
				return nil, ErrProtocol
			}
			b.WriteByte('\n')
			b.WriteString(c.Records)
		}
		b.WriteString("@end\n")
		parsed, err := ParseResponse(strings.NewReader(b.String()))
		if err != nil {
			return nil, err
		}
		if parsed.Shared != r.Shared || !slices.Equal(parsed.Choices, r.Choices) {
			return nil, ErrProtocol
		}
	default:
		return nil, ErrProtocol
	}
	return []byte(b.String()), nil
}
