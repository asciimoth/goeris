# ERIS root block extension

`BlockOptions.Root` is an advisory Boolean flag. Its default is `false`. It
highlights key blocks for indexing and block exchange between nodes. A root
block is the entry point named by an object's read capability. For a block tree,
this is the top node, not the first leaf written by the encoder.

## Encoding and storage

The `CapabilityStore` methods `Encode`, `EncodeRecommended`, `EncodeContent`,
and `EncodeContentRecommended` always set `Root` to `true` on the capability's
root block. This includes empty content and content that fits in one block.
Streaming encoders set the flag on the first write of the root block. Other
blocks receive the caller's options unchanged. An explicit `Root: true` marks
every block in the operation.

`CapabilityStore.EncodeContentWithOptions` marks the root by default. With
`PreserveRoot: true`, it passes the caller's `BlockOptions.Root` unchanged on
every block write. The store's rules for repeated puts still apply.

ERIS-FS marks the index root, which is the entry point for the whole file
system. `EncodeIndex` and `Builder.Finish` set that flag. Builder file writes
preserve the Root option supplied at construction, including for plain,
compressed, and automatically compressed files. They do not automatically mark
each file root. The effective flag is the original option OR the automatic root
flag: an explicit `Root=true` is preserved on all writes. With `Root=false`,
only the index root is marked. Encoding the same file separately as a standalone
blob still marks its root. Nested images have their own index roots. A file
write does not clear a root flag already stored for the same block, because
repeated puts preserve true.

Middleware passes request and response options unchanged. Read request flags do
not change stored flags. Successful read results keep their stored options,
including when another request in the batch fails. Missing result slots have
zero options. Terminal stores can ignore unsupported flags. Repeated puts should
keep `Root` true if either the stored or supplied value is true. `BlockSet`
follows this rule independently for `Root` and `HighPriority`.
`UpdateBlockOptions` can explicitly set or clear either flag.

The flag does not change block bytes, references, encryption keys, capabilities,
or decoded content. It does not imply high priority or prove that a block is a
root. A block can be a root for one object and a child in another object.

## HTTP and S3

HTTP uses a dedicated field:

```http
Eris-Root: true
```

S3 uses user-defined object metadata:

```http
x-amz-meta-eris-root: true
```

These fields use the same rules as the [priority fields](priority.md):

- Send exactly one `true` value for true. Omit the field for false.
- Field names are case-insensitive. The value is case-sensitive; surrounding
  spaces and tabs are ignored.
- Missing, duplicate, comma-joined, and unknown values mean false. Invalid
  advisory metadata must not prevent block access. Parse each flag separately.
- GET and HEAD request fields are operation hints. Block response fields report
  stored options, including in HEAD and range responses.
- A decoded-content request passes the hint to block reads. Its response does
  not report an aggregate root flag.
- An S3 PUT supplies the storage hint. Set the field before signing and include
  it in the signed headers. PUT responses do not report stored options.

The HTTP transport is read-only. Neither network transport has an explicit
options-update operation. Generic S3 replacement can replace metadata; clients
must not assume that it preserves true across repeated PUTs.

## Experimental CoAP option

CoAP uses option **65052**, named Experimental ERIS Root. It has the same
properties and rules as [Experimental ERIS High Priority](priority.md): empty,
non-repeatable, elective, safe to forward, and excluded from the cache key. One
empty option means true; absence, a nonempty value, or repetition means false.
Each block-wise request fragment carries the request flag. Each Content response
fragment carries the stored flag. Created and error responses do not report
stored flags.

Enable `ExperimentalRoot` on both `coap.ClientConfig` and `coap.Handler`. The
switch is independent of `ExperimentalHighPriority` and defaults to false. This
number is experimental, not an IANA allocation. Use it only in coordinated
experiments, with no operational use, as required for the experimental range by
the CoAP rules described in the priority specification.

## Persistence and trust

The existing ERIS CBOR format and volume v1 have no block metadata field. They
omit `Root` and return false when reading. ERIS-FS also does not embed the flag
in its index bytes; the flag is supplied to block storage.

Root metadata is not authenticated by the block reference. A cached flag can be
stale while the block bytes remain valid. Nodes choose their own indexing and
exchange policies. This flag does not grant access or bypass forwarding limits.
