package fetch_test

import (
	"fmt"
	"strings"

	"github.com/asciimoth/goeris/fetch"
)

func ExampleParseSource() {
	s, err := fetch.ParseSource(strings.NewReader("url https://example.org/hello\r\ntype url"))
	if err != nil {
		panic(err)
	}
	options, err := s.Validate(false)
	if err != nil {
		panic(err)
	}
	fmt.Println(options.Kind, options.Direct)
	fmt.Print(s.Canonical())
	fmt.Println(s.ID())
	// Output:
	// blob true
	// type url
	// url https://example.org/hello
	// 7BSALWD5ZKAQCPOZAD3UJZQ2W3G2ITP3BLBIV57CJXBHXXZYGHPQ:blake2
}
