## title: ERIS and Object Storage synopsis: Storing blocks in Object Storage systems

## Introduction

So called cloud service providers offer managed services on their own
infrastructure. One of the services offered is object storage. Object storage
allows data to be stored as objects referenced by a key. This is opposed to
usual file-systems that store data in hierarchical trees. Cloud storage
providers usually provide a HTTP-based API for uploading and accessing objects.
The most wide-spread API for seems to be the S3 API as developed by Amazon.
There are self-hostable software as well as many other providers beside Amazon
that offer the same API.

Object storage can be used to store blocks of ERIS encoded content. In this
document we describe a layout for blocks stored in object storage, describe how
to upload and access blocks from object storage as well as define a store URL
format.

There are [many reasons](https://anarchaserver.org/8m/) for not using services
offered by cloud service providers and only few legitimate reasons for using
them (e.g.
[censorsip-resistance](https://en.wikipedia.org/wiki/Collateral_freedom)).

Self-hostable software that expose the S3 API exists (e.g.
[MinIO](https://min.io/) or [Garage](https://garagehq.deuxfleurs.fr/)). However,
using these as an ERIS store seems unnecessarily complicated as
[simpler protocols](coap.xml) and more light-weight storage backends can be used
instead. Still, it might make sense for collectives to provide object storage as
many existing self-hostable software supports the same APIs (e.g.
[Funkwhale](https://docs.funkwhale.audio/administrator/configuration/object-storage.html)
or
[Nextcloud](https://docs.nextcloud.com/server/20/admin_manual/configuration_files/primary_storage.html)).
If a self-hosted object storage is already in-place it might make sense to use
it for ERIS blocks.

## Layout

Blocks are stored as objects using the Base32-encoded hash as key.

For example:

```
- FGIBBZ22V4GRMJVXNCSLVQHRP3NAPHRR5NW3GDPPXRJKBI452YBA
- RY2DQNYEPJD5TOJZME7MEK3RFFF75ZOIIFAJTKHP4SEMI3CPJXAQ
- EHQ2P3JZUX6TP3PZKFPMWOK4SBQKOEWKU52YJJQTKQJ66WWEQX4Q
- CMC7JNIYGVRPDTU4M2OUPKFNEEUPXYDWUNSINMAOBJXYUZA32CBA
```

## Public Access

The bucket and all objects should have the ACL set to `public-read`. This allows
blocks to be accessed via HTTP without any authentication.

## Store URL

We define an URL that can be used by applications that allow user-defined ERIS
stores (see also the Store URL defined for [ERIS over CoAP](coap.xml)):

```
s3://ENDPOINT/BUCKET/
```

Where `ENDPOINT` is the HTTPS host of the endpoint and `BUCKET` the name of the
bucket.

Example include:

- `s3://s3.eu-central-1.wasabisys.com/eris/`
- `s3://s3.amazonaws.com/my-block-bucket/`

## Implementations

The [python-eris](https://codeberg.org/eris/python-eris) repository has an
[implementation of a store backed by cloud object storage](https://codeberg.org/eris/python-eris/src/branch/main/examples/cloud_storage.py).
