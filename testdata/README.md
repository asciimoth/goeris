# ERIS test data

## Official ERIS vectors

`eris-test-vectors-v1.0.0.tar.gz` is an unchanged copy of the
[official ERIS 1.0.0 vector archive](https://eris.codeberg.page/spec/eris-test-vectors-v1.0.0.tar.gz),
linked by section 2.8.6 of the local
[ERIS specification](../specs/eris-v1.0.0.html#name-test-vectors). It contains
13 positive and 12 negative vectors. Its SHA-256 is:

```text
33603c58098f3cdd5c13cf245f09404533503bd352516f2f5e913ce3dae7c654
```

The included upstream `test-vectors/README` releases these vectors under CC0 and
permits their inclusion in implementations. The archive stays compressed to
reduce repository size. Tests read it directly without extracting files.
