## title: ERIS Cache RDF Vocabulary synopsis: Metadata to annotate that some content is a cache

Most content on the Internet is published using location-addressed identifiers
(URLs). Such content can be made content-addressable with ERIS to improve
availability. However, the ERIS encoded content has an identifier that is not
linked with the original identifier of the content (e.g. the URL). For many
applications knowning the original identifier is important. The ERIS Cache RDF
vocabulary allows the ERIS encoded content to be annotated with the original
identifier as well as other infromation describing the circumstances in which
the cache was created.

## Vocabulary

ERIS Cache is an extension of the
[W3C PROV Ontology](https://www.w3.org/TR/prov-o/). The PROV Ontology provides
terms for describing the provenance of content - the origin and history of some
content. The PROV Ontology is quite abstract and complex to read (in the good
old style of the W3C bureaucrats). Luckily, it is not as complex as it seems.
The ontology is based on two basic classes (that we care about):

- Entity (`prov:Entity`): Some content or artefact (digital or physical)
- Activity (`prov:Activity`): A process that acts upon entitites by transforming
  them or generating them.

ERIS Cache defines a new type of activity - the activity of caching some content
with ERIS:

```turtle
ec:CreateCache
    a rdfs:Class ;
    rdfs:label "CreateCache" ;
    rdfs:comment "The activity of creating an ERIS encoded, content-addressed cache of some content.";
    rdfs:subClassOf prov:Activity .
```

See below for the complete RDF/Turtle serialization of the vocabulary.

The act of creating a cache is reified in an `ec:CreateCache` object. The
activity is itself RDF and should be content-addressed and encoded with ERIS
(see [ERIS and RDF](../eer-002)).

Further terms and properties are used from the PROV ontology. A minimal eample
uses the terms `prov:used` and `prov:generated`:

1. Encode the orignal content with ERIS. The encoded content now has a new
   identifier (a read capability).
1. Create an `ec:CreateCache` object referencing the original content with
   `prov:used` and the ERIS encoded content with `prov:generated`.

![Diagram with three nodes: orignal content, ERIS encoded content and ec:CreateCache activity. An arrow from ec:CreateCache labeled prov:used goes to original content and another arrow ffrom ec:CreateCache labeled prov:generated goes to ERIS encoded content.](./vocabulary.pic)

Note that there is no link from the encoded content directly to the orignal
content. Instead the `ec:CreateCache` activity links to both original content
and ERIS encoded content.

The example can be described in RDF/Turtle:

```turtle
<urn:eris:XXXXXXXXX> a ec:CreateCache ;
          prov:used <http://example.org/some-document.html> ;
          prov:generated <urn:eris:YYYYYYYY> ;
          prov:endedAtTime "2022-05-12"^^xsd:dateTime .
```

Note that we also used the `prov:endedAtTime` to describe the time when the
cache was created (or the process of caching ended).

Other PROV terms may be used. For example `prov:agent` may be used to describe
the agent that performed the caching activity:

```turtle
<urn:eris:XXXXXXXXX> a ec:CreateCache ;
          prov:used <http://example.org/some-document.html> ;
          prov:generated <urn:eris:YYYYYYYY> ;
          prov:endedAtTime "2022-05-12"^^xsd:dateTime ;
		  prov:actor <xmpp:pukkamustard@jblis.xyz> .
```

## Examples

### Multiple caches of the same content

If multiple caches are created at different times but the original content does
not change then the identifier of the ERIS encoded content will remain the same.
The only thing that changes is the identifier of the `ec:CreateCache` activity
as we have embedded a time stamp in it:

![multiple-caches](./multiple-caches.pic)

As RDF/Turtle:

```turtle
<urn:eris:XXXXXXX> a ec:CreateCache ;
          prov:used <http://example.org/some-document.html> ;
          prov:generated <urn:eris:YYYYYYYY> ;
          prov:endedAtTime "2022-05-11"^^xsd:dateTime .

<urn:eris:ZZZZZZZZ> a ec:CreateCache ;
          prov:used <http://example.org/some-document.html> ;
          prov:generated <urn:eris:YYYYYYYY> ;
          prov:endedAtTime "2022-05-12"^^xsd:dateTime .
```

We have made explicit the fact that the content at the orignal location was same
when cached at two different times while only storing one cached, ERIS encoded
version.

### Cacheing same content from multiple locations

If some content is hosted on multiple locations and we cache from both, both
processes will result in the same identifier for the ERIS encoded content.
However we get different `ec:CreateCache` activities describing the fact that
the content was cached from two different locations:

![Cacheing the same content from two different locations.](./multiple-locations.pic)

As RDF/Turtle:

```turtle
<urn:eris:XXXXXXX> a ec:CreateCache ;
          prov:used <http://example.org/some-document.html> ;
          prov:generated <urn:eris:YYYYYYYY> ;
          prov:endedAtTime "2022-05-12"^^xsd:dateTime .

<urn:eris:ZZZZZZZZ> a ec:CreateCache ;
          prov:used <http://somewhere.else/some-document.html> ;
          prov:generated <urn:eris:YYYYYYYY> ;
          prov:endedAtTime "2022-05-12"^^xsd:dateTime .
```

## RDF Turtle

The RDF/Turtle vocabulary is also available at
[http://purl.org/eris/cache](http://purl.org/eris/cache).

```turtle
@prefix ec: <http://purl.org/eris/cache#> .
@prefix prov: <http://www.w3.org/ns/prov#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .

<http://purl.org/eris/cache#>
    a owl:Ontology ;
    rdfs:label "ERIS Cache Vocabulary"@en ;
    rdfs:comment "An extension to the W3C PROVenance Interchange Ontology to describe the Activity of content-addressed caching with the ERIS encoding."@en .

ec:CreateCache
    a rdfs:Class ;
    rdfs:label "CreateCache" ;
    rdfs:comment "The activity of creating an ERIS encoded, content-addressed cache of some content.";
    rdfs:subClassOf prov:Activity .
```
