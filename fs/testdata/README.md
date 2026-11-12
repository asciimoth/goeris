# ERIS-FS test data

## Official ERIS vectors

`eris-test-vectors-v1.0.0.tar.gz` is an unchanged copy of the
[official ERIS 1.0.0 vector archive](https://eris.codeberg.page/spec/eris-test-vectors-v1.0.0.tar.gz),
linked by section 2.8.6 of the local
[ERIS specification](../../specs/eris-v1.0.0.html#name-test-vectors). It
contains 13 positive and 12 negative vectors. Its SHA-256 is:

```text
33603c58098f3cdd5c13cf245f09404533503bd352516f2f5e913ce3dae7c654
```

The included upstream `test-vectors/README` releases these vectors under CC0 and
permits their inclusion in implementations. The archive stays compressed to
reduce repository size. Tests read it directly without extracting files.

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
