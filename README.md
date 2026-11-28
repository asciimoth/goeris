# Go ERIS

This repository provides a Go library for [ERIS 1.0.0](specs/eris-v1.0.0.html).
Local copies of the specification and ERIS extension documents are in
[specs](specs/README.md).

Install this library with `go get github.com/asciimoth/goeris`.

## Storage for tools

Use `github.com/asciimoth/goeris/auto` to open the stores for the current
environment. Pass `stores.Blocks`, `stores.Capabilities`, or `stores.Images` to
code that works with encrypted blocks, decoded objects, or ERIS-FS images.

```go
stores, err := auto.Open()
if err != nil {
    return err
}
defer stores.Close()

image, err := stores.Images.OpenFS(ctx, capability)
```

- Blocks use `volume.OpenAvailableChain` with the semicolon-separated
  `ERIS_BLOCK_PATH`. An empty value selects the platform's default volume paths.
- Capabilities use the configured block chain, then the decoded files under
  `ERIS_MNT` or the default mount roots.
- Images use the configured capability chain, then the expanded trees under
  `ERIS_FS_MNT`. If `ERIS_FS_MNT` is empty, the mount store uses `ERIS_MNT/fs`
  or the `fs` directories under the default mount roots.

The Unix mount roots are `/n/eris` and `/eris`, in that order. Windows uses
`eris` under the absolute `ProgramData` and `LOCALAPPDATA` paths, in that order.
Mount lookups require host support for literal capability URN names. The mounts
handle content verification and provide read access.

`Open` skips missing volume roots. For each selected platform default, it tries
to create the directory and its missing parents, even if the path came from
`ERIS_BLOCK_PATH`. If creation fails, it skips that volume. The block chain can
be empty.

The mount stores check their paths during lookups, so absent mounts do not
prevent setup. Call `Close` after all operations finish to release the volume
handles. The caller owns stores supplied through options.

Use `PrependBlocks`, `AppendBlocks`, or `ReplaceBlocks` to change the block
chain. The corresponding `Capabilities` and `Images` options change those
chains. For each chain, the prepended stores come first, the default or
replacement stores come next, and the appended stores come last. Use
`auto.Open(auto.ReplaceBlocks())` for mount access without opening any volumes.
Use an empty replacement to remove the default entries at any layer.

Fallback uses the library's combined stores. A stream write can try the next
backend while the input is unread. When opening a file of known size, the
default block backend checks the first byte (or EOF for an empty file). This
lets the capability mount supply a file missing from the volumes. A returned
reader or image builder stays with the store that opened it. Use `PrependImages`
to give a custom builder priority.

### Command-line storage flags

Run `goeris dump` to show the environment and the storage lookup order. Set
storage flags before or after the command name. For `goeris block` and
`goeris cap`, you can also set them between the command and the subcommand. Set
all flags before block hashes or capability URNs.

| Chain             | Prepend                  | Append                  | Replace                  |
| ----------------- | ------------------------ | ----------------------- | ------------------------ |
| Block volumes     | `--prepend-blocks`       | `--append-blocks`       | `--replace-blocks`       |
| Capability mounts | `--prepend-capabilities` | `--append-capabilities` | `--replace-capabilities` |
| ERIS-FS mounts    | `--prepend-fs`           | `--append-fs`           | `--replace-fs`           |

Each flag takes a semicolon-separated list of paths. Quote the list if it
contains semicolons or spaces. Explicit block volumes must be existing
directories or ERIS CBOR files. Capability paths name roots with decoded files.
ERIS-FS paths name roots with expanded trees, such as `/eris/fs`. The command
checks mounts on lookup.

Prepends come first, then the defaults or the list from the last replacement,
then the appends. Repeated prepends and appends keep their order. Use an empty
replacement to remove the defaults for that chain. A capability or ERIS-FS
replacement also removes the default backend that reads from the preceding
chain.

```sh
goeris dump --prepend-blocks '/var/eris;backup.cbor' --append-fs /backup/fs
goeris dump --replace-blocks= --replace-capabilities= --replace-fs=
```

### Read and write raw blocks

Use `goeris block put` to store exactly 1024 or 32768 bytes from stdin. The
command prints the unpadded Base32 block hash followed by a newline. Use
`goeris block get` with one or more hashes to write the raw blocks to stdout in
the specified order, with no separators. If a hash is invalid or a fetch fails,
stdout contains the blocks before that failure. The error goes to stderr and the
command exits with a nonzero status. With zero hashes, the command exits without
output.

For multiple hashes, progress shows how many blocks stdout has received out of
the requested total. It uses a dedicated Zig progress pipe or a stderr terminal.
Use `--batch-size N` on `block get` to set the maximum number of blocks per
request, up to the storage limit. Use `0` (the default) for automatic batching
of up to 64 blocks or `1` to request blocks one at a time. Directory and CBOR
volumes currently accept one block per request.

```sh
hash=$(goeris block put --replace-blocks /var/eris < block.bin)
goeris block get --replace-blocks /var/eris "$hash" > restored.bin
```

### Prefetch blocks and manage block options

Use `goeris block prefetch HASH [HASH...]` to send best-effort hints to the
block sources. The command returns without waiting for reads and produces no
output on success. A source can discard hints, so a successful exit gives no
information about block availability.

Use `goeris block check HASH [HASH...]` to read and verify each block and print
its stored options. Each stdout line has the form
`HASH high-priority=BOOL root=BOOL`, where each value is `true` or `false`. A
missing or corrupt block is an error.

Use `goeris block update HASH [HASH...]` to replace both flags for each block.
The command validates every hash before it writes any flags. Each stdout line
has the form `HASH changed=true` when the store changes the options. A line with
`HASH changed=false` means the block already has the requested flags, the block
is absent, or the store ignores the update. Check and update print results in
argument order and stop at the first error. If an update fails during storage,
earlier updates stay in effect.

Put, prefetch, and update accept `--high-priority` and `--root` after the
subcommand and before any hashes. Use `--high-priority` to request priority
handling and `--root` to mark an entry block. On prefetch, the flags apply to
each hint. A bare flag means `true`. Use `--high-priority=false` or
`--root=false` to pass `false` explicitly. Omitted flags default to `false`, so
an update with no flags clears both flags on a store that supports updates. A
repeated put keeps true flags on stores that retain options.

Directory and CBOR volumes ignore prefetch hints and block options. With these
volumes, a check reports both flags as `false` for an existing block, and an
update reports `changed=false`.

```sh
hash=$(goeris block put --root --high-priority < block.bin)
goeris block prefetch --high-priority "$hash"
goeris block check "$hash"
goeris block update --root --high-priority=false "$hash"
```

### Encode and decode content

Use `goeris cap encode` to read a byte stream from stdin, store its ERIS blocks,
and print one canonical capability URN. Pass that URN to `goeris cap decode` to
write the original bytes to stdout. Decode accepts one URN per invocation. It
uses the configured capability chain, which can read from block volumes or
mounts. If a read fails during decoding, stdout can contain part of the content.

```sh
urn=$(goeris cap encode --replace-blocks /var/eris < input.bin)
goeris cap decode --replace-blocks /var/eris "$urn" > restored.bin
```

Encode selects 1024-byte blocks for input shorter than 16 KiB and 32768-byte
blocks for input of 16 KiB or more. Use `--block-size 1024` or
`--block-size 32768` to select a size. Use `--secret-file PATH` to supply a
32-byte convergence secret. Encode uses the null secret by default. The URN and
stored blocks are sufficient to decode the content.

Encode marks the capability's root block by default. Use `--root=true` or
`--root=false` to pass that value for every block write. Encode, decode, and
prefetch accept `--high-priority` to request priority handling.

Use `goeris cap decode --limit N URN` to set a maximum decoded size in bytes.
Use `--size N` to supply a known decoded size. If you set both, the size must
fit within the limit. Decode and prefetch accept `--ttl N` to set the network
forwarding budget. Use the default TTL with `--size`. Directory and CBOR volumes
ignore block options and TTL. If every backend fails to open content, decode
reports the error from the last backend it tried.

Use `goeris cap prefetch URN [URN...]` to send prefetch requests in argument
order. The store can read internal nodes before it sends best-effort hints for
the content blocks. Use `goeris cap resolve-path URN [URN...]` to print the
absolute path to each decoded file on a mount, one per line.

## Packages

The `eris` package implements:

- ERIS padding and recommended block-size selection;
- BLAKE2b-256 references and ChaCha20 node encryption;
- read capability binary, URN, and CBOR encodings;
- deterministic encoding to an in-memory block set;
- streaming encoding from an `io.Reader` directly to a block store;
- verified decoding from an in-memory block set or an abstract block source,
  with sequential and random access; and
- combined block storage with ordered fallback and optional error reporting.

Use `eris.NewMountCapabilityStore(root).ResolvePath(ctx, capability)` to find
decoded content on a local mount that uses the
[ERIS FUSE path layout](specs/eris-go-fuse-layout.md). Set `root` to select one
mount. An empty root uses `ERIS_MNT` or the platform's default mount roots.

The method returns an absolute path to the regular file at
`<mount>/<full capability URN>`. Relative mount paths use the current working
directory. If all candidates are missing, the error matches
`eris.ErrMissingBlock`. For an ERIS-FS capability, the file contains the decoded
index. The mount verifies the content.

The `fetch` package reads source descriptions, response frames, and route
configuration for the [ERIS Fetch Protocol v1](specs/eris-fetchers-v1.md). Use
`fetch.ParseSource` and `Source.Validate` to check a request on the fetcher’s
machine. `Source.ID` returns the prehashed lock key. `fetch.LookupRoute` finds
the first matching route in the configured file order. Handlers can use
`Options.NewTree(images)` to build a flat ERIS-FS result with the selected
compression profile. The `fetch/url` package retrieves HTTP and HTTPS sources.
It writes blobs to `url.Config.Store`. Trees use the same store by default. Set
`url.Config.Images` to build trees in a separate image store.

The fetcher holds each download in memory before checksum verification and
encoding. Set `url.Config.TempStorage` to `fetch.TempDir(dir)` to use temporary
files, or supply a custom storage implementation. The fetcher releases the
temporary data after each candidate. Set `MaxBytes` to stop the fetch if a
candidate exceeds the byte limit. The HTTP client timeout applies to each
candidate, and a context deadline applies to the entire fetch. The caller
supplies the HTTP client and its destination policy. Set `url.Config.Progress`
to receive download updates, or use `url.NodeProgress` to display them on a Zig
progress node.

Go's HTTP client can reject percent-encoded ASCII host characters in source URLs
and redirect targets.

Set `url.Config.Locks` to a `fetch.LockDB` to reuse capabilities across
requests. Leave it `nil` to disable locks. The fetcher checks the exact source
hash first. Requests without a checksum can also use minified locks for the
primary URL and mirrors, in request order. A minified source contains one URL
and keeps its output settings, with defaults omitted. A lock hit returns the
stored capability. A minified hit also tries to record the capability under the
exact source hash. After a successful download and encoding, the fetcher tries
to record the exact source and the minified source for the URL that supplied the
data. Lock hits require no HTTP request or block access. Share a trusted
database with callers authorized to use its capabilities and able to read the
corresponding blocks. Concurrent misses can download independently and each
returns its own result. The first stored binding for each key stays in place.

The `volume` package implements [local block volumes](specs/eris-volume.md). It
stores each block at a fixed `AAA/BBB/CCC/<remaining hash>` path and also reads
flat block files. It verifies block size and hash on reads, publishes writes
through a temporary file and rename, and leaves unrelated files in place. Call
`volume.Open(path)` with an existing root directory, then close the volume after
use. Writers create only the nested directories. If the root path is a regular
file, reads find blocks in its ERIS CBOR contents. Writes to file roots return
`volume.ErrNotDirectory`. Each CBOR lookup validates the complete container
before returning a block. It reads in small parts and retains only the matching
block and the references needed to detect duplicate keys in the current map.

`volume.OpenChain(pathList)` combines a semicolon-separated list of volume paths
with `eris.CombinedBlockStore`. Reads and writes try entries from left to right
and stop at the first success. Applications can use
`volume.OpenChain(os.Getenv("ERIS_BLOCK_PATH"))`, for example with
`/var/eris;/usr/backup.cbor;/home/user/.cache/eris`. Prefer adding new paths at
the end to preserve the existing order. Close the chain after use. An empty
string uses `/eris/block` followed by `$XDG_CACHE_HOME/eris/block` (or
`$HOME/.cache/eris/block`) on Unix. Windows uses `%ProgramData%\eris\block`
followed by `%LOCALAPPDATA%\eris\block`. Both roots must already exist.
`volume.DefaultChainPaths()` returns these paths without opening them.

The `cbor` package implements the
[ERIS CBOR transport format](specs/eer/cbor/index.md):

- deterministic tagged, untagged, and self-described arrays;
- tagged and untagged CBOR sequences;
- definite-length and indefinite-length decoding; and
- typed blocks, references, verified block maps, and read capabilities.

The `fs` package implements [ERIS-FS version 2](specs/eer/eris-fs/index.md),
with version 1 read support:

- canonical deterministic CBOR index encoding and strict decoding;
- low-level index codecs;
- regular, executable, symbolic-link, and nested-image entries with file
  lengths;
- host-independent builders that accept `io.Reader` values and capability
  stores;
- a high-level read-only `FileSystem` interface based on `io/fs`;
- verified random-access reads of uncompressed files through `io.ReaderAt` and
  `io.Seeker`, with bounded caching; and
- deterministic Lzip compression and sequential reads with checksum and length
  checks.

Use `fs.NewMountWithStore(ctx, images, source, options)` to build a mount
adapter. Supply an `fs.ImageStore` for image trees and an `eris.BlockSource` for
raw blocks. A capability-backed `*fs.Store` also supplies decoded content.
Either component can be nil. Paths for an absent component return
`io/fs.ErrNotExist`. With both nil, the mount exposes an empty root directory.
The mount implements the standard `io/fs` interfaces with the
[eris-go mount layout](specs/eris-go-fuse-layout.md): `<capability>` returns
decoded content, and `fs/<capability>/...` exposes an ERIS-FS tree. Full URNs,
unprefixed base32, and hexadecimal capabilities are accepted. With an image
store, the default root lists only `fs`; capability names resolve on demand and
are not listed. `OpenFile` supports `ReadAt` and `Seek` for uncompressed files.
`ReadLink` and `Lstat` provide link targets and metadata for FUSE or 9P
adapters. `Sub` retains an image for repeated access. The caller supplies the
mount mechanism.

`NewMountWithStoreAndLimit` sets the decoded size limit for each index,
including nested indexes. A capability-backed store uses 64 MiB by default. A
host mount controls its own index limits. Image reads use the image store’s
options. Raw-block reads use the options supplied to the mount constructor. Both
use the mount context. Each decoded index confines its own links, including when
the index is nested in another image. `ReadLink` returns the original target for
safe links. In a `Sub` view it also checks the selected directory boundary. When
all backends decode the same image capability at the same directory, they share
its link targets and nested index boundaries. The selected decoder checks the
link. A chain that also uses expanded mounts or other backends checks parent
steps across the backends that can serve the image. An explicit index-size limit
excludes expanded mounts from this check. A chain of independently opened views
can export a link when all views are decoded from the same index capability and
select the same directory within it. This includes views that reach the index at
different paths in enclosing images. Independently opened views of the same
immutable host mount can also export links when they select the same capability
directory and resolved subdirectory. Missing blocks match both
`io/fs.ErrNotExist` and `eris.ErrMissingBlock` in errors returned by `Mount`
methods.

For a base32 block hash `H`, the mount also exposes the complete encrypted block
at `H[:3]/H[3:6]/H[6:9]/H[9:]`, as specified by `volume.BlockPath`, and at the
flat path `H`. Both names select the same source block. Reads verify its size
and hash. Prefix directories resolve on demand and list no entries by default.
Hashes, capabilities, and the reserved `fs` name accept ASCII case variants.
Paths inside an ERIS-FS image stay case-sensitive, so entries such as `File` and
`file` remain distinct. These lookup aliases do not change the strict parsing
rules in the `eris` or `volume` packages.

To list recent accesses, enable the optional cache explicitly:

```go
mount, err := fs.NewMountWithStore(ctx, images, source, options).WithListingCache(128, 256)
```

The two limits count capabilities and raw blocks separately. Zero disables a
category; negative limits return `io/fs.ErrInvalid`. The returned mount starts
with an empty cache and leaves the original mount unchanged. Each category
evicts its least recently used entry. Successful content opens or stats, image
opens, and verified raw-block loads refresh recency. Listing virtual directories
(the root, `fs`, and volume prefixes) or reading entry metadata does not.
Reading a directory inside `fs/C` opens the image and refreshes `C`. Aliases
share one canonical entry.

With a capability-backed `*fs.Store`, the root lists remembered capability URNs.
With a block source, it lists flat hashes and volume prefixes. The `fs`
directory lists capabilities successfully opened as images. Prefix directories
list the next components of remembered block paths. Internal decoder block reads
and file capabilities found inside an image do not populate these listings. The
cache holds identifiers and metadata, not content; it does not serve reads or
guarantee source availability. Virtual `Sub` views share the cache. Open
directories keep a snapshot of their listing across later evictions.

Capability URNs contain decryption keys. If you enable capability listings,
restrict access to the mount when those keys must stay private.

Pass an `fs.ImageStore` to code that builds or opens images. Use
`fs.NewStore(content, options)` for a capability store or
`fs.NewMountStore("/eris/fs")` for a mounted tree. An empty mount path selects
`ERIS_FS_MNT`, then `ERIS_MNT` with `fs` appended. If both are empty, the
backend tries the `fs` directories under the platform's default mount roots.

```go
images := erisfs.NewCombinedStore(erisfs.NewStore(content, options), erisfs.NewMountStore(""))
image, err := images.OpenFS(ctx, capability)
if err != nil {
    return err
}
hostPath, err := image.ResolvePath(ctx, "docs/readme.txt")
```

To migrate from `fs.ResolveRoot` or `fs.ResolvePath`, open the image with
`fs.NewMountStore(mountDir).OpenFS(ctx, capability)`. Call
`image.ResolvePath(ctx, name)` and check its error. Use `"."` for the root.

`ResolvePath` returns an absolute host path. A capability-backed image can
resolve an uncompressed file if its content store supports host paths. A mounted
image can also resolve directories and decoded compressed files. Use a trusted,
immutable mount that verifies content and preserves case-sensitive names. Each
capability entry must be a directory. On a standard ERIS mount, `MountStore`
uses the decoded indexes alongside `fs` to distinguish ordinary directories from
nested images. A relative link can step to a parent directory within its own
index. When the index is unavailable, the link stays within its containing
directory. Mounted `ResolvePath` returns the resolved path. MountStore write
operations return `eris.ErrReadOnly`. Mounted trees report
`errors.ErrUnsupported` for raw-index reads and for positive explicit index
limits. A mounted prefetch asks the host for file metadata throughout the
selected tree.

The chain tries each complete operation in order. A builder, open file or
subdirectory stays with the backend that returned it. Cancellation stops
fallback and returns the context error. A failed index write that changes its
input also stops fallback. If every backend fails, the chain returns the last
error. Prefetch visits all backends and succeeds if any accepts the hint. Use
`fs.NewCombinedFileSystem` to chain images already open.

The `dns` package implements [ERIS DNSLink records](specs/eer/dnslink/index.md):

- canonical `eris` and `erisfs` namespace encoding and decoding;
- extraction from multiple or whitespace-concatenated TXT values;
- deterministic updates of existing TXT values and record sets; and
- DNSLink lookup through an abstract resolver implemented by `*net.Resolver`.

The DNS package does not create or configure a DNS client. Its resolver
interface lets applications supply standard DNS, DNS over HTTPS, or an in-memory
implementation. Resolver implementations must follow CNAME chains, as
`*net.Resolver` does for TXT lookups.

The `http` package implements [ERIS over HTTP](specs/eer/http/index.md):

- low-level RFC 2169 N2R URL and request construction;
- a chainable HTTP handler for encrypted blocks and decoded content;
- abstract block retrieval with no storage implementation; and
- a client that uses a caller-supplied HTTP request executor.

The HTTP package does not create or configure a network client or server. Set
`http.Handler.Blocks` to serve encrypted blocks and `http.Handler.Content` to
serve decoded content. To serve both from one block source, set `Blocks` to the
source and `Content` to `eris.NewBlockCapabilityStore(source)`.

Set `http.Handler.MaxContentSize` to a positive byte limit when clients can
request untrusted capabilities. The handler checks the full decoded size before
sending content, including for HEAD and range requests. It returns HTTP 413 for
content above the limit. Zero leaves the size unlimited. Range requests read
from the decoded reader at the requested offsets. The limit applies only to
decoded content. Both stores receive the request context and must honor it to
cancel a read already in progress.

The `s3` package implements the
[ERIS object-storage recommendation](specs/eer/object-storage/index.md):

- canonical `s3://ENDPOINT/BUCKET/` store URLs and path-style object URLs;
- public block reads and authenticated single-request block writes;
- deterministic AWS Signature Version 4 signing and verification;
- caller-supplied HTTP clients, credentials, authorization, and clocks; and
- a fixed-bucket HTTP handler that permits writes only when `AllowPut` is true.

The S3 package intentionally omits credential discovery, IAM, bucket
administration, object listing, and multipart uploads. It uses only the Go
standard library and the existing ERIS block APIs. The client stops redirects
for `*http.Client` so a redirected request cannot send a session token or PUT
body to another host. Custom `HTTPClient` implementations must also stop
redirects.

The S3 handler rejects GET and HEAD requests with more than 16 range entries
before storage access. This bounds the multipart response overhead for requests
with many small ranges.

The `coap` package implements [ERIS over CoAP](specs/eer/coap/index.md):

- canonical `coap` and `coap+tcp` store URLs;
- raw and Base32 block-reference queries;
- verified GET and reference-returning PUT clients with single-message and
  block-wise transfers;
- storage-backed, context-aware handlers with opt-in PUT support;
- deterministic CoRE Link Format discovery documents;
- cross-network proxy advertisements and Hop-Limit forwarding helpers; and
- caller-supplied CoAP requesters and remote addresses.

The CoAP package performs no name resolution, dialing, listening, socket
creation, or file-system access. Applications supply `coaperation` requesters
over their chosen in-memory or network transports.

CoAP PUT handlers return the computed reference as 32 raw octets in the
`2.01 Created` payload. A PUT request can include an expected reference as a URI
query. For an older server with an empty success payload, the client computes
the reference locally when no expected reference was supplied.

The client adds a separate
[Request-Tag](https://www.rfc-editor.org/rfc/rfc9175.html#section-3) to each
block-wise upload so concurrent uploads to the same resource keep their
fragments separate.

Use `coap.Handler.BlockWise` for block-wise transfers. The direct handler
rejects Block1 and Block2 options; the discovery handler rejects direct Block2
requests. This prevents a fragment from being stored as a complete block or a
Block2 request from receiving an unsegmented response. The block and discovery
handlers also reject unsupported critical options, including conditional request
options and Accept. For these options, confirmable requests receive 4.02 Bad
Option and non-confirmable requests are discarded. Unknown elective options are
ignored.

The block-wise wrapper returns a `*coap.BlockWiseHandler`. It limits active and
retained transfers to 128 by default; set `Handler.MaxTransfers` to select
another positive limit. At capacity, new transfers receive 5.03 Service
Unavailable. Existing transfers can continue. Completed download caches can be
removed to make room for new transfers. Transfer state expires after the
configured `StateLifetime` (two minutes by default), with expiry checked on each
request. Each transfer retains at most 64 KiB of request metadata. The wrapper
checks write permission and critical options before accepting fragments and
rejects repeated Block1, Block2, Size1, and Size2 options. Requests for other
paths go directly to `Next`; that handler controls its own transfer policy.

Reassembled bodies cannot exceed 32 KiB, even if `MaxBodySize` is larger.
Invalid reference queries, content formats, and size hints are rejected before
upload fragments reserve transfer state. GET validation also applies to cached
download fragments. Clients limit response bodies to 32 KiB and response options
to 64 KiB, counting eight bytes per option in addition to its value, before
reassembly copies them. Client transfers also limit the sum of response options
across all fragments to 1 MiB. Discovery response parsing uses the 64 KiB
per-message option limit.

The `link` and `magnet` packages implement the
[ERIS link file](specs/eer/linkfile/index.md) and
[ERIS Magnet URI](specs/eer/magnet-uri/index.md) recommendations.

`link.NewFS(base, store, options)` wraps any `io/fs.FS` and an
`eris.CapabilityStore`. It exposes the content of ERIS link files through
context-aware methods. Use `link.NewIOFS(ctx, image)` with `fs.ReadFile` and
other `io/fs` functions. It detects links by their magic bytes and keeps the
original paths, including any `.eris` extension. Ordinary files remain readable.
Resolved files support `io.Seeker` and `io.ReaderAt`.

`Stat` and directory entry `Info` report the declared content size and preserve
the other base file metadata. Detection requires read access to regular files.
With a block-backed store, these operations read only base files. Content reads
fetch blocks as needed and check the declared size at the file end. With a mount
store, metadata lookup opens the target file and checks its size. Partial reads
do not check unvisited blocks. Invalid links return errors. Resolution stops
after one link; MIME and attributes do not change the content. Use the base FS
to read a link file itself.

The default limit for an encoded link is 1 MiB. Use `link.NewFSWithLimit` to
change it. The limit does not restrict decoded content. `fs.ReadFile` reads all
content into memory; use `Open` and `io.LimitReader` for a content byte limit.
The wrapper follows the base FS policy for symbolic links. When the base lacks
`fs.SubFS`, the wrapper also checks that each target returned by `ReadLink`
stays inside the selected subdirectory. The supplied capability store handles
network timeouts and cancellation.

The core `eris` package provides storage interfaces, an in-memory block set, and
a combined store. It also provides raw content encoding and ERIS tree reading.
The `volume` package supplies local persistent storage. Applications supply
other storage backends, input readers, output writers, networking, and
random-secret generation. ERIS extensions use separate packages.

Use `eris.CapabilityStore` to access decoded content by capability. Wrap a block
source with `eris.NewBlockCapabilityStore(source)`. Writes require the source to
implement `eris.BlockSink`. A source with only read methods returns
`eris.ErrReadOnly` on writes. For files on an ERIS mount, use
`eris.NewMountCapabilityStore(root)`. This read-only store also implements
`ResolvePath`.

The package-level encode, decode, reader, and prefetch functions have moved to
`CapabilityStore`. Pass the store to ERIS-FS, the link filesystem, and the URL
fetcher. Use the mount store's `ResolvePath` method for decoded file paths. For
a write-only block sink, use
`eris.NewBlockCapabilityStore(eris.NewCombinedBlockStore(sink))`.

Use `CapabilityStore.EncodeContentWithOptions` to control block size and root
marking for a stream. A zero `BlockSize` selects the recommended size. The
encoder marks the capability's root block by default. Set `PreserveRoot: true`
to pass `BlockOptions.Root` unchanged on each write. The store's rules for
repeated writes still apply. Custom `CapabilityStore` implementations must
provide `EncodeContentWithOptions`.

Use `eris.NewCombinedCapabilityStore(mount, blocks)` to try the mount first and
the block store second. Read and write operations return the first successful
result, return the last error if all attempts fail, and stop on cancellation.
`Prefetch` calls every store in order. It returns `nil` if any call succeeds and
returns the last error if all calls fail. Once a reader opens, it uses that
store for all reads. Put the mount first when using `NewReaderWithSize`. A
block-backed store can open a sized reader before it knows whether it has the
blocks. If a later read finds a missing block, the caller receives the error.

A failed write stops the chain if the store changed the input bytes or called
`Read` on the input stream. A store that cannot handle a stream should reject it
before reading. A failed write can leave encoded blocks in the destination.

To store a blob from any `io.Reader`, use `store.EncodeContentRecommended`. A
block-backed store reads the source once, selects the recommended block size
with at most 16 KiB of lookahead, writes the complete encrypted tree to an
`eris.BlockSink`, and returns the read capability. This operation creates no
file system index.

```go
capability, err := store.EncodeContentRecommended(ctx, input, secret, eris.BlockOptions{})
if err != nil {
	return err
}
fmt.Println(capability.String()) // urn:eris:...

reader, err := store.NewReader(ctx, capability, eris.BlockOptions{})
if err != nil {
	return err
}
defer reader.Close()
_, err = io.Copy(output, reader)
```

Use `store.EncodeContent(ctx, input, secret, blockSize, options)` to select the
block size explicitly. Both encoders write with `eris.NoTTL`. A read or write
error can leave blocks in the sink; save the capability only after a successful
return.

On a block-backed store, `NewReader` scans internal nodes and the final leaf to
determine the decoded size.
`store.NewReaderWithLimit(ctx, capability, maxBytes, ttl, options)` applies a
size limit and passes `ttl` to every block read. A negative limit disables the
limit. Use `store.NewReaderWithSize(ctx, capability, size, options)` when the
decoded size is known; it opens without block I/O and checks tree shape and
padding as content is read. Readers support `Read`, `ReadAt`, `Seek`, and
`Close`. Closing a reader does not close its block source.

Block-backed readers send best-effort prefetch hints for demand blocks and
likely next blocks. Sequential reads start with four leaves of read-ahead and
grow to at most 32. A random jump resets this window. Demand hints and
read-ahead cover at most 64 leaves per iteration. Speculation follows cached
indexes only; it never adds block reads. `Seek` hints its target through the
same cached paths. A bounded cache of 128 references suppresses repeated hints.
Block-backed ERIS-FS files and compressed streams use this reader policy.

To request all content in advance, use:

```go
err := store.Prefetch(ctx, capability, eris.NoTTL, options)
err = image.Prefetch(ctx, ".")      // All files and nested ERIS-FS indexes.
err = image.Prefetch(ctx, "docs")   // Only this file or directory.
```

These calls can block. ERIS-FS reads nested indexes and passes file capabilities
to the store. A block-backed store reads internal nodes to discover references.
It sends hints for file leaves, including the final padding leaf, without
reading or decompressing them. Repeated internal subtrees and repeated
filesystem capabilities are scanned once per traversal. Directory traversal
skips symbolic links; the explicitly named path follows links as `Open` does.
`sub.Prefetch(ctx, ".")` visits only the selected directory. As with `Sub.Open`,
an explicitly named link can resolve outside that subtree but remains inside the
image. `NewIOFS(ctx, image).Prefetch(name)` uses the bound context. Custom
implementations of the ERIS-FS `FileSystem` interface must also supply
`Prefetch`. Traversal reports errors from index reads, store prefetch calls, and
cancellation. Success does not guarantee that content exists or is cached. Use a
goroutine managed by the caller if the traversal must run in the background.

Use `github.com/asciimoth/goeris/cache` to keep encrypted blocks in a bounded
memory cache and process prefetch hints with a fixed worker pool:

```go
cached, err := cache.New(source, cache.Config{
	CapacityBytes: 16 << 20, // At most 16 MiB of retained block data.
	Workers:       4,
	QueueSize:     64, // Waiting block requests, excluding active workers.
})
if err != nil {
	return err
}
defer cached.Close()

reader, err := eris.NewBlockCapabilityStore(cached).NewReader(ctx, capability, eris.BlockOptions{})
```

All three limits must be positive. Blocks larger than `CapacityBytes` can be
read but are not cached. The byte limit excludes metadata, queued contexts,
returned copies, and reads in progress. Within each priority class, eviction
removes the least recently used block. Regular blocks are evicted before
high-priority blocks. A regular block cannot displace a high-priority block. If
it cannot fit beside protected blocks, existing entries are left in place. A
stored `HighPriority` flag or a read/prefetch hint protects the cache entry
until eviction or invalidation; the hint does not change the options returned by
storage. `Root` does not affect eviction.

Prefetch calls copy accepted requests into the bounded queue and return without
waiting for block I/O, queue space, or a busy cache lock. Workers issue matching
`GetBlock` calls and cache successful results. Hints can be dropped, and
background errors are ignored. Request cancellation also cancels background
reads. `Close` cancels those reads, discards queued work, and waits for the
workers. It does not close the source or wait for foreground calls already in
progress. The source must support concurrent calls and honor context
cancellation.

If the source also implements `eris.BlockSink`, puts and option updates pass
through and invalidate the affected entry. The next read loads the actual stored
options, including flags retained by repeated puts or ignored by the source. Use
the wrapper for all option changes that must be visible through the cache; it
does not track changes made directly to the source. A read-only source returns
`eris.ErrNoBlockStorage` for writes. TTL values pass through unchanged and do
not set cache expiry times.

The byte-slice encode methods return a capability and an error. To keep the
encrypted blocks in memory, supply a writable `BlockSet` when you create the
store:

```go
blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
store := eris.NewBlockCapabilityStore(blocks)
options := eris.BlockOptions{}
capability, err := store.EncodeRecommended(ctx, content, secret, options)
if err != nil {
	return err
}
decoded, err := store.Decode(ctx, capability, options)
```

`Decode` has no application size limit. Use
`store.DecodeWithLimit(ctx, capability, maxBytes, options)` to bound decoded
content from an untrusted source.

Block storage uses ordered batches. Each request has its own TTL and options.
The interfaces are:

```go
GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error)
PrefetchBlock(ctx context.Context, requests []BlockRequest)
PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error)
UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error)
BatchSize() int
```

This is a breaking API change. Custom sources and sinks must implement their
batch methods and `BatchSize`. Sources must also implement `PrefetchBlock`;
write-only sinks do not need it. Replace single-block method calls with the
package helpers shown below, or supply a one-request slice to the new methods.

`BlockRequest` has `Reference`, `TTL`, and `Options` fields. `PutBlockRequest`
has `Data`, `TTL`, `Options`, and an optional `Expected *Reference`. A reference
mismatch rejects that block with `ErrInvalidBlock`. Sources verify each returned
block against its requested reference. Read results include stored options.

Both interfaces use the same `BatchSize` limit for all operations. Zero permits
any size. Implementations with an unknown limit, such as network API clients,
must report one as a safe default. Oversized read and write batches return
`ErrBatchTooLarge` before processing any request. Oversized prefetch batches are
silently discarded. Empty batches succeed without I/O, even when the context is
canceled.

`PrefetchBlock` has the same arguments as `GetBlock` and returns nothing. It
must return immediately after accepting or discarding hints, without waiting for
block I/O, queue space, or background work. Hints are best-effort and may be
ignored. Empty, canceled, and oversized hint batches are discarded. Sources must
copy request slices before keeping them after return. Background work must honor
the supplied context. Middleware preserves TTL and options. Current terminal
sources (`BlockSet`, volumes, HTTP, CoAP, and S3 clients) ignore hints; no
network prefetch protocol is added. Custom sources can implement a no-op:

```go
func (s *MySource) PrefetchBlock(context.Context, []eris.BlockRequest) {}
```

Use `eris.PrefetchBlock(ctx, source, ref, ttl, options)` for a single hint.

Read results keep their request positions. A missing block has nil `Data` and
zero `Options`. `GetBlock` continues after missing blocks and returns every
available block, with an error that matches `ErrMissingBlock` through
`errors.Is`. For example, requests `[A, missing, B]` return
`[blockA, {}, blockB]` and a missing-block error. The first missing error is
preserved, including wrapped storage errors.

Cancellation and other read errors stop processing and return the processed
prefix, including any missing slots. If blocks were missing before that stop,
`errors.Is` matches both `ErrMissingBlock` and the stopping error. When missing
blocks are the only errors, the result length equals the request length.

Writes and option updates stop at the first error and return the completed
prefix. **Batches have no atomicity or rollback guarantee.** Earlier writes may
remain, and the failed operation may also have side effects. Stores that process
blocks one at a time check cancellation before each block. Middleware preserves
the context, TTL, and options for each request.

The single-block helpers and content readers reject malformed successful result
counts with `ErrInvalidBatchResult`. This prevents a panic or a read loop
without progress.

For one operation, use the root package helpers:

```go
block, err, storedOptions := eris.GetBlock(ctx, source, ref, ttl, options)
ref, err = eris.PutBlock(ctx, sink, data, ttl, options) // Optional final expected reference.
changed, err := eris.UpdateBlockOptions(ctx, sink, ref, ttl, options)
```

Stream encoders and readers respect the storage limit and cap their batches at
64 blocks (at most 2 MiB of encrypted data). ERIS-FS uses the same bounded paths
for file content and indexes. It does not collect a complete image for one
write. Network clients and local volumes currently use a limit of one. Combined
stores report the smallest nonzero child limit, or zero if all children accept
any size. A combined store that includes a volume therefore reports one.

`BlockOptions` is passed and returned by value. Its `HighPriority` and `Root`
flags default to `false`.

`HighPriority` is an advisory hint. Terminal stores can use it for cache
eviction, operation scheduling, or another policy, or ignore it. Middleware
passes options unchanged by default and returns the options from a successful
read.

`Root` highlights key blocks for indexing and block exchange between nodes.
Encoders set it by default on the block named by the returned capability,
including single-block content and ERIS-FS index roots. ERIS-FS file writes and
streams that use `PreserveRoot: true` keep the supplied `Root` option on all
writes. An explicit `Root: true` marks every block in the operation. The flag
affects storage metadata only.

`BlockSet` keeps each flag true across repeated puts and returns it on get.
`UpdateBlockOptions` can set or clear either flag. Each result is `true` only if
the stored options changed. Absent blocks, unchanged options, and unsupported
updates produce `false` without an error. A combined store continues after
`false` without an error until an update succeeds or the list ends. Volumes
ignore both flags and return `false` for each request. Network clients do not
support explicit option updates.

`BlockSet` is now a struct with a `Blocks map[Reference]Block` field and an
optional `MaxBatchSize` field for tests and debugging. Initialize it with
`eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}`. Zero
`MaxBatchSize` accepts any batch size. Direct access uses
`blocks.Blocks[ref].Data`. Use the storage methods or single-block helpers to
verify and copy bytes and preserve flags across repeated puts. Direct map access
is not safe during concurrent writes.

HTTP carries true priority in `Eris-High-Priority: true`. S3 uses
`x-amz-meta-eris-high-priority: true`, set before request signing. Both omit the
field for false priority. CoAP supports experimental option 65020 when
`ExperimentalHighPriority` is true on both `coap.ClientConfig` and
`coap.Handler`. It is disabled by default and is only for coordinated,
non-operational experiments. The existing ERIS CBOR grammar has no metadata
field, so CBOR omits priority and reads it as false. See the
[priority specification](specs/priority.md) for wire rules and compatibility
limits.

The root flag uses `Eris-Root: true` over HTTP and `x-amz-meta-eris-root: true`
over S3, with the same parsing and signing rules. CoAP uses experimental option
65052 when `ExperimentalRoot` is enabled on both endpoints. CBOR and volumes do
not retain root metadata. See the [root block specification](specs/root.md).

Content encoders, readers, and index codecs accept options by value. ERIS-FS
builders and filesystems, including the link filesystem, receive options once at
construction. They pass a copy to every block operation, with `Root` always true
on index root writes and unchanged from the original option on file writes. This
includes nested indexes, compressed files, and reads through `Sub`.

Middleware passes context unchanged. `BlockSet` checks cancellation before each
request. `Volume` also checks cancellation between file operations, but cannot
stop a file-system call already in progress.

TTL limits network forwarding along each request path. It does not set a time
limit or block expiry. Use `eris.NoTTL` (`0`) when the caller does not specify a
limit. Terminal stores, such as `BlockSet`, may ignore TTL. Middleware must pass
it unchanged to all attempts, fallbacks, and copied writes. A network client
calls `ttl.Forward()`: `0` starts with a wire value of `16`, `1` returns
`eris.ErrTTLExpired` without network I/O, and `2` through `255` decrease by one.
Thus, `2` reaches one remote store, which can use local storage but cannot
forward the request again. Never forward an exhausted value as `0`, because that
would start a new budget.

HTTP and S3 use the optional `Eris-TTL` request header with a decimal value from
`1` through `255`. S3 signs this header; it is not object metadata. CoAP uses
standard Hop-Limit option 16 from
[RFC 8768](https://www.rfc-editor.org/rfc/rfc8768.html), compatible with
[Kapla](https://codeberg.org/eris/kapla)'s GET forwarding. The same limit
applies to CoAP PUT. Servers pass the received value unchanged to storage. An
absent header or option means `1` (local access only), so old clients can still
use terminal stores. Invalid wire values are rejected. Peers that ignore these
extensions can still serve blocks, but do not enforce forwarding limits.

HTTP and S3 report an exhausted downstream limit with HTTP 508; CoAP uses 5.08.
Client errors for those responses match `eris.ErrTTLExpired` through
`errors.Is`. If HTTP content streaming has already started, a later failure
aborts the stream instead. CoAP's `RequestConfig.HopLimit` can set the initial
wire value when an operation uses `NoTTL`; an explicit operation TTL takes
precedence. Low-level CoAP request builders send `RequestConfig.HopLimit` as is,
and omit it when zero. Use `PrepareForward` to forward an existing CoAP message.
HTTP `NewRequest`, `Resolve`, and `OpenContent`, and S3 `NewRequest` also take
TTL. Encoding and decoding helpers start block operations with `NoTTL`; HTTP
content handlers bind the incoming request budget to every decoded block read.

Use `eris.NewCombinedBlockStore` to try several stores in order:

```go
readOnlySource := struct{ eris.BlockSource }{source}
store := eris.NewCombinedBlockStore(readOnlySource, primaryStore, fallbackStore)
store.OnError = func(index int, err error) {
	log.Printf("block storage %d failed: %v", index, err)
}
block, err, _ := eris.GetBlock(ctx, store, reference, eris.NoTTL, eris.BlockOptions{})
```

Combined stores calculate `BatchSize()` from their wrapped sources and sinks. If
all report zero, the combined store also reports zero and accepts any batch
size. Otherwise, it reports the smallest nonzero limit. For example, limits
`[0, 8, 4]` give a combined limit of 4. Entries that implement neither interface
are ignored. A store with no compatible entries reports one. Limits are read on
each call, so changes to child limits also apply to nested combined stores. The
encoder and reader use this limit automatically. Volumes report one, so
`volume.Chain` retains its limit of one and its existing fallback behavior.

Each child call respects that child's batch limit. Zero means unlimited for the
child; an invalid negative child limit is treated as one. Successful writes are
not repeated. After a partial write failure, only the failed request goes to
fallback stores before later writes resume at the first store. Consecutive
missing reads and unchanged option updates can go to fallback stores as a batch.
Results remain in request order, with missing read slots and completed prefixes
on stopping errors, as defined by the storage interfaces. If a write returns an
error with a full list of references, the combined store returns those
references and the error without replaying the writes.

Batching can change the order of child calls and `OnError` callbacks. A child
can process later requests before an earlier request needs fallback. Thus an
option update can have effects beyond the returned prefix if fallback fails.
Each fallback group reports its discarded error before the next attempt;
separate groups can report the same child batch error. Single-request calls keep
their existing call and callback order. Set `OnError` before concurrent use.

Read errors can retain additional missing-block details from a child's batch
error. The first missing error and any stopping error remain available through
`errors.Is`.

Reads and writes stop at the first success. Prefetch hints go to every
downstream source, including nested combined stores. Writes skip entries without
a sink and store the block in the first successful sink. The wrapper above
exposes `GetBlock`, `PrefetchBlock`, and `BatchSize`. Assigning a store to a
`BlockSource` variable alone does not hide its write methods from the
constructor.

The constructor also accepts sinks. It selects methods for each operation and
skips entries without a suitable method. Changing the input slice after
construction does not change the combined store. Every attempt receives the same
context. After a failed attempt, cancellation is checked before the next store
is called.

A successful attempt returns its result. If the final compatible entry fails,
its error is kept even if the context was canceled during that attempt.
Otherwise, cancellation stops further attempts and returns the context error. An
already canceled context takes priority over `eris.ErrNoBlockStorage`.

For a single request, if all compatible entries fail, the method returns the
last error unchanged and a zero result. `OnError` receives each discarded
attempt's error and its original index in the constructor's input. Calls are
synchronous and occur before the next attempt, or before returning a context
error if cancellation prevents that attempt. The final attempt is not reported
when its error is returned to the caller. Separate attempts can return the same
error value. Leave `OnError` nil to discard these reports. If no entry supports
the operation, the method returns `eris.ErrNoBlockStorage`.

Concurrent calls are safe when `OnError` stays fixed and the stores and callback
support concurrent calls.

Build and open an ERIS-FS image without host file-system access:

```go
blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
builder, err := images.NewBuilder(eris.Secret{})
if err != nil {
	return err
}
if err := builder.AddFile(ctx, "docs/readme", strings.NewReader("hello"), false); err != nil {
	return err
}

capability, err := builder.Finish(ctx)
if err != nil {
	return err
}
image, err := images.OpenFS(ctx, capability)
if err != nil {
	return err
}
file, err := image.OpenFile(ctx, "docs/readme") // io.ReaderAt and io.Seeker
if err != nil {
	return err
}
defer file.Close()
```

`erisfs.NewStore` accepts any `eris.CapabilityStore`. Reading requires a
readable store. Building an image requires a writable store. Image operations
take a context as their first argument.

For standard Go filesystem interfaces, bind a context to the image:

```go
compatible := erisfs.NewIOFS(ctx, image)
data, err := fs.ReadFile(compatible, "docs/readme")
```

`IOFS` supports `fs.FS`, `fs.ReadFileFS`, `fs.ReadDirFS`, `fs.StatFS`,
`fs.ReadLinkFS`, and `fs.SubFS`, as well as `OpenFile`. Files and directory
entries retain the operation context for later reads and metadata access.
Subdirectory wrappers retain the same context. Create a new wrapper to use a
different context; the image can be shared by multiple wrappers.

Use `Index`, `Store.EncodeIndex`, and `Store.DecodeIndex` for the low-level
ERIS-FS API. Use `Builder`, `Store.OpenFS`, and `FileSystem` for the file-system
API. All writers create version 2 indexes. `Builder.AddFile` counts the content
bytes while it encodes them and selects the recommended ERIS block size.

When constructing an `Entry` directly, set `Size` to the file length in bytes.
Zero means an empty file. `Store.DecodeIndex` sets v1 file sizes to `-1` because
v1 does not store lengths. To encode such an index as v2, open the original
image with `Store.OpenFS` and use `Stat` to obtain each missing length, then set
`Entry.Size`. Encoding fails with `ErrUnknownSize` until all file lengths are
known. Nested indexes can use either version. Existing v1 images with text
symlink targets or length-first CBOR map order remain readable; new indexes use
arrays of target components and RFC 8949 section 4.2.1 bytewise key order.

For uncompressed v2 files, `Stat` and directory entry metadata use the length in
the index. `OpenFile` passes that length to `CapabilityStore.NewReaderWithSize`.
A block-backed reader opens without fetching file content. Random reads fetch
the required tree paths and leaf blocks. Each reader caches at most 64 internal
nodes and one leaf. A reader checks the shape of each visited path and checks
padding when it reads the file end. Unvisited blocks are not checked.
Block-backed v1 files first scan internal nodes and the final leaf to obtain the
content length. This scan uses temporary summaries that grow with the number of
distinct internal nodes. The reader releases these summaries after the scan and
applies the same 64-node cache limit during reads.

Other capability stores supply their own readers. For example, a mount-backed
store opens the decoded file and checks its length against a v2 index.
Applications use `OpenFile` for random access to files in an image.

`Builder.AddCompressedFile` reads a file once and writes a deterministic Lzip
stream with the recommended 8 MiB dictionary. It stores the uncompressed size in
a v2 entry. The encoder uses bounded memory and selects the ERIS block size from
the compressed length. For an existing Lzip stream, use `Builder.AddLzipFile`
with its uncompressed size. This method stores the supplied stream without
checking it. A low-level caller can set `Entry.Compression` to `Lzip` and
`Entry.Size` to the uncompressed length. Use `Open` to read compressed files.
Reads decode members in order and check their CRC, member length, and total
output length. `OpenFile` returns `ErrNotSeekable` for compressed files. The
decoder accepts dictionaries up to 64 MiB and limits the allocated dictionary to
the remaining output size, plus one probe byte, with a 4 KiB minimum. It does
not buffer a whole file. The ERIS-FS recommendation advises compression only for
small files or files read in sequence, and advises against compressing files
smaller than 1 KiB. Leave files uncompressed when they need random access.

`Builder.AddAutoFile` selects either mode with the fixed
[ERIS-FS-AUTO-1 policy](specs/eris-fs-auto-compression.md). It samples the first
32 KiB, leaves files below 1 KiB uncompressed, and selects Lzip when a specified
LZ77 probe estimates at least 12.5% savings, including 26 bytes of container
cost. `ChooseCompression` exposes the same decision for a supplied prefix. The
choice does not depend on a Lzip implementation. The sample is replayed into the
chosen encoder, so the source is read once and need not support seeking. Later
content does not change the decision, and actual savings can differ from the
estimate. The policy document includes pseudocode and portable conformance
vectors.

To get the same capability across ERIS-FS implementations, use `AddFile` for
every file, the same 32-byte secret, version 2 indexes, and the same tree
structure and metadata. All encoders must disable compression and use 1 KiB
blocks below 16384 bytes, or 32 KiB blocks at and above that size, for both
files and indexes. The
[cross-implementation procedure](specs/eris-fs-encoding.md#deterministic-encoding-across-implementations)
also specifies paths, symlinks, empty directories, and CBOR encoding.

For repeatable encoding with goeris auto compression, use `AddAutoFile` for
every file and keep the same inputs, secret, index structure, goeris version,
and effective LZMA dependency version. Entry insertion order and input read
chunk sizes do not change the result. The
[goeris auto compression procedure](specs/eris-fs-encoding.md#deterministic-encoding-with-goeris-auto-compression)
explains the fixed decision rule and version requirements. ERIS-FS compliance
alone does not ensure that two encoders produce the same capability.

The [encoding profile](specs/eris-fs-encoding.md) specifies all Lzip settings
and the pinned encoder algorithm. Fixed regression vectors check compressed
bytes, file capabilities, and complete image capabilities. ERIS-FS permits Lzip
encoders to produce different valid streams. The ERIS conformance tests check
encoding and decoding against 13 official positive vectors and verify that
decoding rejects all 12 negative vectors.

`Store.OpenFS` and `Store.DecodeIndex` limit each decoded index to 64 MiB. Use
`Store.OpenFSWithLimit` or `Store.DecodeIndexWithLimit` to select another
application limit. This limit applies to index bytes. File contents are stored
separately. The builder retains entry metadata in memory and streams the
contents. A store such as `volume.Volume` can keep the encrypted blocks on disk.

File sizes in v2 indexes are untrusted metadata until the content is read.
`ReadFile` reads the whole file into memory and grows its buffer from the bytes
read. For large files, use `Open` and `io.Copy` to stream the contents, or
`OpenFile` for random access to uncompressed files. Use `io.LimitReader` when
the output needs an application size limit.

Callers that need stronger resistance to confirmation attacks must generate a
cryptographically secure, unique `eris.Secret` outside this package. ERIS does
not provide confidentiality.

Run the complete checks with:

```sh
just check
```

The optional multi-GiB ERIS-FS test checks the default uncompressed profile. It
builds and reads 100,000 files plus one file larger than 2 GiB. Allow about 6
GiB of free space in the temporary directory.

```sh
GOERIS_LARGE_IMAGE_TEST=1 go test ./fetch -run '^TestMultiGiBTreeDefaultEncoding$' -count=1 -v -timeout=60m
```

Go 1.26.10 can report a false `context deadline exceeded` failure when a timed
fuzz run ends. This is a [Go fuzz-runner race](https://go.dev/issue/75804). The
flake dev shell uses Go 1.27.4, which includes the fix. Do not ignore a failing
fuzz input.

The `vulncheck` check accepts
[GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859) while the scanned packages
do not use `http.MaxBytesReader` or `http.MaxBytesHandler`. The affected race
needs one of these body-limit APIs and concurrent request-body reads and
response-header writes. The check fails if a scanned package starts using either
API. Applications that add one of these APIs must use a patched Go toolchain.

## License

The Go library and this README are distributed under the CC0 license. The copied
ERIS specifications in [specs](specs/README.md) retain their CC BY-SA 4.0
license.

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
  <span property="dct:title">goeris's original code and documentation</span>.
</p>
