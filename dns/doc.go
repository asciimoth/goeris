// Package dns encodes and resolves ERIS DNSLink records.
//
// It implements the eris and erisfs namespaces from the [ERIS DNSLink
// recommendation].
//
// The package does not create or configure a network client. Resolve delegates
// lookup to a caller-supplied TXTResolver. A *net.Resolver satisfies this
// interface, as can an in-memory resolver or a different DNS transport.
//
// [ERIS DNSLink recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/dnslink/index.md
package dns
