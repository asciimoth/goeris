## title: ERIS over HTTP synopsis: Transport blocks over HTTP status: STABLE

When you think of ERIS you might think of fancy protocols such as
[IPFS](https://ipfs.io), [NDN](named-data.net) or
[IPoAC](https://datatracker.ietf.org/doc/html/rfc1149). But ERIS can also be
used over ye good old HTTP.
[RFC2169](https://datatracker.ietf.org/doc/html/rfc2169) describes a convention
for generic URN resolution over HTTP. This can be easily adapted for our use.

## Resolving blocks

Blocks of ERIS encoded content can be
[referenced by URNs](http://purl.org/eris#name-block-urn). For example the URN:

```
urn:blake2b:H77AGSYKAVTQPUHODJTQA7WZPTWGTTKLRB2GLMF5H53NEKFJ3FUQ 
```

The endpoint we use is the `/uri-res/N2R`. This is the
[N2R](https://datatracker.ietf.org/doc/html/rfc2169#section-3.3) endpoint
defined by [RFC2169](https://datatracker.ietf.org/doc/html/rfc2169) (N2R stands
for Name-to-Resource).

A block can be resolved with a HTTP `GET` request:

```
GET /uri-res/N2R?urn:blake2b:H77AGSYKAVTQPUHODJTQA7WZPTWGTTKLRB2GLMF5H53NEKFJ3FUQ 
```

The endpoint should return the block using standard HTTP mechanisms. The
`Content-type` header should be set to the MIME type `application/octet-stream`.

## Resolving ERIS encoded content

It is recommenced that applications resolve individual blocks via the HTTP(S)
endpoint described above and decode content from the blocks locally. This
prevents resolver services from being able to decode content and allows the
integrity of the content to be verified.

In certain circumstances it may be necessary to decode content remotely and
retrieve it. For this also the `/uri-res/N2R` endpoint should be used.

For example:

```
GET /uri-res/N2R?urn:eris:BIAT2G3VUQWWDDSHAQ4OSUPA4D3LVVLXZQQ34VCDUGOCYGOADWKABBOVWQNT4D4JI3T65ZV4F2FR6YCSAY3T46M5B2BCEUC2G3JJFGWKMI
```

The endpoint should return the decoded content using standard HTTP mechanisms.
The `Content-type` header should be set to the MIME type
`application/octet-stream` or if possible an inferred MIME type of the content.
