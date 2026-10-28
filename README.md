# ERIS tools

This repository provides pure Go tools for [ERIS 1.0.0](https://eris.codeberg.page/).

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

Use `Index`, `EncodeIndex`, `DecodeIndex`, and `NewReader` for low-level
ERIS-FS plumbing. Use `Builder`, `OpenFS`, and `FileSystem` for the high-level
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
