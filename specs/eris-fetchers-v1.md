# ERIS Fetch Protocol v1

**Proposed specification · 25 November 2026**

An ERIS fetcher takes a text description of a source and publishes the blocks
needed to read it by capability. A caller can use a local executable or an HTTP
service. Both receive the same bytes and speak the same result protocol. Each
fetcher can implement one source type or several.

For example, suppose the following URL serves the twelve ASCII bytes
`Hello world!`.

```text
type url
url https://example.org/hello
```

The fetcher reads the URL, stores the blocks, and replies in a single
invocation.

```text
eris-fetch/1 ok
kind blob
cap urn:eris:BIAD77QDJMFAKZYH2DXBUZYAP3MXZ3DJZVFYQ5DFWC6T65WSFCU5S2IT4YZGJ7AC4SYQMP2DM2ANS2ZTCP3DJJIRV733CRAAHOSWIYZM3M
```

The caller can lock this capability under the source ID of the two-line request
and use that capability on subsequent reads. A publisher-provided checksum is an
optional assertion about the download. An explicit Git tag or commit also
fetches directly. A Git request containing just the repository, or the
repository and a branch, returns a default revision and the other available
choices (§5). The example URL is illustrative and the capability is a test
vector.

**Contents** · [Source text](#1-source-text) ·
[Identity and locks](#2-source-identity-and-locks) ·
[Encoding](#3-result-encoding) · [Source types](#4-standard-source-types) ·
[Resolution](#5-resolution-and-refinement) · [Transports](#6-wire-protocol) ·
[Discovery](#7-finding-a-fetcher) · [Conformance](#8-conformance-vectors) ·
[References](#9-references)

## 1. Source text

MUST, SHOULD and MAY denote requirements, recommendations and options. Octet
counts are in bytes. A *description* is a map from a key to a nonempty sequence
of text values. All values on the wire are strings. The selected source type
defines each field's cardinality and interpretation. The core requirements and
examples in §§1–8 define v1. Automatic file compression is optional.

1. Read in binary mode. Validate the entire input as UTF-8 (RFC 3629), including
   comments. Reject a leading UTF-8 BOM, byte `7f` and bytes `00` through `1f`
   except HT (`09`), LF (`0a`) and CR (`0d`). A failed read is an error. A short
   read alone does not indicate EOF.
1. Each LF or CR ends a line. Ignore empty lines, so LF, CRLF, LFCR and CR all
   work, even in mixed input. EOF also terminates the last line. Writers use LF.
1. Ignore a line consisting entirely of ASCII spaces and tabs, or a line
   starting with `#` in column one. Records start in column one. On a record,
   split at the **first ASCII space**. The left side is the key. The remaining
   bytes, including extra spaces, tabs, backslashes, quotes or `#`, are the
   value. A key alone and a key followed by exactly one space both denote the
   empty string. A tab cannot separate key from value. No trimming, quoting,
   escaping, interpolation, case folding or Unicode normalization occurs.
1. A key consists of one or more dot-separated segments. Each segment matches
   the ASCII expression `[A-Za-z][A-Za-z0-9_-]*`. All comparisons are
   case-sensitive. A key and its component-prefix cannot both be leaves (for
   example, `a` and `a.b`). Append each value to its exact key in encounter
   order. Interleaving other keys has no effect on that sequence.

Exactly one `type` is required. Its value matches `[a-z][a-z0-9-]*`. An unknown
type gives `unsupported-type`. For a recognized type, a fetcher MUST validate
all records before retrieval, including fields it could otherwise ignore.
Unknown keys give `unknown-key`. Every declared key is scalar unless marked
**list**. Repeated scalars are errors even if the values match. Repeated list
elements retain duplicates. Booleans accept exactly `true` or `false`. A missing
scalar takes its documented default. An explicitly empty scalar must satisfy its
field's grammar.

The wire ceiling for a source is 1,048,576 bytes, including comments and
terminators. Its canonical form must fit the same ceiling. A physical line
before its terminator may contain at most 65,535 bytes. Enforce bounds while
reading. An implementation can tokenize one input buffer into (offset, length)
slices and sort record indices by (key bytes, original record number). No
recursive parser is needed. Report a size violation as `limit`. Never hash or
process a truncated prefix.

### Structured front ends

A JSON front end can map nested object members to segments, string leaves to
scalar values and arrays of strings to repeated records. This JSON object
describes one download with two mirrors.

```json
{
  "type": "url",
  "url": "https://example.org/hello",
  "alt": ["https://a.example/hello", "https://b.example/hello"],
  "name": "hello",
  "erisfs": {"use": "true"}
}
```

For this mapping, require single-segment member names and reject duplicate
members, empty objects, nulls, nested arrays and incompatible leaf types. A list
uses an array even for one element. An empty list maps to absence and decodes as
`[]` for a list field with no records. Encode JSON escapes to their actual text,
then apply all wire restrictions, including the prohibition on CR or LF *inside*
a value. A UI may display a checkbox or accept a native boolean and emit the
exact lowercase text. Every front end hashes the resulting source records.

## 2. Source identity and locks

A lock associates the source ID with the resulting capability. A download can
additionally carry a publisher’s checksum.

| Value               | Input                                                                     | Use                                                               |
| ------------------- | ------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| Source ID           | Version prefix and canonical description                                  | Look up the user's request in a lock or cache                     |
| Optional `checksum` | Downloaded bytes before any unpacking or ERIS encoding                    | Verify an externally supplied checksum on the primary or a mirror |
| `cap` result        | ERIS encoding of a blob, or of an ERIS-FS index that references its files | Read or independently verify the stored result                    |

To produce canonical text, visit the full keys in unsigned ASCII bytewise
ascending order, shorter first when one byte string is a prefix. For each key
emit its values in original order. Emit `key SP value LF` for a nonempty value
and `key LF` for an empty value. Emit no comments, blank lines or BOM. Do not
insert schema defaults. In particular a missing, empty and explicit zero
`eris.secret` have the same encoding behavior but three source IDs. Changing a
mirror, an explicit default, or the spelling of a URL changes the request
identity. The same bytes and encoding options can still yield the same
capability.

```text
raw-key   = ASCII("eris-fetch/source/v1") || 0x00 || canonical-UTF-8
digest32  = BLAKE2b(raw-key, output_bytes=32)
source-id = BASE32(digest32) || ":blake2"
```

BLAKE2b uses sequential unkeyed mode, no salt or personalization and a native
32-byte output. Truncation of a 64-byte output gives a different digest. BASE32
is RFC 4648 uppercase, unpadded, with zero unused bits. A source ID has 52
Base32 characters followed by `:blake2`. ERIS already uses BLAKE2b-256. A Locks
v1 store can receive `raw-key` through its raw BLAKE2 operation or `source-id`
through its prehashed operation. Hashing the printable ID again would look up
another record.

**Lock lifecycle.** After validation, look up the requested source ID. A trusted
hit supplies the recorded ERIS URN, with the kind derived from the source
schema. On a miss, fetch directly if the description supplies enough
information, or select a proposal as in §5. Only a validated `ok` can install a
binding. In either case the lookup key is the ID of the original request. If a
user chooses an alternative, that choice supplies this first observation. The
caller may also retain the selected source for repair and index the capability
under its source ID. The lock value can be just the canonical URN.

Use atomic create-if-absent for the first observation. Equal simultaneous
results agree. Different capabilities for the same ID are a lock conflict.
Preserve the existing entry. Replacement requires an explicit refresh policy. A
URL or tag may have changed on a later fetch. To repair missing blocks, fetch
from the recorded description or an available mirror and require the resulting
capability to equal the lock before reporting recovery. A changed result cannot
silently replace the original.

## 3. Result encoding

| Key                  | Accepted values                                      | Default and scope                                            |
| -------------------- | ---------------------------------------------------- | ------------------------------------------------------------ |
| `eris.secret`        | Empty or 64 lowercase hexadecimal digits             | 32 zero bytes. Applies to every ERIS encoding in the result. |
| `erisfs.use`         | `true`, `false`                                      | See the source-type table in §4.                             |
| `erisfs.version`     | `2`                                                  | `2` for trees                                                |
| `erisfs.compression` | `none` (required support), `auto` (optional support) | `none` for every regular and executable file                 |
| `erisfs.layout`      | `flat`                                               | `flat` for the index                                         |

`eris.secret` supplies the **ERIS convergence secret**, the 32-byte input to
leaf-key derivation. The ERIS algorithm derives the actual encryption keys.
Apply the same decoded secret to files and indexes. A descriptor containing a
private secret and the capability itself are sensitive. A configured HTTP
fetcher sees both. Keep private descriptors out of public locks and logs.
Credentials for fetching or block storage, proxy configuration, temporary paths,
retry budgets and store selection belong to the execution environment.

Reject an unsupported version, layout, compression value or output mode with
`unsupported-option`. For a blob reject all `erisfs.*` fields other than
`erisfs.use`, even when they spell tree defaults. Validate unknown keys
separately. The default requires no compressor. A handler accepting `auto` must
implement the selection policy and the Lzip encoder specified below. An
implementation without this optional profile must reject it before retrieval,
even if all the eventual files would be small.

### Blob and tree construction

Use ERIS 1.0.0. For every input to the ERIS encoder, take the **unpadded byte
length**. Use 1,024-byte blocks below 16,384 bytes and 32,768-byte blocks at or
above 16,384. This rule applies independently to a raw blob, each file and the
complete serialized index. Streaming implementations can buffer the first 16,384
bytes to make the same choice when length is unknown. Preserve every content
byte, including file line endings.

Build a virtual tree using component arrays. Each entry path has at least one
component. Each component is valid UTF-8, nonempty and contains neither NUL nor
`/`. Reject components `.` and `..`. Case, Unicode spelling and literal
backslash are significant. The source reader may impose additional archive
restrictions below. Reject duplicate leaves and every file/link-versus-directory
conflict. Directories are implicit in the flat index. Omit empty directories,
including after root stripping. An empty tree has a valid root index containing
an empty map.

For each regular or executable file, encode its stored bytes with ERIS, then
form the following CBOR objects. In this notation `tag(n, x)` wraps `x` in CBOR
tag `n` and `bstr66` is the ERIS binary read capability.

| Object          | CBOR value                                            |
| --------------- | ----------------------------------------------------- |
| Root index      | `tag(1701996916, [2, {path: entry, ...}])`            |
| Path key        | Array of component text strings, relative to the root |
| Regular file    | `[0, tag(276, bstr66), length_field]`                 |
| Executable file | `[1, tag(276, bstr66), length_field]`                 |
| Symbolic link   | `[2, target_components]`                              |

A symlink target is valid UTF-8 with no NUL. Split on literal `/` preserving
**every** empty part, including at both ends. An empty target becomes `[""]`. A
target can contain `.`, `..`, a backslash or an absolute path. Store the target
without following it. Apply this rule to link targets, never to entry path
validation.

Write definite lengths, shortest integers and shortest length/tag arguments.
Encode an uncompressed file’s `length_field` as its original byte count, an
untagged unsigned integer from 0 through 2⁶⁴−1. The optional compressed form
tags that same integer as specified below. Sort map keys lexicographically by
their complete deterministic CBOR encodings, comparing unsigned bytes (RFC 8949
§4.2.1). The comparison includes the array and text headers. Emit one root index
with full paths, no type-3 subindexes, outer wrapper or self-description tag.
ERIS-encode those exact index bytes and return its capability. Omit ownership,
times, ACLs, xattrs, hard-link identity and permission bits other than the
source-defined executable indicator.

Publish every block referenced by the root, including the file DAGs reached
through the index. Report `ok` only after the configured destination store
acknowledges their availability under its documented retention policy. A remote
service and its clients need a prearranged readable store or block transport.
The fetch response carries the root capability only. On failure some immutable
blocks may already exist. Reuse them on retry, without publishing a success or a
lock for the failed request.

### Optional automatic compression

With `erisfs.compression auto`, apply ERIS-FS-AUTO-1 to each regular and
executable file. Store the index and symlink targets uncompressed.
[ERIS-FS-AUTO-1 selection policy and probe](eris-fs-auto-compression.md)

1. Read the first 32,768 original bytes or the complete file if it is shorter.
   Continue across short reads. Fail the operation on a read error.
1. Let `N` be the sample length. If `N < 1024`, select uncompressed storage.
1. Otherwise compute the policy's LZ77 probe size `P` on the sample. Select Lzip
   if `8 * (P + 26) <= 7 * N`. Otherwise select uncompressed storage.
1. Encode the entire file, including the sampled bytes, with the selected mode.
   Keep the choice even if the remaining bytes have different properties or the
   Lzip stream grows.
   [Deterministic encoding with goeris auto compression](eris-fs-encoding.md#deterministic-encoding-with-goeris-auto-compression)

When the policy selects Lzip, produce one Lzip v1 member. Start with hex
`4c5a49500117`. Encode the entire original file with the LZMA algorithm of
`github.com/ulikunitz/xz/lzma` v0.5.15, using its `HashTable4` matcher,
8,388,608-byte dictionary, 4,096-byte lookahead and `lc=3, lp=0, pb=2`. Use
unknown input size, an end-of-stream marker, and no intermediate flush,
dictionary reset or member split. Store the range-coded stream without the
13-byte standalone LZMA header. Append the IEEE CRC-32 of the original bytes (4
bytes), the original length (8 bytes), and the total member length including the
trailer (8 bytes), all little-endian. Match the pinned encoder’s match
selection, tie breaking and range coding. Another language can use any
implementation that produces those exact bytes.

Use the complete **stored member length** for the ERIS block-size threshold. Set
the index’s `length_field` to `tag(1280985424, original_byte_length)` for each
compressed file. For example, a 50,000-byte file that compresses to 400 bytes
uses 1,024-byte ERIS blocks and a tagged length of 50,000. ERIS-FS readers use
the tag to identify Lzip content. Section 8 gives test vectors for both
compression choices.
[Pinned encoder behavior](https://github.com/asciimoth/goeris/blob/983b7c41be81cf34a5305e13c1257078ca7dd706/specs/eris-fs-encoding.md#lzip-encoding-profile)
·
[LZMA implementation v0.5.15](https://pkg.go.dev/github.com/ulikunitz/xz@v0.5.15/lzma)

## 4. Standard source types

The table is the complete list of type-specific keys, in addition to `type` and
the common keys in §3. Bold keys are required. Fetchers advertise a type by
configuration and MUST implement its base behavior. A deployment can deny access
or impose resource limits and must report that outcome.

| `type`    | Type-specific keys                                          | Default `erisfs.use`                           | Fetches directly when           |
| --------- | ----------------------------------------------------------- | ---------------------------------------------- | ------------------------------- |
| `url`     | **`url`**, `alt` (list), `checksum`, `name`, `executable`   | `true` if `name` is present, otherwise `false` | The URL and options are valid   |
| `archive` | **`url`**, `alt` (list), `checksum`, `format`, `strip-root` | `true`                                         | The URL and options are valid   |
| `git`     | **`url`**, `commit`, `tag`, `branch`, `subdir`              | `true`, required mode                          | A `tag` or `commit` is supplied |

### Resource bytes for URL and archive

`url` and each `alt` identify a single resource by absolute URI (RFC 3986). Use
a lowercase scheme, no userinfo or fragment, valid percent escapes, and ASCII
URI spelling. Raw spaces, controls and backslashes are invalid. Preserve the
spelling in the source ID. For network schemes below require a nonempty host. An
explicit port must be decimal digits denoting 1 through 65535. Do not infer a
scheme, expand URL globs, or treat source values as command-line options.
Credentials and protocol permissions belong to the fetcher configuration.

Both `url` and `archive` use the same scheme handling. Fetchers implementing
`url` or `archive` MUST support HTTP and HTTPS. An implementation may enable
other schemes that define retrieval of a complete byte stream, including
protocols available through its curl build. It must document the schemes and
retrieval profiles it supports. The following rules apply wherever the
corresponding scheme is supported.

| Scheme          | Byte stream                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `http`, `https` | Full GET with `Accept-Encoding: identity`. Accept a completed final 200 with no `Content-Range` and absent `Content-Encoding` or the single coding `identity` (case-insensitive). Remove transfer framing and preserve the body bytes. Check any declared length. No partial response or automatic content decompression.                                                                                                                                                                                                                                                              |
| `ftp`, `ftps`   | Retrieve a file in binary/image mode. Interpret the FTP URL path per RFC 1738 §3.2, including the initial login directory and percent-escaped path components. Accept an optional terminal `;type=i`. Reject queries, ASCII and directory transfer modes. Use the file data only and require successful transfer completion. For `ftps` use implicit TLS for control and protected data transfers.                                                                                                                                                                                     |
| `sftp`          | Read the named remote file from offset zero through EOF using SFTP. File bytes are unchanged. Do not generate directory listings. Use a slash-absolute remote path with no query. Decode percent escapes once per component and reject decoded NUL or slash. No shell or home-directory expansion.                                                                                                                                                                                                                                                                                     |
| `file`          | Read a local regular file in binary mode, on the **fetcher’s** machine. Support local absolute RFC 8089 URIs with empty authority or `localhost`, without port or query. Separate components at literal `/`, percent-decode once, reject NUL or encoded separators. POSIX preserves decoded component bytes. On Windows use UTF-8 components and a drive-absolute form such as `file:///C:/releases/pkg.gz`. Reject Windows device and UNC forms in this profile. Reject a decoded backslash on Windows. POSIX treats it as a literal name byte. A service can deny local-file access. |
| Other scheme    | A fetcher may implement a published scheme-specific read operation. Use its exact completed resource payload. A scheme with no implemented byte-retrieval profile gives `unsupported-scheme`.                                                                                                                                                                                                                                                                                                                                                                                          |

In an FTP or FTPS path, percent-encode semicolons in file and directory names.
An unescaped semicolon starts the terminal transfer mode suffix.

HTTP source redirects 301, 302, 303, 307 and 308 are restricted to allowed
HTTP(S) destinations. Resolve `Location` against the current URI, remove any
fragment, and reapply destination and credential policy. Reject loops,
HTTPS-to-HTTP downgrades and more than ten hops for a candidate. A redirect
leaves the source text intact. Other source schemes use the retrieval profile
for that scheme. The configured **fetcher service endpoint** itself always uses
HTTP(S), independently of the source schemes it can retrieve.

**Optional checksum.** The scalar `checksum` has the form `algorithm:hex`.
Require exactly one colon, a nonempty algorithm name matching
`[a-z][a-z0-9-]{0,31}`, and nonempty lowercase hexadecimal digits. An empty
field is invalid. V1 defines `sha256` (SHA-256) and `blake2b-256` (the 32-byte
BLAKE2b in §2). Both use exactly 64 lowercase hex digits. Unknown or
unimplemented algorithms give `unsupported-option`. Malformed values for
recognized algorithms give `invalid-source`. A supplied checksum must be
checked, including on a byte cache hit. With no `checksum`, the source places no
checksum requirement on the fetcher. For an archive it covers the downloaded
container, including compression headers, before decompression. For the opening
fixture the optional field is the following line.

```text
checksum sha256:c0535e4be2b79ffd93291305436bf889314e4a3faec05ecffcbb7df31ad9e51a
```

When the field is absent, use the first successful download directly. No
separate download hash or checksum proposal is required. The request can
complete with `ok` on its first invocation.

**Mirrors.** Validate every candidate URI before retrieval. An unsupported
scheme is a property of the candidate. Try the primary then each `alt` in
encounter order, even when they use different schemes. Skip a candidate that is
unsupported, denied, unavailable, absent or fails the supplied checksum. The
first complete acceptable byte stream supplies the input to the requested
encoder or decoder. Subsequent format or store failures terminate the operation.
If all candidates fail, return `integrity` if any complete download mismatched,
otherwise the last candidate’s error. For HTTP, classify 404/410 as `not-found`,
401/403 as `denied` and other unacceptable responses as `unavailable`. An
exhausted request-wide budget or cancellation ends the operation immediately.

#### `url`

Blob mode encodes exactly those bytes. Tree mode requires `name`, one valid path
component as defined in §3. Insert exactly one file with that name. `executable`
is a boolean, default `false` in tree mode. Reject `name` or `executable` in
explicit blob mode and reject `executable` alone when the derived mode is blob.
An explicit `erisfs.use true` with no name gives `invalid-source`. The name
never comes from a redirect, a URL basename or the caller's temporary file.

#### `archive`

Tree output is the default. Set `erisfs.use false` for a decompressed blob.
`format` is one of `auto` (default), `gzip`, `tar`, `tar.gz` and `zip`. Select
the decoder from this table. An explicit format must validate. In `auto` mode
the two ZIP signatures are hex `504b0304` and `504b0506`. The gzip signature is
`1f8b`.

| Mode | `auto` detection                                                     | Explicit formats and result                                                                                                                                                                                                                                                                                                                                                   |
| ---- | -------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tree | Gzip signature → `tar.gz`. ZIP signature → `zip`. Otherwise → `tar`. | `tar`, `tar.gz` and `zip` produce the validated member tree, with `strip-root` as below. Explicit `gzip` with tree output gives `unsupported-option`.                                                                                                                                                                                                                         |
| Blob | Gzip signature → `gzip`. ZIP signature → `zip`. Otherwise → `tar`.   | `gzip` yields the complete decompressed byte stream, even when those bytes happen to be tar. Explicit `tar.gz` parses the decompressed tar. For `tar`, `tar.gz` and `zip` validate the entire archive and return the contents of its sole regular or executable file. Ignore directory entries when counting. Any link, zero files or multiple files gives `invalid-content`. |

For `gzip` and `tar.gz` decode all RFC 1952 members in order. Verify reserved
flags, optional header CRC when present, and each trailer CRC-32 and ISIZE
(length modulo 2³²). Concatenate the decompressed bytes and reject truncation or
trailing non-member data. Header names and timestamps never name an output file.
A correctly encoded empty stream produces a valid empty blob. Reject
`strip-root` in blob mode even when explicitly false. This request returns the
bytes of a gzip-compressed file.

```text
type archive
url https://example.org/message.txt.gz
format gzip
erisfs.use false
```

If the decompressed bytes are `Hello world!`, this request returns the opening
example’s blob capability on its first call. To keep the original compressed
bytes use `type url`.

In tree mode `strip-root` is a boolean, default `true`. Before discarding empty
directories, require the virtual root to have exactly one child, an explicit or
implicit directory. Remove that component from all paths. A lone file, multiple
top-level children or no children gives `invalid-content`. A single explicit
empty directory strips to a valid empty tree. With `strip-root false` keep the
paths and allow an empty archive. Preserve symlink targets byte for byte when
moving the root.

**Member paths.** Decode names as specified below and work in a virtual POSIX
namespace on every OS. Reject an empty raw name, a leading slash and any literal
backslash. Remove leading `./` components. For a directory only, remove one
trailing slash if present. A directory that consisted solely of `.` or repeated
`./` components is a root marker and contributes no child. Otherwise split on
`/` and reject any empty, `.` or `..` component, or a first component beginning
with an ASCII letter followed by `:`. Apply validation before stripping.
Repeated explicit directory records and an explicit declaration of an inferred
directory coalesce. Two non-directory records at the same path, even identical
ones, or any file/directory conflict give `invalid-content` regardless of order.
Validate every member, including omitted directories. Never open an
archive-supplied path on the host to resolve a link. A conforming reader can
process colliding host names, such as `A` and `a`, entirely in memory or with
synthetic temporary names.

| Container | Required v1 decoding profile                                                                                                                                                                                                                                                                                                                                                                                                                         |
| --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tar       | POSIX ustar, including per-member pax extensions described below. Form the ustar name from `prefix/name` when prefix is nonempty. Accept regular (type `0` or NUL), directory (`5`) and symbolic link (`2`). For regular files use any set bit of `mode & 0111` (octal) as executable. Link target comes from `linkname`. Require zero data length for a directory or link. Reject hard links, devices, FIFO, sparse records and other member types. |
| Gzip tar  | Decode RFC 1952 member(s) in order, verify each trailer CRC and size, concatenate uncompressed bytes and apply the same tar profile. Reject trailing non-member bytes.                                                                                                                                                                                                                                                                               |
| ZIP       | Single-disk ZIP32, stored or deflate, using local entries and one central directory. Use the central entry's creator system and external attributes as below. Require valid sizes and CRC-32 of each uncompressed payload. Reject encryption, ZIP64, self-extracting prefixes and split archives.                                                                                                                                                    |

**Tar details.** Use POSIX octal header fields and unsigned header checksums.
Require complete headers, payloads, record padding, and the two zero end blocks.
Any remaining bytes must be whole zero blocks. Accept an empty tar represented
by the end blocks. Decode effective names and link targets as UTF-8, independent
of locale. Support pax `x` records overriding `path`, `linkpath` and `size` for
the immediately following real member. `size` is the unsigned decimal payload
length. Parse each length-framed pax record as specified by POSIX. Accept
`mtime`, `atime`, `ctime`, `uid`, `gid`, `uname`, `gname` and `comment` as
ignored metadata, including in `g` global headers. Reject other attributes and
global overrides of paths or sizes as `unsupported-content`. Reject duplicate
keys within a pax header, an empty override, a dangling or stacked per-member
header, and `linkpath` on any entry other than a symlink. The v1 subset excludes
GNU long-name records and base-256 numeric extensions. Never silently skip an
unrecognized member that could affect the tree.

**ZIP details.** Use the central filename bytes. With general-purpose bit 11
set, decode strict UTF-8. With it clear, require ASCII. Ignore extra fields for
timestamps and ownership (IDs `000a`, `5455` and `7875` in hex) and
archive/member comments. Apply this extra-field allowlist to both local and
central headers. Reject other extra-field IDs, including alternative Unicode
path overrides. Accept only general-purpose bits 1–2 for deflate options, 3 for
the data descriptor and 11 for UTF-8. Match central and local names, methods and
flags, validate non-overlapping member extents, and reject orphan local entries,
contradictory sizes or duplicate/ambiguous directory structures. When bit 3 is
set, use the central sizes to delimit payload and validate the following ZIP32
data descriptor, with or without its optional signature. Each deferred local
size or CRC must be zero or equal to its central value. Otherwise local sizes
and CRC must agree with the central entry. Consume the declared ZIP structure,
including the end record's comment, through EOF.

For creator system 3 (Unix), use the high 16 bits of the central external
attributes as `st_mode`. File type `0100000` is regular, `0040000` directory,
`0120000` symlink (octal). Any other nonzero type is unsupported. If Unix file
type is zero or the creator is another system, classify as directory when the
name ends with `/` or the low DOS directory bit `0x10` is set, otherwise
regular. Reject a regular/symlink Unix type conflicting with a directory
indicator. For a non-Unix creator, reject a DOS volume-label member (attribute
bit 0x08). Only Unix regular files inherit `0111` executable bits. Other regular
files are non-executable. Directory payloads must be empty. A ZIP symlink's
entire uncompressed payload is its UTF-8 target. Validate target text as in §3.

#### `git`

The base Git transport accepts an absolute HTTP(S) repository URI using the
network URI syntax above. Other documented Git transports may be supported under
the same destination policy. A Git source with `erisfs.use false` gives
`unsupported-option`. Supply at most one of `commit`, `tag` and `branch`. A
`tag` or `branch` value names the exact short name within `refs/tags/` or
`refs/heads/`. Validate the fully prefixed ref under the `git check-ref-format`
full-ref rules, without revision expressions or normalization.

With an explicit tag, fetch that tag directly, recursively peel annotated tag
objects to their commit, encode its selected tree and return `ok` or `fail`.
This applies to lightweight, annotated, versioned and non-versioned tags. A
caller-supplied tag needs no branch selection, tag listing or SemVer parsing.
Its spelling is literal even with `subdir`. Verify each tag object’s Git object
ID while peeling. A missing ref or object gives `not-found`. A cycle or explicit
tag pointing to a non-commit gives `invalid-content`. With only a branch, or
with no revision field, use §5 to propose a revision.

`commit` is exactly `sha1:` followed by 40 lowercase hex digits or `sha256:`
followed by 64. Require the matching Git object format and a commit object at
that exact ID. Recompute or verify object IDs under Git's object hashing rules,
including their type/length headers, for the commit and the trees and blobs
used. Use `invalid-source` for an abbreviated ID or revision expression and
`invalid-content` if a pinned object exists but is not a commit. A full commit
remains the selector if remote HEAD, a tag or a branch moves.

`subdir` optionally selects a tree under the commit's root. Its value is a
nonempty slash-separated path of valid components from §3 with no
leading/trailing slash. Walk exact tree names. A missing component gives
`not-found` and a non-tree component gives `invalid-content`. Emit paths
relative to the selected tree. Use the following mapping for each committed tree
entry.

| Git entry mode (octal)                                   | Result                                                      |
| -------------------------------------------------------- | ----------------------------------------------------------- |
| `040000`                                                 | Traverse the child tree. An empty tree contributes no leaf. |
| `100644` / `100755`                                      | Regular / executable file with exact blob bytes             |
| `120000`                                                 | Symlink with its blob bytes as the target                   |
| `160000` (gitlink)                                       | `unsupported-content` in the selected tree                  |
| Other mode or an entry pointing to the wrong object type | `invalid-content`                                           |

Do not apply attributes, line-ending conversion, smudge filters, LFS downloads,
replace refs, grafts, export substitutions, checkout hooks or user-configured
URL rewrites. An LFS pointer is committed blob content. Only entries in the
selected tree contribute to the result. Administrative `.git` data, clone depth,
tag signatures, network fetch order and the host executable-bit policy add no
fields. A fetcher may acquire objects by any allowed means if it verifies the
same named commit and computes the specified tree.

```text
type git
url https://example.org/team/project.git
tag releases/v1
subdir docs
```

This fetches `docs` at the exact tag `refs/tags/releases/v1` in one invocation.
A later unlocked call observes the new target if that tag moves.

## 5. Resolution and refinement

A *fetchable* description supplies every selection its source type requires. It
may name mutable content. Validate the entire description first. For a fetchable
source, fetch and publish the result, then return `ok` or `fail`. Missing
mandatory input such as `url` is `invalid-source`. Refinement is reserved for a
valid source with an unresolved choice. Standard `url` and `archive`
descriptions never produce suggestions. Among the standard types only Git
without `tag` or `commit` returns `refine`.

### Selecting a Git revision

1. **Choose the branch.** Use the explicit `branch`. If absent, use the
   repository’s advertised default branch (symbolic `HEAD` into `refs/heads/`).
   A missing or unborn branch gives `not-found`. If no default branch name can
   be established, return `fail` with `code no-default-branch`. The caller can
   supply `branch` explicitly. Never guess `main` or `master`.
1. **Capture one observation.** Record the selected branch name, its tip commit
   ID, and the advertised tag names and target IDs from the same ref
   advertisement or local ref snapshot. Use those object IDs throughout
   enumeration. An annotated tag can point through other tags. Verify and peel
   to its final object. Include a tag exactly when that object is a commit
   reachable from the captured tip, following **all** parents, including the tip
   itself. Ignore tags ending at trees/blobs and commits outside this history.
   Never use a shallow-history boundary as proof of absence. Obtain sufficient
   history or fail. A missing required object, corrupt graph or resource limit
   is an error. Never publish a partial list as complete.
1. **Classify every reachable tag.** For version matching, the prefix is empty
   at the root, otherwise the exact UTF-8 `subdir` bytes followed by `/`. Remove
   the prefix once if it matches at byte zero. A missing prefix makes the tag
   unversioned for this request. Class A is a remainder matching a full SemVer
   2.0.0 version. Class B is a remainder starting with one lowercase `v`
   followed by a full SemVer. Class C contains **every other reachable tag**,
   including tags for the root or another module. Apply this prefix only during
   version comparison. Always display the full tag name. This takes Go’s
   directory-prefix convention literally for the given directory. It does not
   inspect `go.mod` or infer major-version suffixes.
1. **Order the suggestions.** Place class A first, class B next, class C next,
   then the branch tip. Within A and B sort by decreasing SemVer precedence,
   including prereleases. Compare numeric identifiers without machine-integer
   overflow. Build metadata has no effect on precedence. A strict version
   includes major, minor and patch. For example, `v1.2`, `1.02.0` and `V1.2.3`
   all fall into class C at the repository root. For equal-precedence versions
   and for all of class C, sort by decreasing **peeled commit’s committer
   timestamp** (integer UTC seconds), then by increasing shortest parent-edge
   distance from the captured branch tip through any parents, then by ascending
   full short tag name as unsigned bytes. This definition of “latest” works for
   both lightweight and annotated tags and ignores the date when a tag
   annotation was created. Branch tip means the captured tip itself, regardless
   of timestamps.
1. **Return the first choice as default and all remaining choices as
   alternatives.** Tags sharing a commit remain distinct choices. Include the
   branch tip exactly once even when it coincides with a tag. If there are no
   reachable commit tags, the branch tip is the only choice. Each proposed
   source removes `branch` if present and adds the captured full `commit`.
   Preserve all other input records. The choice label records the tag or branch
   name. Selecting a tag suggestion fetches the commit observed during
   enumeration even if the tag subsequently moves. A tag supplied directly in
   the original request follows the direct-fetch rule in §4.

For example, with `subdir pkg/tool`, tags `pkg/tool/1.2.0`, `pkg/tool/v8.0.0`
and `v99.0.0` fall into A, B and C respectively. The default is
`pkg/tool/1.2.0`. All three appear if reachable. The subdirectory need not have
existed at every historical tag. Fetching such a choice gives `not-found` if its
selected tree does not exist.

### Consuming suggestions

A `refine` response contains a default and zero or more labeled alternatives in
preference order. It contains a shared set of source records followed by each
choice’s own records (§6). Combining the shared records with one choice produces
a complete source ready for stdin or POST. This lets even large tag lists carry
the URL, subdirectory and encoding settings just once. Other source types may
use the same framing for their own selection criteria or return a single
default.

A caller validates the entire frame before using any choice. Every assembled
proposal must be directly fetchable under the type schema and have a different
source ID from the original. Check the label grammar and uniqueness, the §1
limits and schema for each combined source, and the type’s permitted edits
against the original. In standard Git, the shared records must equal the
original records with `branch` removed. Each choice adds exactly one valid
`commit`. All commits must use the same object format. Tag and tip labels
identify separate choices even if they yield identical source IDs. A caller can
check these structural constraints locally. Verifying completeness, reachability
or tag ordering independently needs the same repository observation.

An unattended caller may select the default under its first-observation policy.
An interactive caller may show all choices. Resubmit only the chosen description
to the same handler. Standard Git has at most one refinement round. Reject `ok`
on unresolved Git, `refine` on a directly fetchable standard source, and any
proposal that changes a locator, ordered mirror, scope or encoding option
outside the type’s declared edit rules. For a third-party type, a client needs
that type’s schema to auto-accept a suggestion. A proposal itself never creates
a lock.

## 6. Wire protocol

One invocation receives one source document and returns one response. The
request ends at stdin EOF or at the end of the HTTP message. Responses use
strict UTF-8, one ASCII space at each indicated separator and **LF only**,
including the final LF. No BOM, comments, blank lines, extra records or log text
are allowed. Literals below are case-sensitive. Brackets denote optional content
and `*` denotes zero or more occurrences.

```text
ok       = "eris-fetch/1 ok" LF "kind " kind LF "cap " urn LF
fail     = "eris-fetch/1 fail" LF "code " error-code LF [ "key " key LF ]
refine   = "eris-fetch/1 refine" LF shared default *alternative "@end" LF
shared   = *record
default  = "@default " label-kind SP label LF *record
alternative = "@alt " label-kind SP label LF *record
kind     = "blob" / "erisfs"
```

A `record` is one §2 canonical key/value line, including its LF. The shared
block and each choice block are independently in canonical order. A marker
begins with `@` at column one, which cannot begin a key. An `@` inside a value
is ordinary data. The shared block ends at `@default`. Each choice ends at the
next marker. Require exactly one default in first position, one final `@end`,
and immediate EOF after its LF. Reject unknown markers, a missing terminator or
duplicate (label-kind, decoded label) pairs. Every choice inherits the shared
records. The two blocks must have disjoint key sets. Their concatenation is
valid source text, which a caller can canonicalize to compute its ID. No fields
are inherited from the original request implicitly.

Only marker lines use the following label encoding. `label-kind` matches
`[a-z][a-z0-9-]{0,31}`. The nonempty `label` is a byte string encoded as ASCII
for display metadata. Write ASCII letters, digits, `-._~/` literally. Encode
every other byte as `%HH` using uppercase hex. Reject raw reserved bytes,
lowercase escapes, and escaped literal-set bytes. This also represents Git ref
names whose bytes are not UTF-8. Git uses `tag` with the full short tag name and
`tip` with the chosen short branch name. Require exactly one `tip`, last in
preference order (the default when it is the only choice). For an explicit
branch verify its name. Other types specify their own label kinds. Labels do not
enter source IDs.

For example, an unresolved request consisting of `type git`,
`url https://example.org/project.git` and `branch main` might receive this
complete response. Both reachable tags point to the same commit. The IDs are
illustrative.

```text
eris-fetch/1 refine
type git
url https://example.org/project.git
@default tag 1.2.0
commit sha1:1111111111111111111111111111111111111111
@alt tag v2.0.0
commit sha1:1111111111111111111111111111111111111111
@alt tip main
commit sha1:2222222222222222222222222222222222222222
@end
```

To select the tip, concatenate the two shared records with the record following
`@alt tip main`. The next invocation sends precisely these source bytes and
returns `ok` or `fail`.

```text
type git
url https://example.org/project.git
commit sha1:2222222222222222222222222222222222222222
```

Each assembled source must satisfy §1, including its byte ceilings. Count the
shared bytes once per assembled source for its limit, and once per response for
the transport limit. The entire response, including all markers and choices, may
contain up to 67,108,864 bytes. A marker line excluding LF may contain up to
262,144 bytes. Count bytes with checked arithmetic. If the complete enumeration
cannot fit the wire or local work budget, send a `fail` frame with `code limit`
in place of the suggestion list. Never silently drop tags or accept a truncated
prefix. Spooling the response or replaying choices from a captured snapshot
permits bounded memory. A receiver can retain shared records once and check each
choice incrementally.

`urn` is exactly lowercase `urn:eris:` followed by the 106 uppercase unpadded
RFC 4648 Base32 characters for a 66-byte ERIS 1.0.0 capability. Decode and
re-encode to check unused bits and exact spelling. Validate the ERIS block-size
byte (`0a` or `0f` hex). The following bytes are a one-byte root level, a
32-byte reference and a 32-byte key. The caller checks `kind` against the
effective mode. A valid URN by itself does not prove the handler encoded the
requested content. A trusted prior capability can be compared locally. Receivers
MUST consume the full response and verify transport completion before using it.

`error-code` matches `[a-z][a-z0-9-]{0,31}`. `key`, if present, is an offending
key in the §1 grammar, never a value. For multiple independent violations a
fetcher may report any applicable one. Clients treat an unfamiliar well-formed
code as generic failure. Invalid output is a local `protocol-error` and has no
usable result.

| Standard failure code              | When to use it                                                                                                                         |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `invalid-source`                   | Text, schema or field value is malformed, a required field is absent, a scalar repeats, or known fields conflict                       |
| `unsupported-type` / `unknown-key` | No implementation for the type / key outside that type's schema                                                                        |
| `unsupported-scheme`               | No retrieval profile for the selected source URI scheme                                                                                |
| `no-default-branch`                | Git discovery could not establish a default branch name and the caller supplied none                                                   |
| `unsupported-option`               | Unsupported checksum algorithm, output mode, version, compression, layout or format choice, or explicitly inapplicable encoding option |
| `not-found` / `denied`             | Resource, ref or selected path missing / retrieval or publication refused by authorization or destination policy                       |
| `unavailable`                      | Input read or acquisition failed, e.g. incomplete HTTP body, redirect failure, network error or unsupported HTTP content coding        |
| `integrity`                        | Bytes disagree with a supplied checksum or a Git object fails its ID check                                                             |
| `invalid-content`                  | Corrupt container, bad archive checksum, ambiguous path, malformed tree or content that violates the declared format                   |
| `unsupported-content`              | Recognized but excluded archive feature, member kind or Git gitlink                                                                    |
| `limit` / `cancelled`              | Size, depth, work or deadline budget exceeded / request cancelled while a reply is still possible                                      |
| `store` / `internal`               | Destination failed to acknowledge blocks / unexpected handler error                                                                    |

For example, a Git fetcher rejecting blob output can return the following bytes
and process exit code 1.

```text
eris-fetch/1 fail
code unsupported-option
key erisfs.use
```

### Executable transport

A command is a nonempty vector of strings. Execute it directly, without a shell.
Write the source bytes to stdin and close it. Read stdout as the response.
Stderr is an independent arbitrary diagnostic stream, possibly empty or
non-UTF-8. Drain stdout and stderr while sending input to avoid pipe deadlock.
Use binary pipes on Windows. Treat a failure to deliver the complete request, an
unexpected exit code, a signal, a timeout, invalid output, or an exit/body
disagreement as a local invocation or protocol error.

| Completed outcome                                 | Required process exit code | Result body |
| ------------------------------------------------- | -------------------------: | ----------- |
| Stored                                            |                          0 | `ok`        |
| Rejected or failed                                |                          1 | `fail`      |
| Default suggestion and any alternatives available |                          2 | `refine`    |

The fetcher reads to EOF for a valid operation. If it detects an input error
early, it SHOULD drain the bounded request or arrange orderly shutdown so its
`fail` frame reaches the caller. Do not send source values to stderr by default.
Capabilities, private locations and convergence secrets may appear in inputs or
outputs.

### HTTP transport

POST the source as the entire body to the exact configured endpoint, with no
suffix, form wrapper, or query argument added. Both sides send
`Content-Type: application/octet-stream`. The client can also send
`Accept: application/octet-stream`. Use no HTTP content coding in either
direction. Compare the media type case-insensitively and require no parameters.
Use HTTP message length/framing, independent of chunk boundaries. Requests and
protocol responses SHOULD send `Cache-Control: no-store`.

| HTTP outcome                                                            | Meaning to the caller                                                                                                                 |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `200`, correct media type, complete valid body                          | The same `ok`, `fail` or `refine` result as the executable. Even a source syntax error is a well-formed `fail` here.                  |
| `401` / `403`, `405`, `413`, `415`                                      | Endpoint authentication/authorization failure, wrong method (send `Allow: POST`), request too large, or unsupported media type/coding |
| `429`, `5xx`, connection failure                                        | Service or transport failure. A retry is subject to caller policy.                                                                    |
| Redirect, any other status, malformed/truncated 200 or unknown envelope | No usable protocol result. Do not follow endpoint redirects or interpret an intermediary's error page as a fetch response.            |

The client MUST reject a response with a content coding. The endpoint may reject
at the HTTP layer before accepting a request. Once it accepts the invocation, it
uses HTTP 200 for each of the three protocol outcomes, including an origin's 404
or a storage failure. The origin's 404 is a `fail` with `not-found`,
distinguishable from a 404 on the fetcher endpoint. A retry of the same commit
selection or the same checksum-verified download produces the same capability
for the same encoding options on success. A lost reply may leave published
blocks. Retrying an unlocked URL, archive or explicit tag can observe a new
result. A lock hit continues to select its recorded capability. Any process or
remote endpoint that sees the source, capability or credentials must be trusted
for that data.

## 7. Finding a fetcher

Read routing configuration before contacting a fetcher. Configuration is locally
trusted executable policy. An application may take an explicit ordered list of
config files in its API. If supplied, that list completely replaces discovery,
including when empty. Otherwise use `ERIS_FETCH` if set. Only when it is
**unset** use the platform defaults below.

### File list

`ERIS_FETCH` is an ordered, semicolon-separated sequence of **absolute native
file paths** on every platform. Read the environment value after any shell
processing. These rules support drive letters, UNC paths and POSIX paths.

- An unquoted element extends to the next `;` or end and cannot contain `"`.
  Empty unquoted elements are skipped. A set but empty variable therefore
  selects no configuration.
- An element may instead begin with `"`. Read to its closing quote, interpreting
  `""` inside the element as one literal quotation mark. The next byte must be
  `;` or end. Reject an empty quoted element or unmatched quote. Backslashes are
  literal everywhere. No trimming, tilde, environment or glob expansion takes
  place.
- Validate the entire list before opening any file. Reject a relative path or
  malformed element as a configuration error. POSIX paths start at `/`. On
  Windows accept drive-absolute and fully qualified UNC share paths only. Reject
  drive-relative paths, bare drives, device namespaces and root-relative paths.
  No HTTP endpoints or Locks role prefixes occur in this list. On POSIX, parse
  the ASCII delimiters from the native environment bytes, preserving all other
  path bytes. On Windows parse the native Unicode environment using the same
  ASCII characters as delimiters. Configuration **contents** use UTF-8.

These examples show environment values after shell quoting.

```text
"/srv/teams;a/fetch.conf";/etc/eris/fetch.conf
C:\Users\Ada\fetch.conf;\\fileserver\config\eris\fetch.conf
```

| Platform                              | Default files in descending priority                                                                                                                                                                                                        |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Linux, BSD and other Unix/XDG systems | `$XDG_CONFIG_HOME/eris/fetch.conf` (fallback `$HOME/.config/eris/fetch.conf`), then `eris/fetch.conf` under each absolute `$XDG_CONFIG_DIRS` directory in listed order (fallback `/etc/xdg` if unset or empty), then `/etc/eris/fetch.conf` |
| macOS                                 | `~/Library/Application Support/ERIS/fetch.conf`, then `/Library/Application Support/ERIS/fetch.conf`                                                                                                                                        |
| Windows                               | `FOLDERID_RoamingAppData\ERIS\fetch.conf`, then `FOLDERID_ProgramData\ERIS\fetch.conf`                                                                                                                                                      |

Use native known-folder APIs on Windows. The tilde in the macOS table denotes
the resolved user home. Use an absolute base or omit that entry. Treat empty or
relative `XDG_CONFIG_HOME` as unset. Split `XDG_CONFIG_DIRS` on colons as
defined by XDG, omitting relative and empty components. A caller may include a
trusted project file first in an explicit file list. Automatic discovery starts
at the user level so merely opening a checkout cannot give it authority to
select a program.

### Routing records

Config files use §1 lexical rules and byte/line ceilings. They have no `type`
record or source schema. The key is exactly `TYPE.exec` or `TYPE.http`, where
`TYPE` matches the source-type identifier grammar. A file can contain routes for
many types. Each type selects exactly one transport.

```text
# Command vector ["eris-fetch-local", "--fetch"] for url
url.exec eris-fetch-local
url.exec --fetch

# Command vector with an argument containing spaces
git.exec /opt/eris tools/git-fetch
git.exec --store
git.exec /srv/shared ERIS blocks

archive.http https://fetch.example.org/v1
```

Repeated `TYPE.exec` lines supply executable name then arguments in order. An
empty later argument is valid. One `TYPE.http` line supplies a single endpoint
URL satisfying the §4 HTTP URI grammar. Both transports for one type, repeated
endpoints, empty executable or unknown routing key invalidate the entire file.
An empty file supplies no routes. A bare command name is looked up in the
caller's trusted executable search path. An absolute executable path is also
valid. Reject a relative executable containing native directory separators, the
special names `.` and `..`, and Windows drive-relative names such as
`C:tool.exe`. A POSIX launcher passes config argument values as their exact
UTF-8 bytes. Skip empty or relative search directories. On Windows, launch a
native executable, using the exact absolute path or resolving a bare name in
search-path order (append `.exe` if it has no extension). Preserve the
UTF-8-to-Unicode argument vector, including empty strings, spaces, quotes and
trailing backslashes, using the Windows C-runtime argument convention for
arguments after `argv[0]`. Never invoke a shell or command-script association
implicitly.

Search in order. A missing, unreadable or partially read file contributes no
routes. A malformed file contributes no routes and SHOULD produce a diagnostic.
Parse and validate the **whole file** before using a route. If the file has no
matching type, continue. Invoke the first matching route and stop discovery. A
handler rejecting an option, refusing access, returning `unsupported-type`, or
failing to start is a result/error for that route. Do not retry the description
on a lower-priority route. If no file supplies a matching route, report
caller-local `no-fetcher`. Config syntax, lookup and launch failures are caller
errors, distinct from fetcher `fail` frames.

Use authenticated HTTPS for a remote fetcher on an untrusted network. Permit
plain HTTP for a fetcher only on an explicitly trusted transport. A public
service must bound source bytes, downloaded and expanded data, object counts,
nesting, runtime and temporary storage. Apply outbound destination policy to
every origin request and redirect, including the resolved network address, and
keep credentials scoped to the intended origin. Operate on inert archive entries
and Git objects. Execute only the configured fetcher. Privileged callers must
use config files, executable search paths and environment overrides trusted at
their privilege level. These policies can deny an invocation but must never
substitute a different successful encoding.

## 8. Conformance vectors

These vectors use the default secret. Example URIs and Git refs can be served by
fixtures. They require no public network service.

### Canonical source IDs

The notation below uses `\n`, `\r` and `\t` solely to display bytes in test
cases. The source language has no escape syntax. For the opening example the
canonical text is `type url\nurl https://example.org/hello\n`. Its raw-key
prefix in hex is `657269732d66657463682f736f757263652f763100`.

| Input or change                                                                                      | Expected source ID or outcome                                                |
| ---------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| Opening request                                                                                      | `7BSALWD5ZKAQCPOZAD3UJZQ2W3G2ITP3BLBIV57CJXBHXXZYGHPQ:blake2`                |
| `# note\r\nurl https://example.org/hello\n\rtype url`, no final terminator                           | Same ID                                                                      |
| Opening request plus the example `checksum sha256:…` from §4 (full value)                            | `OKJKPCCUMKORAS4SGS47VBP7FXUL2ZI3PQLLM74GZ2F4TROMUHZA:blake2`                |
| Opening request plus `alt https://a.example/hello` then `alt https://b.example/hello`                | `5UZVXYW5TYYW5QBH35FNTI2L5U36S5D3WR3CD3NTRGCAQLZYXLGA:blake2`                |
| Same two mirrors in reverse order                                                                    | `VXQJBNKSSWY765UPRBL6YUMJR3YDDULPPXRVGPOSFZFE42XABFJA:blake2`                |
| `name  A#b `                                                                                         | Parsed name has bytes `20 41 23 62 20`. Canonicalization preserves all five. |
| `eris.secret` and `eris.secret ` in separate otherwise identical descriptions                        | Both produce the same empty field. Omitting the record gives a different ID. |
| `type\turl`, repeated `type`, BOM, NUL in a comment, `a..b`, or leaves `a` and `a.b` in one document | Reject                                                                       |
| `erisfs.use True` or `Type url` without `type`                                                       | Reject                                                                       |

### ERIS-FS bytes and capabilities

The following are independent root-index fixtures. Hex is the complete CBOR
payload before ERIS padding. For the middle row, replace `B` with the **66
decoded bytes** of the opening example's blob capability. The twelve file bytes
have no final newline.

| Tree                                           | Index bytes in hex                                          | Expected root capability                                                                                              |
| ---------------------------------------------- | ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Empty                                          | `da657269748202a0`                                          | `urn:eris:BIAMBMAQBTL6WWRPPQRYWPQFOPBX27WPOWGLPWNYCO5X2FRMHHWRYAD4K3ORFSM7JNOMEJWDIBEG5VAQ6ULESJB6NB7U7Z26GYQXZRYYNA` |
| Regular file `hello` containing `Hello world!` | `da657269748202a1816568656c6c6f8300d901145842` + `B` + `0c` | `urn:eris:BIANO4IXQY4XU6EWRTRPBF6SMPKFQ5HUSYMIL2V7WNTBDS5WK7E4E32FKMDUIODQOEM2QUYS37TMRGZ6GCLKOR36VQQMGVU4LFVSR3CUJU` |
| Link `l` to `/a//b/`                           | `da657269748202a181616c82028560616160616260`                | `urn:eris:BIAPFENWSCHVP33WLV5C52NICNFOBGU4HCE6QWN5DZW3B7AKMZ5XZ5R5NWT46WK5BGG4LDYKZKVRVWB7LQBGUJ4LARAXMJDJM7L72DPGRI` |

A named `url`, an archive with only `pkg/hello` after stripping `pkg`, and a Git
root with just mode `100644` at `hello` must yield the middle capability when
the file bytes agree. Changing only the file to executable changes the entry's
first integer from 0 to 1 and therefore the root capability. The file capability
stays the same. Uncompressed files of 16,383, 16,384 and 16,385 bytes must use
block-size bytes `0a`, `0f` and `0f` respectively. Apply that boundary
independently to the complete index length. For map keys `["zzzz"]` and
`["a","b"]` the one-component array sorts first despite its longer encoded key.

**Automatic compression vectors.** With `auto`, one regular file named `file`
containing 1,024 zero bytes has the 44-byte stored stream below. Its length
field is hex `da4c5a4950190400`. At the zero secret, the resulting root is
`urn:eris:BIACZ5E57FY2IDGF7I34DWENBOVRH3B4SWPEWEO2D3XPPVZOR5MNHD4MKZ43NFF5KRRCCLMMC6RVZHGEZKO7ACCP55TH2QIE5WVUWIJ53M`.
For 1,023 zero bytes, the file stays uncompressed and its index length is the
untagged integer 1,023.
[goeris CC0 regression vectors, zeros-1024](https://github.com/asciimoth/goeris/blob/983b7c41be81cf34a5305e13c1257078ca7dd706/fs/testdata/lzip-conformance-v1.json)

```text
4c5a4950011700006ffdffffa3b7800c82dbffff9ff000002eafb5ef00040000000000002c00000000000000
```

For the same path and zero secret, use the 1,024-byte `xorshift32` input from
the auto policy vectors. Its probe size is 1,032 bytes. Store the original bytes
and encode the untagged length as hex `190400`. The resulting root is
`urn:eris:BIAOWCYFF3SO3ZVEXC7WGJIL5RWP4LWFMXKK6D2NIUT75LKYCOJCGDCTP2MWYXAUNJG5CPAZ2A5ZKQR4WFH5H6KYHSUUECFBWUJC7C2PIM`.
[Input recipe and probe values](eris-fs-auto-compression.md#conformance-vectors)

### Interoperability and failure cases

| Setup                                                                                                                                     | Required observable behavior                                                                                                                                |
| ----------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Opening URL, no checksum, origin serves the fixture                                                                                       | One request yields `ok blob` with the opening capability. Lock it under the opening request ID. A later trusted hit needs no origin request.                |
| FTP binary, SFTP or a local file supplies the same fixture                                                                                | Each enabled scheme yields that same blob capability. An unsupported primary followed by a working mirror still succeeds.                                   |
| Publisher supplies the §4 SHA-256, primary differs and second mirror matches                                                              | Check each downloaded candidate. Use the matching bytes. If none match and any candidate mismatched, return `integrity`.                                    |
| Repack an archive with new timestamps and identical member metadata and bytes                                                             | Without a container checksum the new download yields the same tree capability. A stale supplied container checksum gives `integrity`.                       |
| Standalone gzip containing `Hello world!`, `erisfs.use false`, `format auto`                                                              | Direct `ok blob` with the opening capability. A bad CRC or truncated second gzip member gives `invalid-content`.                                            |
| Gzip-wrapped tar containing two files, blob mode                                                                                          | `auto` yields the full decompressed tar stream. Explicit `tar.gz` gives `invalid-content`. With one file, explicit `tar.gz` returns only that file’s bytes. |
| Explicit lightweight or annotated tag, with no branch                                                                                     | Fetch its observed commit tree directly. A later unlocked fetch may observe a moved tag. A locked lookup keeps its capability.                              |
| Only `branch stable`, with an unrelated higher tag on the default branch                                                                  | Enumerate tags reachable from `stable`. Exclude commits solely on the other branch. Include tags on merged side branches.                                   |
| `subdir pkg/tool` and reachable `pkg/tool/1.10.0`, `pkg/tool/1.9.0`, `pkg/tool/v8.0.0`, `v99.0.0`                                         | Choose `pkg/tool/1.10.0`. Return the other three tags and the tip. An off-branch `pkg/tool/9.0.0` is absent.                                                |
| No class A, reachable `v1.10.0` and `v1.10.0-rc.10`                                                                                       | Default to `v1.10.0`. For equal precedence, e.g. build metadata variants, apply the documented timestamp, distance and name tie-breakers.                   |
| No SemVer match, two informal tags, older target has a newly created annotation                                                           | Choose the more recent target commit by committer time. With no reachable tags emit one `@default tip` and no `@alt`.                                       |
| Two tag names and the tip alias one commit, then a tag moves                                                                              | Preserve all three labels. Resubmitting any captured choice still addresses the original commit.                                                            |
| Missing `@end`, altered common URL, repeated label, a choice overriding a common key, or a silently truncated list lacking its end marker | Reject the response and create no lock. A fetcher unable to enumerate all choices returns `limit` or the relevant acquisition error.                        |
| Git symlink on Windows with native symlink creation disabled, CRLF checkout enabled                                                       | Use the link blob as target and ordinary blob bytes verbatim. Obtain the same index as on POSIX.                                                            |
| Both `A` and `a`, or both NFC and NFD spellings, present                                                                                  | Keep distinct entries on every OS. Ignore host normalization.                                                                                               |
| Archive with `../x`, a file/link `a` and another member `a/b`, or duplicate ZIP file names                                                | Reject the archive independently of member order. No host-path traversal.                                                                                   |
| Multi-root archive with default stripping, then `strip-root false`                                                                        | First fails. Second includes all valid leaves. Empty directories are omitted.                                                                               |
| Unrecognized source key plus a mutable URL                                                                                                | Reject before retrieval. A standard URL producing `refine` is a protocol error. A Git proposal changing `url`, `subdir` or `eris.secret` is also invalid.   |
| Executable emits valid `ok` but exits 2, emits CRLF, appends a log line, or HTTP returns that body with 503                               | Reject the outcome. No lock is installed.                                                                                                                   |
| First config unreadable, next maps only `git`, third maps `archive`                                                                       | An archive request selects the third config. A failing selected handler never causes another route to run.                                                  |
| `ERIS_FETCH` unset, empty, malformed, or containing a quoted semicolon in a path                                                          | Respectively use defaults, select no config, report configuration error, or open the one literal path element.                                              |
| Two first resolutions of the same floating ID produce different capabilities                                                              | Atomic creation records one binding. The competing result is reported as a conflict.                                                                        |

### Adding a source type

A new type defines its identifier, complete key schema, defaults, permitted
result modes, conditions for direct fetching, and the byte or virtual-tree
construction. A type supporting suggestions also defines choice enumeration,
default order and tie-breakers, label kinds, the shared/choice record split, and
permitted edits. A type that always fetches directly needs no suggestion
machinery. Publish valid and invalid fixtures and an expected capability. Use a
distinct name such as `x-vendor-name` for private experiments. Implementations
must reject unrecognized keys on every supported type. A generic router needs
only the common text parser and `type` to locate a handler. A client performing
unattended refinement or lock validation needs the type's schema. An
incompatible revision to existing identity, default encoding or source semantics
requires a new version or type. Keep v1's source-ID prefix, result header and
configured v1 routes specific to this contract.

## 9. References

- ERIS 1.0.0 defines the convergence secret, encoding, block sizes and
  binary/text read capabilities. Its positive test vector 00 is the blob in the
  opening exchange. [ERIS project](https://eris.codeberg.page/) ·
  [ERIS encoding specification](https://eris.codeberg.page/spec/)
- The ERIS-FS extension defines the version 2 index and entries. The goeris
  reproducibility procedure supplies the flat, uncompressed profile and the
  exact 16 KiB tie rule adopted in §3.
  [ERIS-FS wire format](https://eris.codeberg.page/eer/eris-fs.xml) ·
  [goeris encoding and reproducibility, revision 983b7c4](https://github.com/asciimoth/goeris/blob/983b7c41be81cf34a5305e13c1257078ca7dd706/specs/eris-fs-encoding.md)
  ·
  [CBOR deterministic encoding, RFC 8949 §4.2.1](https://datatracker.ietf.org/doc/html/rfc8949#section-4.2.1)
- Nixpkgs supplies the precedent for fetching file bytes, unpacking with
  optional root stripping and selecting Git revisions. This draft defines the
  optional download checksum, virtual tree and result capability.
  [Nixpkgs fetchers, including fetchurl, fetchzip and fetchgit](https://nixos.org/manual/nixpkgs/stable/#chap-pkgs-fetchers)
- Version and branch selection. The subdirectory prefix follows the Go module
  tag convention. This draft defines its own priority for unprefixed and
  `v`-prefixed versions, recency fallback and branch-tip choice.
  [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html) ·
  [Go module versions and commits](https://go.dev/ref/mod#vcs-version) ·
  [Git reachability filters](https://git-scm.com/docs/git-for-each-ref)
- Source URI profiles. [curl URL syntax](https://curl.se/docs/url-syntax.html) ·
  [libcurl protocol selection](https://curl.se/libcurl/c/CURLOPT_PROTOCOLS_STR.html)
  · [File URIs, RFC 8089](https://datatracker.ietf.org/doc/html/rfc8089) ·
  [FTP URLs, RFC 1738 §3.2](https://datatracker.ietf.org/doc/html/rfc1738#section-3.2)
- Locks v1 supplies the prehashed identifier spelling, first-observation
  operation and ordered path-list conventions. Config paths here hold fetcher
  routes.
  [Locks v1 §§1, 5–6, revision 9b29c51](https://github.com/asciimoth/locks/blob/9b29c51fd35da3775bbbfcfa98785ee8bbcb7429/spec.md)
  ·
  [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir/latest/)
  ·
  [Windows known folders](https://learn.microsoft.com/en-us/windows/win32/shell/knownfolderid)
  ·
  [Windows C argument parsing](https://learn.microsoft.com/en-us/cpp/c-language/parsing-c-command-line-arguments)
- Text, hash and transport definitions.
  [UTF-8, RFC 3629](https://datatracker.ietf.org/doc/html/rfc3629) ·
  [BLAKE2, RFC 7693](https://datatracker.ietf.org/doc/html/rfc7693) ·
  [Base32, RFC 4648](https://datatracker.ietf.org/doc/html/rfc4648) ·
  [URI syntax, RFC 3986](https://datatracker.ietf.org/doc/html/rfc3986) ·
  [HTTP semantics, RFC 9110](https://datatracker.ietf.org/doc/html/rfc9110)
- Container and Git formats.
  [POSIX pax and ustar](https://man7.org/linux/man-pages/man1/pax.1p.html) ·
  [Gzip, RFC 1952](https://datatracker.ietf.org/doc/html/rfc1952) ·
  [PKWARE ZIP APPNOTE 6.3.10](https://pkware.cachefly.net/webdocs/casestudies/APPNOTE.TXT)
  · [Git ref-name rules](https://git-scm.com/docs/git-check-ref-format) ·
  [Git tree objects and modes](https://git-scm.com/docs/git-ls-tree)
