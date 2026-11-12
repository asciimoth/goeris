## title: ERIS link files synopsis: References to ERIS data for traditional file-systems

The storage and retrieval of ERIS data is orthoginal to data handling in
traditional file-systems. ERIS link files are a simple construction for storing
references to ERIS data on "on-disk" file-systems, version control systems, or
exchanging references over an attachment-based medium such as email.

## Format

An ERIS link file is a simple CBOR encoded tuple of data:

```cddl
ErisLinkFile = #6.55799(ErisLink)

ErisLink = [
  ReadCapability
  Size
  Mime
  Attributes
]

ReadCapability = #6.276(bstr)
Size = uint
Mime = tstr
Attributes = { * tstr => any }
```

## Design Rational

The primary design goal is a terse encoding. A typical link file is less than a
hundred bytes and is trival to generate and parse. It supports extensible
metatdata without requiring a parser to read or parse any more of the
representation than is desired. A link can also be represented by a QR code of
modest complexity.

## Identification

- The ERIS link MIME type is `application/x-eris-link+cbor`
- The recommended link file extension is `.eris`.
- Files start with the magic bytes `0xD9D9F784D901145842`. The format is
  arranged to put the most predictable bytes first. The format is simple and
  recognizable enough that a dedicated CBOR tag is not defined.

## Attributes

The final "attributes" field in the CBOR tuple is a map of string keys to
arbitrary values. This standard does not restrict the use of this field but the
recommendation is to minimize its use in order to keep link files small.

Possible uses of the attributes field include:

- RDF metadata
- Revision history
- Probablistic filters for matching any block that constitutes the ERIS encoding
  of the referred content.
