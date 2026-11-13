package httpttl

import (
	"net/http"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestParse(t *testing.T) {
	for _, test := range []struct {
		values []string
		want   eris.TTL
		bad    bool
	}{
		{nil, 1, false}, {[]string{"1"}, 1, false}, {[]string{"255"}, 255, false},
		{[]string{"0"}, 0, true}, {[]string{"256"}, 0, true}, {[]string{"-1"}, 0, true},
		{[]string{"+2"}, 0, true}, {[]string{""}, 0, true}, {[]string{"2, 3"}, 0, true},
		{[]string{"2", "3"}, 0, true}, {[]string{" 2"}, 0, true}, {[]string{"99999999999999999999"}, 0, true},
	} {
		got, err := Parse(http.Header{Header: test.values})
		if (err != nil) != test.bad || got != test.want {
			t.Errorf("%v: %d, %v", test.values, got, err)
		}
	}
	if _, err := Parse(http.Header{"Eris-Ttl": {"2"}, "eris-ttl": {"3"}}); err == nil {
		t.Fatal("duplicate case variants accepted")
	}
}
