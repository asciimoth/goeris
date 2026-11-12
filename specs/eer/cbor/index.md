## title: CBOR Serialization of ERIS Encoded Content synopsis: Useful for transporting encoded content on an USB stick or via e-mail

This document specifies a CBOR
([RFC 8949](https://www.rfc-editor.org/rfc/rfc8949.html)) based serialization of
content-addressed blocks, references to blocks as well as ERIS read
capabilities.

The serialization can be used to transport ERIS encoded content. The
serialization is easy to implement and is well-suited for constrained
environments such as embedded devices or web applications.

In order to decode content, all encoded blocks might have to be first read. This
makes the serialization inefficient for direct decoding. See
[SQLite for ERIS block storge](https://eris.codeberg.page/eer/sqlite.xml) for a
portable format that allows random-access to blocks.

## CBOR Serialization

The serialization is a CBOR array or a CBOR sequence
([RFC 8742](https://www.rfc-editor.org/rfc/rfc8742.html)) where the elements are
either blocks, blocks with a reference, references to blocks or read
capabilities.

The array MAY be encoded as CBOR indefinite length arrays.

The CBOR tag `1701996915` MAY be used to tag the array containing blocks and
read capabilities and SHOULD be present in contexts where the semantics of the
array might be ambiguous. Furthermore this tag MAY be preceded by the
self-describing CBOR tag `55799` to establish that the content following it is
in fact a CBOR tagged array. This would as an example identify blocks serialized
into a file-system as such. For a complete description of this tagging
convention see [RFC 9277](https://datatracker.ietf.org/doc/rfc9277/).

The serialization is specified in CDDL
([RFC 8610](https://datatracker.ietf.org/doc/html/rfc8610)):

```cddl

block = bstr . size 1024 / bstr . size 32768
reference = bstr . size 32
block-map = { * reference => block }
read-capability = #6.276(bstr)

eris-cbor-element = block / reference / block-map / read-capability

eris-cbor = [ * eris-cbor-element ]
tagged-eris-cbor = #6.1701996915(eris-cbor)
```

Note that blocks and references are only differentiated by size.

### CBOR Sequence

The term `eris-cbor` may be encoded as a CBOR Sequence
([RFC 8742](https://www.rfc-editor.org/rfc/rfc8742.html)).

This means that `eris-cbor-element` terms are concatenated instead of packed
into a CBOR array. This allows the serialization to be appendable.

In order to tag a CBOR sequence the initial item should a CBOR null element
tagged using `1701996915`:

```cddl
tagged-eris-cbor-sequence = [ #6.1701996915(null), * eris-cbor-element ]
```

This allows CBOR Sequences of ERIS encoded content to be detected.

## IANA Considerations

### CBOR Tags Registry

This specification requires the assignment of a CBOR tag:

- Tag: `1701996915`
- Data Item: `array`
- Semantics: Array of content-addressed blocks and ERIS read capabilities

The tags is added to the CBOR Tags Registry as defined in
[RFC 8949](https://www.rfc-editor.org/rfc/rfc8949.html).
