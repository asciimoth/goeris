# ERIS-FS test data

Base ERIS test vectors are in
[the root test data directory](../../testdata/README.md).

## Mount compatibility and fuzz tests

`mount_test.go` uses the exact v1 index bytes and three capabilities from the
[eris-go layout example](../../specs/eris-go-fuse-layout.md). It checks decoded
content and image access through the same block source. The tests also cover v2
images, nested indexes, compressed files, links, context cancellation, storage
errors, and concurrent reads. `testing/fstest` checks the virtual directories
and selected image trees.

`mount_block_test.go` checks encrypted blocks at volume and flat paths,
including ASCII case aliases. Files exported at `volume.BlockPath` are read back
through `volume.Open`. Tests also cover both block sizes, prefix directory
views, corrupt source data, concurrent file operations, and case-sensitive image
names.

`mount_cache_test.go` checks opt-in listing caches, independent limits,
canonical aliases, LRU eviction, shared virtual `Sub` views, directory
snapshots, failed loads, metadata reads, and concurrent operations.

The mount fuzz files contain six targets:

- `FuzzMountPaths`: public operations, path validation, capability aliases,
  error paths, and directory listings.
- `FuzzMountContent`: sequential and random-access reads against `bytes.Reader`,
  both block sizes, and raw and image paths.
- `FuzzMountIndex`: valid and malformed index bytes through block storage, while
  checking that raw reads preserve the original content.
- `FuzzMountLinks`: link targets and lookup results against the image API.
- `FuzzMountBlocks`: encrypted bytes and random access against `bytes.Reader`,
  with both block sizes, flat and volume paths, case aliases, and prefix views.
- `FuzzMountListingCache`: operation sequences against an independent LRU model,
  including failures, aliases, listings, prefix views, snapshots, and cache
  resets.

`FuzzIndexDecode` also includes the eris-go example as a seed. Run the mount
targets separately for longer sessions, for example:

```sh
go test ./fs -run '^$' -fuzz '^FuzzMountPaths$' -fuzztime=1m
go test ./fs -run '^$' -fuzz '^FuzzMountContent$' -fuzztime=1m
go test ./fs -run '^$' -fuzz '^FuzzMountIndex$' -fuzztime=1m
go test ./fs -run '^$' -fuzz '^FuzzMountLinks$' -fuzztime=1m
go test ./fs -run '^$' -fuzz '^FuzzMountBlocks$' -fuzztime=1m
go test ./fs -run '^$' -fuzz '^FuzzMountListingCache$' -fuzztime=1m
```

`just check` runs all repository fuzz targets, with five seconds per target by
default. Set `FUZZ_TIME` to change that duration.

## Lzip regression vectors

`lzip-conformance-v1.json` fixes the output of the
[goeris Lzip encoding profile](../../specs/eris-fs-encoding.md#lzip-encoding-profile).
These are library regression vectors, not upstream ERIS-FS vectors. They were
recorded from the explicit v0.5.15 encoder profile. Before acceptance, their
streams were checked with Python's liblzma decoder and the `xz` Lzip decoder.
Python also checked CRCs, lengths, and independently constructed v2 CBOR bytes.

Each vector gives the original byte count and SHA-256, the Lzip size and
SHA-256, the file capability, the exact v2 index bytes, and the image
capability. Streams of at most 512 bytes also include the exact Lzip bytes in
hexadecimal. Each image has one regular file named `file`, compressed with Lzip,
and uses the null convergence secret. The builder selects ERIS block sizes from
stored lengths.

The input recipes are:

- `zeros`: all bytes are zero.
- `cycle`: byte at offset `i` is `i modulo 256`.
- `xorshift32`: start with unsigned 32-bit state `x = 1`. At each offset, apply
  `x ^= x << 13`, then `x ^= x >> 17`, then `x ^= x << 5`. Left shifts wrap at
  32 bits and right shifts are unsigned. Output the low byte of `x`.
- `mixed`: advance the same xorshift state at **every** offset. When
  `i modulo 8192 < 4096`, output the low byte of `x`; otherwise output
  `i modulo 256`.

The dictionary-wrap input is 8392705 bytes. It crosses both the 8 MiB dictionary
boundary and the 4096-byte lookahead boundary.

## Auto-mode vectors

`auto-compression-v1.json` contains independent probe vectors for
[ERIS-FS-AUTO-1](../../specs/eris-fs-auto-compression.md#conformance-vectors).
They determine the compression choice, not the Lzip bitstream.

The original library fixtures and this document use the repository's CC0
license. Do not replace fixed expected values during a dependency update without
first checking whether the output contract has changed.
