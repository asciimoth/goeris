package fetch

import (
	"encoding/hex"
	"net/netip"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/goeris"
)

var (
	schemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.\-]*$`)
	ipFuture      = regexp.MustCompile(`^[vV][0-9A-Fa-f]+\.[A-Za-z0-9._~!$&'()*+,;=:\-]+$`)
	lowerHex      = regexp.MustCompile(`^[a-f0-9]+$`)
)

const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
const subDelims = "!$&'()*+,;="

func uriChars(s, extra string) bool {
	allowed := unreserved + subDelims + extra
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(allowed, s[i]) >= 0 {
			continue
		}
		if s[i] != '%' || i+2 >= len(s) {
			return false
		}
		if _, err := hex.DecodeString(s[i+1 : i+3]); err != nil {
			return false
		}
		i += 2
	}
	return true
}

// validAuthority implements RFC 3986 host and port without URL decoding or
// normalizing the request identity. User information is outside this profile.
func validAuthority(a string) bool {
	host, port, hasPort := strings.Cut(a, ":")
	if strings.HasPrefix(a, "[") {
		ip, rest, ok := strings.Cut(a[1:], "]")
		if !ok {
			return false
		}
		addr, err := netip.ParseAddr(ip)
		if (err != nil || !addr.Is6() || addr.Zone() != "") && !ipFuture.MatchString(ip) {
			return false
		}
		hasPort = rest != ""
		if hasPort && rest[0] != ':' {
			return false
		}
		port = strings.TrimPrefix(rest, ":")
	} else if !uriChars(host, "") {
		return false
	}
	if hasPort {
		n := 0
		for _, b := range []byte(port) {
			if b < '0' || b > '9' {
				return false
			}
			n = n*10 + int(b-'0')
			if n > 65535 {
				return false
			}
		}
		return n > 0
	}
	return true
}

// validURI checks the common URI grammar and the standard scheme profiles.
// An unknown scheme can be skipped later by a handler trying its mirrors.
func validURI(s string, windowsFiles bool) bool {
	scheme, rest, ok := strings.Cut(s, ":")
	if !ok || !schemePattern.MatchString(scheme) {
		return false
	}
	path, query, hasQuery := strings.Cut(rest, "?")
	authority := ""
	if strings.HasPrefix(path, "//") {
		var tail string
		var slash bool
		authority, tail, slash = strings.Cut(path[2:], "/")
		path = ""
		if slash {
			path = "/" + tail
		}
		if !validAuthority(authority) {
			return false
		}
	}
	if !uriChars(path, "/:@") || !uriChars(query, "/:@?") {
		return false
	}
	hasHost := authority != "" && authority[0] != ':'
	switch scheme {
	case "http", "https":
		return hasHost
	case "ftp", "ftps":
		// A semicolon in a file or directory name must be percent-encoded.
		_, mode, typed := strings.Cut(path, ";")
		return hasHost && !hasQuery && (!typed || strings.EqualFold(mode, "type=i"))
	case "sftp", "file":
		if !strings.HasPrefix(path, "/") || hasQuery {
			return false
		}
		if scheme == "sftp" && !hasHost || scheme == "file" && authority != "" && !strings.EqualFold(authority, "localhost") {
			return false
		}
		for c := range strings.SplitSeq(path, "/") {
			d, _ := url.PathUnescape(c)
			if strings.ContainsAny(d, "\x00/") {
				return false
			}
		}
		if scheme == "file" && windowsFiles {
			d, _ := url.PathUnescape(path)
			return len(d) >= 4 && asciiLetter(d[1]) && d[2:4] == ":/" &&
				utf8.ValidString(d) && !strings.Contains(d, `\`)
		}
	}
	return true
}

func httpURI(s string) bool {
	return (strings.HasPrefix(s, "http:") || strings.HasPrefix(s, "https:")) && validURI(s, false)
}

func asciiLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func component(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\x00")
}

func validRef(s string) bool {
	if strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.ContainsAny(s, " ~^:?*[\\") {
		return false
	}
	for _, c := range []byte(s) {
		if c < 32 || c == 127 {
			return false
		}
	}
	for part := range strings.SplitSeq(s, "/") {
		if part == "" || part[0] == '.' || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func validCommit(s string) bool {
	alg, h, _ := strings.Cut(s, ":")
	lengthOK := alg == "sha1" && len(h) == 40 || alg == "sha256" && len(h) == 64
	return lengthOK && lowerHex.MatchString(h)
}

// Options contains effective output settings. The original Source retains only
// explicit records. Direct is false when Git needs a revision proposal.
type Options struct {
	Kind        string // "blob" or "erisfs"
	Secret      eris.Secret
	Compression string // "none" or "auto" for a tree, empty for a blob
	Direct      bool
}

var commonKeys = []string{"type", "url", "eris.secret", "erisfs.use", "erisfs.version", "erisfs.layout", "erisfs.compression"}
var typeKeys = map[string][]string{
	"url":     {"alt", "checksum", "name", "executable"},
	"archive": {"alt", "checksum", "format", "strip-root"},
	"git":     {"commit", "tag", "branch", "subdir"},
}

func allowedKey(keys []string, key string) bool {
	return slices.Contains(commonKeys, key) || slices.Contains(keys, key)
}

// Validate checks the standard schema for a handler on this machine.
// Set autoCompression to true if the handler supports the optional auto profile.
func (s Source) Validate(autoCompression bool) (Options, error) {
	return s.validate(autoCompression, runtime.GOOS == "windows")
}

func (s Source) validate(autoCompression, windowsFiles bool) (Options, error) {
	if err := s.checkFields(windowsFiles); err != nil {
		return Options{}, err
	}
	opt, err := s.outputOptions(autoCompression)
	if err != nil {
		return Options{}, err
	}
	if err = s.checkSelection(opt.Kind == "erisfs"); err != nil {
		return Options{}, err
	}
	opt.Direct = s.Type() != "git" || s.fields["commit"] != nil || s.fields["tag"] != nil
	return opt, nil
}

func (s Source) checkFields(windowsFiles bool) error {
	keys, known := typeKeys[s.Type()]
	if !known {
		return Failure{"unsupported-type", "type"}
	}
	for key, values := range s.fields {
		if !allowedKey(keys, key) {
			return Failure{"unknown-key", key}
		}
		if key != "alt" && len(values) != 1 {
			return Failure{"invalid-source", key}
		}
	}
	for _, key := range []string{"erisfs.use", "executable", "strip-root"} {
		if s.fields[key] != nil && s.value(key) != "true" && s.value(key) != "false" {
			return Failure{"invalid-source", key}
		}
	}
	if s.fields["url"] == nil {
		return Failure{"invalid-source", "url"}
	}
	for _, key := range []string{"url", "alt"} {
		for _, uri := range s.fields[key] {
			if !validURI(uri, windowsFiles) {
				return Failure{"invalid-source", key}
			}
		}
	}
	if s.Type() == "git" && !httpURI(s.value("url")) {
		return Failure{"unsupported-scheme", "url"}
	}
	if s.fields["checksum"] != nil {
		_, err := ParseChecksum(s.value("checksum"))
		return err
	}
	return nil
}

func (s Source) outputOptions(autoCompression bool) (Options, error) {
	opt := Options{Kind: "blob"}
	if secret := s.value("eris.secret"); secret != "" {
		if len(secret) != 64 || !lowerHex.MatchString(secret) {
			return Options{}, Failure{"invalid-source", "eris.secret"}
		}
		_, _ = hex.Decode(opt.Secret[:], []byte(secret))
	}
	tree := s.Type() != "url" || s.fields["name"] != nil
	if s.fields["erisfs.use"] != nil {
		tree = s.value("erisfs.use") == "true"
	}
	if tree {
		opt.Kind = "erisfs"
		opt.Compression = "none"
	}
	for _, field := range []struct{ key, value string }{{"erisfs.version", "2"}, {"erisfs.layout", "flat"}, {"erisfs.compression", "none"}} {
		k := field.key
		if s.fields[k] == nil {
			continue
		}
		if tree && autoCompression && k == "erisfs.compression" && s.value(k) == "auto" {
			opt.Compression = "auto"
			continue
		}
		if !tree || s.value(k) != field.value {
			return Options{}, Failure{"unsupported-option", k}
		}
	}
	return opt, nil
}

func (s Source) checkSelection(tree bool) error {
	switch s.Type() {
	case "url":
		if tree && !component(s.value("name")) || !tree && s.fields["name"] != nil {
			return Failure{"invalid-source", "name"}
		}
		if !tree && s.fields["executable"] != nil {
			return Failure{"invalid-source", "executable"}
		}
	case "archive":
		if !tree && s.fields["strip-root"] != nil {
			return Failure{"unsupported-option", "strip-root"}
		}
		if s.fields["format"] != nil {
			switch s.value("format") {
			case "auto", "tar", "tar.gz", "zip":
			case "gzip":
				if tree {
					return Failure{"unsupported-option", "format"}
				}
			default:
				return Failure{"unsupported-option", "format"}
			}
		}
	case "git":
		if !tree {
			return Failure{"unsupported-option", "erisfs.use"}
		}
		n := 0
		for _, k := range []string{"branch", "tag", "commit"} {
			if s.fields[k] != nil {
				n++
				v := s.value(k)
				if n > 1 || k == "commit" && !validCommit(v) || k != "commit" && !validRef(v) {
					return Failure{"invalid-source", k}
				}
			}
		}
		if s.fields["subdir"] != nil {
			for p := range strings.SplitSeq(s.value("subdir"), "/") {
				if !component(p) {
					return Failure{"invalid-source", "subdir"}
				}
			}
		}
	}
	return nil
}
