package httppriority

import (
	"net/http"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestPriorityHeaders(t *testing.T) {
	for _, name := range []string{Header, S3Header} {
		for _, test := range []struct {
			values []string
			want   bool
		}{
			{nil, false}, {[]string{"true"}, true}, {[]string{" true\t"}, true},
			{[]string{"false"}, false}, {[]string{"TRUE"}, false}, {[]string{"1"}, false},
			{[]string{"true, true"}, false}, {[]string{"true", "true"}, false},
		} {
			headers := http.Header{name: test.values, "Unrelated": {"true"}}
			if got := Parse(headers, name).HighPriority; got != test.want {
				t.Errorf("%s %v: %v", name, test.values, got)
			}
		}
		ambiguous := http.Header{name: {"true"}, strings.ToLower(name): {"true"}}
		if Parse(ambiguous, name).HighPriority {
			t.Fatal("duplicate field names accepted")
		}
		lower := http.Header{strings.ToLower(name): {"true"}}
		if !Parse(lower, name).HighPriority {
			t.Fatal("lowercase field name ignored")
		}
		headers := make(http.Header)
		Set(headers, name, eris.BlockOptions{HighPriority: true})
		if headers.Get(name) != "true" {
			t.Fatal(headers)
		}
		Set(headers, name, eris.BlockOptions{})
		if _, present := headers[name]; present {
			t.Fatal("false field was not omitted")
		}
	}
}

func TestSetReplacesAllFieldNames(t *testing.T) {
	for _, name := range []string{Header, S3Header} {
		for _, high := range []bool{false, true} {
			headers := http.Header{
				name: {"false"}, strings.ToLower(name): {"true"}, strings.ToUpper(name): {"true"},
				"Unrelated": {"keep"},
			}
			Set(headers, name, eris.BlockOptions{HighPriority: high})
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
			if fields != wantFields || Parse(headers, name).HighPriority != high || headers.Get("Unrelated") != "keep" {
				t.Errorf("%s, high=%v: %v", name, high, headers)
			}
		}
	}
}
