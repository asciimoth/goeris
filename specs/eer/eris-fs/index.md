## title: ERIS-FS: A format for encoding file systems synopsis: Encode file system trees in ERIS with de-duplication of files

## Introduction

ERIS defines how a sequence of bytes can be encoded and made available robustly.
Many types of content can be serialized to a sequence of bytes that can be then
encoded with ERIS. One type of content that deserves special attention are
collections of files organized in a tree - file systems. ERIS-FS is a format of
encoding file systems that is optimized for usage with the ERIS encoding. In
particular, files that appear multiple times within a file system and also
within multiple file systems can be de-duplicated using ERIS-FS. This is
important for applications such as software package management (e.g.
[Guix](https://guix.gnu.org/) or [Nix](https://nixos.org/)) where two versions
of a package might share many common files.

A file system encoded with ERIS-FS can be decoded to a directory containing the
tree of file system objects or be directly mounted.

ERIS-FS is similar to formats such as SquashFS, Tar or EROFS.

### File System Objects

Operating systems have different kinds of objects with a lot of metadata that
live in a file system. ERIS-FS only encodes minimal information that is required
for simple file sharing and software packages:

- Regular files
- Executable files
- Symbolic Links

This is directly inspired by file system objects encoded as handled by Nix or
Guix (see
[Section 5.2.1 of Eelco Dolstra thesis](https://edolstra.github.io/pubs/phd-thesis.pdf)).

### Deterministic Encoding

The same tree of file system objects is encoded to the same image by specifying
a strict ordering of files. Metadata structures are encoded using
deterministically encoded CBOR as defined in
[section 4.2.1 of RFC 8949](https://www.rfc-editor.org/rfc/rfc8949.html#section-4.2.1).

### Limitations

- Images are in general less compact than SquashFS, Tar or EROFS images (but
  offer much better de-duplication)
- Can only handle a very limited set of files and metadata
- Empty directories can not be encoded.

## ERIS-FS Index

ERIS-FS is a CBOR structure that holds the structure of the file system. Content
of files is referenced with an ERIS read capability. Subdirectories can be
represented by nested ERIS-FS indexes.

The ERIS-FS Index is described using CDDL
[RFC8610](https://datatracker.ietf.org/doc/html/rfc8610):

```CDDL
eris-fs-index = #6.1701996916 ( index )

index = [
  version: 2,
  { * path => entry }
]

path = [ * tstr ]

entry = file-entry / executable-entry / symlink-entry / erisfs-entry

file-entry = [ 
  type: 0,
  content: read-capability,
  length: file-length
]

executable-entry = [ 
  type: 1,
  content: read-capability,
  length: file-length
]

symlink-entry = [
  type: 2,
  target: path
]

erisfs-entry = [
  type: 3,
  subdirectory: read-capability
]

read-capability = #6.276 ( bstr )

file-length = int / lzip-length

lzip-length = #6.1280985424 ( int )
```

The index is tagged with the CBOR tag `1701996916` according to
[RFC 9277](https://www.rfc-editor.org/rfc/rfc9277.html).

The first field of the index is a version number. The second field is a
dictionary of paths to entries. A path is an array of names, the leading names
are directories and the last is a file name. To convert to a native file-system
path the names are joined together using the native separator (for Unix this is
"/").

No path can be a prefix of another path. For example, an index cannot contain
entries keyed by both path `[ "a" "b" ]` and path `[ "a" "b" "c" ]`.

### File entry

A file entry must contain three fields. The first field is the constant 0 value.
The second field is an ERIS read-capability that identifies file content. The
third field is the length of the file content. The file length field may be
different than the length of the read-capability content in which case the
length field should be tagged with an additional piece of metadata indicated
why.

### Executable entry

An executable entry has the same fields as a file entry, except that first field
is the constant 1 value. An executable entry corresponds to an executable file
in a traditional file system.

## Symlink entry

A symlink entry must contain two fields. The first field is the constant 2
value. The second field is an array of names that indicate the symlink target.
The names of the target are joined by the native directory separator. Absolute
paths may be represented by a leading zero-length string in the target but the
semantics of the target field is otherwise undefined.

## ERIS-FS entry

An ERIS-FS entry must contain two fields. The first field is the constant 3
value. The second field is a read-capability to another ERIS-FS index which is
nested at the path of this entry.

The existence of directories is implicit and an empty directory can only be
expressed by an empty ERIS-FS.

## Block size

Files should be encoded using block size as recommended in the ERIS
specification (see
[ERIS specification section 2.2.1](http://purl.org/eris/spec/#name-recommendation-on-block-siz)).
That is files smaller than 16KiB should use block size 1KiB and larger files
should use block size 32KiB.

The index should also be encoded using the same block size recommendation -
block size 1KiB if the index is smaller than 16KiB and block size 32KiB else.

## File Compression

Regular and executable files may be compressed using
[Lzip](https://www.nongnu.org/lzip/lzip.html). Lzip was selected as a compressor
because it uses a deterministic
[Lempel-Ziv-Markov chain-Algorithm](https://en.wikipedia.org/wiki/LZMA) and
robust encoding format.

If a the `length` field of a `file-entry` or `executable-entry` is tagged as an
`lzip-length` then the data encoded in the `read-capability` of the second field
is compressed with Lzip. This tagged integer specifies the length of a file
after decompression.

## MIME type

The ERIS-FS MIME type is `application/x-eris-fs+cbor`.

This can be used to identify ERIS-FS file systems in applications or protocols.

## Implementation Notes

### Detecting ERIS-FS

An ERIS-FS file system can be detected by checking if the first five bytes are
0xDA65726974. The seventh byte is likely the version number.

See also [RFC 9277](https://www.rfc-editor.org/rfc/rfc9277.html).

### Parallelization

Creating and decoding ERIS-FS file systems can be parallelized by
encoding/decoding files in parallel.

### Symbolic Links

ERIS-FS can hold symbolic links that point outside of the encoded file system.
This seems to be necessary for properly supporting Nix/Guix substitutes.

Symbolic links can be dangerous and implementations SHOULD issue a warning when
decoding symbolic links that point outside of the encoded file system.

### Compression

Lzip compressed files perform poorly under random access patterns therefore
compression should only be used for small files or files that are accessed
sequentially, such as source code. Do not compress files smaller than 1KiB
because file size cannot be reduced below the minimum ERIS block size. A default
8MiB dictionary size is recommended for convergent compression. Assume that
applying a media-specific lossless compressor such as AVIF or FLAC before
ERIS-FS encoding will outperform Lzip compression in both time and space.

Implementations that do not recognize the Lzip field on file entries may expose
those files in compressed form. This can be detected by the unexpected presence
of Lzip headers on files and read underruns.

## Deprecated Version

Old versions of this specification are available in the EER
[Git repository](https://codeberg.org/eris/eer/src/branch/main/eer/eris-fs/index.md).

### Version 0

Version 0 of the ERIS-FS format encoded all files into a continuous sequence of
bytes that so that file boundaries are aligned to ERIS block boundaries. This
version of the encoding is considered to be deprecated and SHOULD NOT be used.

### Version 1

Version 1 of the ERIS-FS format is the same as version 2 except the `length`
field of the `file-entry` and `executable-entry` is omitted.

## Implementations

- [Guile-ERIS](https://codeberg.org/eris/guile-eris)
- The [mkerisfs](https://codeberg.org/eris/nim-eris#mkerisfs) tool which is part
  of [nim-eris](https://codeberg.org/eris/nim-eris)
- [eris-go](https://codeberg.org/eris/eris-go)

## IANA Considerations

### CBOR Tags Registry

This specification requires the assignment of a CBOR tag:

- Tag: `1701996916`
- Data Item: `array`
- Semantics: ERIS-FS image header

The tags is added to the CBOR Tags Registry as defined in
[RFC 8949](https://www.rfc-editor.org/rfc/rfc8949.html).
