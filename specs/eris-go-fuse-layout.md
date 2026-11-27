# ERIS FUSE path layout

This document describes the de-facto path layout of the Linux `fuse-` mount in
[eris-go](https://codeberg.org/eris/eris-go). Other implementations can use the
same layout for compatible file access. This layout is an implementation
convention; the
[ERIS specification](https://eris.codeberg.page/spec/eris-v1.0.0.html) defines
the encoded content and read capabilities, not these mount paths.

## Paths

Let `M` be the mountpoint and `C` be a complete ERIS read capability:

```text
M/
├── C                 decoded content as a read-only file
└── fs/
    └── C/            ERIS-FS directory tree, when C contains an ERIS-FS index
        └── ...       files, directories, and symbolic links from that index
```

`M/C` works for any available ERIS content. If `C` contains an ERIS-FS index,
this path returns its decoded index bytes. `M/fs/C/` interprets those bytes as
an [ERIS-FS tree](https://eris.codeberg.page/eer/eris-fs.xml). The `fs`
directory does not contain a list of known capabilities: both `M/C` and `M/fs/C`
resolve names on demand. The mount is read-only. See the
[root and ERIS-FS lookup code](https://codeberg.org/eris/eris-go/src/branch/trunk/hell/fuse.go).

The preferred name for `C` is its full `urn:eris:` form. At `M/C`, eris-go also
accepts the 66 capability bytes as 132 hexadecimal characters. Its capability
parser also accepts unprefixed base32. A capability contains a block size, a
tree level, a 32-byte root block reference, and a 32-byte decryption key. It is
more than a block hash. See the
[capability encoding code](https://codeberg.org/eris/eris-go/src/branch/trunk/eris/capability.go).

There is **no raw-block path** in this FUSE mount. A known 32-byte block hash
alone cannot select `M/C`: it does not supply the key or tree information needed
to decode content.

## Compatibility example

This example has three real, 1 KiB ERIS blocks: two file blocks and one ERIS-FS
index block. All three use a 32-byte convergence secret of zero bytes. Each
capability has tree level 0. The file contents are exact UTF-8 byte strings,
including the final newline:

```text
hello.txt       = "Hello, ERIS!\n"
docs/readme.txt = "ERIS-FS example\n"
latest.txt      -> hello.txt
```

The ERIS-FS index has version 1. Its entries are `hello.txt` (file kind 0),
`docs/readme.txt` (file kind 0), and `latest.txt` (symbolic link kind 2 with
target `hello.txt`). The decoded index is 203 bytes. Its exact CBOR bytes in
hexadecimal are:

```text
da657269748201a36968656c6c6f2e7478748200d9011458420a008b56bb1db84d96170d75751443a7a1e0f27b25ff672ccaf51bee6d0c1acc9d52d4114d1b9fc1e2220b2713aa1e996ad7f6472cdfc1a3ed7a6a5be32f05435f626a6c61746573742e74787482026968656c6c6f2e7478746f646f63732f726561646d652e7478748200d9011458420a00efa4a97b1c111d320ad9ea0214f982fe85c6497a8d4057fdb04c7e105f315d20e1e18bee02a1367e23a93febc276cfee3b207840513ee1f9549f58210358475c
```

The full read capabilities are:

| Content           | Capability                                                                                                            |
| ----------------- | --------------------------------------------------------------------------------------------------------------------- |
| `hello.txt`       | `urn:eris:BIAIWVV3DW4E3FQXBV2XKFCDU6Q6B4T3EX7WOLGK6UN643IMDLGJ2UWUCFGRXH6B4IRAWJYTVIPJS2WX6ZDSZX6BUPWXU2S34MXQKQ27MI` |
| `docs/readme.txt` | `urn:eris:BIAO7JFJPMOBCHJSBLM6UAQU7GBP5BOGJF5I2QCX7WYEY7QQL4YV2IHB4GF64AVBGZ7CHKJ75PBHNT7OHMQHQQCRH3Q7SVE7LAQQGWCHLQ` |
| ERIS-FS index     | `urn:eris:BIACGBUGVO7DG6RBLO75M3WY6JUXSVOKCD2C3NRSPKJKVHFZO6XBARY5ZN56UT4GIKHWGFKTUYQO7WFCNSRII5HBF42XLXZCUQSPQXTYCU` |

Each capability starts with byte `0a` (1 KiB block size) and byte `00` (tree
level 0), followed by its root reference and key. The block references and the
first 32 bytes of each stored, encrypted block are:

| Content           | Block reference, hex                                               | First 32 stored bytes, hex                                         |
| ----------------- | ------------------------------------------------------------------ | ------------------------------------------------------------------ |
| `hello.txt`       | `8b56bb1db84d96170d75751443a7a1e0f27b25ff672ccaf51bee6d0c1acc9d52` | `3f9c5d9b31aedf7ed5362ffb85806f6ce7993587f2a6873446a9ec1fd8dcf0ad` |
| `docs/readme.txt` | `efa4a97b1c111d320ad9ea0214f982fe85c6497a8d4057fdb04c7e105f315d20` | `51b3db7a4829900e9aa5aa769ead6f9203addced703a33cceb0752882790510f` |
| ERIS-FS index     | `230686abbe337a215bbfd66ed8f2697955ca10f42db6327a92aa9cb977ae1047` | `c7c8be6d32c8ce4fe9e298946cdf81b745820faba0073e1bb4bcf623b87439dd` |

The complete stored blocks can be reproduced from the three decoded byte strings
above. For each one, append byte `80` and then zero bytes to reach 1,024 bytes.
Compute the key as keyed BLAKE2b-256 of this padded block with the 32-byte zero
secret. Encrypt the padded block with ChaCha20 using that key, a 12-byte zero
nonce, and initial counter 0. The block reference is unkeyed BLAKE2b-256 of the
resulting 1,024-byte ciphertext. The read capability is the bytes
`0x0a || 0x00 || reference || key`, encoded as unpadded RFC 4648 base32 and
prefixed with `urn:eris:`. See the
[ERIS encoder](https://codeberg.org/eris/eris-go/src/branch/trunk/eris/encoder.go)
and
[block encryption code](https://codeberg.org/eris/eris-go/src/branch/trunk/eris/store.go).

Given these three blocks in the backing store, the following paths should
resolve. The labels in angle brackets stand for the full capabilities in the
table; they are not literal path components:

```text
M/
├── <hello capability>             "Hello, ERIS!\n"
├── <readme capability>            "ERIS-FS example\n"
├── <index capability>             203 decoded CBOR bytes
└── fs/
    └── <index capability>/
        ├── hello.txt              "Hello, ERIS!\n"
        ├── latest.txt -> hello.txt
        └── docs/
            └── readme.txt         "ERIS-FS example\n"
```

## Appendix: a shared read-only mount with volume paths

An alternative FUSE implementation can expose the paths above and also expose
the raw-block paths from [ERIS file-system volume v1](eris-volume.md). This
makes one mountpoint usable for both capability-based file reads and volume
block reads. It extends the layout described here; the eris-go mount itself does
not provide raw-block paths.

Let `H` be the canonical, uppercase, 52-character Base32 encoding of a block
reference. The additional path is:

```text
M/
├── C                              decoded content
├── fs/C/...                       decoded ERIS-FS tree
└── H[0:3]/H[3:6]/H[6:9]/H[9:52]  encrypted block bytes
```

The raw-block file must contain the complete 1,024-byte or 32,768-byte ERIS
block. It must not contain decoded content. A volume reader verifies its size
and hash. The FUSE implementation can resolve these paths on demand; it does not
need to list known blocks. If it exposes legacy flat block paths at `M/H`, the
canonical path takes precedence, as specified by the volume read procedure.

These names do not overlap on a case-sensitive Linux mount. A volume prefix is
three uppercase Base32 characters, `fs` is lowercase, and a complete capability
has a different length from the 52-character flat block name. The paths under
`M/fs/C/` belong to the decoded ERIS-FS tree and do not affect raw-block lookup.

In `goeris/fs.Mount`, an `ImageStore` supplies `M/fs/C`. A capability-backed
`Store` also supplies `M/C`. A separate `BlockSource` supplies both the
canonical volume path and the flat path `M/H`. Either component can be nil.
Lookups for its paths then return `io/fs.ErrNotExist`. With a block source, the
three prefix directories resolve on demand and have empty listings by default.
Reads verify the block size and hash.

Mount also accepts ASCII case variants of block hashes, capability names, and
the reserved `fs` component. These aliases do not conflict: a prefix has three
characters, `fs` has two, a flat hash has 52, and each capability form is
longer. Unused base32 bits must still be zero; padding and other directory
splits are rejected. These aliases apply only to mount lookup. The canonical
volume names remain uppercase. Names and link targets inside `M/fs/C/` keep
their original case-sensitive meaning, so entries named `File` and `file` stay
distinct.

`ReadLink` checks each target with link expansion before returning it. A link
that can leave its ERIS-FS image returns an error, including a link that crosses
the boundary through another link. Safe targets keep their original spelling.
Targets with a backslash, colon, or NUL byte are rejected because a native
adapter can interpret them as a host path.

Callers can explicitly enable recent listings with
`Mount.WithListingCache(capabilityLimit, blockLimit)`. Both categories are
disabled by default. Each has a separate entry limit and least-recently-used
eviction. With a capability-backed `Store`, canonical URNs appear at the root
after successful content opens, metadata queries, or image opens. Each image
store lists successfully opened image capabilities under `fs`. Verified
raw-block loads add both flat hash entries and their volume paths. Alias names
share one cache entry. The cache holds only identifiers and metadata. Direct
reads use the configured backend. Listing virtual directories (the root, `fs`,
and volume prefixes) does not refresh recency. Reading a directory inside `fs/C`
opens the image and refreshes `C`. An open directory retains a snapshot after
eviction; closing it releases that snapshot.

Capability URNs contain decryption keys. Enabling capability listings exposes
these keys to callers who can list the mount directories.
