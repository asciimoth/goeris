# ERIS block priority extension

This document defines an optional high-priority flag for ERIS block storage and
transport. It does not change encrypted block bytes, block references, read
capabilities, or the ERIS content encoding.

In this document, MUST states a requirement, SHOULD states a recommendation, and
MAY states permission. MUST NOT states a prohibition.

## Meaning and default

The flag is a Boolean. Its default is `false`. `true` has higher priority than
`false`.

A terminal block store MAY interpret the flag as it chooses. For example, it can
use the flag for cache eviction, read or write scheduling, or another local
policy. It MAY ignore the flag. The flag does not guarantee retention, faster
service, or a particular order of operations.

A write carries the requested storage priority. A read request carries a hint
for that operation. A read response carries the stored block's priority, as
reported by the store. A read hint does not itself change stored priority. A
store that does not support priority reports `false`.

When the same block is written more than once, a store SHOULD keep the highest
priority from those writes. Thus, a write with `false` SHOULD NOT clear an
existing `true`. An explicit metadata update can set either value, including a
change from `true` to `false`. Such an update does not create a missing block.

Middleware SHOULD pass request and response priority unchanged by default. A
transport without this extension can lose the hint. This must not prevent block
access. A successful write does not prove that the store retained the hint.

## HTTP

The [ERIS over HTTP protocol](../specs/eer/http/index.md) uses this optional
field:

```http
Eris-High-Priority: true
```

The field is an end-to-end extension field under
[RFC 9110, section 5.1](https://www.rfc-editor.org/rfc/rfc9110.html#section-5.1).
It is not a standard HTTP scheduling priority field.

A sender MUST omit the field when the flag is `false`. When the flag is `true`,
it MUST send exactly one field value, `true`. Field names are case-insensitive.
After removal of leading and trailing spaces and horizontal tabs, field values
are case-sensitive. A missing, repeated, or unrecognized value means `false`. A
recipient MUST NOT reject block access only because it cannot use this hint.

The field on a block GET or HEAD request carries the operation hint. The field
on a successful block response carries the stored priority. This includes HEAD
and partial-content responses. Request and response values are independent. A
high-priority read can return a block with ordinary stored priority, and the
reverse can occur.

A request for decoded content can pass the hint to each required block read.
There is no aggregate priority for the decoded content response.

The ERIS HTTP protocol defines reads. This extension does not add a write or
metadata-update method. A client MUST ignore the priority field on an error
response.

## S3 object storage

The [ERIS object-storage protocol](../specs/eer/object-storage/index.md) uses
this optional field:

```http
x-amz-meta-eris-high-priority: true
```

The field uses
[S3 user-defined object metadata](https://docs.aws.amazon.com/AmazonS3/latest/userguide/UsingMetadata.html).
An ordinary S3 service can store it with a PUT and return it with GET or HEAD.
It uses the same omission and parsing rules as the HTTP field above. A sender
that signs a request MUST set this field before signing and include it in the
signed headers.

On PUT, the field supplies the requested storage priority. On GET or HEAD, the
request field is an operation hint for a service that understands this
extension; it does not update object metadata. The response field reports the
stored priority. A service can ignore the request hint.

Standard S3 object replacement can replace metadata. It does not guarantee the
highest-priority rule for repeated PUTs. A store that implements this extension
SHOULD retain the highest priority, but a client MUST NOT assume that a generic
S3 service does so. This extension does not redefine S3 object replacement or
introduce a separate metadata-update operation. A PUT response does not echo the
request hint as proof of the stored priority.

## Experimental CoAP option

The [ERIS over CoAP protocol](../specs/eer/coap/index.md) can carry the flag in
an optional elective option without a change to its block payload or reference
query. This document defines an **opt-in experiment**, not an allocated option
for operational use.

[RFC 7252, section 12.2](https://www.rfc-editor.org/rfc/rfc7252.html#section-12.2)
reserves option numbers 65000 through 65535 for experimental use, with no
operational use. Both endpoints MUST explicitly enable this experiment. It MUST
be disabled by default and MUST NOT be enabled for operational deployments.
Participants MUST coordinate its meaning outside the protocol to avoid a
collision with another experiment. Operational use requires an appropriate
option registration and a revised specification.

| Property          | Value                           |
| ----------------- | ------------------------------- |
| Name              | Experimental ERIS High Priority |
| Number            | 65020                           |
| Format            | Empty                           |
| Length            | 0 bytes                         |
| Repeatable        | No                              |
| Critical          | No (elective)                   |
| Unsafe to forward | No (safe to forward)            |
| Part of cache key | No                              |
| Default           | `false` when absent             |

Presence of one empty option means `true`. A sender MUST omit the option for
`false`. A recipient that enables the experiment treats a nonempty or repeated
option as `false`. A recipient that does not enable it ignores the option, as
per the elective option rules in
[RFC 7252, section 5.4.1](https://www.rfc-editor.org/rfc/rfc7252.html#section-5.4.1).

On GET, the request option carries the read hint and the successful Content
response option carries stored priority. On PUT, the request option carries the
requested storage priority. A Created response does not report storage options
and does not echo the hint. Error responses do not carry the flag. This
extension does not define a new metadata-update method.

With block-wise transfer, each request fragment carries the same hint. Each
Content response fragment carries the same stored priority. The option does not
change the block identity or the cache key.

## CBOR and persistent volumes

The [ERIS CBOR serialization](../specs/eer/cbor/index.md) permits blocks,
references, maps from references to block byte strings, and read capabilities.
Its CDDL has no optional metadata field. A Boolean, an additional map member, or
a wrapper for a block would change that grammar. This extension therefore does
not add a priority field to the existing CBOR format. Serialization omits
priority; deserialization gives the default `false`.

[ERIS file-system volume v1](../specs/eris-volume.md) stores block bytes without
priority metadata. A volume ignores write priority and always reports `false`.
An ERIS-FS image does not embed this storage hint in its file-system encoding.

## Caching and security

Priority is advisory metadata, not part of a block's authenticated content.
Verification of a block reference does not verify priority. Use authenticated
transport when the origin of the hint matters.

A cached response can contain an older priority value. The block remains valid
because its bytes and reference do not change when priority changes. The flag
does not invalidate an otherwise fresh cached block response.

A store MAY apply local resource limits to high-priority traffic. The flag does
not grant access or bypass authentication, authorization, or forwarding limits.

The [root block extension](root.md) defines an independent flag with the same
storage and transport rules. Encoders set that flag on object entry blocks.
