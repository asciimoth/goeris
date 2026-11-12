## title: ERIS and RDF synopsis: Content-addressable RDF for the win

ERIS can be used to encode and share large pieces of content, similar to
protocols like BitTorrent. Unlike BitTorrent, ERIS is also usable for encoding
very small pieces of content (\<1KiB). This enables small pieces of data to be
encoded and made available robustly. One way of encoding small pieces of data is
with the Resource Description Framework (RDF).

In RDF data is modeled as a graph consisting of nodes that are either IRIs
(internationalized URIs, see
[RFC3987](https://datatracker.ietf.org/doc/html/rfc3987)) or literal values
(e.g. strings or integers). Edges between nodes are labeled with IRIs. RDF turns
out to be very well suited for decentralized systems as identifiers are
universally valid (by being URI/IRIs) and fragments of graphs can always be
merged without conflict.

There are two ways in which ERIS and RDF work together: Referencing ERIS encoded
content from RDF and encoding RDF with ERIS. Both will be described in the
following.

The usage of ERIS and RDF was first researched as part of the
[openEngiadina](https://openengiadina.net) project.

## Referencing ERIS encoded content from RDF

ERIS encoded content can be referenced by the
[URN encoding of the read capability](http://purl.org/eris#name-urn). The URN
can be used in any RDF. For example:

```turtle
<https://example.org/a-track-by-funki-porcini>
  a mo:Track ;
  dcterms:creator "Funki Porcini" ;
  dcterms:title "Back Home" ;
  mo:musicbrainz <urn:uuid:a9dae29a-3f23-4c4b-804d-e125d4582adf> ;
  mo:release <urn:uuid:0f028066-5891-322e-ad8d-6aa588063a2e> ;
  foaf:maker <urn:uuid:2adb429d-e39c-467b-b175-3f40440ff630> ;
  mo:available_as <urn:eris:BIAD77QDJMFAKZYH2DXBUZYAP3MXZ3DJZVFYQ5DFWC6T65WSFCU5S2IT4YZGJ7AC4SYQMP2DM2ANS2ZTCP3DJJIRV733CRAAHOSWIYZM3M> .
```

A digital representation of the track (as Ogg or Flac) that is encoded using
ERIS is referenced with the `mo:available_as` property.

Consumers must know how to dereference ERIS encoded content, but the
representation itself is completely conformant RDF and compatible with all
existing RDF software.

## Content-addressable RDF with ERIS

In the previous example the small RDF fragment that describes the track and
references the ERIS encoded digital representation is identified by an URL
(`https://example.org/a-track-by-funki-porcini`). Is it possible to encode this
little piece of RDF with ERIS itself and reference it with a ERIS URN? Yes, of
course!

However, it turns out that just encoding the RDF/Turtle textual representation
(as shown above) does not work very well, for following reasons:

- The original identifier is contained in the textual representation above. We
  would like the identifier to not be dependant on the original location, so it
  should not appear in a representation that we encode with ERIS.
- The RDF/Turtle representation is not very robust in the sense that we can
  reorder terms, add spaces and other syntactic tricks to get different textual
  representations of the same data. This would result in different ERIS URNs for
  the exactly same content. It would be much better if the identifier is not
  dependant on such syntactic things - we would need a canonical representation.
- The textual representation is not very efficient. RDF/Turtle is designed for
  human readability we would prefer a representation that is more efficient.
- When content-addressing large graphs we need a consistent way of grouping
  statements of the graph that are content-addressed together.

As part of the [openEngiadina](https://openengiadina.net) project we have made a
concrete proposal to do all this:
[Content-addressable RDF](http://purl.org/ca-rdf). NOTE: An update of this paper
is planned!

Finally we can ERIS encode RDF data and get a nice URN for our track:

```turtle
<urn:eris:BIAT2G3VUQWWDDSHAQ4OSUPA4D3LVVLXZQQ34VCDUGOCYGOADWKABBOVWQNT4D4JI3T65ZV4F2FR6YCSAY3T46M5B2BCEUC2G3JJFGWKMI>
  a mo:Track ;
  dcterms:creator "Funki Porcini" ;
  dcterms:title "Back Home" ;
  mo:musicbrainz <urn:uuid:a9dae29a-3f23-4c4b-804d-e125d4582adf> ;
  mo:release <urn:uuid:0f028066-5891-322e-ad8d-6aa588063a2e> ;
  foaf:maker <urn:uuid:2adb429d-e39c-467b-b175-3f40440ff630> ;
  mo:available_as <urn:eris:BIAD77QDJMFAKZYH2DXBUZYAP3MXZ3DJZVFYQ5DFWC6T65WSFCU5S2IT4YZGJ7AC4SYQMP2DM2ANS2ZTCP3DJJIRV733CRAAHOSWIYZM3M> .
```

See also [ERIS Cache](../eer-005) for an application of this.
