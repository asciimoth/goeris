// Package dns encodes and resolves ERIS DNSLink records.
//
// It implements the eris and erisfs namespaces from the ERIS DNSLink
// recommendation: https://eris.codeberg.page/eer/dnslink.xml.
//
// The package does not create or configure a network client. Resolve delegates
// lookup to a caller-supplied TXTResolver. A *net.Resolver satisfies this
// interface, as can an in-memory resolver or a different DNS transport.
package dns
