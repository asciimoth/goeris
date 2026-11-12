## title: ERIS DNSLink Records synopsis: Publish ERIS capabilities using the Domain Name System

> DNSLink is the specification of a format for DNS TXT records that allows the
> association of arbitrary content paths and identifiers with a domain.
>
> DNSLink leverages the powerful distributed architecture of DNS for a variety
> of systems that require internet scale mutable names or pointers.

[The DNSLink Standard](https://dnslink.org/)

Two recommended namespaces are for publishing ERIS capabilities are `/eris/` and
`/erisfs/`.

## /eris/

`dnslink=/eris/B4AUQBBYSMTVN7MQ6SSPZO3TSRDDMTCQ7VONZT6EWS4AAIJBR5DOBAHOIVJ7O3WOTW5YNMYICMAHWGY26QFBYOT2VA23BS4VG5QJVZGJ2I`

The `/eris/` namespace is appropriate for publishing any ERIS capability and is
formed by replacing the `urn:eris:` prefix of a capability URN with `/eris/`.
The `/eris/` namespace does not convey a content type and therefore the consumer
of the record must discover the type using out-of-band metadata or by analysing
the content directly.

## /erisfs/

`dnslink=/erisfs/B4A6PNIT5KF4CLVDQ2UFC45UEVXS3LRPSJHSKYOLLGXPG4IADBJOAYIYTVNHFQ5LXUKO26S5DPZ64NGZ273AYIKYL62USS74VC2MJENMV4`

It is recommended to publish [ERIS-FS](./eris-fs.xml) capabilities to the
`/erisfs/` namespace. When publishing website archives via DNS the
recommendation is to use ERIS-FS combined with this DNSLink namespace.
