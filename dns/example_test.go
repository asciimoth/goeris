package dns_test

import (
	"context"
	"fmt"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/dns"
)

type textResolver map[string][]string

func (resolver textResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return resolver[name], nil
}

func ExampleParse() {
	const text = "dnslink=/eris/B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I"
	link, err := dns.Parse(text)
	if err != nil {
		panic(err)
	}
	fmt.Println(link.Namespace)
	fmt.Println(link.Capability.String())
	// Output:
	// eris
	// urn:eris:B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I
}

func ExampleUpdate() {
	capability, err := eris.ParseURN("urn:eris:B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I")
	if err != nil {
		panic(err)
	}
	link, err := dns.New(dns.NamespaceERIS, capability)
	if err != nil {
		panic(err)
	}
	text, err := dns.Update("v=spf1", link)
	if err != nil {
		panic(err)
	}
	fmt.Println(text)
	// Output:
	// v=spf1 dnslink=/eris/B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I
}

func ExampleResolve() {
	const record = "dnslink=/eris/B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I"
	resolver := textResolver{"_dnslink.example.org": {"v=spf1", record}}
	links, err := dns.Resolve(context.Background(), resolver, "example.org")
	if err != nil {
		panic(err)
	}
	fmt.Println(links[0])
	// Output:
	// dnslink=/eris/B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I
}
