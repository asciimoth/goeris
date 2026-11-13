# Go ERIS

This repository provides a Go library for [ERIS 1.0.0](specs/eris-v1.0.0.html).
Local copies of the specification and ERIS extension documents are in
[specs](specs/README.md).

Install this library with `go get github.com/asciimoth/goeris`.

The `eris` package implements:

- ERIS padding and recommended block-size selection;
- BLAKE2b-256 references and ChaCha20 node encryption;
- read capability binary, URN, and CBOR encodings;
- deterministic encoding to an in-memory block set;
- verified decoding from an in-memory block set, with abstract block storage
  interfaces; and
- combined block storage with ordered fallback and optional error reporting.

The `cbor` package implements the
[ERIS CBOR transport format](specs/eer/cbor/index.md):

- deterministic tagged, untagged, and self-described arrays;
- tagged and untagged CBOR sequences;
- definite-length and indefinite-length decoding; and
- typed blocks, references, verified block maps, and read capabilities.

The `fs` package implements [ERIS-FS version 2](specs/eer/eris-fs/index.md),
with version 1 read support:

- canonical deterministic CBOR index encoding and strict decoding;
- low-level index codecs and streaming content encoding;
- regular, executable, symbolic-link, and nested-image entries with file
  lengths;
- host-independent builders that accept `io.Reader` values and abstract block
  stores;
- a high-level read-only `FileSystem` interface based on `io/fs`;
- verified random-access reads of uncompressed files through `io.ReaderAt` and
  `io.Seeker`, with bounded caching; and
- deterministic Lzip compression and sequential reads with checksum and length
  checks.

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

The HTTP package does not create or configure a network client or server.
Applications supply the HTTP request executor, block source, handler chain, and
server registration.

Set `http.Handler.MaxContentSize` to a positive byte limit when clients can
request untrusted capabilities. The handler checks the full decoded size before
sending content, including for HEAD and range requests. It returns HTTP 413 for
content above the limit. Zero leaves the size unlimited. Encrypted block reads
are unaffected. The handler checks request cancellation before each block fetch;
use a `ContextBlockSource` to cancel a fetch that is already in progress.

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

The core `eris` package provides storage interfaces, an in-memory block set, and
a combined store. Applications supply persistent storage, file or stream I/O,
networking, and random-secret generation. ERIS extensions use separate packages.

```go
secret := eris.Secret{} // The null convergence secret.
capability, blocks, err := eris.EncodeRecommended(content, secret)
if err != nil {
	return err
}

decoded, err := eris.Decode(capability, blocks)
```

`Decode` has no application size limit. When blocks come from an untrusted
source, use `DecodeWithLimit(capability, blocks, maxBytes)` to reject a tree
whose decoded content exceeds the selected size limit before expansion.

`BlockSink.PutBlock(block, ttl)` returns the reference computed by storage.
`BlockSink.PutBlock(block, ttl, expected)` rejects a block that does not match
the expected reference. `BlockSource.GetBlock(ref, ttl)` returns only a matching
block; the source verifies stored or received data if it can be incorrect.

All block storage methods now require an `eris.TTL` argument. This is a breaking
Go API change; there are no adapters for the old signatures:

```go
GetBlock(ref Reference, ttl TTL) ([]byte, error)
PutBlock(block []byte, ttl TTL, expected ...Reference) (Reference, error)
GetBlockContext(ctx context.Context, ref Reference, ttl TTL) ([]byte, error)
PutBlockContext(ctx context.Context, block []byte, ttl TTL, expected ...Reference) (Reference, error)
```

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
[RFC 8768](https://www.rfc-editor.org/rfc/rfc8768.html), compatible with Kapla's
GET forwarding. The same limit applies to CoAP PUT. Servers pass the received
value unchanged to storage. An absent header or option means `1` (local access
only), so old clients can still use terminal stores. Invalid wire values are
rejected. Peers that ignore these extensions can still serve blocks, but do not
enforce forwarding limits.

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
block, err := store.GetBlock(reference, eris.NoTTL)
```

Each operation stops at the first success. Writes skip entries without a sink
and store the block in the first successful sink. The wrapper above exposes only
`GetBlock`. Assigning a store to a `BlockSource` variable alone does not hide
its write methods from the constructor.

The wrapper also hides context methods. If `source` implements both read
interfaces, preserve its cancellation support with this wrapper:

```go
readOnlySource := struct {
	eris.BlockSource
	eris.ContextBlockSource
}{source, source}
```

The constructor also accepts sinks or implementations that support only context
methods. Plain `GetBlock` and `PutBlock` calls skip entries that provide only
context methods. The constructor selects methods for each operation. Entries
without a suitable method are skipped. Changing the input slice after
construction does not change the combined store.

Context methods prefer an entry's `ContextBlockSource` or `ContextBlockSink`
method and pass the context unchanged. If an entry has only a plain method, they
use it after checking for cancellation. Cancellation cannot stop a plain method
while it runs.

A successful attempt returns its result. If the final compatible entry fails,
its error is kept even if the context was canceled during that attempt.
Otherwise, cancellation stops further attempts and returns the context error. An
already canceled context takes priority over `eris.ErrNoBlockStorage`.

If all compatible entries fail, the method returns the last error unchanged and
a zero result. `OnError` receives each discarded attempt's error and its
original index in the constructor's input. Calls are synchronous and occur
before the next attempt, or before returning a context error if cancellation
prevents that attempt. The final attempt is not reported when its error is
returned to the caller. Separate attempts can return the same error value. Leave
`OnError` nil to discard these reports. If no entry supports the operation, the
method returns `eris.ErrNoBlockStorage`.

Concurrent calls are safe when `OnError` stays fixed and the stores and callback
support concurrent calls.

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

`eris.BlockSet` is an example store. You can pass any `eris.BlockSink` to
`NewBuilder` and any `eris.BlockSource` to `OpenFS`. The `io/fs` methods expose
the decoded image; they do not open paths on the host file system. An
application can supply a file-backed or remote block store outside this package.

Use `Index`, `EncodeIndex`, and `DecodeIndex` for the low-level ERIS-FS API. Use
`Builder`, `OpenFS`, and `FileSystem` for the file-system API. All writers
create version 2 indexes. `Builder.AddFile` counts the content bytes while it
encodes them and selects the recommended ERIS block size.

When constructing an `Entry` directly, set `Size` to the file length in bytes.
Zero means an empty file. `DecodeIndex` sets v1 file sizes to `-1` because v1
does not store lengths. To encode such an index as v2, open the original image
with `OpenFS` and use `Stat` to obtain each missing length, then set
`Entry.Size`. Encoding fails with `ErrUnknownSize` until all file lengths are
known. Nested indexes can use either version. Existing v1 images with text
symlink targets or length-first CBOR map order remain readable; new indexes use
arrays of target components and RFC 8949 section 4.2.1 bytewise key order.

For uncompressed v2 files, `Stat`, directory entry metadata, and `OpenFile` need
no content reads. Random reads fetch only the required tree paths and leaf
blocks. Each reader caches at most 64 internal nodes and one leaf. A reader
checks the shape of each visited path and checks padding when it reads the file
end. Unvisited blocks are not checked. V1 files first scan internal nodes and
the final leaf to obtain the content length. This scan uses temporary summaries
that grow with the number of distinct internal nodes. The reader releases these
summaries after the scan and applies the same 64-node cache limit during reads.

The generic ERIS reader is a shared internal helper used by `fs` and `http`.
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
bytes, file capabilities, and complete image capabilities. Other Lzip encoders
can produce different valid streams; ERIS-FS does not prescribe a unique Lzip
bitstream. The streaming encoder also passes all 25 official base ERIS vectors.

`OpenFS` and `DecodeIndex` limit each decoded index to 64 MiB. Use
`OpenFSWithLimit` or `DecodeIndexWithLimit` to select another application limit.
File sizes in v2 indexes are untrusted metadata until the content is read.
`ReadFile` grows its buffer from the bytes read, rather than the declared size.
It still reads the whole file into memory. Use `Open` with `io.LimitReader` when
file content also needs an application size limit.

Callers that need stronger resistance to confirmation attacks must generate a
cryptographically secure, unique `eris.Secret` outside this package. ERIS does
not provide confidentiality.

Run the complete checks with:

```sh
just check
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
