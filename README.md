# ERIS tools

This repository provides pure Go tools for
[ERIS 1.0.0](https://eris.codeberg.page/).

The `eris` package implements:

- ERIS padding and recommended block-size selection;
- BLAKE2b-256 references and ChaCha20 node encryption;
- read capability binary, URN, and CBOR encodings;
- deterministic encoding to an in-memory block set; and
- verified decoding from an in-memory block set.

The `eris/cbor` package implements the ERIS CBOR transport format:

- deterministic tagged, untagged, and self-described arrays;
- tagged and untagged CBOR sequences;
- definite-length and indefinite-length decoding; and
- typed blocks, references, verified block maps, and read capabilities.

The `eris/fs` package implements ERIS-FS version 1:

- canonical deterministic CBOR index encoding and strict decoding;
- low-level index, ERIS reader, and abstract block storage APIs;
- regular, executable, symbolic-link, and nested-image entries;
- host-independent builders that accept `io.Reader` values and abstract block
  stores;
- a high-level read-only `FileSystem` interface based on `io/fs`; and
- verified random-access file reads through `io.ReaderAt` and `io.Seeker`.

The `eris/dns` package implements ERIS DNSLink records:

- canonical `eris` and `erisfs` namespace encoding and decoding;
- extraction from multiple or whitespace-concatenated TXT values;
- deterministic updates of existing TXT values and record sets; and
- DNSLink lookup through an abstract resolver implemented by `*net.Resolver`.

The DNS package does not create or configure a DNS client. Its resolver
interface lets applications supply standard DNS, DNS over HTTPS, or an in-memory
implementation. Resolver implementations must follow CNAME chains, as
`*net.Resolver` does for TXT lookups.

The `eris/http` package implements ERIS over HTTP:

- low-level RFC 2169 N2R URL and request construction;
- a chainable HTTP handler for encrypted blocks and decoded content;
- abstract block retrieval with no storage implementation; and
- a client that uses a caller-supplied HTTP request executor.

The HTTP package does not create or configure a network client or server.
Applications supply the HTTP request executor, block source, handler chain, and
server registration.

The `eris/s3` package implements the ERIS object-storage recommendation:

- canonical `s3://ENDPOINT/BUCKET/` store URLs and path-style object URLs;
- public block reads and authenticated single-request block writes;
- deterministic AWS Signature Version 4 signing and verification;
- caller-supplied HTTP clients, credentials, authorization, and clocks; and
- a fixed-bucket HTTP handler with an optional read-only mode.

The S3 package intentionally omits credential discovery, IAM, bucket
administration, object listing, and multipart uploads. It uses only the Go
standard library and the existing ERIS block APIs.

The `eris/coap` package implements ERIS over CoAP:

- canonical `coap` and `coap+tcp` store URLs;
- raw and Base32 block-reference queries;
- verified GET and PUT clients with single-message and block-wise transfers;
- storage-backed, context-aware handlers with opt-in PUT support;
- deterministic CoRE Link Format discovery documents;
- cross-network proxy advertisements and Hop-Limit forwarding helpers; and
- caller-supplied CoAP requesters and remote addresses.

The CoAP package performs no name resolution, dialing, listening, socket
creation, or file-system access. Applications supply `coaperation` requesters
over their chosen in-memory or network transports.

The core `eris` package does not implement storage, file or stream I/O,
networking, random-secret generation, or ERIS extensions.

```go
secret := eris.Secret{} // The null convergence secret.
capability, blocks, err := eris.EncodeRecommended(content, secret)
if err != nil {
	return err
}

decoded, err := eris.Decode(capability, blocks)
```

Build and open an ERIS-FS image without host file-system access:

```go
blocks := make(eris.BlockSet)
builder := erisfs.NewBuilder(blocks, eris.Secret{})
if err := builder.AddFile("docs/readme", strings.NewReader("hello"), false); err != nil {
	return err
}

capability, err := builder.Finish()
if err != nil {
	return err
}
image, err := erisfs.OpenFS(capability, blocks)
if err != nil {
	return err
}
file, err := image.OpenFile("docs/readme") // io.ReaderAt and io.Seeker
if err != nil {
	return err
}
defer file.Close()
```

Use `Index`, `EncodeIndex`, `DecodeIndex`, and `NewReader` for low-level ERIS-FS
plumbing. Use `Builder`, `OpenFS`, and `FileSystem` for the high-level
file-system API.

`OpenFS` and `DecodeIndex` limit each decoded index to 64 MiB. Use
`OpenFSWithLimit` or `DecodeIndexWithLimit` to select another application limit.

Callers that need stronger resistance to confirmation attacks must generate a
cryptographically secure, unique `eris.Secret` outside this package. ERIS does
not provide confidentiality.

Run the complete checks with:

```sh
go test -race -cover ./...
golangci-lint run ./...
```

## License

Files in this repository are distributed under the CC0 license.

<p xmlns:dct="http://purl.org/dc/terms/">
  <a rel="license"
     href="https://creativecommons.org/publicdomain/zero/1.0/">
    <img src="https://i.creativecommons.org/p/zero/1.0/88x31.png" style="border-style: none;" alt="CC0" />
  </a>
  <br />
  To the extent possible under law,
  <a rel="dct:publisher"
     href="https://github.com/asciimoth">
    <span property="dct:title">ASCIIMoth</span></a>
  has waived all copyright and related or neighboring rights to
  <span property="dct:title">eris-tools</span>.
</p>
