## title: ERIS over CoAP synopsis: Transport blocks over CoAP

ERIS is designed to traverse as many media as possible but in practice block
transport should be dominated by only a handful of protocols selected for
maximum interoperability. With that goal in mind this document describes a
transport layer using the Constrained Application Protocol (CoAP)
\[[RFC 7252](https://datatracker.ietf.org/doc/rfc7252/)\]. CoAP may be
transported over UDP \[[RFC 7252](https://datatracker.ietf.org/doc/rfc7252/)\],
stream transports such as TCP or WebSockets
\[[RFC 8323](https://www.rfc-editor.org/rfc/rfc8323)\], Unix sockets, over Delay
Tolerant Networks \[[Auzias2015](https://sci-hub.se/10.1109/FiCloud.2015.33)\]
or experimental protocols such as GNUnet CADET.

The protocol described here is only concerned with opaque ERIS blocks and must
never transport decoded ERIS content.

## Store URL

The protocol defines a store that provides multiple resources (e.g. block
access). A store is identified by an URL
[(RFC 3986](https://datatracker.ietf.org/doc/html/rfc3986)\] - the **store
URL**, resources are mapped to sub-paths of the store URL.

Examples:

- Store URL: `coap://example.net/my-eris-store`
  - URl of the blocks resource: `coap://example.net/my-eris-store/blocks`
- Store URL: `coap+tcp://spam.works/eris/not-spam`
  - URL of blocks resource: `coap+tcp://spam.works/eris/not-spam/blocks`
- Store URL: `coap+tcp://spam.works/eris/another-store`
  - URL of blocks resource: `coap+tcp://spam.works/eris/another-store/blocks`
- Store URL: `coap+ws://jblis.xyz/stores/35bc6e8f-6b01-46f0-ba77-ddd41ae3ee51`
  - URL of blocks resource:
    `coap+ws://jblis.xyz/stores/35bc6e8f-6b01-46f0-ba77-ddd41ae3ee51/blocks`

A CoAP endpoint can host multiple stores at different paths. The endpoint may
use the same underlying storage for all stores. The individual stores can be
used to manage specific block pinnings or quotas. The store URL might serve as a
capability to post blocks and should in general not be shared indiscrimantely.

Applications that allow user-defined stores should allow stores to be defined by
store URLs.

Store URLs can also be used for peer discovery.

### The `.well-known` store

Endpoints may provide an endpoint wide store at the relative URL path
`.well-known/eris`.

The `.well-known` store may be a read-only store that does not allow storing of
blocks but provides access to all blocks stored by an endpoint.

## Store Resources

### `blocks`

An ERIS CoAP endpoint should at minimum provide a `blocks` resource that can be
used to retrieve a stored block using the `GET` verbs.

#### GET

The [GET](https://www.rfc-editor.org/rfc/rfc7252.html#section-5.8.1) verb
retrieves a block.

The block reference (Blake2b256 digest) is encoded in the request as an
URI-Query option. The block reference may be the digest as 32 octets or a Base32
\[[RFC 4648](https://www.rfc-editor.org/info/rfc4648)\] encoding of 52
characters. The human readable form must use uppercase characters (Unicode
codepoints 0x30-0x39 and 0x41-0x5A). Implementations must support paths in the
human-readable form but should default to transmitting the raw encoding.

If the block is available the response code is 2.05 (Content) with a payload
containing the block data. The payload must be in raw octets and the content
format must be *application/octet-stream* (this is the default if no
Content-Format option is present).

Example:

- Client sends a request for a block to the store at
  `coap://example.net/my-eris-store`:
  - Uri-Host = "example.net"
  - Uri-Path = "my-eris-store"
  - Uri-Path = "blocks"
  - Uri-Query = "H77AGSYKAVTQPUHODJTQA7WZPTWGTTKLRB2GLMF5H53NEKFJ3FUQ"
- Sever retrieves block and responds with 2.05 (Content) with payload containing
  the block data.

Optionally a
[Size1 option](https://www.rfc-editor.org/rfc/rfc7252.html#section-5.10.9)
specifying the size of the block may be included in the request. This may be
used by the server to optimize block retrieval. For example:

- Client sends a request for a block to the store at
  `coap://example.net/my-eris-store`:
  - Uri-Host = "example.net"
  - Uri-Path = "my-eris-store"
  - Uri-Path = "blocks"
  - Uri-Query = "H77AGSYKAVTQPUHODJTQA7WZPTWGTTKLRB2GLMF5H53NEKFJ3FUQ"
  - Size1 = 1024
- Sever retrieves block and responds with 2.05 (Content) with payload containing
  the block data.

Caching ERIS blocks is encouraged and can always be considered "fresh" in the
CoAP caching model. This freshness is expressed with a large
[Max-Age](https://www.rfc-editor.org/rfc/rfc7252#section-5.10.5) option value.
The default Max-Age value is 60 seconds so this option should be set to a large
value (maximum of 0xFFFFFFFF, a long time) unless there is a compelling reason
not to do so.

#### PUT

The [PUT](https://www.rfc-editor.org/rfc/rfc7252.html#section-10.1.2) verb
submits a block. The payload is the block.

If the block was successfully committed to storage or was already present the
response code is 2.01 (Created).

Optionally the `Size1` option may be included to indicate the size of the block
to be stored.

Stores that do not alow or do not support storing blocks may responds with 4.01
(Unauthorized) or 4.04 (Not Found).

Example:

- Client wants to store a block at the store `coap://example.net/my-eris-store`:
  - Uri-Host = "example.net"
  - Uri-Path = "my-eris-store"
  - Uri-Path = "blocks"
  - Payload = BLOCK-DATA
  - Size1 = 1024
- Sever stores block and responds with 2.01 (Created)

## Bi-directionality of requests

CoAP allows bi-directional requests. A peer that initiates a connection can be
both sender and recipient of requests. The terms server and client are relative
to requests - a peer that sends a request is the client of the request and the
receiving peer is the server of the request.

Peers that initiate a connection should also be able to respond to requests.
Peers that accept connections may also make requests. This allows a more
peer-to-peer exchange of blocks.

## Discovery

ERIS CoAP block stores can be made discoverable via CoAP Resource Discovery (see
[Section 7.2 of RFC 7252](https://www.rfc-editor.org/rfc/rfc7252#section-7.2)).
This is done by exposing information at the endpoint `.well-known/core` in the
[CoRE Link Format (RFC 6690)](https://www.rfc-editor.org/rfc/rfc6690).

Services advertising ERIS CoAP block stores should expose the _store URL_ as
CoRE links using following parameters:

- `if` (Interface Description): `"http://purl.org/eris/coap"`
- `base`: This can be used to indicate that block transport should use CoAP over
  TCP instead of UDP (see [RFC 9176](https://www.rfc-editor.org/rfc/rfc9176)).

The mechanism can be used to discover peers on local networks (via multicast)
and by learning new peers from already known peers.

### Discovery of store via multicast

CoAP servers can be discovered by making a UDP multicast broadcast as described
in
[Section 7.1 of RFC 7252](https://www.rfc-editor.org/rfc/rfc7252#section-7.1).

An example:

1. Server listens on an IPv6 UDP socket and joins the multicast group `ff02::fd`
   (all link-local CoAP nodes).
1. Client make a UDP multicast GET request to the endpoint `.well-known/core`
   and group `ff02::fd`.
1. Server responds with resources in the CoRE link format:

```
</>;if="http://purl.org/eris/coap";base=coap+tcp:
```

4. Client connects to the ERIS block store at `coap+tcp://SERVER_ADDRESS/`.
   where `SERVER_ADDRESS` is the IP address contained in the UDP response.

Note that the `base` parameter indicates that the store should be contacted via
TCP even when the discovery is made via UDP.

### Discovery of new peers from already known peers

A peer connecting to a server may learn about peers that the server knows by
querying the CoRE links exposed by the server.

The server shares peers using the CoRE Link format as described above, using an
URL where the shared peer can be reached.

For example:

1. Server shares known peers `coap+tcp://eris.jblis.xyz/` and
   `coap+tcp://y.inqlab.net/` by serving following CoRE links at the endpoint
   `.well-known/core`:

```
</>;if="http://purl.org/eris/coap",
<coap+tcp://eris.jblis.xyz/>;if="http://purl.org/eris/coap",
<coap+tcp://y.inqlab.net/>;if="http://purl.org/eris/coap"
```

2. A peer connecting to the server can be discovered by making a GET request to
   `.well-known/core`.

Note that the server also advertises itself as an ERIS CoAP store (using the
relative URL `/`).

## Proxy and Hop-Limit

Stores may proxy block requests to other stores. When doing so they should use
and honor the CoAP `Hop-Limit` option as defined in
[RFC 8768](https://www.rfc-editor.org/rfc/rfc8768.html) to prevent infinite
loops of requests. If no `Hop-Limit` option is set in a request, the store may
only respond with locally available blocks and not proxy the request.

### Cross-Proxy between different networks

A CoAP service may proxy block requests to other networks. It may advertise the
ability to proxy to other networks using CoRE Link format and following
parameters:

- `rt=core.proxy`: This is defined in
  [Section 5.1 of CoAP Transport Indication](https://www.ietf.org/archive/id/draft-ietf-core-transport-indication-10.html#section-5.1)
  and indicates that the service is available to act as a proxy.
- `proxy-scheme`: This parameter can be used to indicate the URI scheme of the
  network the proxy can reach.

For example a service that can proxy requests to the
[OCapN](https://www.ocapn.org/) network may advertise itself using following
CoRE link:

```
</>;if="http://purl.org/eris/coap";rt=core.proxy;proxy-scheme=ocapn
```

Note that the `if="http://purl.org/eris/coap"` is included to indicate that ERIS
block store requests can be proxied. This is necessary as it is not possible to
translate generic CoAP semantics to the proxied protocol.

A service using the proxy should use the
[Proxy-Uri](https://www.rfc-editor.org/rfc/rfc7252#section-5.10.2) option to
indicate where the block store request should go. The `Uri-Path` and `Uri-Query`
options MUST be used to understand the type of request. In that sense we diverge
from RFC 7252, as it requires `Proxy-Uri` to take precedence over `Uri-Path` and
`Uri-Query` options. This is not possible in our case as it may not be possible
to encode the block store semantics in the URI of the network we proxy to.

The
[`eris-captp`](https://codeberg.org/eris/goblins-eris/src/branch/main/bin/eris-captp.scm)
script implements such a proxy to the [OCapN](https://www.ocapn.org/) network.
[Kapla](https://codeberg.org/eris/kapla/) is a service that can use such
proxies.

## Security Considerations

In order to prevent passive adversaries from observing block access patterns
(see
[relevant section in ERIS specification](http://purl.org/eris#name-observing-block-access))
adequate transport layer encryption should be used (or the protocol should be
used over distances that can be physically secured).

This protocol does not recommend or define any specific transport layer
encryption. Options include DTLS when using UDP (see
[Section 9.1 of RFC 7252](https://www.rfc-editor.org/rfc/rfc7252#section-9.1)),
TLS (see
[Section 9 of RFC 8323](https://www.rfc-editor.org/rfc/rfc8323#section-9)),
IPsec \[[RFC 5406](https://datatracker.ietf.org/doc/html/rfc5406)\],
[Tor](https://torproject.org/) or
[Yggdrasil](https://yggdrasil-network.github.io/)).
