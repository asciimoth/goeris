package main

const capUsage = `Usage: goeris cap [storage flags] COMMAND [options] [arguments]

Commands:
  encode        Store content from stdin and print its capability URN.
  decode        Write the content for one capability URN to stdout.
  prefetch      Request content in advance.
  resolve-path  Print paths to decoded files on a mount.

Run 'goeris cap COMMAND --help' for command help.
` + storageHelp

const capEncodeUsage = `Usage: goeris cap encode [storage flags] [options]

Read content from stdin and print its canonical ERIS capability URN on one line.

Options:
  --block-size N      Use 1024 or 32768 bytes. Use 0 for automatic selection.
  --secret-file PATH  Read the convergence secret from a file of exactly 32 bytes.
  --high-priority     Request priority handling for block writes.
  --root=BOOL         Pass this root flag value on every block write.

By default, the encoder uses the null secret and sets the root flag only on
the capability's root block. Automatic selection uses 1024-byte blocks for
input shorter than 16 KiB and 32768-byte blocks for input of 16 KiB or more.
` + storageHelp

const capReadOptions = `
  --ttl N          Set the network forwarding budget (0-255). Use 0 for the
                   store default and 1 for local access only.
  --high-priority  Request priority handling for block operations.
  --root           Pass the root flag in block requests.
`

const capDecodeUsage = `Usage: goeris cap decode [storage flags] [options] URN

Write the decoded bytes for one capability to stdout. Set flags before the URN.
If a read fails during decoding, stdout can contain part of the content.

Options:
  --size N         Supply the known decoded size in bytes. Use -1 to derive it.
  --limit N        Set the maximum decoded size in bytes. Use -1 for no limit.` + capReadOptions + `
When you set both --size and --limit, the size must fit within the limit.
Use the default TTL with --size.
` + storageHelp

const capPrefetchUsage = `Usage: goeris cap prefetch [storage flags] [options] URN [URN...]

Request content in argument order. A block store can read internal nodes
before it sends best-effort hints for the leaves. Success produces no output.
Leaf availability depends on the store. Set flags before the URNs.

Options:` + capReadOptions + storageHelp

const capResolvePathUsage = `Usage: goeris cap resolve-path [storage flags] URN [URN...]

Print the absolute path to each decoded file on a mount, one per line.
The command stops at the first error. Set flags before the URNs.
` + storageHelp

var capOperationUsage = map[string]string{
	"encode":       capEncodeUsage,
	"decode":       capDecodeUsage,
	"prefetch":     capPrefetchUsage,
	"resolve-path": capResolvePathUsage,
}
