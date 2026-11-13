# ERIS-FS v2 encoding and reproducibility

This document records the encoding choices made by goeris. The wire format is
defined by the local [ERIS-FS specification](eer/eris-fs/index.md). These
choices do not add fields or change that format.

Equal file trees do not, by themselves, imply equal ERIS-FS capabilities. The
format permits several encoding choices. Use one of the procedures below and
keep its inputs and settings fixed.

## Deterministic encoding across implementations

Use **uncompressed files** to get the same capability without depending on a
particular compression encoder. Every implementation must use these settings:

| Input or setting   | Required value for this procedure                                                                                  |
| ------------------ | ------------------------------------------------------------------------------------------------------------------ |
| ERIS-FS version    | Version 2 for every index                                                                                          |
| Convergence secret | The same 32 bytes for all files and indexes; `eris.Secret{}` is 32 zero bytes                                      |
| Compression        | Disabled for every regular and executable file                                                                     |
| ERIS block size    | 1024 bytes for content shorter than 16384 bytes; 32768 bytes for content of 16384 bytes or more                    |
| Index block size   | Apply the same rule to the complete encoded CBOR index, before ERIS padding                                        |
| Index structure    | One root index with full relative component paths; no nested indexes                                               |
| CBOR encoding      | RFC 8949 section 4.2.1: definite lengths, shortest integer and length forms, bytewise ordering of encoded map keys |

This procedure represents files and symlinks. Directories that contain entries
are implicit. Omit empty directories, which have no entry in this flat index. To
preserve empty directories or use nested indexes, agree on exactly which
directories get separate indexes. Encode each child with the same settings and
put its capability in a type 3 entry. An empty directory uses an empty child
index. Apply this rule recursively. `AddSubdirectory` uses the supplied
capability as it is; it does not encode the child again.

Before encoding, agree on the exact tree inputs:

1. Use the same bytes for each file. Do not convert line endings or text
   encodings. Read from a stable snapshot if source files can change.
1. Use the same UTF-8 bytes for each path component, including letter case and
   Unicode form. Separate components with `/` in the goeris API. The encoder
   does not normalize names. A backslash is a literal character.
1. Assign the same regular or executable type to each file. If importing host
   permissions, use an agreed rule to derive the `executable` argument. Other
   permission bits, owners, timestamps, and hard-link identity are not stored.
1. Preserve symlinks as symlinks, with the same target bytes. Do not resolve or
   clean their targets. Split targets on `/` and retain empty components:
   `/a//b/` becomes `["", "a", "", "b", ""]`. goeris encodes an empty target as
   `[""]`, not `[]`.

For each file, store type 0 or 1, its tagged binary ERIS capability, and its
exact byte length as an untagged integer. Use the version 2 structure described
in [Index and ERIS encoding](#index-and-eris-encoding). Encode file content and
index bytes with the standard ERIS algorithm, including its padding and tree
construction. Do not add a CBOR self-description tag or other optional wrapper.

In goeris, use `NewBuilder`, `AddFile` for every file, `AddSymlink` for each
link, and `Finish`. No extra deterministic mode is needed:

```go
blocks := make(eris.BlockSet)
builder := erisfs.NewBuilder(blocks, eris.Secret{})
if err := builder.AddFile("docs/readme", strings.NewReader("hello\n"), false); err != nil {
	return err
}
if err := builder.AddSymlink("readme", "docs/readme"); err != nil {
	return err
}
capability, err := builder.Finish()
if err != nil {
	return err
}
fmt.Println(capability)
```

Here `eris` is `github.com/asciimoth/goeris` and `erisfs` is
`github.com/asciimoth/goeris/fs`. Another implementation must select the same
settings; its defaults or a claim of ERIS-FS compliance are not sufficient. File
insertion order does not matter. The builder sorts the encoded map keys. With
the low-level API, pass the required block size to `EncodeContent`, set
`Entry.Size` and `Entry.Type`, leave `Entry.Compression` as `Uncompressed`, and
use `EncodeIndex`.

Compression can also produce equal capabilities across implementations, but only
if they produce or receive exactly the same Lzip bytes for each file. An 8 MiB
dictionary alone is not sufficient. Either reproduce the complete
[Lzip encoding profile](#lzip-encoding-profile), or share the compressed stream
and use `AddLzipFile` with the same original length. Select file block sizes
from the stored Lzip length. Agree on which files are compressed as well.

## Deterministic encoding with goeris auto compression

Use `Builder.AddAutoFile` for every regular and executable file. Keep the
secret, tree inputs, and index structure fixed as described above. Keep the
goeris version and the effective LZMA dependency version fixed too. This gives
repeatable compression choices and compressed bytes within that encoding
profile.

In the example above, replace the file call with:

```go
if err := builder.AddAutoFile("docs/readme", strings.NewReader("hello\n"), false); err != nil {
	return err
}
```

The builder applies **ERIS-FS-AUTO-1** (`AutoCompressionVersion == 1`):

1. Read the first 32768 bytes, or the complete file if it is shorter. Short
   reads do not end the sample.
1. Leave files shorter than 1024 bytes uncompressed.
1. For other files, calculate the fixed LZ77 probe size `P`. Select Lzip if
   `8 * (P + 26) <= 7 * N`, where `N` is the sample length. Equality selects
   Lzip. Otherwise select uncompressed content.
1. Replay the sample and encode the complete file in the selected mode. Keep
   that choice even if later data compresses poorly or the final stream grows.

The [auto compression policy](eris-fs-auto-compression.md) defines the exact
probe and its test vectors. It does not use a trial Lzip encoding, file names,
extensions, host settings, or file read sizes. `ChooseCompression` exposes the
same decision: supply the whole file when it is shorter than 32768 bytes, or at
least the first 32768 bytes otherwise. An arbitrary shorter prefix can give a
different result.

When Lzip is selected, `AddAutoFile` calls `AddCompressedFile`, which uses the
fixed profile below. When uncompressed content is selected, it calls `AddFile`.
Both paths select ERIS block sizes from the stored length. The index records the
original file length. Entry insertion order and reader chunk sizes do not change
a successful encoding. Use the builder sequentially; it is not safe for
concurrent calls.

For files that need random access, use `AddFile` and keep the same per-path
override on each run. `AddCompressedFile` always compresses, including files
below 1024 bytes. `AddLzipFile` uses caller-supplied compressed bytes. Mixing
these methods without a fixed per-file rule can change the image capability.

The auto policy version fixes the compression **choice**, not the Lzip encoder
algorithm. The current profile uses `github.com/ulikunitz/xz` v0.5.15. Check the
effective module version in the application build: a dependency upgrade or a
module replacement can override the version in this library's `go.mod`. For
reproducible builds, record the goeris revision and effective dependency
versions, and check the fixed conformance vectors before accepting an upgrade.
Other implementations can use ERIS-FS-AUTO-1 and still produce different Lzip
bytes and capabilities.

## Index and ERIS encoding

New indexes use version 2 and tag 1701996916. File entries contain type, tagged
ERIS read capability, and original file length. Executable files use type 1.
Lzip lengths use tag 1280985424. Symlink targets are arrays of path components,
including empty components for leading, repeated, or trailing slashes. Nested
indexes use type 3 and a tagged read capability.

The encoder uses the shortest CBOR integer and length encodings and definite
lengths. Map keys are compared byte by byte in their encoded form, as required
by RFC 8949 section 4.2.1. This is not a comparison of native paths or a
length-first comparison of complete keys. Input entry order has no effect. The
encoder rejects duplicate paths and paths that are prefixes of other entries. It
does not normalize Unicode names or symlink targets.

The builder uses 1 KiB ERIS blocks for stored content shorter than 16384 bytes,
and 32 KiB blocks for other content. The same rule applies to index bytes. For
compressed files, the stored content is the Lzip stream; the index still records
the length before compression. Explicit `EncodeContent` calls use the block size
supplied by the caller.

The convergence secret is supplied by the caller. No random secret, timestamp,
host name, file name, or operating system field is added during encoding. Nested
index boundaries remain explicit: flattening a nested index can change the image
capability even when the extracted files are the same.

## Lzip encoding profile

The writer produces one Lzip version 1 member for the entire file, with these
fixed settings:

| Setting                                 | Value                                         |
| --------------------------------------- | --------------------------------------------- |
| Header                                  | `4c 5a 49 50 01 17`                           |
| Dictionary                              | 8388608 bytes (8 MiB), including short inputs |
| LZMA literal context bits (`lc`)        | 3                                             |
| LZMA literal position bits (`lp`)       | 0                                             |
| LZMA position bits (`pb`)               | 2                                             |
| LZMA encoder                            | `github.com/ulikunitz/xz/lzma` v0.5.15        |
| Match algorithm                         | `HashTable4`                                  |
| Lookahead buffer                        | 4096 bytes                                    |
| Input size in the temporary LZMA header | Unknown                                       |
| End-of-stream marker                    | Required                                      |
| Intermediate flushes or resets          | None                                          |
| Member splitting                        | None                                          |

All settings are explicit in `fs/lzip_writer.go`. The temporary 13-byte LZMA
header is removed; it is not part of the stored stream. The Lzip trailer is the
IEEE CRC-32 of the original bytes, followed by the original length and the total
member length. The fields have lengths of 4, 8, and 8 bytes and use
little-endian order.

The pinned encoder source determines match selection, tie breaking, and range
coding. This implementation has fixed hash constants and no random seed. Read
call boundaries do not flush the encoder. All input bytes pass through the same
dictionary, and the end marker is written only after normal EOF. Input errors
must fail the operation and must not produce a successful entry.

The profile includes the encoder algorithm, not only its visible options. A
dependency upgrade or a change of matcher can change compressed bytes even when
the Lzip dictionary remains 8 MiB. The fixed regression vectors must remain
unchanged for an output-compatible change. Do not regenerate them merely to make
a changed encoder pass. An intentional output change needs a separately
identified profile and a documented migration.

`AddLzipFile` stores caller-supplied bytes and does not apply this profile.
`AddCompressedFile` explicitly selects compression, including for short input.
For the specification's recommendation to leave files below 1 KiB uncompressed,
use `AddAutoFile` or select `AddFile` explicitly. Auto mode uses the separate
[ERIS-FS-AUTO-1 policy](eris-fs-auto-compression.md), then applies this Lzip
profile when it selects compression.

## What the specification guarantees

The local ERIS-FS specification requires deterministic CBOR. It permits Lzip and
recommends an 8 MiB dictionary. It does not fix a match algorithm, compression
level, lookahead size, member splitting rule, or compression selection policy.
It provides no ERIS-FS or compressed-output test vectors.

Thus, valid ERIS-FS implementations can produce different compressed streams for
the same file. Matching an arbitrary `lzip` command's output is not a format
requirement. In particular, the command can reduce its dictionary for short
files. The
[Lzip format](https://www.nongnu.org/lzip/manual/lzip_manual.html#File-format)
fixes the container and LZMA properties, but does not prescribe one encoding
algorithm.

This library produces the same bytes for the same file bytes, entry metadata,
index structure, convergence secret, compression choices, and encoding profile.
This guarantee holds across input read sizes and entry insertion orders. It does
not claim byte equality with a different Lzip encoder. A cross-language tool
that needs identical compressed capabilities must reproduce the profile's
encoder algorithm, or use exactly the same precompressed bytes.

## Conformance tests

- `TestERISOfficialVectors` uses all 13 positive and 12 negative vectors linked
  by ERIS 1.0.0 section 2.8.6. It checks exact capabilities and block bytes with
  both the base encoder and the streaming FS encoder. Both readers must reject
  the negative vectors.
- `TestV2WireVectors` and `TestV2CanonicalMapVector` use hand-written CBOR bytes
  derived from the local CDDL. They cover every entry type, the compression tag,
  symlink components, and bytewise key order. These are library fixtures, not
  upstream test vectors.
- `TestIndexRecommendedBlockSizeBoundary` checks index lengths immediately
  below, at, and above 16 KiB. File block-size tests cover the same boundary.
- `TestLzipEncodingVectors` checks fixed compressed bytes or SHA-256 hashes,
  compressed capabilities, index bytes, and image capabilities. Its inputs cover
  lookahead boundaries, mixed and incompressible data, and an input longer than
  the 8 MiB dictionary. The same fixtures run on 32-bit and 64-bit builds.
- `TestLzipIndependentDecoder` uses `xz --format=lzip --decompress` when `xz` is
  installed. This checks the output with liblzma, independently of the Go LZMA
  reader. The test reports a skip if the external tool is absent.
- Compression and auto-mode fuzz tests check exact output equality across read
  sizes, EOF delivered with data, and decoded-content round trips.

Fixture sources, input recipes, and licenses are recorded in
[`fs/testdata/README.md`](../fs/testdata/README.md). Normal tests need no
network.
