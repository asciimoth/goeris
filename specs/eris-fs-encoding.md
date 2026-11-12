# ERIS-FS v2 encoding and reproducibility

This document records the encoding choices made by goeris. The wire format is
defined by the local [ERIS-FS specification](eer/eris-fs/index.md). These
choices do not add fields or change that format.

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
This guarantee includes input read sizes and entry insertion order. It does not
claim byte equality with a different Lzip encoder. A cross-language tool that
needs identical compressed capabilities must reproduce the profile's encoder
algorithm, or use exactly the same precompressed bytes.

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
