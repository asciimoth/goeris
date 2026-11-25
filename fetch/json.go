package fetch

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParseJSON reads the structured front end for the three standard types. It
// accepts the optional auto compression profile. A handler can call Validate
// to check native file paths and compression support. Wire limits apply to
// the emitted records. The caller controls the size of the JSON input buffer.
func ParseJSON(data []byte) (Source, error) {
	if !utf8.Valid(data) || !json.Valid(data) || !pairedSurrogates(data) {
		return Source{}, errInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	first, _ := d.Token()
	if first != json.Delim('{') {
		return Source{}, errInvalid
	}
	p := jsonSource{decoder: d, fields: make(map[string][]string)}
	if err := p.object(); err != nil {
		return Source{}, err
	}
	s, err := makeSource(p.fields)
	if err != nil {
		return Source{}, err
	}
	if keys, known := typeKeys[s.Type()]; known && p.wrongShape != "" {
		if !allowedKey(keys, p.wrongShape) {
			return Source{}, Failure{"unknown-key", p.wrongShape}
		}
		return Source{}, Failure{"invalid-source", p.wrongShape}
	}
	if _, err = s.validate(true, false); err != nil {
		return Source{}, err
	}
	if len(s.fields["alt"]) == 0 {
		delete(s.fields, "alt")
	}
	return s, nil
}

// UnmarshalJSON replaces s with a validated standard source. On failure it
// leaves the previous source intact.
func (s *Source) UnmarshalJSON(data []byte) error {
	parsed, err := ParseJSON(data)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

// encoding/json replaces isolated UTF-16 surrogates with U+FFFD. Reject
// these escapes before decoding so distinct inputs cannot silently coalesce.
// The input has already passed json.Valid, including escape-length checks.
func pairedSurrogates(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		n, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if n < 0xd800 || n > 0xdfff {
			continue
		}
		if n > 0xdbff || len(data)-i < 7 || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		next, _ := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if next < 0xdc00 || next > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

type jsonSource struct {
	decoder    *json.Decoder
	fields     map[string][]string
	path       []byte
	size       int
	wrongShape string // first scalar/list mismatch, checked once type is known
}

// Each object reuses the path buffer for its children. Keep only the keys
// needed for emitted records and diagnostics.
func (p *jsonSource) object() error {
	prefixLen := len(p.path)
	seen := make(map[string]bool)
	for p.decoder.More() {
		member, _ := p.decoder.Token()
		name := member.(string)
		if strings.Contains(name, ".") || !keyPattern.MatchString(name) || seen[name] {
			return errInvalid
		}
		seen[name] = true
		if len(name) > MaxLineSize-prefixLen {
			return errLimit
		}
		p.path = append(p.path[:prefixLen], name...)
		token, _ := p.decoder.Token()
		switch v := token.(type) {
		case string:
			if string(p.path) == "alt" && p.wrongShape == "" {
				p.wrongShape = "alt"
			}
			if err := p.record(v); err != nil {
				return err
			}
		case json.Delim:
			if v == '{' {
				p.path = append(p.path, '.')
				if err := p.object(); err != nil {
					return err
				}
			} else if err := p.array(); err != nil {
				return err
			}
		default:
			return errInvalid
		}
	}
	_, _ = p.decoder.Token()
	if len(seen) == 0 {
		return errInvalid
	}
	return nil
}

func (p *jsonSource) array() error {
	if string(p.path) == "alt" {
		// Keep the key until schema validation, even when the list is empty.
		p.fields["alt"] = nil
	} else if p.wrongShape == "" {
		p.wrongShape = string(p.path)
	}
	for p.decoder.More() {
		item, _ := p.decoder.Token()
		value, ok := item.(string)
		if !ok {
			return errInvalid
		}
		if err := p.record(value); err != nil {
			return err
		}
	}
	_, _ = p.decoder.Token()
	return nil
}

func (p *jsonSource) record(value string) error {
	if !validText(value) {
		return errInvalid
	}
	n := len(p.path)
	if value != "" {
		n += 1 + len(value)
	}
	if n > MaxLineSize || n+1 > MaxSourceSize-p.size {
		return errLimit
	}
	p.size += n + 1
	key := string(p.path)
	p.fields[key] = append(p.fields[key], value)
	return nil
}

// MarshalJSON writes the standard structured representation. It preserves
// source identity. An absent list is represented by an empty JSON array.
func (s Source) MarshalJSON() ([]byte, error) {
	if _, err := s.validate(true, false); err != nil {
		return nil, err
	}
	root := make(map[string]any)
	if s.Type() != "git" {
		root["alt"] = []string{}
	}
	for k, values := range s.fields {
		if k == "alt" {
			root[k] = values
			continue
		}
		object := root
		if group, leaf, nested := strings.Cut(k, "."); nested {
			child, _ := root[group].(map[string]any)
			if child == nil {
				child = make(map[string]any)
				root[group] = child
			}
			object = child
			k = leaf
		}
		object[k] = values[0]
	}
	return json.Marshal(root)
}
