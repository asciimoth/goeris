## title: Canons of ERIS synopsis: Append-only logs of ERIS encoded content status: DRAFT

Canons are logs of CBOR encoded data with an efficient append operation.

Canons are largely inspired by the
[Secure Scuttlebutt feed protocol](https://ssbc.github.io/scuttlebutt-protocol-guide/#feeds)
and are designed for similar use cases. Unlike Secure Scuttlebutt this
specification does not include features for enforcing that logs are append only,
but it does not exclude the use of signed back-references to this effect.

A canon exposes a traversable and appendable sequence of CBOR items to an
application. Internally the canon is a sequence of binary encoded CBOR items
that are aligned to fit within ERIS block boundaries (1KiB or 32KiB) using
zero-length CBOR arrays as padding. This is to minimize the number of ERIS
meta-blocks created over the lifetime of the canon and allows for efficient
lookup or selective sharing of Canon items. An item within a canon can be
referenced by an ERIS URNs combined with an offset index.

Items that are too large to fit within a single block are replaced by an
[ERIS read-capability](https://eris.codeberg.page/spec/#_binary_encoding_of_read_capability)
to an off-canon encoding.

The recommended ERIS encoding of a Canon is unique rather than
[convergent](https://eris.codeberg.page/spec/#_convergence_secret) to mitigate
confirmation attacks against Canon data at-rest.

Some of the invariant properties of canons:

- All items within the raw canon are less than the block size minus one.
  Off-canon items may be of arbitrary length.

- Each block of the canon is terminated by one or more zero-length arrays. If a
  single block or the final block of a canon is retrieved from ERIS storage then
  one or more of the terminating zero-length arrays will be missing. This is
  because the normalized encoding of a zero-length array is a single byte with a
  value of 0x80, and ERIS padding is defined as the byte 0x80 followed by zero
  or more null bytes.

- Zero-length arrays may be appended to a canon but are discarded during
  traversal.

- ERIS read-capabilities in
  [CBOR encoding](https://eris.codeberg.page/spec/#_binary_encoding_of_read_capability)
  may be appended but are dereferenced during traversal. Appending a capability
  that does not deference to CBOR data will invalidate a canon.

- The former two rules do not apply to items nested within CBOR arrays, maps, or
  tags.
