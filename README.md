# ERIS tools

This repository provides pure Go tools for [ERIS 1.0.0](https://eris.codeberg.page/).

The `eris` package implements:

- ERIS padding and recommended block-size selection;
- BLAKE2b-256 references and ChaCha20 node encryption;
- read capability binary, URN, and CBOR encodings;
- deterministic encoding to an in-memory block set; and
- verified decoding from an in-memory block set.

The package does not implement storage, file or stream I/O, networking,
random-secret generation, or ERIS extensions.

```go
secret := eris.Secret{} // The null convergence secret.
capability, blocks, err := eris.EncodeRecommended(content, secret)
if err != nil {
	return err
}

decoded, err := eris.Decode(capability, blocks)
```

Callers that need stronger resistance to confirmation attacks must generate a
cryptographically secure, unique `eris.Secret` outside this package. ERIS does
not provide confidentiality.

Run the complete checks with:

```sh
go test -race -cover ./...
golangci-lint run ./...
```
