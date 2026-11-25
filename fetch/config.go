package fetch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// ErrConfig indicates invalid local routing policy.
var ErrConfig = errors.New("invalid fetch configuration")

// Route selects either a direct executable argument vector or an HTTP endpoint.
type Route struct {
	Exec []string
	HTTP string
}

// ParseConfig validates a whole routing file before returning any routes.
func ParseConfig(r io.Reader) (map[string]Route, error) {
	fields, err := readRecords(r, configDocument)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	routes := map[string]Route{}
	for k, v := range fields {
		typ, transport, _ := strings.Cut(k, ".")
		if !typePattern.MatchString(typ) {
			return nil, ErrConfig
		}
		if _, exists := routes[typ]; exists {
			return nil, ErrConfig
		}
		switch transport {
		case "exec":
			if !validCommand(v[0]) {
				return nil, ErrConfig
			}
			routes[typ] = Route{Exec: v}
		case "http":
			if len(v) != 1 || !ValidHTTPURI(v[0]) {
				return nil, ErrConfig
			}
			routes[typ] = Route{HTTP: v[0]}
		default:
			return nil, ErrConfig
		}
	}
	return routes, nil
}

func validCommand(s string) bool {
	if absolute(s) {
		return true
	}
	return s != "" && s != "." && s != ".." && !strings.Contains(s, "/") && (runtime.GOOS != "windows" || !strings.ContainsAny(s, `\:`))
}

func absolute(s string) bool {
	if strings.IndexByte(s, 0) >= 0 || !filepath.IsAbs(s) {
		return false
	}
	if runtime.GOOS != "windows" {
		return true
	}
	return windowsAbsolute(s)
}

func windowsAbsolute(s string) bool {
	p := strings.ReplaceAll(s, "/", `\`)
	if len(p) >= 3 && asciiLetter(p[0]) && p[1:3] == `:\` {
		return true
	}
	if !strings.HasPrefix(p, `\\`) {
		return false
	}
	server, rest, _ := strings.Cut(p[2:], `\`)
	share, _, _ := strings.Cut(rest, `\`)
	return server != "" && server != "." && server != "?" && share != ""
}

// ParseFileList decodes ERIS_FETCH with native absolute path rules. A quoted
// element can contain semicolons and doubled quotes. Empty gaps are skipped.
func ParseFileList(s string) ([]string, error) {
	var paths []string
	for s != "" {
		var path string
		if s[0] == '"' {
			s = s[1:]
			var b strings.Builder
			for {
				part, rest, ok := strings.Cut(s, "\"")
				if !ok {
					return nil, ErrConfig
				}
				b.WriteString(part)
				s = rest
				if !strings.HasPrefix(s, "\"") {
					break
				}
				b.WriteByte('"')
				s = s[1:]
			}
			if s != "" && s[0] != ';' {
				return nil, ErrConfig
			}
			s = strings.TrimPrefix(s, ";")
			path = b.String()
		} else {
			path, s, _ = strings.Cut(s, ";")
			if path == "" {
				continue
			}
			if strings.Contains(path, "\"") {
				return nil, ErrConfig
			}
		}
		if !absolute(path) {
			return nil, ErrConfig
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// ConfigFiles resolves the ordered configuration paths. A non-nil explicit
// list (including an empty list) overrides ERIS_FETCH and platform defaults.
func ConfigFiles(explicit []string) ([]string, error) {
	if explicit != nil {
		for _, p := range explicit {
			if !absolute(p) {
				return nil, ErrConfig
			}
		}
		return slices.Clone(explicit), nil
	}
	if s, ok := os.LookupEnv("ERIS_FETCH"); ok {
		return ParseFileList(s)
	}
	return defaultFiles(), nil
}

// ErrNoFetcher means none of the selected files supplied the requested type.
var ErrNoFetcher = errors.New("no-fetcher")

// LookupRoute validates the file list, then reads whole files until one
// supplies typ. It skips missing files. It returns other read and syntax
// errors as diagnostics, even if a later file supplies a usable route.
// The final error reports invalid configuration or ErrNoFetcher.
func LookupRoute(typ string, explicit []string) (Route, []error, error) {
	paths, err := ConfigFiles(explicit)
	if err != nil {
		return Route{}, nil, err
	}
	var diagnostics []error
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				diagnostics = append(diagnostics, err)
			}
			continue
		}
		routes, err := ParseConfig(f)
		_ = f.Close()
		if err != nil {
			diagnostics = append(diagnostics, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if route, ok := routes[typ]; ok {
			return route, diagnostics, nil
		}
	}
	return Route{}, diagnostics, ErrNoFetcher
}
