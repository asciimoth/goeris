## title: ERIS and Magnet URI synopsis: ERIS URNs in Magnet URIs

One of the design goals of ERIS is interoperability with existing systems by the
use of URIs. The ERIS specification defines its own
[URN format](http://purl.org/eris#name-urn), but in practice this is hardly
enough to make ERIS usable. This document presents some recomendations on how to
embed and extend ERIS URNs by using
[Magnet URIs](https://en.wikipedia.org/wiki/Magnet_URI_scheme).

## Magnet URI

Magnet URIs consist of a series of one or more parameters.

The most important parameter is `xt` (eXact Topic) which describes the content
exactly. For ERIS encoded content the `xt` parameter should be set to the ERIS
URN:

```
magnet:xt=urn:eris:BIAJ4PS…PIB3CDY
```

Further parameters can be used to attribute content with a human readable name
(using the `dn` paramter) or an exact length (using the `xl` parameter):

```
magnet:xt=urn:eris:BIAJ4PS…PIB3CDY&dn=eris.svg&xl=539
```

See the
[Wikipedia page on Magnet URIs](https://en.wikipedia.org/wiki/Magnet_URI_scheme)
for other parameters.
