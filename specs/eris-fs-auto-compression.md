# ERIS-FS automatic compression policy v1

This document defines **ERIS-FS-AUTO-1**, a deterministic, per-file choice
between uncompressed content and Lzip. It is a policy defined by this library,
not an amendment to the [ERIS-FS v2 format](eer/eris-fs/index.md). It requires
no new wire fields: the resulting entry uses the existing uncompressed length or
Lzip-tagged length.

The words MUST, MUST NOT, and SHOULD specify requirements for implementations of
this policy. All lengths are byte counts. All offsets start at zero. An
implementation MUST NOT change these rules while calling the result version 1. A
future policy with different rules needs a different version identifier.

## Purpose and limits

The policy examines a bounded prefix, so it works with a stream whose total
length is unknown. It estimates whether repeated byte sequences justify the cost
of compression. It does not inspect file names, extensions, executable bits,
timestamps, locale, or the host platform.

The [ERIS-FS compression guidance](eer/eris-fs/index.md#compression) recommends
compression for small or sequentially read files, excludes files below 1 KiB,
and recommends an 8 MiB Lzip dictionary. Auto mode enforces the 1 KiB minimum.
It cannot know how an application will read a file. Applications that need
random access SHOULD select uncompressed encoding explicitly.

The prefix may not represent the rest of the file. A file selected for Lzip may
ultimately grow after compression. An implementation MUST NOT reconsider the
choice after it reads the remainder, checks the actual Lzip output size, or
counts the resulting ERIS blocks. Those would be different policies.

## Constants and decision

| Parameter                         |                Value |
| --------------------------------- | -------------------: |
| Maximum sample size               | 32768 bytes (32 KiB) |
| Minimum file size for compression |   1024 bytes (1 KiB) |
| Probe hash slots                  |                16384 |
| Probe hash multiplier             |         `0x9e3779b1` |
| Minimum match length              |              4 bytes |
| Maximum match length              |            131 bytes |
| Maximum literal run length        |            128 bytes |
| Added container cost              |             26 bytes |
| Maximum accepted estimated ratio  |          7/8 (87.5%) |

Let `S` be the first `min(file_length, 32768)` bytes and `N = len(S)`. An
implementation MUST fill the sample until it has 32768 bytes or reaches normal
end of input. A short read is not end of input. An input failure MUST fail the
operation; it MUST NOT become a reason to select uncompressed content.

1. If `N < 1024`, select **uncompressed** without running the probe.
1. Otherwise calculate `P`, the probe byte count defined below.
1. Set `C = P + 26`.
1. Select **Lzip** if `8 * C <= 7 * N`; otherwise select **uncompressed**.

Use exact integer arithmetic. Equality selects Lzip. No floating-point ratio,
rounding operation, or platform-dependent integer overflow is permitted. The
largest possible `P` is `32768 + 256`, so the comparison fits in an unsigned
32-bit integer.

The 26 added bytes model the six-byte Lzip header and 20-byte trailer. The probe
is a lightweight LZ77 estimate, **not a trial Lzip stream**. It does not model
LZMA probability coding or its end marker. Using the size from an arbitrary Lzip
encoder would make the choice depend on that encoder's match selection and
coding decisions, even with the same dictionary size.

## Exact probe encoding

The probe produces a virtual byte stream. Implementations MAY count its bytes
without allocating the stream, provided that they get exactly the same count.
The stream is never stored in ERIS and is not an ERIS-FS compression format.

The stream has two token types and no header, trailer, or end marker:

- **Literal run:** for `L` literal bytes, where `1 <= L <= 128`, emit the byte
  `L - 1`, followed by those bytes. The cost is `L + 1` bytes.
- **Match:** for a match of `L` bytes at backward distance `D`, emit the byte
  `128 + (L - 4)`, followed by `D - 1` as a two-byte unsigned little-endian
  integer. The cost is three bytes. `4 <= L <= 131`. Copying a match may overlap
  the bytes being produced; each next byte is copied from `D` bytes behind it.

The hash at a valid sample offset `i` is:

```text
key = S[i] + 256*S[i+1] + 65536*S[i+2] + 16777216*S[i+3]
hash(i) = ((key * 0x9e3779b1) modulo 2^32) >> 18
```

The multiplication MUST wrap at 32 bits before the unsigned right shift. There
are 16384 slots. Each holds either no position or one sample offset. All slots
initially hold no position. **Offset zero is a valid position.**

Process the sample from left to right:

1. At position `i`, inspect the hash slot only if at least four bytes remain.
   Let `q` be its stored position, if present.
1. Accept `q` as a candidate only if `S[q:q+4] == S[i:i+4]`. A hash collision
   does not constitute a match. Search no other position.
1. For an accepted candidate, extend the match as far as possible while bytes
   remain equal, stopping at length 131 or the end of the sample. Comparing
   overlapping ranges is permitted. Flush pending literals, then emit a match
   token with that length and distance `i - q`. Always use this match; there is
   no lazy matching, cost search, or alternative candidate search.
1. Without a candidate, append `S[i]` to the pending literal run. Flush that run
   as soon as it contains 128 bytes.
1. After selecting the token, insert **every consumed sample position** into the
   hash table in increasing order, including positions inside a match. Insert a
   position only if four bytes remain there. An insertion overwrites its slot
   even when the prior key differs. The hash uses the original sample bytes,
   including lookahead bytes beyond the just-consumed range.
1. Advance past the consumed bytes. Repeat until the sample ends, then flush any
   pending literals.

The probe MUST NOT retain state between files. Hash table iteration order is
irrelevant because only a slot selected by `hash(i)` is read or changed.

### Reference pseudocode

```text
latest = array(16384, NONE)
output = empty byte array
literals = empty byte array
i = 0

flush():
    if len(literals) > 0:
        output.append(len(literals) - 1)
        output.extend(literals)
        literals.clear()

while i < N:
    length = 0
    if i + 4 <= N:
        q = latest[hash(i)]
        if q != NONE and S[q:q+4] == S[i:i+4]:
            length = 4
            while length < min(131, N-i) and S[q+length] == S[i+length]:
                length += 1

    if length != 0:
        flush()
        output.append(128 + length - 4)
        output.extend(uint16_little_endian(i - q - 1))
    else:
        length = 1
        literals.append(S[i])
        if len(literals) == 128:
            flush()

    for j from i through i+length-1:
        if j + 4 <= N:
            latest[hash(j)] = j
    i += length

flush()
P = len(output)
```

## Encoding the selected file

The choice applies to the entire file. It is not changed at ERIS block
boundaries. The sample MUST be replayed at the start of the selected encoder,
followed by the untouched remainder of the source. No sample byte may be lost or
encoded twice. The probe MUST NOT store any trial blocks in the block sink.

For uncompressed content, encode the original bytes with ERIS. For Lzip,
compress the entire original file with an 8 MiB dictionary, then ERIS-encode the
Lzip stream. The v2 entry always records the **original** length. Use an
untagged length for uncompressed content and the Lzip tag for compressed
content. Use 1 KiB ERIS blocks when the stored stream is shorter than 16384
bytes, and 32 KiB blocks otherwise. The stored stream is the original file in
the first case and the completed Lzip stream in the second case.

A streaming implementation needs at most 32 KiB of input lookahead for this
policy. A hash table that stores `position+1` in a 16-bit cell uses another 32
KiB, with zero as the absent value. Compression and ERIS encoding have their own
bounded buffers. Seeking or a second read of the source is not required.

## Interoperability and reproducibility

Conforming implementations MUST choose the same mode for the same sample. The
decision does not depend on a compression library, its version, or its
compression level. The Go API is `fs.ChooseCompression(prefix)`, and
`Builder.AddAutoFile` applies that choice while streaming the file.

This policy specifies a portable **choice**, not a unique Lzip bitstream.
Different Lzip encoders may produce different valid streams. Matching ERIS
capabilities across tools additionally requires matching the actual Lzip bytes,
ERIS secret, block-size choices, file metadata, and index structure. This
library tests equal output for equal inputs despite different read chunk sizes
and entry insertion orders. Its Lzip writer uses the pinned Go LZMA dependency
from `go.mod`; that reproducibility guarantee does not imply byte equality with
an arbitrary other Lzip implementation.

The [encoding profile](eris-fs-encoding.md#lzip-encoding-profile) records all
Lzip settings and the fixed output fixtures that guard this library's encoder.
For API usage and version requirements, follow the
[goeris auto compression procedure](eris-fs-encoding.md#deterministic-encoding-with-goeris-auto-compression).
For equal capabilities without a shared compression encoder, follow the
[uncompressed procedure across implementations](eris-fs-encoding.md#deterministic-encoding-across-implementations).

## Conformance vectors

The machine-readable vectors are in
[`fs/testdata/auto-compression-v1.json`](../fs/testdata/auto-compression-v1.json).
They include SHA-256 hashes of the complete input and were generated with a
separate Python implementation that emits the virtual tokens.

Input recipes are:

- `zeros`: `length` zero bytes.
- `cycle`: byte `i` equals `i modulo 256`.
- `xorshift32`: start with unsigned 32-bit state `x = 1`. For each output byte,
  apply `x = x XOR ((x << 13) modulo 2^32)`, then `x = x XOR (x >> 17)`, then
  `x = x XOR ((x << 5) modulo 2^32)`. Emit `x modulo 256` after all three steps.
  Right shifts are unsigned. Reset the state for each vector.
- `random-prefix`: generate `random_prefix` bytes with `xorshift32`, then append
  zero bytes to reach `length`.

`P` is shown even for short inputs to allow testing the probe separately. The
policy itself skips the probe when `N < 1024`.

| Input                         |     N |     P |     C | Choice                 |
| ----------------------------- | ----: | ----: | ----: | ---------------------- |
| Empty                         |     0 |     0 |    26 | Uncompressed           |
| One zero byte                 |     1 |     2 |    28 | Uncompressed           |
| 1023 zero bytes               |  1023 |    26 |    52 | Uncompressed           |
| 1024 zero bytes               |  1024 |    26 |    52 | Lzip                   |
| 32768 zero bytes              | 32768 |   755 |   781 | Lzip                   |
| Cycle, 128 bytes              |   128 |   129 |   155 | Uncompressed           |
| Cycle, 1024 bytes             |  1024 |   276 |   302 | Lzip                   |
| Cycle, 32768 bytes            | 32768 |  1005 |  1031 | Lzip                   |
| Xorshift32, 1024 bytes        |  1024 |  1032 |  1058 | Uncompressed           |
| Xorshift32, 32768 bytes       | 32768 | 33024 | 33050 | Uncompressed           |
| Random prefix 855, then zeros |  1024 |   869 |   895 | Lzip                   |
| Random prefix 856, then zeros |  1024 |   870 |   896 | Lzip (exact threshold) |
| Random prefix 857, then zeros |  1024 |   871 |   897 | Uncompressed           |

For every full 32768-byte sample, appending arbitrary bytes to the file MUST NOT
change the choice. Normal read chunk boundaries MUST NOT change it either.
