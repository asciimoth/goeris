package main

const blockUsage = `Usage: goeris block [storage flags] COMMAND [options] [arguments]

Commands:
  put       Store a block from stdin and print its hash.
  get       Write raw blocks to stdout in the given order.
  prefetch  Send best-effort prefetch hints.
  check     Print each block hash and its stored flags.
  update    Replace the stored flags and report each result.

Run 'goeris block COMMAND --help' for command help.
` + storageHelp

const blockOptionHelp = `
Options:
  --high-priority  Request priority handling.
  --root           Mark the block as an entry block.

Each flag defaults to false. Use --high-priority=false or --root=false
to set a flag to false explicitly.
`

const blockPutUsage = `Usage: goeris block put [storage flags] [options]

Read one block of exactly 1 KiB or 32 KiB from stdin. Store the block
and print its hash to stdout. Directory and CBOR volumes ignore the block flags.
` + blockOptionHelp + storageHelp

const blockGetUsage = `Usage: goeris block get [storage flags] [options] [HASH...]

Write raw blocks to stdout in the given order. Set flags before the hashes.

Options:
  --batch-size N  Limit the number of blocks per request, up to the store limit.
                  Use 0 for automatic batching (the default).
                  Use 1 for one block per request.
` + storageHelp

const blockPrefetchUsage = `Usage: goeris block prefetch [storage flags] [options] HASH [HASH...]

Send best-effort prefetch hints for the given hashes. The command returns
without waiting for reads. Directory and CBOR volumes ignore the hints.
Set flags before the hashes.
` + blockOptionHelp + storageHelp

const blockCheckUsage = `Usage: goeris block check [storage flags] HASH [HASH...]

Read each block and print its hash and stored flags to stdout in this format:
  HASH high-priority=BOOL root=BOOL

Set flags before the hashes.
` + storageHelp

const blockUpdateUsage = `Usage: goeris block update [storage flags] [options] HASH [HASH...]

Replace both flags for each block. Print HASH changed=BOOL to stdout for each
result. Directory and CBOR volumes ignore updates. Set flags before the hashes.
` + blockOptionHelp + storageHelp

var blockOperationUsage = map[string]string{
	"put":      blockPutUsage,
	"get":      blockGetUsage,
	"prefetch": blockPrefetchUsage,
	"check":    blockCheckUsage,
	"update":   blockUpdateUsage,
}
