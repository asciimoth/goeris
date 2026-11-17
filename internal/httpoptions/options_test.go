package httpoptions

import (
	"net/http"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestFlagHeaders(t *testing.T) {
	for _, name := range []string{HighPriorityHeader, S3HighPriorityHeader, RootHeader, S3RootHeader} {
		for _, test := range []struct {
			values []string
			want   bool
		}{
			{nil, false}, {[]string{"true"}, true}, {[]string{" true\t"}, true},
			{[]string{"false"}, false}, {[]string{"TRUE"}, false}, {[]string{"1"}, false},
			{[]string{"true, true"}, false}, {[]string{"true", "true"}, false},
		} {
			headers := http.Header{name: test.values, "Unrelated": {"true"}}
			if got := parse(headers, name); got != test.want {
				t.Errorf("%s %v: %v", name, test.values, got)
			}
		}
		ambiguous := http.Header{name: {"true"}, strings.ToLower(name): {"true"}}
		if parse(ambiguous, name) {
			t.Fatal("duplicate field names accepted")
		}
		lower := http.Header{strings.ToLower(name): {"true"}}
		if !parse(lower, name) {
			t.Fatal("lowercase field name ignored")
		}
		headers := make(http.Header)
		set(headers, name, true)
		if headers.Get(name) != "true" {
			t.Fatal(headers)
		}
		set(headers, name, false)
		if _, present := headers[name]; present {
			t.Fatal("false field was not omitted")
		}
	}
}

func TestSetReplacesAllFieldNames(t *testing.T) {
	for _, name := range []string{HighPriorityHeader, S3HighPriorityHeader, RootHeader, S3RootHeader} {
		for _, high := range []bool{false, true} {
			headers := http.Header{
				name: {"false"}, strings.ToLower(name): {"true"}, strings.ToUpper(name): {"true"},
				"Unrelated": {"keep"},
			}
			set(headers, name, high)
			fields := 0
			for key := range headers {
				if strings.EqualFold(key, name) {
					fields++
				}
			}
			wantFields := 0
			if high {
				wantFields = 1
			}
			if fields != wantFields || parse(headers, name) != high || headers.Get("Unrelated") != "keep" {
				t.Errorf("%s, high=%v: %v", name, high, headers)
			}
		}
	}
}

func TestIndependentFlags(t *testing.T) {
	for _, names := range [][2]string{{HighPriorityHeader, RootHeader}, {S3HighPriorityHeader, S3RootHeader}} {
		for _, options := range []eris.BlockOptions{{}, {HighPriority: true}, {Root: true}, {HighPriority: true, Root: true}} {
			headers := make(http.Header)
			Set(headers, names[0], names[1], options)
			if got := Parse(headers, names[0], names[1]); got != options {
				t.Fatalf("got %v, want %v", got, options)
			}
			headers[names[1]] = []string{"true", "true"}
			options.Root = false
			if got := Parse(headers, names[0], names[1]); got != options {
				t.Fatalf("invalid root affected priority: %v", got)
			}
		}
	}
}
